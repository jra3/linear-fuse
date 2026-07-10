package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/reconcile"
)

// Default staleness threshold for on-demand data (comments, documents, updates).
// Set to 5 minutes (2.5× the 2-minute sync interval) so genuinely missed syncs
// get caught by user access without causing redundant refreshes on every read.
const defaultStalenessThreshold = 5 * time.Minute

// reconcileCooldown is the minimum gap between proactive reconciliation
// passes. The pass is triggered by reactive orphan deletions, then
// suppressed for this window to bound API cost.
const reconcileCooldown = 6 * time.Hour

// SQLiteRepository is the read path: it reads from SQLite and optionally
// falls back to the API client for data that hasn't been synced yet.
//
// It is deliberately a concrete type with no interface in front of it. A
// Repository interface (with an in-memory mock) existed for the project's
// whole life without ever gaining a consumer — one adapter means a
// hypothetical seam — so it was deleted (round 14). If a real second
// adapter appears (a read-through cache, an alternate store), re-extract
// the interface from this type mechanically. Testing strategy for fs code
// is pure-projection extraction (dirManifest.find, the listing modules),
// not repo mocking; fs write handlers touch *db.Store directly anyway, so
// a mock repo could never unit-test them.
//
// For on-demand data (comments, documents, updates), it implements
// stale-while-revalidate: returns cached data immediately and triggers
// a background refresh if the data is stale.
// maxConcurrentRefreshes limits how many background refresh goroutines can
// be in-flight at once. When the limit is reached, new refresh requests are
// silently dropped — callers already have cached data to return.
const maxConcurrentRefreshes = 10

// refreshTimeout caps how long a background refresh can block waiting for
// a rate limiter token. Prevents indefinite blocking during budget exhaustion.
const refreshTimeout = 30 * time.Second

type SQLiteRepository struct {
	store              *db.Store
	client             *api.Client   // Optional: for fallback/on-demand fetch
	currentUser        *api.User     // Cached current user
	stalenessThreshold time.Duration // How long before data is considered stale

	// extractor owns embedded-file extraction (HEAD + upsert) for the SWR
	// issue-details path. Nil in fixture mode (no client) — Deps.Extract nil
	// skips extraction.
	extractor *reconcile.Extractor

	// Track in-flight refreshes to avoid duplicate API calls
	refreshMu      sync.Mutex
	refreshing     map[string]bool
	refreshContext context.Context
	refreshCancel  context.CancelFunc

	// Semaphore to limit concurrent background refreshes
	refreshSem chan struct{}

	// SWR-layer instruments, bound at construction (zero value = no-op).
	metrics swrMetrics

	// Adaptive reconciliation: triggered by reactive orphan deletions,
	// rate-limited by reconcileCooldown.
	reconcileMu      sync.Mutex
	lastReconcileAt  time.Time
	reconcilePending atomic.Bool
}

// NewSQLiteRepository creates a new SQLite-backed repository.
// If client is nil, the repository will only serve data from SQLite.
func NewSQLiteRepository(store *db.Store, client *api.Client) *SQLiteRepository {
	ctx, cancel := context.WithCancel(context.Background())
	r := &SQLiteRepository{
		store:              store,
		client:             client,
		stalenessThreshold: defaultStalenessThreshold,
		refreshing:         make(map[string]bool),
		refreshContext:     ctx,
		refreshCancel:      cancel,
		refreshSem:         make(chan struct{}, maxConcurrentRefreshes),
		metrics:            newSWRMetrics(),
	}
	if client != nil {
		r.extractor = &reconcile.Extractor{Q: store.Queries(), AuthHeader: client.AuthHeader}
	}
	return r
}

// catchUpStaleness is the staleness threshold used during catch-up syncs.
// Suppresses on-demand refreshes while the sync worker is already fetching the same data.
const catchUpStaleness = 30 * time.Minute

// SetCatchUpMode toggles between normal (5min) and catch-up (30min) staleness thresholds.
// Called by the sync worker when it detects a large batch of changed issues.
func (r *SQLiteRepository) SetCatchUpMode(active bool) {
	if active {
		r.stalenessThreshold = catchUpStaleness
		log.Printf("[repo] catch-up mode enabled: staleness threshold increased to %s", catchUpStaleness)
	} else {
		r.stalenessThreshold = defaultStalenessThreshold
		log.Printf("[repo] catch-up mode disabled: staleness threshold restored to %s", defaultStalenessThreshold)
	}
}

// Close stops any background refresh operations
func (r *SQLiteRepository) Close() {
	r.refreshCancel()
}

