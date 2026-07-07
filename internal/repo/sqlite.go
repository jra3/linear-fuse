package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
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

	// Track in-flight refreshes to avoid duplicate API calls
	refreshMu      sync.Mutex
	refreshing     map[string]bool
	refreshContext context.Context
	refreshCancel  context.CancelFunc

	// Semaphore to limit concurrent background refreshes
	refreshSem chan struct{}

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
	return &SQLiteRepository{
		store:              store,
		client:             client,
		stalenessThreshold: defaultStalenessThreshold,
		refreshing:         make(map[string]bool),
		refreshContext:     ctx,
		refreshCancel:      cancel,
		refreshSem:         make(chan struct{}, maxConcurrentRefreshes),
	}
}

// SetStalenessThreshold sets how long before on-demand data is considered stale
func (r *SQLiteRepository) SetStalenessThreshold(d time.Duration) {
	r.stalenessThreshold = d
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
func (r *SQLiteRepository) triggerBackgroundRefresh(key string, refreshFn func(context.Context) error) {
	if r.client == nil {
		return
	}

	r.refreshMu.Lock()
	if r.refreshing[key] {
		r.refreshMu.Unlock()
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
		return
	}

	go func() {
		defer func() {
			<-r.refreshSem
			r.refreshMu.Lock()
			delete(r.refreshing, key)
			r.refreshMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(r.refreshContext, refreshTimeout)
		defer cancel()
		if err := refreshFn(ctx); err != nil {
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
func (r *SQLiteRepository) reconcileIssues(ctx context.Context) int {
	teams, err := r.store.Queries().ListTeams(ctx)
	if err != nil {
		log.Printf("[reconcile] list teams: %v", err)
		return 0
	}
	deleted := 0
	for _, team := range teams {
		if r.client.LowBudget() {
			log.Printf("[reconcile] budget low; deferring remaining teams")
			return deleted
		}
		apiIDs, err := r.client.GetTeamIssueIDs(ctx, team.ID)
		if err != nil {
			log.Printf("[reconcile] issues team %s: %v (skipping)", team.Key, err)
			continue
		}
		deleted += r.reconcileIssuesForTeam(ctx, team.ID, apiIDs)
	}
	return deleted
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

func (r *SQLiteRepository) GetTeamByKey(ctx context.Context, key string) (*api.Team, error) {
	team, err := r.store.Queries().GetTeamByKey(ctx, key)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get team by key: %w", err)
	}
	result := db.DBTeamToAPITeam(team)
	return &result, nil
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
	issue, err := r.store.Queries().GetIssueByIdentifier(ctx, identifier)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get issue by identifier: %w", err)
	}
	result, err := db.DBIssueToAPIIssue(issue)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *SQLiteRepository) GetIssueByID(ctx context.Context, id string) (*api.Issue, error) {
	issue, err := r.store.Queries().GetIssueByID(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get issue by id: %w", err)
	}
	result, err := db.DBIssueToAPIIssue(issue)
	if err != nil {
		return nil, err
	}
	return &result, nil
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

func (r *SQLiteRepository) GetIssuesByPriority(ctx context.Context, teamID string, priority int) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssuesByPriority(ctx, db.ListTeamIssuesByPriorityParams{
		TeamID:   teamID,
		Priority: sql.NullInt64{Int64: int64(priority), Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list issues by priority: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

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
	state, err := r.store.Queries().GetStateByName(ctx, db.GetStateByNameParams{
		TeamID: teamID,
		Name:   name,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get state by name: %w", err)
	}
	result := db.DBStateToAPIState(state)
	return &result, nil
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

func (r *SQLiteRepository) GetLabelByName(ctx context.Context, teamID, name string) (*api.Label, error) {
	label, err := r.store.Queries().GetLabelByName(ctx, db.GetLabelByNameParams{
		TeamID: sql.NullString{String: teamID, Valid: true},
		Name:   name,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get label by name: %w", err)
	}
	result := db.DBLabelToAPILabel(label)
	return &result, nil
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

func (r *SQLiteRepository) GetUserByID(ctx context.Context, id string) (*api.User, error) {
	user, err := r.store.Queries().GetUser(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	result := db.DBUserToAPIUser(user)
	return &result, nil
}

func (r *SQLiteRepository) GetUserByEmail(ctx context.Context, email string) (*api.User, error) {
	user, err := r.store.Queries().GetUserByEmail(ctx, email)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	result := db.DBUserToAPIUser(user)
	return &result, nil
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

func (r *SQLiteRepository) GetCycleByName(ctx context.Context, teamID, name string) (*api.Cycle, error) {
	cycle, err := r.store.Queries().GetCycleByName(ctx, db.GetCycleByNameParams{
		TeamID: teamID,
		Name:   sql.NullString{String: name, Valid: true},
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get cycle by name: %w", err)
	}
	result := db.DBCycleToAPICycle(cycle)
	return &result, nil
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

func (r *SQLiteRepository) GetProjectBySlug(ctx context.Context, slug string) (*api.Project, error) {
	project, err := r.store.Queries().GetProjectBySlug(ctx, slug)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get project by slug: %w", err)
	}
	result, err := db.DBProjectToAPIProject(project)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *SQLiteRepository) GetProjectByID(ctx context.Context, id string) (*api.Project, error) {
	project, err := r.store.Queries().GetProject(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get project by id: %w", err)
	}
	result, err := db.DBProjectToAPIProject(project)
	if err != nil {
		return nil, err
	}
	return &result, nil
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

func (r *SQLiteRepository) GetMilestoneByName(ctx context.Context, projectID, name string) (*api.ProjectMilestone, error) {
	milestone, err := r.store.Queries().GetMilestoneByName(ctx, db.GetMilestoneByNameParams{
		ProjectID: projectID,
		Name:      name,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get milestone by name: %w", err)
	}
	result := db.DBMilestoneToAPIProjectMilestone(milestone)
	return &result, nil
}

func (r *SQLiteRepository) GetMilestoneByID(ctx context.Context, id string) (*api.ProjectMilestone, error) {
	milestone, err := r.store.Queries().GetProjectMilestone(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get milestone by id: %w", err)
	}
	result := db.DBMilestoneToAPIProjectMilestone(milestone)
	return &result, nil
}

func (r *SQLiteRepository) CreateProjectMilestone(ctx context.Context, projectID, name, description string) (*api.ProjectMilestone, error) {
	if r.client == nil {
		return nil, fmt.Errorf("API client not available")
	}

	// Create via API
	milestone, err := r.client.CreateProjectMilestone(ctx, projectID, name, description)
	if err != nil {
		return nil, fmt.Errorf("create milestone: %w", err)
	}

	// Upsert to SQLite for immediate visibility. Route through the forward
	// converter so the full milestone lands in `data` — a hand-built params with
	// Data:"{}" would round-trip to a milestone stripped of any JSON-only field.
	if params, cerr := db.APIProjectMilestoneToDBMilestone(*milestone, projectID); cerr != nil {
		log.Printf("[repo] convert milestone %s failed: %v", milestone.ID, cerr)
	} else if err := r.store.Queries().UpsertProjectMilestone(ctx, params); err != nil {
		log.Printf("[repo] upsert milestone %s failed: %v", milestone.ID, err)
	}

	return milestone, nil
}

func (r *SQLiteRepository) UpdateProjectMilestone(ctx context.Context, milestoneID string, input api.ProjectMilestoneUpdateInput) (*api.ProjectMilestone, error) {
	if r.client == nil {
		return nil, fmt.Errorf("API client not available")
	}

	// Get the current milestone to find the project ID
	existing, err := r.store.Queries().GetProjectMilestone(ctx, milestoneID)
	if err != nil {
		return nil, fmt.Errorf("get milestone: %w", err)
	}

	// Update via API
	milestone, err := r.client.UpdateProjectMilestone(ctx, milestoneID, input)
	if err != nil {
		return nil, fmt.Errorf("update milestone: %w", err)
	}

	// Upsert to SQLite for immediate visibility (see CreateProjectMilestone: the
	// full milestone must land in `data`, not "{}").
	if params, cerr := db.APIProjectMilestoneToDBMilestone(*milestone, existing.ProjectID); cerr != nil {
		log.Printf("[repo] convert milestone %s failed: %v", milestone.ID, cerr)
	} else if err := r.store.Queries().UpsertProjectMilestone(ctx, params); err != nil {
		log.Printf("[repo] upsert milestone %s failed: %v", milestone.ID, err)
	}

	return milestone, nil
}

func (r *SQLiteRepository) DeleteProjectMilestone(ctx context.Context, milestoneID string) error {
	if r.client == nil {
		return fmt.Errorf("API client not available")
	}

	// Delete via API
	if err := r.client.DeleteProjectMilestone(ctx, milestoneID); err != nil {
		return fmt.Errorf("delete milestone: %w", err)
	}

	// Delete from SQLite
	if err := r.store.Queries().DeleteProjectMilestone(ctx, milestoneID); err != nil {
		log.Printf("[repo] delete milestone %s from DB failed: %v", milestoneID, err)
	}

	return nil
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
	if r.client == nil {
		return
	}

	bgCtx := context.Background()

	// Get issue's updated_at
	issueUpdatedAt, err := r.store.Queries().GetIssueUpdatedAt(bgCtx, issueID)
	if err != nil {
		// Issue not in DB yet — let sync worker handle it
		return
	}

	// Get sub-resource synced_at timestamps (MAX across rows, or nil if none)
	commentsSyncedAt, _ := r.store.Queries().GetIssueCommentsSyncedAt(bgCtx, issueID)
	docsSyncedAt, _ := r.store.Queries().GetIssueDocumentsSyncedAt(bgCtx, sql.NullString{String: issueID, Valid: true})
	attachSyncedAt, _ := r.store.Queries().GetIssueAttachmentsSyncedAt(bgCtx, issueID)

	// Parse timestamps (handle SQLite space-separated format + nil for empty tables)
	issueTime := issueUpdatedAt
	commentsTime := parseTime(commentsSyncedAt)
	docsTime := parseTime(docsSyncedAt)
	attachTime := parseTime(attachSyncedAt)

	// Refresh if issue changed after last sync OR never synced (zero time)
	commentsStale := commentsTime.IsZero() || issueTime.After(commentsTime)
	docsStale := docsTime.IsZero() || issueTime.After(docsTime)
	attachStale := attachTime.IsZero() || issueTime.After(attachTime)

	if commentsStale || docsStale || attachStale {
		r.triggerBackgroundRefresh("issue-details:"+issueID, func(ctx context.Context) error {
			return r.refreshIssueDetails(ctx, issueID)
		})
	}
}

// isEntityNotFound reports whether err is Linear's "Entity not found" GraphQL
// error, indicating the issue (or other entity) no longer exists upstream.
// When seen on a refresh, the local row is an orphan and should be deleted —
// otherwise every FUSE traversal retriggers the same failing refresh forever.
func isEntityNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Entity not found")
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

// refreshIssueDetails fetches comments, documents, and attachments in a single
// API call and stores them all in SQLite.
func (r *SQLiteRepository) refreshIssueDetails(ctx context.Context, issueID string) error {
	details, err := r.client.GetIssueDetails(ctx, issueID)
	if err != nil {
		if isEntityNotFound(err) {
			r.deleteOrphanIssue(ctx, issueID)
		}
		return err
	}

	for _, comment := range details.Comments {
		params, err := db.APICommentToDBComment(comment, issueID)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertComment(ctx, params); err != nil {
			log.Printf("[repo] upsert comment %s failed: %v", comment.ID, err)
		}
	}

	for _, doc := range details.Documents {
		params, err := db.APIDocumentToDBDocument(doc)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertDocument(ctx, params); err != nil {
			log.Printf("[repo] upsert document %s failed: %v", doc.ID, err)
		}
	}

	for _, attachment := range details.Attachments {
		params, err := db.APIAttachmentToDBAttachment(attachment, issueID)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertAttachment(ctx, params); err != nil {
			log.Printf("[repo] upsert attachment %s failed: %v", attachment.ID, err)
		}
	}

	return nil
}

// parseTime converts interface{} from SQLite to time.Time (see db.ParseSQLiteTimeAny).
// Returns zero time for nil (no rows exist).
func parseTime(v interface{}) time.Time {
	return db.ParseSQLiteTimeAny(v)
}

func (r *SQLiteRepository) GetCommentByID(ctx context.Context, id string) (*api.Comment, error) {
	comment, err := r.store.Queries().GetComment(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get comment by id: %w", err)
	}
	result, err := db.DBCommentToAPIComment(comment)
	if err != nil {
		return nil, err
	}
	return &result, nil
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

// staleSince reports whether a cached entity's last-sync instant is older than
// threshold. A query error or a nil instant (never synced) counts as stale, so
// the caller refreshes. Pure, so the parseTime/threshold rule — historically a
// source of timezone-comparison bugs — is unit-tested directly.
func staleSince(syncedAt interface{}, err error, threshold time.Duration) bool {
	return err != nil || syncedAt == nil || time.Since(parseTime(syncedAt)) > threshold
}

// maybeRefresh triggers a deduplicated background refresh when an entity's
// cached rows are staler than the threshold. syncedAt returns the entity's
// last-sync instant, key dedups concurrent refreshes, and refresh does the
// fetch-and-upsert. In fixture mode (nil client) it never fires. The refresh
// runs in the background so a directory listing (e.g. find) never blocks on
// the API. This owns the staleness/trigger policy the four Get*Documents and
// Get*Updates read paths used to each restate.
func (r *SQLiteRepository) maybeRefresh(key string, syncedAt func() (interface{}, error), refresh func(context.Context) error) {
	if r.client == nil {
		return
	}
	ts, err := syncedAt()
	if staleSince(ts, err, r.stalenessThreshold) {
		r.triggerBackgroundRefresh(key, refresh)
	}
}

func (r *SQLiteRepository) GetProjectDocuments(ctx context.Context, projectID string) ([]api.Document, error) {
	docs, err := r.store.Queries().ListProjectDocuments(ctx, sql.NullString{String: projectID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list project documents: %w", err)
	}

	r.maybeRefresh("project-docs:"+projectID, func() (interface{}, error) {
		return r.store.Queries().GetProjectDocumentsSyncedAt(context.Background(), sql.NullString{String: projectID, Valid: true})
	}, func(ctx context.Context) error {
		return r.refreshProjectDocuments(ctx, projectID)
	})

	return db.DBDocumentsToAPIDocuments(docs)
}

// refreshProjectDocuments fetches documents from API and stores in SQLite
func (r *SQLiteRepository) refreshProjectDocuments(ctx context.Context, projectID string) error {
	docs, err := r.client.GetProjectDocuments(ctx, projectID)
	if err != nil {
		if isEntityNotFound(err) {
			r.deleteOrphanProject(ctx, projectID)
		}
		return err
	}

	for _, doc := range docs {
		params, err := db.APIDocumentToDBDocument(doc)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertDocument(ctx, params); err != nil {
			log.Printf("[repo] upsert document %s failed: %v", doc.ID, err)
		}
	}
	return nil
}

func (r *SQLiteRepository) GetInitiativeDocuments(ctx context.Context, initiativeID string) ([]api.Document, error) {
	docs, err := r.store.Queries().ListInitiativeDocuments(ctx, sql.NullString{String: initiativeID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list initiative documents: %w", err)
	}

	r.maybeRefresh("initiative-docs:"+initiativeID, func() (interface{}, error) {
		return r.store.Queries().GetInitiativeDocumentsSyncedAt(context.Background(), sql.NullString{String: initiativeID, Valid: true})
	}, func(ctx context.Context) error {
		return r.refreshInitiativeDocuments(ctx, initiativeID)
	})

	return db.DBDocumentsToAPIDocuments(docs)
}

// refreshInitiativeDocuments fetches documents from API and stores in SQLite
func (r *SQLiteRepository) refreshInitiativeDocuments(ctx context.Context, initiativeID string) error {
	docs, err := r.client.GetInitiativeDocuments(ctx, initiativeID)
	if err != nil {
		if isEntityNotFound(err) {
			r.deleteOrphanInitiative(ctx, initiativeID)
		}
		return err
	}

	for _, doc := range docs {
		params, err := db.APIDocumentToDBDocument(doc)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertDocument(ctx, params); err != nil {
			log.Printf("[repo] upsert document %s failed: %v", doc.ID, err)
		}
	}
	return nil
}

func (r *SQLiteRepository) GetDocumentBySlug(ctx context.Context, slug string) (*api.Document, error) {
	doc, err := r.store.Queries().GetDocumentBySlug(ctx, slug)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get document by slug: %w", err)
	}
	result, err := db.DBDocumentToAPIDocument(doc)
	if err != nil {
		return nil, err
	}
	return &result, nil
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

func (r *SQLiteRepository) GetInitiativeBySlug(ctx context.Context, slug string) (*api.Initiative, error) {
	initiative, err := r.store.Queries().GetInitiativeBySlug(ctx, slug)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get initiative by slug: %w", err)
	}
	result, err := db.DBInitiativeToAPIInitiative(initiative)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *SQLiteRepository) GetInitiativeProjects(ctx context.Context, initiativeID string) ([]api.Project, error) {
	projects, err := r.store.Queries().ListInitiativeProjects(ctx, initiativeID)
	if err != nil {
		return nil, fmt.Errorf("list initiative projects: %w", err)
	}
	return db.DBProjectsToAPIProjects(projects)
}

// =============================================================================
// Status Updates
// =============================================================================

func (r *SQLiteRepository) GetProjectUpdates(ctx context.Context, projectID string) ([]api.ProjectUpdate, error) {
	updates, err := r.store.Queries().ListProjectUpdates(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project updates: %w", err)
	}

	r.maybeRefresh("project-updates:"+projectID, func() (interface{}, error) {
		return r.store.Queries().GetProjectUpdatesSyncedAt(context.Background(), projectID)
	}, func(ctx context.Context) error {
		return r.refreshProjectUpdates(ctx, projectID)
	})

	return db.DBProjectUpdatesToAPIUpdates(updates)
}

// refreshProjectUpdates fetches updates from API and stores in SQLite
func (r *SQLiteRepository) refreshProjectUpdates(ctx context.Context, projectID string) error {
	updates, err := r.client.GetProjectUpdates(ctx, projectID)
	if err != nil {
		if isEntityNotFound(err) {
			r.deleteOrphanProject(ctx, projectID)
		}
		return err
	}

	for _, update := range updates {
		params, err := db.APIProjectUpdateToDBUpdate(update, projectID)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertProjectUpdate(ctx, params); err != nil {
			log.Printf("[repo] upsert project update %s failed: %v", update.ID, err)
		}
	}
	return nil
}

func (r *SQLiteRepository) GetInitiativeUpdates(ctx context.Context, initiativeID string) ([]api.InitiativeUpdate, error) {
	updates, err := r.store.Queries().ListInitiativeUpdates(ctx, initiativeID)
	if err != nil {
		return nil, fmt.Errorf("list initiative updates: %w", err)
	}

	r.maybeRefresh("initiative-updates:"+initiativeID, func() (interface{}, error) {
		return r.store.Queries().GetInitiativeUpdatesSyncedAt(context.Background(), initiativeID)
	}, func(ctx context.Context) error {
		return r.refreshInitiativeUpdates(ctx, initiativeID)
	})

	return db.DBInitiativeUpdatesToAPIUpdates(updates)
}

// refreshInitiativeUpdates fetches updates from API and stores in SQLite
func (r *SQLiteRepository) refreshInitiativeUpdates(ctx context.Context, initiativeID string) error {
	updates, err := r.client.GetInitiativeUpdates(ctx, initiativeID)
	if err != nil {
		if isEntityNotFound(err) {
			r.deleteOrphanInitiative(ctx, initiativeID)
		}
		return err
	}

	for _, update := range updates {
		params, err := db.APIInitiativeUpdateToDBUpdate(update, initiativeID)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertInitiativeUpdate(ctx, params); err != nil {
			log.Printf("[repo] upsert initiative update %s failed: %v", update.ID, err)
		}
	}
	return nil
}

// =============================================================================
// Store Access (for sync worker)
// =============================================================================

// Store returns the underlying database store for direct access (e.g., sync worker)
func (r *SQLiteRepository) Store() *db.Store {
	return r.store
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

// GetAttachmentByID returns an attachment by ID
func (r *SQLiteRepository) GetAttachmentByID(ctx context.Context, id string) (*api.Attachment, error) {
	att, err := r.store.Queries().GetAttachment(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get attachment: %w", err)
	}
	result, err := db.DBAttachmentToAPIAttachment(att)
	if err != nil {
		return nil, fmt.Errorf("convert attachment: %w", err)
	}
	return &result, nil
}

// =============================================================================
// Issue History
// =============================================================================

func (r *SQLiteRepository) GetIssueHistory(ctx context.Context, issueID string) ([]api.IssueHistoryEntry, error) {
	cache, err := r.store.Queries().GetIssueHistoryCache(ctx, issueID)
	if err == nil {
		// Have cached data — check staleness and maybe refresh in background
		r.maybeRefreshHistory(issueID, cache.SyncedAt)

		var entries []api.IssueHistoryEntry
		if err := json.Unmarshal(cache.Data, &entries); err != nil {
			return nil, fmt.Errorf("unmarshal history cache: %w", err)
		}
		return entries, nil
	}

	// No cached data — trigger a background fetch and return empty immediately.
	// This avoids blocking the FUSE dispatch goroutine on a cold-cache API call.
	// History will be available on next access once the background fetch completes.
	if r.client == nil {
		return nil, nil
	}
	r.triggerBackgroundRefresh("history:"+issueID, func(ctx context.Context) error {
		entries, err := r.client.GetIssueHistory(ctx, issueID)
		if err != nil {
			if isEntityNotFound(err) {
				r.deleteOrphanIssue(ctx, issueID)
			}
			return err
		}
		r.upsertHistoryCache(ctx, issueID, entries)
		return nil
	})
	return nil, nil
}

func (r *SQLiteRepository) maybeRefreshHistory(issueID string, cachedSyncedAt time.Time) {
	if r.client == nil {
		return
	}

	bgCtx := context.Background()
	issueUpdatedAt, err := r.store.Queries().GetIssueUpdatedAt(bgCtx, issueID)
	if err != nil {
		return
	}

	issueTime := issueUpdatedAt
	historyTime := cachedSyncedAt

	// Refresh if issue changed after history was cached OR never cached
	if historyTime.IsZero() || issueTime.After(historyTime) {
		r.triggerBackgroundRefresh("history:"+issueID, func(ctx context.Context) error {
			entries, err := r.client.GetIssueHistory(ctx, issueID)
			if err != nil {
				if isEntityNotFound(err) {
					r.deleteOrphanIssue(ctx, issueID)
				}
				return err
			}
			r.upsertHistoryCache(ctx, issueID, entries)
			return nil
		})
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

// TouchIssueSubResources bumps the synced_at timestamp for all sub-resources
// (comments, documents, attachments, history) to the given time. Used by the
// sync worker to prevent staleness-based refreshes for unchanged issues.
func (r *SQLiteRepository) TouchIssueSubResources(ctx context.Context, issueID string, syncedAt time.Time) {
	q := r.store.Queries()
	q.TouchIssueComments(ctx, db.TouchIssueCommentsParams{SyncedAt: syncedAt, IssueID: issueID})
	q.TouchIssueDocuments(ctx, db.TouchIssueDocumentsParams{SyncedAt: syncedAt, IssueID: sql.NullString{String: issueID, Valid: true}})
	q.TouchIssueAttachments(ctx, db.TouchIssueAttachmentsParams{SyncedAt: syncedAt, IssueID: issueID})
	q.TouchIssueHistoryCache(ctx, db.TouchIssueHistoryCacheParams{SyncedAt: syncedAt, IssueID: issueID})
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
// issue's identifier/title. It is the one converter behind all three relation
// reads (outgoing, inverse, by-id), which were byte-identical but for this
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

// GetIssueRelationByID returns a relation by ID
func (r *SQLiteRepository) GetIssueRelationByID(ctx context.Context, id string) (*api.IssueRelation, error) {
	rel, err := r.store.Queries().GetIssueRelation(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get issue relation: %w", err)
	}
	view := r.relationView(ctx, rel, relOutgoing)
	return &view, nil
}