// triggerBackgroundRefresh starts a background refresh if not already in progress.
// Uses a semaphore to limit concurrency — if too many refreshes are in-flight,
// new requests are dropped. This prevents stampedes after connectivity loss.
//
// It takes the refreshKind (not a pre-built key) so its three exits — the
// round-18 leak surface — record linearfs.swr.triggers with the bounded kind
// attribute; the dedup key is still minted only by refreshKind.key. The
// nil-client return records nothing.
func (r *SQLiteRepository) triggerBackgroundRefresh(kind refreshKind, id string, refreshFn func(context.Context) error) {
	if r.client == nil {
		return
	}

	key := kind.key(id)
	r.refreshMu.Lock()
	if r.refreshing[key] {
		r.refreshMu.Unlock()
		r.metrics.recordTrigger(kind, "deduped")
		return
	}
	r.refreshing[key] = true
	r.refreshMu.Unlock()

	// Try to acquire semaphore without blocking. If full, drop this refresh —
	// the caller already has cached data to return.
	select {
	case r.refreshSem <- struct{}{}:
	default:
		r.refreshMu.Lock()
		delete(r.refreshing, key)
		r.refreshMu.Unlock()
		r.metrics.recordTrigger(kind, "sem_dropped")
		return
	}

	r.metrics.recordTrigger(kind, "triggered")
	go func() {
		defer func() {
			<-r.refreshSem
			r.refreshMu.Lock()
			delete(r.refreshing, key)
			r.refreshMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(r.refreshContext, refreshTimeout)
		defer cancel()
		err := refreshFn(ctx)
		r.metrics.recordRefreshOutcome(kind, err)
		if err != nil {
			if r.refreshContext.Err() == nil && ctx.Err() == nil {
				log.Printf("[repo] background refresh %s failed: %v", key, err)
			}
		}
	}()
}

// maybeScheduleReconcile fires a proactive reconciliation pass if no pass
// has run within reconcileCooldown. Called from every deleteOrphan* helper
// after a successful orphan deletion — the deletion itself is evidence of
// drift between SQLite and Linear, justifying a full sweep to find siblings.
func (r *SQLiteRepository) maybeScheduleReconcile() {
	if r.client == nil {
		return
	}
	if r.reconcilePending.Load() {
		return
	}

	r.reconcileMu.Lock()
	elapsed := time.Since(r.lastReconcileAt)
	r.reconcileMu.Unlock()

	if elapsed < reconcileCooldown {
		return
	}
	if !r.reconcilePending.CompareAndSwap(false, true) {
		return
	}

	go r.runReconcile()
}

// runReconcile performs a full sweep across issues, projects, and
// initiatives, deleting any local row whose ID is absent from Linear's
// authoritative response. Triggered by maybeScheduleReconcile.
func (r *SQLiteRepository) runReconcile() {
	defer r.reconcilePending.Store(false)
	ctx, cancel := context.WithTimeout(r.refreshContext, 10*time.Minute)
	defer cancel()

	log.Printf("[reconcile] adaptive trigger after orphan delete; pass starting")
	start := time.Now()

	issues := r.reconcileIssues(ctx)
	projects := r.reconcileProjects(ctx)
	initiatives := r.reconcileInitiatives(ctx)

	r.reconcileMu.Lock()
	r.lastReconcileAt = time.Now()
	r.reconcileMu.Unlock()

	log.Printf("[reconcile] pass complete: issues=%d projects=%d initiatives=%d duration=%s",
		issues, projects, initiatives, time.Since(start).Round(time.Millisecond))
}

// reconcileIssues walks every team in SQLite and, for each, fetches the
// authoritative issue ID set from Linear, diffs against the local set,
// and deletes the orphans. Returns the total number of orphans removed.
// This is the reactive pass's entry point; its LowBudget-defers and
// per-team-error-skips semantics are unchanged (it ignores completeness).
func (r *SQLiteRepository) reconcileIssues(ctx context.Context) int {
	deleted, _ := r.reconcileIssuesWith(ctx, r.client.GetTeamIssueIDs, r.client.LowBudget)
	return deleted
}

// ReconcileIssueIDs runs the issues portion of the reconcile pass on behalf
// of the sync worker's scheduled hourly sweep (#245). The drain is injected
// (rather than read from r.client) so the sweep flows through the worker's
// API-client seam — the op-recording mock and fake clock can drive it — while
// the diff-and-delete machinery (reconcileIssuesForTeam → deleteOrphanIssue,
// the full sub-resource cleanup) is reused verbatim, not copied.
//
// complete reports whether EVERY team's drain succeeded: the caller stamps
// its persisted schedule only on a complete pass, so a failed or
// budget-deferred (api.ErrBudget) drain leaves the sweep due. Per-team
// all-or-nothing still holds regardless: a team whose drain errored deletes
// nothing for that team, while teams whose drains completed are cleaned.
//
// The reconcilePending CAS makes the sweep and the reactive runReconcile
// mutually exclusive (no concurrent double-drain) and — because
// maybeScheduleReconcile no-ops while pending is set — keeps the sweep's own
// deletions from chaining a reactive pass, exactly as deletions inside
// runReconcile already don't. lastReconcileAt is deliberately NOT touched:
// the reactive path's cooldown semantics stay unchanged for its own triggers.
func (r *SQLiteRepository) ReconcileIssueIDs(ctx context.Context, drain func(ctx context.Context, teamID string) ([]string, error)) (deleted int, complete bool) {
	if !r.reconcilePending.CompareAndSwap(false, true) {
		// A reactive pass is in flight; stay due and let the next cycle retry.
		return 0, false
	}
	defer r.reconcilePending.Store(false)
	return r.reconcileIssuesWith(ctx, drain, nil)
}

// reconcileIssuesWith is the shared core of the two issue-reconcile entry
// points (the reactive pass above and the worker's scheduled sweep). It walks
// every team in SQLite, drains that team's authoritative issue ID set, and
// diffs-and-deletes local orphans. The drain is all-or-nothing per team
// (fetchAll guarantees complete-or-error), so a drain error deletes nothing
// for that team. lowBudget, when non-nil, is a preflight that defers all
// remaining teams (the reactive path's gate; the scheduled path passes nil
// and relies on the drain's own ErrBudget preflight). complete is true only
// when every team drained successfully.
func (r *SQLiteRepository) reconcileIssuesWith(ctx context.Context, drain func(ctx context.Context, teamID string) ([]string, error), lowBudget func() bool) (deleted int, complete bool) {
	teams, err := r.store.Queries().ListTeams(ctx)
	if err != nil {
		log.Printf("[reconcile] list teams: %v", err)
		return 0, false
	}
	complete = true
	for _, team := range teams {
		if lowBudget != nil && lowBudget() {
			log.Printf("[reconcile] budget low; deferring remaining teams")
			return deleted, false
		}
		apiIDs, err := drain(ctx, team.ID)
		if err != nil {
			log.Printf("[reconcile] issues team %s: %v (skipping)", team.Key, err)
			complete = false
			continue
		}
		deleted += r.reconcileIssuesForTeam(ctx, team.ID, apiIDs)
	}
	return deleted, complete
}

// reconcileAgainst diffs a provably-complete authoritative ID set (apiIDs)
// against the local IDs from getLocal, deletes every local orphan through
// deleteOrphan, and returns the count removed. The diff-and-delete that can
// mass-delete rows lives here once, behind the getLocal/deleteOrphan seam;
// each caller owns only the budget-gated authoritative fetch and its
// per-entity local query and orphan delete. label names the entity in the
// log line when the local query fails.
func (r *SQLiteRepository) reconcileAgainst(ctx context.Context, label string, apiIDs []string, getLocal func() ([]string, error), deleteOrphan func(context.Context, string)) int {
	localIDs, err := getLocal()
	if err != nil {
		log.Printf("[reconcile] list local %s: %v", label, err)
		return 0
	}
	deleted := 0
	for _, id := range setDiff(localIDs, apiIDs) {
		deleteOrphan(ctx, id)
		deleted++
	}
	return deleted
}

// reconcileIssuesForTeam diffs apiIDs against SQLite's issue IDs for the
// given team and deletes any locals missing from the API set. Split out
// so tests can drive the diff/delete logic without needing a live client.
func (r *SQLiteRepository) reconcileIssuesForTeam(ctx context.Context, teamID string, apiIDs []string) int {
	return r.reconcileAgainst(ctx, "issues for team "+teamID, apiIDs, func() ([]string, error) {
		rows, err := r.store.Queries().ListTeamIssueIDs(ctx, teamID)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids, nil
	}, r.deleteOrphanIssue)
}

// reconcileProjects fetches the authoritative project ID set from Linear,
// diffs against SQLite, and deletes the orphans.
func (r *SQLiteRepository) reconcileProjects(ctx context.Context) int {
	if r.client.LowBudget() {
		log.Printf("[reconcile] budget low; skipping projects")
		return 0
	}
	apiIDs, err := r.client.GetWorkspaceProjectIDs(ctx)
	if err != nil {
		log.Printf("[reconcile] projects fetch: %v (skipping)", err)
		return 0
	}
	return r.reconcileAgainst(ctx, "projects", apiIDs, func() ([]string, error) {
		rows, err := r.store.Queries().ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(rows))
		for _, p := range rows {
			ids = append(ids, p.ID)
		}
		return ids, nil
	}, r.deleteOrphanProject)
}

// reconcileInitiatives fetches the authoritative initiative ID set,
// diffs against SQLite, and deletes the orphans.
func (r *SQLiteRepository) reconcileInitiatives(ctx context.Context) int {
	if r.client.LowBudget() {
		log.Printf("[reconcile] budget low; skipping initiatives")
		return 0
	}
	apiIDs, err := r.client.GetWorkspaceInitiativeIDs(ctx)
	if err != nil {
		log.Printf("[reconcile] initiatives fetch: %v (skipping)", err)
		return 0
	}
	return r.reconcileAgainst(ctx, "initiatives", apiIDs, func() ([]string, error) {
		rows, err := r.store.Queries().ListInitiatives(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(rows))
		for _, i := range rows {
			ids = append(ids, i.ID)
		}
		return ids, nil
	}, r.deleteOrphanInitiative)
}

// setDiff returns elements in `local` that are not in `api`. Used by the
// reconciliation pass to identify orphan rows.
func setDiff(local, api []string) []string {
	if len(local) == 0 {
		return nil
	}
	apiSet := make(map[string]struct{}, len(api))
	for _, id := range api {
		apiSet[id] = struct{}{}
	}
	var orphans []string
	for _, id := range local {
		if _, ok := apiSet[id]; !ok {
			orphans = append(orphans, id)
		}
	}
	return orphans
}

// =============================================================================
// Teams
// =============================================================================

func (r *SQLiteRepository) GetTeams(ctx context.Context) ([]api.Team, error) {
	teams, err := r.store.Queries().ListTeams(ctx)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	return db.DBTeamsToAPITeams(teams), nil
}

// =============================================================================
// Issues
// =============================================================================

func (r *SQLiteRepository) GetTeamIssues(ctx context.Context, teamID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssues(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssueByIdentifier(ctx context.Context, identifier string) (*api.Issue, error) {
	return queryOne("get issue by identifier",
		func() (db.Issue, error) { return r.store.Queries().GetIssueByIdentifier(ctx, identifier) },
		db.DBIssueToAPIIssue)
}

func (r *SQLiteRepository) GetIssueByID(ctx context.Context, id string) (*api.Issue, error) {
	return queryOne("get issue by id",
		func() (db.Issue, error) { return r.store.Queries().GetIssueByID(ctx, id) },
		db.DBIssueToAPIIssue)
}

func (r *SQLiteRepository) GetIssueChildren(ctx context.Context, parentID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssuesByParent(ctx, sql.NullString{String: parentID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list issue children: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

// =============================================================================
// Filtered Issue Queries
// =============================================================================

func (r *SQLiteRepository) GetIssuesByState(ctx context.Context, teamID, stateID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssuesByState(ctx, db.ListTeamIssuesByStateParams{
		TeamID:  teamID,
		StateID: sql.NullString{String: stateID, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list issues by state: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssuesByAssignee(ctx context.Context, teamID, assigneeID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssuesByAssignee(ctx, db.ListTeamIssuesByAssigneeParams{
		TeamID:     teamID,
		AssigneeID: sql.NullString{String: assigneeID, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list issues by assignee: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssuesByLabel(ctx context.Context, teamID, labelID string) ([]api.Issue, error) {
	// Get label name first
	label, err := r.store.Queries().GetLabel(ctx, labelID)
	if err != nil {
		if err == sql.ErrNoRows {
			return []api.Issue{}, nil
		}
		return nil, fmt.Errorf("get label: %w", err)
	}

	// Use the store's JSON-based label query
	issues, err := r.store.ListIssuesByLabel(ctx, teamID, label.Name)
	if err != nil {
		return nil, fmt.Errorf("list issues by label: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

// NB: GetIssuesByPriority was deleted (round 19) — it had no production
// caller (there is no by/priority/ view). Its sqlc query
// (ListTeamIssuesByPriority) was removed in the round-20 dead-code prune.

func (r *SQLiteRepository) GetUnassignedIssues(ctx context.Context, teamID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamUnassignedIssues(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list unassigned issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssuesByProject(ctx context.Context, projectID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListProjectIssues(ctx, sql.NullString{String: projectID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list issues by project: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssuesByCycle(ctx context.Context, cycleID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListCycleIssues(ctx, sql.NullString{String: cycleID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list issues by cycle: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

// =============================================================================
// My Issues
// =============================================================================

func (r *SQLiteRepository) GetMyIssues(ctx context.Context) ([]api.Issue, error) {
	user, err := r.GetCurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return []api.Issue{}, nil
	}

	issues, err := r.store.Queries().ListUserAssignedIssues(ctx, sql.NullString{String: user.ID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list my issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetMyCreatedIssues(ctx context.Context) ([]api.Issue, error) {
	user, err := r.GetCurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return []api.Issue{}, nil
	}
	issues, err := r.store.Queries().ListUserCreatedIssues(ctx, sql.NullString{String: user.ID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list user created issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetUserIssues(ctx context.Context, userID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListUserAssignedIssues(ctx, sql.NullString{String: userID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list user issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetMyActiveIssues(ctx context.Context) ([]api.Issue, error) {
	user, err := r.GetCurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return []api.Issue{}, nil
	}

	issues, err := r.store.Queries().ListUserActiveIssues(ctx, sql.NullString{String: user.ID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list my active issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

// =============================================================================
// States
// =============================================================================

func (r *SQLiteRepository) GetTeamStates(ctx context.Context, teamID string) ([]api.State, error) {
	states, err := r.store.Queries().ListTeamStates(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team states: %w", err)
	}
	return db.DBStatesToAPIStates(states), nil
}

func (r *SQLiteRepository) GetStateByName(ctx context.Context, teamID, name string) (*api.State, error) {
	return queryOne("get state by name",
		func() (db.State, error) {
			return r.store.Queries().GetStateByName(ctx, db.GetStateByNameParams{TeamID: teamID, Name: name})
		},
		pure(db.DBStateToAPIState))
}

// =============================================================================
// Labels
// =============================================================================

func (r *SQLiteRepository) GetTeamLabels(ctx context.Context, teamID string) ([]api.Label, error) {
	labels, err := r.store.Queries().ListTeamLabels(ctx, sql.NullString{String: teamID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list team labels: %w", err)
	}
	return db.DBLabelsToAPILabels(labels), nil
}

// GetProjectLabels returns the workspace project-label catalog, sorted by
// name. The converter populates Parent strictly as an ID from the parent_id
// column; the full catalog is in hand here, so parent names are stitched in
// one in-memory pass over the id→name map (the wire never carries them —
// see projectLabelFieldsFragment).
func (r *SQLiteRepository) GetProjectLabels(ctx context.Context) ([]api.ProjectLabel, error) {
	rows, err := r.store.Queries().ListProjectLabels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list project labels: %w", err)
	}
	labels := db.DBProjectLabelsToAPIProjectLabels(rows)
	byID := make(map[string]string, len(labels))
	for _, l := range labels {
		byID[l.ID] = l.Name
	}
	for i := range labels {
		if p := labels[i].Parent; p != nil {
			p.Name = byID[p.ID] // unknown parent stays name-less; render copes
		}
	}
	return labels, nil
}

func (r *SQLiteRepository) GetLabelByName(ctx context.Context, teamID, name string) (*api.Label, error) {
	return queryOne("get label by name",
		func() (db.Label, error) {
			return r.store.Queries().GetLabelByName(ctx, db.GetLabelByNameParams{
				TeamID: sql.NullString{String: teamID, Valid: true},
				Name:   name,
			})
		},
		pure(db.DBLabelToAPILabel))
}

// =============================================================================
// Users
// =============================================================================

func (r *SQLiteRepository) GetUsers(ctx context.Context) ([]api.User, error) {
	users, err := r.store.Queries().ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return db.DBUsersToAPIUsers(users), nil
}

func (r *SQLiteRepository) GetCurrentUser(ctx context.Context) (*api.User, error) {
	// Return cached user if set (via SetCurrentUser)
	if r.currentUser != nil {
		return r.currentUser, nil
	}

	// Current user must be set externally via SetCurrentUser
	// This is typically done during LinearFS initialization
	return nil, nil
}

func (r *SQLiteRepository) GetTeamMembers(ctx context.Context, teamID string) ([]api.User, error) {
	users, err := r.store.Queries().ListTeamMembers(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}
	return db.DBUsersToAPIUsers(users), nil
}

// =============================================================================
// Cycles
// =============================================================================

func (r *SQLiteRepository) GetTeamCycles(ctx context.Context, teamID string) ([]api.Cycle, error) {
	cycles, err := r.store.Queries().ListTeamCycles(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team cycles: %w", err)
	}
	return db.DBCyclesToAPICycles(cycles), nil
}

// =============================================================================
// Projects
// =============================================================================

func (r *SQLiteRepository) GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error) {
	projects, err := r.store.Queries().ListTeamProjects(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team projects: %w", err)
	}
	return db.DBProjectsToAPIProjects(projects)
}

func (r *SQLiteRepository) GetProjectByID(ctx context.Context, id string) (*api.Project, error) {
	return queryOne("get project by id",
		func() (db.Project, error) { return r.store.Queries().GetProject(ctx, id) },
		db.DBProjectToAPIProject)
}

func (r *SQLiteRepository) GetProjectPrimaryTeamKey(ctx context.Context, projectID string) (string, error) {
	key, err := r.store.Queries().GetProjectPrimaryTeamKey(ctx, projectID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("get project primary team key: %w", err)
	}
	return key, nil
}

// =============================================================================
// Project Milestones
// =============================================================================

func (r *SQLiteRepository) GetProjectMilestones(ctx context.Context, projectID string) ([]api.ProjectMilestone, error) {
	milestones, err := r.store.Queries().ListProjectMilestones(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project milestones: %w", err)
	}
	return db.DBMilestonesToAPIProjectMilestones(milestones), nil
}

// =============================================================================
// Comments
// =============================================================================

func (r *SQLiteRepository) GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error) {
	comments, err := r.store.Queries().ListIssueComments(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("list issue comments: %w", err)
	}

	return db.DBCommentsToAPIComments(comments)
}

// MaybeRefreshIssueDetails triggers a combined refresh of comments, documents,
// and attachments for an issue if any of them are stale. Uses a single API call
// via GetIssueDetails instead of three separate calls.
//
// This is NOT called automatically by Get* methods — callers in the FS layer
// should invoke it explicitly when the user browses into comments/, docs/, or
// attachments/ directories. This avoids triggering API calls when reading
// issue.md (which calls GetIssueAttachments for the links: frontmatter field).
func (r *SQLiteRepository) MaybeRefreshIssueDetails(issueID string) {
	// Both staleness inputs come from ONE fetch of the issues row:
	// detail_synced_at (the one per-issue detail-freshness fact, stamped
	// clean-gated by syncDetails and refreshIssueDetails) and updated_at (the
	// change event). NULL detail_synced_at → zero → stale, unchanged swrStale
	// semantics. The old shape — the min of three per-family MAX(synced_at)
	// aggregates — read every empty family (most issues have zero docs) as
	// "never synced", so each browse re-triggered a refetch that upserted
	// nothing: a permanent per-browse API loop.
	//
	// The fetch is memoized across the two spec closures (changedAt runs
	// first, then syncedAt, sequentially in maybeRefreshSWR) and stays lazy so
	// the module's nil-client check still precedes any query.
	var fresh db.GetIssueDetailFreshnessRow
	var freshErr error
	loaded := false
	load := func() {
		if !loaded {
			fresh, freshErr = r.store.Queries().GetIssueDetailFreshness(context.Background(), issueID)
			loaded = true
		}
	}
	r.maybeRefreshSWR(swrSpec{
		kind: kindIssueDetails,
		id:   issueID,
		syncedAt: func() (interface{}, error) {
			load()
			if freshErr != nil || !fresh.DetailSyncedAt.Valid {
				return nil, nil // never detail-synced → stale
			}
			return fresh.DetailSyncedAt.Time, nil
		},
		changedAt: func() (time.Time, bool) {
			load()
			// ok=false when the issue is not in the DB — discovery belongs to
			// the sync worker, so no refresh fires (as before).
			return fresh.UpdatedAt, freshErr == nil
		},
		refresh: func(ctx context.Context) error {
			return r.refreshIssueDetails(ctx, issueID)
		},
		orphan: func(ctx context.Context) { r.deleteOrphanIssue(ctx, issueID) },
	})
}

// deleteOrphanIssue removes an issue and all its sub-resources from SQLite.
// Called when Linear reports the issue no longer exists. Errors are logged
// but not propagated — partial cleanup beats no cleanup, and the caller has
// no recovery action available.
func (r *SQLiteRepository) deleteOrphanIssue(ctx context.Context, issueID string) {
	q := r.store.Queries()
	if err := q.DeleteIssueComments(ctx, issueID); err != nil {
		log.Printf("[repo] orphan cleanup: comments for %s: %v", issueID, err)
	}
	if err := q.DeleteIssueDocuments(ctx, sql.NullString{String: issueID, Valid: true}); err != nil {
		log.Printf("[repo] orphan cleanup: documents for %s: %v", issueID, err)
	}
	if err := q.DeleteIssueAttachments(ctx, issueID); err != nil {
		log.Printf("[repo] orphan cleanup: attachments for %s: %v", issueID, err)
	}
	if err := q.DeleteIssueEmbeddedFiles(ctx, issueID); err != nil {
		log.Printf("[repo] orphan cleanup: embedded files for %s: %v", issueID, err)
	}
	if err := q.DeleteIssueRelations(ctx, issueID); err != nil {
		log.Printf("[repo] orphan cleanup: relations for %s: %v", issueID, err)
	}
	if err := q.DeleteIssueHistoryCache(ctx, issueID); err != nil {
		log.Printf("[repo] orphan cleanup: history for %s: %v", issueID, err)
	}
	if err := q.DeletePendingDetailSync(ctx, issueID); err != nil {
		log.Printf("[repo] orphan cleanup: pending sync for %s: %v", issueID, err)
	}
	if err := q.DeleteIssue(ctx, issueID); err != nil {
		log.Printf("[repo] orphan cleanup: issue %s: %v", issueID, err)
		return
	}
	log.Printf("[repo] deleted orphan issue %s (no longer exists in Linear)", issueID)
	r.maybeScheduleReconcile()
}

// deleteOrphanProject removes a project and all its sub-resources from SQLite.
// Called when Linear reports the project no longer exists. Errors are logged
// but not propagated — partial cleanup beats no cleanup, and the caller has
// no recovery action available. Does not modify the issues.project_id column
// on issues that referenced this project — those rows stay until the issue is
// next synced.
func (r *SQLiteRepository) deleteOrphanProject(ctx context.Context, projectID string) {
	q := r.store.Queries()
	if err := q.DeleteProjectTeams(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: project teams for %s: %v", projectID, err)
	}
	if err := q.DeleteProjectDocuments(ctx, sql.NullString{String: projectID, Valid: true}); err != nil {
		log.Printf("[repo] orphan cleanup: project documents for %s: %v", projectID, err)
	}
	if err := q.DeleteProjectUpdates(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: project updates for %s: %v", projectID, err)
	}
	if err := q.DeleteProjectMilestones(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: project milestones for %s: %v", projectID, err)
	}
	if err := q.DeleteInitiativeProjectsByProject(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: initiative-project links for %s: %v", projectID, err)
	}
	if err := q.DeleteProject(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: project %s: %v", projectID, err)
		return
	}
	log.Printf("[repo] deleted orphan project %s (no longer exists in Linear)", projectID)
	r.maybeScheduleReconcile()
}

// deleteOrphanInitiative removes an initiative and all its sub-resources from SQLite.
// Called when Linear reports the initiative no longer exists. Errors are logged
// but not propagated — partial cleanup beats no cleanup, and the caller has no
// recovery action available.
func (r *SQLiteRepository) deleteOrphanInitiative(ctx context.Context, initiativeID string) {
	q := r.store.Queries()
	if err := q.DeleteInitiativeDocuments(ctx, sql.NullString{String: initiativeID, Valid: true}); err != nil {
		log.Printf("[repo] orphan cleanup: initiative documents for %s: %v", initiativeID, err)
	}
	if err := q.DeleteInitiativeUpdates(ctx, initiativeID); err != nil {
		log.Printf("[repo] orphan cleanup: initiative updates for %s: %v", initiativeID, err)
	}
	if err := q.DeleteInitiativeProjects(ctx, initiativeID); err != nil {
		log.Printf("[repo] orphan cleanup: initiative-project links for %s: %v", initiativeID, err)
	}
	if err := q.DeleteInitiative(ctx, initiativeID); err != nil {
		log.Printf("[repo] orphan cleanup: initiative %s: %v", initiativeID, err)
		return
	}
	log.Printf("[repo] deleted orphan initiative %s (no longer exists in Linear)", initiativeID)
	r.maybeScheduleReconcile()
}

// refreshIssueDetails fetches comments, documents, attachments, and relations
// in a single API call and persists them through reconcile.PersistIssueDetails
// — the same five-collection tail the sync worker uses, so the SWR path gets
// prunes-when-complete, the clean guard, and embedded-file extraction, which
// the old hand-rolled upsert loops all lacked. The cutoff is taken BEFORE the
// fetch so rows written mid-flight survive pruning. The clean return gates
// the detail_synced_at stamp (symmetric with the worker's syncDetails): an
// unclean pass stays unstamped, so it reads stale and retriggers.
func (r *SQLiteRepository) refreshIssueDetails(ctx context.Context, issueID string) error {
	pruneCutoff := db.Now()
	details, err := r.client.GetIssueDetails(ctx, issueID)
	if err != nil {
		return err
	}

	deps := reconcile.Deps{Q: r.store.Queries()}
	if r.extractor != nil {
		deps.Extract = r.extractor.ExtractAndStore
	}
	if clean := reconcile.PersistIssueDetails(ctx, deps, issueID, details, pruneCutoff); clean {
		// db.Now() at stamp time: the stamp only needs to exceed the issue's
		// updated_at as-of-fetch, and now > pruneCutoff > that.
		if err := r.store.Queries().StampIssueDetailSynced(ctx, db.StampIssueDetailSyncedParams{
			DetailSyncedAt: db.ToNullTime(db.Now()),
			ID:             issueID,
		}); err != nil {
			log.Printf("[repo] stamp detail synced %s: %v", issueID, err)
		}
	}
	return nil
}

// parseTime converts interface{} from SQLite to time.Time (see db.ParseSQLiteTimeAny).
// Returns zero time for nil (no rows exist).
func parseTime(v interface{}) time.Time {
	return db.ParseSQLiteTimeAny(v)
}

// =============================================================================
// Documents
// =============================================================================

func (r *SQLiteRepository) GetIssueDocuments(ctx context.Context, issueID string) ([]api.Document, error) {
	docs, err := r.store.Queries().ListIssueDocuments(ctx, sql.NullString{String: issueID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list issue documents: %w", err)
	}

	return db.DBDocumentsToAPIDocuments(docs)
}

func (r *SQLiteRepository) GetProjectDocuments(ctx context.Context, projectID string) ([]api.Document, error) {
	docs, err := r.store.Queries().ListProjectDocuments(ctx, sql.NullString{String: projectID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list project documents: %w", err)
	}

	r.maybeRefreshSWR(swrSpec{
		kind: kindProjectDocs,
		id:   projectID,
		syncedAt: func() (interface{}, error) {
			return r.store.Queries().GetProjectDocumentsSyncedAt(context.Background(), sql.NullString{String: projectID, Valid: true})
		},
		refresh: func(ctx context.Context) error {
			return r.refreshProjectDocuments(ctx, projectID)
		},
		orphan: func(ctx context.Context) { r.deleteOrphanProject(ctx, projectID) },
	})

	return db.DBDocumentsToAPIDocuments(docs)
}

// refreshProjectDocuments fetches documents from API and stores in SQLite.
// Upsert-only (nil Prune): nothing licenses a prune for this fetch.
func (r *SQLiteRepository) refreshProjectDocuments(ctx context.Context, projectID string) error {
	docs, err := r.client.GetProjectDocuments(ctx, projectID)
	if err != nil {
		return err
	}

	reconcile.Collection(ctx, reconcile.CollectionSpec[api.Document]{
		Label: "project document " + projectID,
		Kind:  "document",
		Items: docs,
		Upsert: func(ctx context.Context, doc api.Document) error {
			params, err := db.APIDocumentToDBDocument(doc)
			if err != nil {
				return err
			}
			return r.store.Queries().UpsertDocument(ctx, params)
		},
	})
	return nil
}

func (r *SQLiteRepository) GetInitiativeDocuments(ctx context.Context, initiativeID string) ([]api.Document, error) {
	docs, err := r.store.Queries().ListInitiativeDocuments(ctx, sql.NullString{String: initiativeID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list initiative documents: %w", err)
	}

	r.maybeRefreshSWR(swrSpec{
		kind: kindInitiativeDocs,
		id:   initiativeID,
		syncedAt: func() (interface{}, error) {
			return r.store.Queries().GetInitiativeDocumentsSyncedAt(context.Background(), sql.NullString{String: initiativeID, Valid: true})
		},
		refresh: func(ctx context.Context) error {
			return r.refreshInitiativeDocuments(ctx, initiativeID)
		},
		orphan: func(ctx context.Context) { r.deleteOrphanInitiative(ctx, initiativeID) },
	})

	return db.DBDocumentsToAPIDocuments(docs)
}

// refreshInitiativeDocuments fetches documents from API and stores in SQLite.
// Upsert-only (nil Prune): nothing licenses a prune for this fetch.
func (r *SQLiteRepository) refreshInitiativeDocuments(ctx context.Context, initiativeID string) error {
	docs, err := r.client.GetInitiativeDocuments(ctx, initiativeID)
	if err != nil {
		return err
	}

	reconcile.Collection(ctx, reconcile.CollectionSpec[api.Document]{
		Label: "initiative document " + initiativeID,
		Kind:  "document",
		Items: docs,
		Upsert: func(ctx context.Context, doc api.Document) error {
			params, err := db.APIDocumentToDBDocument(doc)
			if err != nil {
				return err
			}
			return r.store.Queries().UpsertDocument(ctx, params)
		},
	})
	return nil
}

// =============================================================================
// Initiatives
// =============================================================================

func (r *SQLiteRepository) GetInitiatives(ctx context.Context) ([]api.Initiative, error) {
	initiatives, err := r.store.Queries().ListInitiatives(ctx)
	if err != nil {
		return nil, fmt.Errorf("list initiatives: %w", err)
	}
	return db.DBInitiativesToAPIInitiatives(initiatives)
}

// =============================================================================
// Status Updates
// =============================================================================

func (r *SQLiteRepository) GetProjectUpdates(ctx context.Context, projectID string) ([]api.ProjectUpdate, error) {
	updates, err := r.store.Queries().ListProjectUpdates(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project updates: %w", err)
	}

	r.maybeRefreshSWR(swrSpec{
		kind: kindProjectUpdates,
		id:   projectID,
		syncedAt: func() (interface{}, error) {
			return r.store.Queries().GetProjectUpdatesSyncedAt(context.Background(), projectID)
		},
		refresh: func(ctx context.Context) error {
			return r.refreshProjectUpdates(ctx, projectID)
		},
		orphan: func(ctx context.Context) { r.deleteOrphanProject(ctx, projectID) },
	})

	return db.DBProjectUpdatesToAPIUpdates(updates)
}

// refreshProjectUpdates fetches updates from API and stores in SQLite.
// Upsert-only (nil Prune): nothing licenses a prune for this fetch.
func (r *SQLiteRepository) refreshProjectUpdates(ctx context.Context, projectID string) error {
	updates, err := r.client.GetProjectUpdates(ctx, projectID)
	if err != nil {
		return err
	}

	reconcile.Collection(ctx, reconcile.CollectionSpec[api.ProjectUpdate]{
		Label: "project update " + projectID,
		Kind:  "project-update",
		Items: updates,
		Upsert: func(ctx context.Context, update api.ProjectUpdate) error {
			params, err := db.APIProjectUpdateToDBUpdate(update, projectID)
			if err != nil {
				return err
			}
			return r.store.Queries().UpsertProjectUpdate(ctx, params)
		},
	})
	return nil
}

func (r *SQLiteRepository) GetInitiativeUpdates(ctx context.Context, initiativeID string) ([]api.InitiativeUpdate, error) {
	updates, err := r.store.Queries().ListInitiativeUpdates(ctx, initiativeID)
	if err != nil {
		return nil, fmt.Errorf("list initiative updates: %w", err)
	}

	r.maybeRefreshSWR(swrSpec{
		kind: kindInitiativeUpdates,
		id:   initiativeID,
		syncedAt: func() (interface{}, error) {
			return r.store.Queries().GetInitiativeUpdatesSyncedAt(context.Background(), initiativeID)
		},
		refresh: func(ctx context.Context) error {
			return r.refreshInitiativeUpdates(ctx, initiativeID)
		},
		orphan: func(ctx context.Context) { r.deleteOrphanInitiative(ctx, initiativeID) },
	})

	return db.DBInitiativeUpdatesToAPIUpdates(updates)
}

// refreshInitiativeUpdates fetches updates from API and stores in SQLite.
// Upsert-only (nil Prune): nothing licenses a prune for this fetch.
func (r *SQLiteRepository) refreshInitiativeUpdates(ctx context.Context, initiativeID string) error {
	updates, err := r.client.GetInitiativeUpdates(ctx, initiativeID)
	if err != nil {
		return err
	}

	reconcile.Collection(ctx, reconcile.CollectionSpec[api.InitiativeUpdate]{
		Label: "initiative update " + initiativeID,
		Kind:  "initiative-update",
		Items: updates,
		Upsert: func(ctx context.Context, update api.InitiativeUpdate) error {
			params, err := db.APIInitiativeUpdateToDBUpdate(update, initiativeID)
			if err != nil {
				return err
			}
			return r.store.Queries().UpsertInitiativeUpdate(ctx, params)
		},
	})
	return nil
}

// SetCurrentUser sets the cached current user (useful for testing)
func (r *SQLiteRepository) SetCurrentUser(user *api.User) {
	r.currentUser = user
}

// =============================================================================
// Attachments
// =============================================================================

func (r *SQLiteRepository) GetIssueAttachments(ctx context.Context, issueID string) ([]api.Attachment, error) {
	attachments, err := r.store.Queries().ListIssueAttachments(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("list issue attachments: %w", err)
	}

	return db.DBAttachmentsToAPIAttachments(attachments)
}

func (r *SQLiteRepository) GetIssueEmbeddedFiles(ctx context.Context, issueID string) ([]api.EmbeddedFile, error) {
	files, err := r.store.Queries().ListIssueEmbeddedFiles(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("list issue embedded files: %w", err)
	}
	return db.DBEmbeddedFilesToAPIFiles(files), nil
}

func (r *SQLiteRepository) UpdateEmbeddedFileCache(ctx context.Context, id, cachePath string, size int64) error {
	return r.store.Queries().UpdateEmbeddedFileCache(ctx, db.UpdateEmbeddedFileCacheParams{
		CachePath: sql.NullString{String: cachePath, Valid: cachePath != ""},
		FileSize:  sql.NullInt64{Int64: size, Valid: true},
		ID:        id,
	})
}

// =============================================================================
// Issue History
// =============================================================================

func (r *SQLiteRepository) GetIssueHistory(ctx context.Context, issueID string) ([]api.IssueHistoryEntry, error) {
	cache, err := r.store.Queries().GetIssueHistoryCache(ctx, issueID)
	if err == nil {
		// Have cached data — event-driven refresh if the issue changed after
		// the history was cached.
		spec := r.historySpec(issueID)
		spec.syncedAt = func() (interface{}, error) { return cache.SyncedAt, nil }
		spec.changedAt = r.issueChangedAt(issueID)
		r.maybeRefreshSWR(spec)

		var entries []api.IssueHistoryEntry
		if err := json.Unmarshal(cache.Data, &entries); err != nil {
			return nil, fmt.Errorf("unmarshal history cache: %w", err)
		}
		return entries, nil
	}

	// No cached data — trigger a background fetch and return empty immediately.
	// This avoids blocking the FUSE dispatch goroutine on a cold-cache API call.
	// History will be available on next access once the background fetch
	// completes. syncedAt reporting nil = never synced, so the spec always
	// reads stale here (no GetIssueUpdatedAt gate: an issue unknown to the DB
	// still gets its cold fetch, as before).
	spec := r.historySpec(issueID)
	spec.syncedAt = func() (interface{}, error) { return nil, nil }
	r.maybeRefreshSWR(spec)
	return nil, nil
}

// historySpec is the single constructor behind both history refresh call
// sites in GetIssueHistory — the cold-cache trigger and the warm event-driven
// check (whose fetch closures used to be pasted verbatim). Callers attach the
// staleness inputs (syncedAt/changedAt) they own.
func (r *SQLiteRepository) historySpec(issueID string) swrSpec {
	return swrSpec{
		kind: kindHistory,
		id:   issueID,
		refresh: func(ctx context.Context) error {
			entries, err := r.client.GetIssueHistory(ctx, issueID)
			if err != nil {
				return err
			}
			r.upsertHistoryCache(ctx, issueID, entries)
			return nil
		},
		orphan: func(ctx context.Context) { r.deleteOrphanIssue(ctx, issueID) },
	}
}

func (r *SQLiteRepository) upsertHistoryCache(ctx context.Context, issueID string, entries []api.IssueHistoryEntry) {
	data, err := json.Marshal(entries)
	if err != nil {
		log.Printf("[repo] marshal history for %s failed: %v", issueID, err)
		return
	}
	if err := r.store.Queries().UpsertIssueHistoryCache(ctx, db.UpsertIssueHistoryCacheParams{
		IssueID:  issueID,
		SyncedAt: time.Now(),
		Data:     data,
	}); err != nil {
		log.Printf("[repo] upsert history cache %s failed: %v", issueID, err)
	}
}

// =============================================================================
// Issue Relations
// =============================================================================

// GetIssueRelations returns all relations for an issue (outgoing)
// relationDir picks which end of a stored relation is the "other" issue — the
// one placed in the view and enriched. Outgoing relations (this issue → target)
// fill RelatedIssue from related_issue_id; incoming/inverse relations
// (source → this issue) fill Issue from issue_id.
type relationDir int

const (
	relOutgoing relationDir = iota
	relIncoming
)

// relationView maps one stored db.IssueRelation to an api.IssueRelation, placing
// the other end in the field the direction dictates and enriching it with the
// issue's identifier/title. It is the one converter behind both relation
// reads (outgoing, inverse), which were byte-identical but for this
// direction — so a field added to the mapping now lives in exactly one place.
func (r *SQLiteRepository) relationView(ctx context.Context, rel db.IssueRelation, dir relationDir) api.IssueRelation {
	view := api.IssueRelation{
		ID:        rel.ID,
		Type:      rel.Type,
		CreatedAt: rel.CreatedAt.Time,
		UpdatedAt: rel.UpdatedAt.Time,
	}
	otherID := rel.RelatedIssueID
	if dir == relIncoming {
		otherID = rel.IssueID
	}
	end := &api.ParentIssue{ID: otherID}
	if issue, err := r.GetIssueByID(ctx, otherID); err == nil && issue != nil {
		end.Identifier = issue.Identifier
		end.Title = issue.Title
	}
	if dir == relIncoming {
		view.Issue = end
	} else {
		view.RelatedIssue = end
	}
	return view
}

func (r *SQLiteRepository) GetIssueRelations(ctx context.Context, issueID string) ([]api.IssueRelation, error) {
	relations, err := r.store.Queries().ListIssueRelations(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("list issue relations: %w", err)
	}
	result := make([]api.IssueRelation, len(relations))
	for i, rel := range relations {
		result[i] = r.relationView(ctx, rel, relOutgoing)
	}
	return result, nil
}

// GetIssueInverseRelations returns all inverse relations (incoming)
func (r *SQLiteRepository) GetIssueInverseRelations(ctx context.Context, issueID string) ([]api.IssueRelation, error) {
	relations, err := r.store.Queries().ListIssueInverseRelations(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("list issue inverse relations: %w", err)
	}
	result := make([]api.IssueRelation, len(relations))
	for i, rel := range relations {
		result[i] = r.relationView(ctx, rel, relIncoming)
	}
	return result, nil
}
