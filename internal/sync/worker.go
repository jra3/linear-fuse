// Package sync implements background synchronization of Linear issues to SQLite.
//
// The sync strategy is "sync until unchanged": fetch issues ordered by updatedAt DESC
// and stop when we hit issues that haven't changed since our last sync. This allows
// efficient incremental updates without fetching all issues on every sync.
package sync

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/reconcile"
)

// APIClient defines the interface for API operations needed by the sync worker
type APIClient interface {
	// Teams
	GetTeams(ctx context.Context) ([]api.Team, error)
	GetTeamIssuesPage(ctx context.Context, teamID string, cursor string, pageSize int) ([]api.Issue, api.PageInfo, error)

	// Consolidated team metadata (states, labels, cycles, projects, members in one call)
	GetTeamMetadata(ctx context.Context, teamID string) (*api.TeamMetadata, error)

	// Consolidated workspace data (users + initiatives in one call)
	GetWorkspace(ctx context.Context) (*api.WorkspaceData, error)

	// Workspace project-label catalog (complete drain, retired included —
	// completeness licenses the prune in syncProjectLabels)
	GetProjectLabels(ctx context.Context) ([]api.ProjectLabel, error)

	// Issue details (comments, documents, attachments)
	GetIssueDetails(ctx context.Context, issueID string) (*api.IssueDetails, error)
	GetIssueDetailsBatch(ctx context.Context, issueIDs []string) (map[string]*api.IssueDetails, error)
	GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error)
	GetIssueDocuments(ctx context.Context, issueID string) ([]api.Document, error)
	GetIssueAttachments(ctx context.Context, issueID string) ([]api.Attachment, error)

	// Auth
	AuthHeader() string

	// Viewer (the authenticated user). The worker fires this once as the
	// cold-start budget probe (see probeBudget): the cheapest possible
	// query whose response headers seed the client's rate budget before
	// any expensive work is issued.
	GetViewer(ctx context.Context) (*api.User, error)

	// Server-reported rate limit window reset time, per the client's rate
	// budget (parsed from the per-axis millisecond reset headers; zero if
	// no response has been observed yet).
	RateLimitResetAt() time.Time
}

const detailsBatchSize = 15 // Number of issues to fetch details for in one API call (Linear has 10k complexity limit; 20 was 80-90% of budget)

// Budget thresholds for rate limit awareness.
// Detail batches (~2001 complexity each) are expensive; we defer them when budget is tight.
const (
	budgetSkipSyncPct    = 80.0 // Skip entire sync cycle when budget exceeds this
	budgetDeferDetailPct = 70.0 // Defer detail batches to pending_detail_sync above this
)

// BudgetReporter provides rate limit budget information.
type BudgetReporter interface {
	BudgetSnapshot() (count int, pct float64)
}

// CatchUpModeToggler controls the repo staleness threshold during large syncs.
type CatchUpModeToggler interface {
	SetCatchUpMode(active bool)
}

// Worker handles background synchronization of Linear issues to SQLite
type Worker struct {
	client    APIClient
	store     *db.Store
	extractor *reconcile.Extractor // embedded-file extraction (HEAD + upsert)
	interval  time.Duration
	stopCh    chan struct{}
	doneCh    chan struct{}
	mu        sync.RWMutex
	running   bool
	lastSync  time.Time
	budget    BudgetReporter     // optional: for rate limit budget logging
	catchUp   CatchUpModeToggler // optional: controls repo staleness during catch-up
	cycle     atomic.Int64       // sync-cycle counter; rotates the team order

	// Rate limit tracking for issue details sync
	rateLimitMu     sync.RWMutex
	rateLimitedAt   time.Time
	rateLimitExpiry time.Time
}

// Config holds configuration for the sync worker
type Config struct {
	// Interval between sync cycles (default: 2 minutes)
	Interval time.Duration
	// PageSize for API pagination (default: 100)
	PageSize int
}

// DefaultConfig returns a Config with default values
func DefaultConfig() Config {
	return Config{
		Interval: 2 * time.Minute,
		PageSize: 100,
	}
}

// NewWorker creates a new sync worker
func NewWorker(client APIClient, store *db.Store, cfg Config) *Worker {
	if cfg.Interval == 0 {
		cfg.Interval = 2 * time.Minute
	}
	return &Worker{
		client:    client,
		store:     store,
		extractor: &reconcile.Extractor{Q: store.Queries(), AuthHeader: client.AuthHeader},
		interval:  cfg.Interval,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// SetBudgetReporter sets the rate limit budget reporter for enhanced logging.
func (w *Worker) SetBudgetReporter(b BudgetReporter) {
	w.budget = b
}

// SetCatchUpModeToggler sets the repo reference for toggling catch-up mode
// during large sync operations.
func (w *Worker) SetCatchUpModeToggler(t CatchUpModeToggler) {
	w.catchUp = t
}

// Start begins the background sync process
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.mu.Unlock()

	go w.run(ctx)
}

// Stop gracefully stops the sync worker
func (w *Worker) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	close(w.stopCh)
	<-w.doneCh
}

// Running returns whether the worker is currently running
func (w *Worker) Running() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.running
}

// LastSync returns the time of the last successful sync
func (w *Worker) LastSync() time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastSync
}

// SyncNow triggers an immediate sync cycle
func (w *Worker) SyncNow(ctx context.Context) error {
	return w.syncAllTeams(ctx)
}

func (w *Worker) run(ctx context.Context) {
	defer func() {
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
		close(w.doneCh)
	}()

	// Cold-start probe: seed the rate budget from one cheap query BEFORE
	// the first (expensive) sync cycle. Aborts only on shutdown.
	if !w.probeBudget(ctx) {
		return
	}

	// Initial sync
	if err := w.syncAllTeams(ctx); err != nil {
		log.Printf("[sync] initial sync failed: %v", err)
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			if err := w.syncAllTeams(ctx); err != nil {
				log.Printf("[sync] sync failed: %v", err)
			}
		}
	}
}

func (w *Worker) syncAllTeams(ctx context.Context) error {
	// Skip entire sync cycle when budget is critically high
	if w.budgetExceeds(budgetSkipSyncPct) {
		count, pct := 0, 0.0
		if w.budget != nil {
			count, pct = w.budget.BudgetSnapshot()
		}
		log.Printf("[sync] skipping sync cycle: budget at %d/1500 (%.0f%%), threshold %.0f%%",
			count, pct, budgetSkipSyncPct)
		return nil
	}

	// H-5: Drain any issues that were queued during a previous rate-limit backoff
	w.drainPendingDetailSync(ctx)

	// First, sync workspace-level entities
	if err := w.syncWorkspace(ctx); err != nil {
		log.Printf("[sync] workspace sync failed: %v", err)
		// Continue with teams even if workspace sync fails
	}

	// Sync teams list
	teams, err := w.client.GetTeams(ctx)
	if err != nil {
		return fmt.Errorf("get teams: %w", err)
	}

	// Rotate the starting team each cycle. Teams sync in order against one
	// token bucket, so under budget pressure the deferrals always land on
	// whoever is last — with a fixed order that is the same team every
	// cycle, which starved it permanently (observed live once metadata
	// went from one call per team to two). Rotation bounds any team's
	// worst-case staleness at len(teams) cycles instead.
	if n := len(teams); n > 0 {
		start := int(w.cycle.Add(1)-1) % n
		rotated := make([]api.Team, 0, n)
		rotated = append(rotated, teams[start:]...)
		rotated = append(rotated, teams[:start]...)
		teams = rotated
	}

	for _, team := range teams {
		// Upsert team
		if err := w.store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
			log.Printf("[sync] upsert team %s failed: %v", team.Key, err)
		}

		// Sync team metadata (states, labels, cycles, projects, members)
		if err := w.syncTeamMetadata(ctx, team); err != nil {
			log.Printf("[sync] sync team %s metadata failed: %v", team.Key, err)
		}

		// Sync team issues
		if err := w.syncTeam(ctx, team); err != nil {
			log.Printf("[sync] sync team %s failed: %v", team.Key, err)
			// Continue with other teams
		}
	}

	w.mu.Lock()
	w.lastSync = time.Now()
	w.mu.Unlock()

	return nil
}

// SyncTeamResult contains the results of syncing a single team
type SyncTeamResult struct {
	TeamID        string
	IssuesAdded   int
	IssuesUpdated int
	PagesFetched  int
	Duration      time.Duration
}

func (w *Worker) syncTeam(ctx context.Context, team api.Team) error {
	start := time.Now()

	// Get last sync metadata
	meta, err := w.store.Queries().GetSyncMeta(ctx, team.ID)
	var lastSyncedUpdatedAt time.Time
	if err == nil && meta.LastIssueUpdatedAt.Valid {
		lastSyncedUpdatedAt = meta.LastIssueUpdatedAt.Time
	}

	added, updated, pages, err := w.syncTeamIssues(ctx, team.ID, lastSyncedUpdatedAt)

	// Disable catch-up mode after sync completes (or fails)
	if w.catchUp != nil && (added+updated) > 50 {
		w.catchUp.SetCatchUpMode(false)
	}

	if err != nil {
		return err
	}

	// Update sync metadata
	count, _ := w.store.Queries().GetTeamIssueCount(ctx, team.ID)
	latestUpdatedAtRaw, _ := w.store.Queries().GetLatestTeamIssueUpdatedAt(ctx, team.ID)

	// MAX() returns different types depending on the driver; the db helper
	// handles them all.
	lastIssueUpdatedAt := db.ParseSQLiteTimeAny(latestUpdatedAtRaw)

	if err := w.store.Queries().UpsertSyncMeta(ctx, db.UpsertSyncMetaParams{
		TeamID:             team.ID,
		LastSyncedAt:       db.Now(),
		LastIssueUpdatedAt: db.ToNullTime(lastIssueUpdatedAt),
		IssueCount:         db.ToNullInt64(count),
	}); err != nil {
		log.Printf("[sync] update sync meta for %s failed: %v", team.Key, err)
	}

	duration := time.Since(start)
	log.Printf("[sync] team %s: added=%d updated=%d pages=%d duration=%s",
		team.Key, added, updated, pages, duration.Round(time.Millisecond))

	return nil
}

// syncTeamIssues fetches issues ordered by updatedAt DESC and stops when hitting unchanged issues
func (w *Worker) syncTeamIssues(ctx context.Context, teamID string, lastSyncedUpdatedAt time.Time) (added, updated, pages int, err error) {
	var cursor string
	var pendingDetailIssues []issueRef

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return added, updated, pages, ctx.Err()
		default:
		}

		// Fetch next page of issues ordered by updatedAt DESC
		issues, pageInfo, fetchErr := w.client.GetTeamIssuesPage(ctx, teamID, cursor, 100)
		if fetchErr != nil {
			return added, updated, pages, fmt.Errorf("fetch issues: %w", fetchErr)
		}
		pages++

		if len(issues) == 0 {
			break
		}

		// Process issues, tracking how many are unchanged
		unchangedCount := 0
		for _, issue := range issues {
			// Check if this issue is unchanged (updatedAt <= lastSyncedUpdatedAt)
			if !lastSyncedUpdatedAt.IsZero() && !issue.UpdatedAt.After(lastSyncedUpdatedAt) {
				// Nothing to stamp: under event-driven staleness an unchanged
				// issue is fresh by definition (detail_synced_at > updatedAt)
				// and a never-detail-synced one SHOULD read stale. The old
				// touch-on-unchanged block here also re-stamped the history
				// cache fresh every cycle — history is never worker-fetched,
				// so a stale history.md served pre-update history forever.
				unchangedCount++
				continue
			}

			// Check if issue already exists
			_, getErr := w.store.Queries().GetIssueByID(ctx, issue.ID)
			isNew := getErr != nil

			// Convert and upsert
			data, convErr := db.APIIssueToDBIssue(issue)
			if convErr != nil {
				log.Printf("[sync] convert issue %s failed: %v", issue.Identifier, convErr)
				continue
			}

			if upsertErr := w.store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); upsertErr != nil {
				log.Printf("[sync] upsert issue %s failed: %v", issue.Identifier, upsertErr)
				continue
			}

			// Extract embedded files from issue description
			if issue.Description != "" {
				w.extractor.ExtractAndStore(ctx, issue.ID, issue.Description, "description")
			}

			// Queue for batch details sync
			pendingDetailIssues = append(pendingDetailIssues, issueRef{ID: issue.ID, Identifier: issue.Identifier})

			// Sync details in batches. The outcome is ignored here: any
			// gated/deferred issue landed in pending_detail_sync, so the next
			// cycle's drain retries it.
			if len(pendingDetailIssues) >= detailsBatchSize {
				w.syncDetails(ctx, pendingDetailIssues)
				pendingDetailIssues = nil
			}

			if isNew {
				added++
			} else {
				updated++
			}
		}

		// Enable catch-up mode when we detect a large sync, suppressing
		// on-demand refreshes that would duplicate the sync worker's effort
		if w.catchUp != nil && (added+updated) > 50 {
			w.catchUp.SetCatchUpMode(true)
		}

		// If all issues in this page are unchanged, we're done
		if unchangedCount == len(issues) {
			log.Printf("[sync] team %s: hit %d unchanged issues, stopping sync", teamID, unchangedCount)
			break
		}

		// If no more pages, stop
		if !pageInfo.HasNextPage || pageInfo.EndCursor == "" {
			break
		}

		cursor = pageInfo.EndCursor
	}

	// Sync any remaining pending issue details (outcome ignored, see above)
	if len(pendingDetailIssues) > 0 {
		w.syncDetails(ctx, pendingDetailIssues)
	}

	return added, updated, pages, nil
}

// CleanupArchivedIssues removes issues that have been archived in Linear
// This should be called periodically to clean up the local database
func (w *Worker) CleanupArchivedIssues(ctx context.Context, teamID string) (int64, error) {
	// This is a more expensive operation that fetches all issue IDs from Linear
	// and removes any local issues that no longer exist
	// For now, we'll skip this - archived issues can be cleaned up manually
	return 0, nil
}

// =============================================================================
// Workspace-Level Sync
// =============================================================================

// syncWorkspace syncs workspace-level entities (users + initiatives).
// GetWorkspace drains every connection — including each initiative's
// nested projects — so the junction prune in syncInitiativeProjects runs
// against the complete server-side truth.
func (w *Worker) syncWorkspace(ctx context.Context) error {
	// The prune cutoff is taken BEFORE the fetch: any junction row upserted
	// after this instant (this pass, or a user linking a project mid-sync)
	// survives.
	pruneCutoff := db.Now()

	data, err := w.client.GetWorkspace(ctx)
	if err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}

	var errs []error

	// Process users
	for _, user := range data.Users {
		params, err := db.APIUserToDBUser(user)
		if err != nil {
			log.Printf("[sync] convert user %s failed: %v", user.Email, err)
			continue
		}
		if err := w.store.Queries().UpsertUser(ctx, params); err != nil {
			log.Printf("[sync] upsert user %s failed: %v", user.Email, err)
		}
	}
	log.Printf("[sync] synced %d users", len(data.Users))

	// Process initiatives
	for _, initiative := range data.Initiatives {
		params, err := db.APIInitiativeToDBInitiative(initiative)
		if err != nil {
			log.Printf("[sync] convert initiative %s failed: %v", initiative.Slug, err)
			continue
		}
		if err := w.store.Queries().UpsertInitiative(ctx, params); err != nil {
			log.Printf("[sync] upsert initiative %s failed: %v", initiative.Slug, err)
			continue
		}

		// Sync initiative-project associations (best-effort; logs internally)
		w.syncInitiativeProjects(ctx, initiative, pruneCutoff)
	}
	log.Printf("[sync] synced %d initiatives", len(data.Initiatives))

	// Project-label catalog (workspace-scoped; see CONTEXT.md "Project-label
	// selection"). Isolated log-and-continue: a catalog failure must not block
	// users/initiatives, and vice versa. Reuses pruneCutoff: taken before ANY
	// fetch this pass, so it is strictly conservative for the synced_at <
	// cutoff prune (the converter stamps SyncedAt at upsert time, after it).
	// The drain includes retired labels (live-verified), so retirement never
	// reads as removal — only true deletion/archival does.
	w.syncProjectLabels(ctx, pruneCutoff)

	if len(errs) > 0 {
		return fmt.Errorf("workspace sync errors: %v", errs)
	}
	return nil
}

// syncProjectLabels reconciles the workspace project-label catalog. The
// complete GetProjectLabels drain is the completeness set that licenses the
// full-table prune; a fetch failure skips the pass entirely (no prune without
// a complete drain).
func (w *Worker) syncProjectLabels(ctx context.Context, pruneCutoff time.Time) {
	plabels, err := w.client.GetProjectLabels(ctx)
	if err != nil {
		log.Printf("[sync] project labels fetch failed: %v", err)
		return
	}
	reconcile.Collection(ctx, reconcile.CollectionSpec[api.ProjectLabel]{
		Label: "project-label",
		Items: plabels,
		Upsert: func(ctx context.Context, l api.ProjectLabel) error {
			params, err := db.APIProjectLabelToDBProjectLabel(l)
			if err != nil {
				return err
			}
			return w.store.Queries().UpsertProjectLabel(ctx, params)
		},
		Prune: func(ctx context.Context) error {
			return w.store.Queries().PruneProjectLabels(ctx, pruneCutoff)
		},
	})
	log.Printf("[sync] synced %d project labels", len(plabels))
}

// syncInitiativeProjects upserts an initiative's junction rows and prunes
// the ones the fetch no longer returned (a project unlinked in Linear).
// The prune only runs after every upsert succeeded — a row that merely
// failed to refresh must not read as a removal — and initiative.Projects
// is complete by GetWorkspace's contract, which is what makes pruning
// against it safe. Reconciles through the shared reconcile.Collection tail.
func (w *Worker) syncInitiativeProjects(ctx context.Context, initiative api.Initiative, pruneCutoff time.Time) {
	reconcile.Collection(ctx, reconcile.CollectionSpec[api.InitiativeProject]{
		Label: "initiative-project",
		Items: initiative.Projects.Nodes,
		Upsert: func(ctx context.Context, project api.InitiativeProject) error {
			return w.store.Queries().UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
				InitiativeID: initiative.ID,
				ProjectID:    project.ID,
				SyncedAt:     db.Now(),
			})
		},
		Prune: func(ctx context.Context) error {
			return w.store.Queries().PruneInitiativeProjects(ctx, db.PruneInitiativeProjectsParams{
				InitiativeID: initiative.ID,
				SyncedAt:     pruneCutoff,
			})
		},
	})
}

// =============================================================================
// Team Metadata Sync
// =============================================================================

// syncTeamMetadata syncs all metadata for a team: states, labels, cycles,
// projects (with milestones), and members. GetTeamMetadata drains every
// unbounded connection, so meta is the complete server-side truth — which
// is what makes the project_teams prune below safe.
func (w *Worker) syncTeamMetadata(ctx context.Context, team api.Team) error {
	// The prune cutoff is taken BEFORE the fetch: any association upserted
	// after this instant (this pass, or a concurrent user edit) survives.
	pruneCutoff := db.Now()

	meta, err := w.client.GetTeamMetadata(ctx, team.ID)
	if err != nil {
		return fmt.Errorf("get team metadata: %w", err)
	}

	// Each metadata collection reconciles through the same tail — upsert every
	// item, then prune the rows the (complete) fetch no longer returned, but
	// only if every upsert succeeded. reconcile.Collection owns that
	// prune-safety invariant so no site can drop the guard. See CONTEXT.md
	// "Sync reconcile tail (syncCollection)".

	// States are workflow-bounded and fetched single-page, so nothing licenses
	// a prune — upsert-only (nil prune).
	reconcile.Collection(ctx, reconcile.CollectionSpec[api.State]{
		Label: "state",
		Items: meta.States,
		Upsert: func(ctx context.Context, state api.State) error {
			params, err := db.APIStateToDBState(state, team.ID)
			if err != nil {
				return err
			}
			return w.store.Queries().UpsertState(ctx, params)
		},
	})

	// Labels are already deduplicated by GetTeamMetadata. team_id comes from
	// label.Team (fetched via the LabelFields fragment), not team.ID: team.labels
	// returns workspace labels mixed in, so stamping team.ID here is what churned
	// workspace labels between teams.
	reconcile.Collection(ctx, reconcile.CollectionSpec[api.Label]{
		Label: "label",
		Items: meta.Labels,
		Upsert: func(ctx context.Context, label api.Label) error {
			params, err := db.APILabelToDBLabel(label)
			if err != nil {
				return err
			}
			return w.store.Queries().UpsertLabel(ctx, params)
		},
		Prune: func(ctx context.Context) error {
			return w.store.Queries().PruneTeamLabels(ctx, db.PruneTeamLabelsParams{
				TeamID:   sql.NullString{String: team.ID, Valid: true},
				SyncedAt: pruneCutoff,
			})
		},
	})

	reconcile.Collection(ctx, reconcile.CollectionSpec[api.Cycle]{
		Label: "cycle",
		Items: meta.Cycles,
		Upsert: func(ctx context.Context, cycle api.Cycle) error {
			params, err := db.APICycleToDBCycle(cycle, team.ID)
			if err != nil {
				return err
			}
			return w.store.Queries().UpsertCycle(ctx, params)
		},
		Prune: func(ctx context.Context) error {
			return w.store.Queries().PruneTeamCycles(ctx, db.PruneTeamCyclesParams{
				TeamID:   team.ID,
				SyncedAt: pruneCutoff,
			})
		},
	})

	// Projects prune the project_teams junction (a project that moved off this
	// team, or was deleted). The upsert closure's completeness set is the project
	// entity plus the project_teams row: a failure in either suppresses the
	// prune. Milestones are a nested best-effort sub-write in a capped,
	// never-pruned connection — outside that set — so a milestone failure is
	// logged and swallowed, never suppressing the prune.
	reconcile.Collection(ctx, reconcile.CollectionSpec[api.Project]{
		Label: "project",
		Items: meta.Projects,
		Upsert: func(ctx context.Context, project api.Project) error {
			params, err := db.APIProjectToDBProject(project)
			if err != nil {
				return err
			}
			if err := w.store.Queries().UpsertProject(ctx, params); err != nil {
				return err
			}
			// A junction failure marks the item unclean but does not abort the
			// milestone sub-writes below, so return it after they run.
			junctionErr := w.store.Queries().UpsertProjectTeam(ctx, db.UpsertProjectTeamParams{
				ProjectID: project.ID,
				TeamID:    team.ID,
				SyncedAt:  db.Now(),
			})
			if project.Milestones != nil {
				for _, milestone := range project.Milestones.Nodes {
					mParams, mErr := db.APIProjectMilestoneToDBMilestone(milestone, project.ID)
					if mErr != nil {
						log.Printf("[sync] convert milestone %s failed: %v", milestone.Name, mErr)
						continue
					}
					if err := w.store.Queries().UpsertProjectMilestone(ctx, mParams); err != nil {
						log.Printf("[sync] upsert milestone %s failed: %v", milestone.Name, err)
					}
				}
			}
			return junctionErr
		},
		Prune: func(ctx context.Context) error {
			return w.store.Queries().PruneProjectTeams(ctx, db.PruneProjectTeamsParams{
				TeamID:   team.ID,
				SyncedAt: pruneCutoff,
			})
		},
	})

	// Members prune the team_members junction (a departed member), not the
	// workspace-wide users table, which other teams share.
	reconcile.Collection(ctx, reconcile.CollectionSpec[api.User]{
		Label: "member",
		Items: meta.Members,
		Upsert: func(ctx context.Context, member api.User) error {
			params, err := db.APIUserToDBUser(member)
			if err != nil {
				return err
			}
			if err := w.store.Queries().UpsertUser(ctx, params); err != nil {
				return err
			}
			return w.store.Queries().UpsertTeamMember(ctx, db.UpsertTeamMemberParams{
				TeamID:   team.ID,
				UserID:   member.ID,
				SyncedAt: db.Now(),
			})
		},
		Prune: func(ctx context.Context) error {
			return w.store.Queries().PruneTeamMembers(ctx, db.PruneTeamMembersParams{
				TeamID:   team.ID,
				SyncedAt: pruneCutoff,
			})
		},
	})

	return nil
}

// =============================================================================
// Rate Limit Handling
// =============================================================================

// isRateLimitError checks if an error indicates a rate limit. Detection is
// the shared api.IsRateLimited predicate (its case-insensitive "rate limit"
// fallback subsumes the "Rate limit exceeded" phrasing this used to match).
func isRateLimitError(err error) bool {
	return api.IsRateLimited(err)
}

// budgetExceeds returns true if the current hourly budget usage exceeds the given threshold.
// Returns false if no budget reporter is configured.
func (w *Worker) budgetExceeds(pct float64) bool {
	if w.budget == nil {
		return false
	}
	_, usage := w.budget.BudgetSnapshot()
	return usage > pct
}

// isRateLimited returns true if we're currently rate limited for issue details
func (w *Worker) isRateLimited() bool {
	w.rateLimitMu.RLock()
	defer w.rateLimitMu.RUnlock()
	return time.Now().Before(w.rateLimitExpiry)
}

// setRateLimited marks that we've hit a rate limit. The backoff consults
// the client's rate budget: RateLimitResetAt is the server-reported window
// reset (parsed from the per-axis millisecond headers), so the pause ends
// when the budget actually refills; the fixed 15-minute backoff is only the
// fallback for a reset the server never told us about.
func (w *Worker) setRateLimited() {
	w.rateLimitMu.Lock()
	defer w.rateLimitMu.Unlock()
	w.rateLimitedAt = time.Now()

	// Use the budget's server-provided reset time if it's in the future
	backoff := 15 * time.Minute
	if resetAt := w.client.RateLimitResetAt(); !resetAt.IsZero() && resetAt.After(time.Now()) {
		backoff = time.Until(resetAt) + 5*time.Second // 5s buffer past the reset
	}
	w.rateLimitExpiry = w.rateLimitedAt.Add(backoff)

	if w.budget != nil {
		count, pct := w.budget.BudgetSnapshot()
		log.Printf("[sync] rate limited, pausing issue details sync until %s (backoff=%s, budget: %d requests this hour, %.0f%%)",
			w.rateLimitExpiry.Format(time.RFC3339), backoff.Round(time.Second), count, pct)
	} else {
		log.Printf("[sync] rate limited, pausing issue details sync until %s (backoff=%s)",
			w.rateLimitExpiry.Format(time.RFC3339), backoff.Round(time.Second))
	}
}

// probeBudget is the cold-start probe: before the worker's first sync cycle
// it fires one cheap viewer query so the client's rate budget is seeded from
// real response headers BEFORE any expensive team-metadata/issue/detail
// query is admitted. Without it a fresh process's budget has seen neither
// axis (unseen axes don't gate), so the initial burst could all admit
// un-gated before any response lands — the exact cold-start thundering herd
// the budget exists to prevent. The viewer is the cheapest query we have
// (~1-2 complexity points) and dual-purpose: /my needs it anyway.
//
// If the probe itself reports RATELIMITED, the account is already exhausted:
// mark the worker rate-limited (the backoff honors the budget's
// server-reported reset, which this very response's headers just seeded) and
// sleep until the backoff expires instead of bursting into the wall. Any
// other probe failure (network down, auth) is logged and sync proceeds —
// those failures repeat identically in syncAllTeams and are handled there,
// and the budget stays conservative once the first response does land.
//
// Returns false only when shutdown (ctx cancellation / Stop) interrupts the
// delay, so run can exit without firing a post-stop sync cycle.
func (w *Worker) probeBudget(ctx context.Context) bool {
	_, err := w.client.GetViewer(ctx)
	if err == nil {
		return true
	}
	if !isRateLimitError(err) {
		log.Printf("[sync] budget probe failed (continuing): %v", err)
		return true
	}

	w.setRateLimited()
	w.rateLimitMu.RLock()
	expiry := w.rateLimitExpiry
	w.rateLimitMu.RUnlock()

	wait := time.Until(expiry)
	log.Printf("[sync] budget probe RATELIMITED; delaying sync start %s (until %s)",
		wait.Round(time.Second), expiry.Format(time.RFC3339))
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-w.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

// =============================================================================
// Issue Details Sync (Comments and Documents)
// =============================================================================

// issueRef identifies an issue for detail syncing: the ID the API keys on and
// the identifier used in log lines and the pending queue.
type issueRef struct {
	ID         string
	Identifier string
}

// detailOutcome is syncDetails' per-issue ledger: every issue handed in lands
// in exactly one of the two slices. synced holds issues whose details
// persisted cleanly (detail_synced_at stamped + dequeued); deferred holds
// everything else (re-enqueued to pending_detail_sync, NOT stamped, NOT
// dequeued).
// gated=true means conditions preclude further detail syncing this cycle —
// budget too tight, rate-limited, or a failed fetch — so a batching loop
// (drainPendingDetailSync) should stop rather than burn more batches.
type detailOutcome struct {
	synced   []issueRef
	deferred []issueRef
	gated    bool
}

// deferDetailIssues enqueues every issue to pending_detail_sync for a later
// cycle, stamping one QueuedAt for the batch. Shared by syncDetails' defer
// paths (the whole-batch gates and the per-issue unclean/contract-violation
// cases) so the enqueue contract lives in one place and a new path cannot
// drift.
func (w *Worker) deferDetailIssues(ctx context.Context, issues []issueRef) {
	now := db.Now()
	for _, issue := range issues {
		_ = w.store.Queries().UpsertPendingDetailSync(ctx, db.UpsertPendingDetailSyncParams{
			IssueID:    issue.ID,
			Identifier: issue.Identifier,
			QueuedAt:   now,
		})
	}
}

// syncDetails is the single entry point for issue-detail syncing
// (comments/documents/attachments/relations). It owns every gate — budget,
// rate-limited before the fetch, rate-limited mid-fetch, fetch failure —
// fetches the batch in one API call, persists per issue through
// reconcile.PersistIssueDetails, and returns a per-issue outcome ledger.
//
// Only a CLEAN issue (all five collections persisted without error) is
// stamped (detail_synced_at, the one per-issue detail-freshness fact) and
// dequeued from pending_detail_sync. ANY failure — a gate, a fetch error, or
// a single collection's upsert — defers the affected issues to
// pending_detail_sync instead: an issue that was silently dropped or
// partially persisted must never be stamped fresh (masking staleness from
// the SWR path) nor lose its worker-side retry.
func (w *Worker) syncDetails(ctx context.Context, issues []issueRef) detailOutcome {
	deferAll := func() detailOutcome {
		w.deferDetailIssues(ctx, issues)
		return detailOutcome{deferred: issues, gated: true}
	}

	// Gate 1: budget too tight for detail fetches this cycle.
	if w.budgetExceeds(budgetDeferDetailPct) {
		return deferAll()
	}

	// Gate 2 (H-5): already rate limited — defer so the issues survive the backoff.
	if w.isRateLimited() {
		return deferAll()
	}

	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}

	// The prune cutoff is taken BEFORE the fetch: any row upserted after this
	// instant (a comment created through FUSE while the fetch was in flight)
	// carries a newer synced_at and survives pruning even though the fetch
	// response predates it.
	pruneCutoff := db.Now()

	// Fetch all details in one API call
	detailsMap, err := w.client.GetIssueDetailsBatch(ctx, ids)
	if err != nil {
		if isRateLimitError(err) {
			// Gate 3: rate limited mid-fetch.
			w.setRateLimited()
			return deferAll()
		}
		// Gate 4: any other fetch failure. Deferring (not just logging) keeps
		// the worker-side retry for team-sync-sourced issues, which otherwise
		// exist nowhere but this call's arguments.
		log.Printf("[sync] batch fetch details failed, deferring %d issues: %v", len(issues), err)
		return deferAll()
	}

	// Store each issue's comments/documents/attachments/relations through
	// reconcile.PersistIssueDetails — five reconcile.Collection calls per
	// issue. The module contributes the CLEAN guard, PersistIssueDetails
	// contributes COMPLETENESS (page-size checks), so a prune fires only when
	// the fetch was clean AND complete.
	//
	// Completeness relies on GetIssueDetailsBatch's documented all-or-nothing
	// contract: a nil error guarantees a non-nil map entry for every requested
	// ID, so a partially-failed response never reaches this loop as a
	// short-but-"complete" details struct. The nil branch below is a trap for
	// a violation of that contract, not expected flow.
	deps := reconcile.Deps{Q: w.store.Queries(), Extract: w.extractor.ExtractAndStore}
	var outcome detailOutcome
	now := db.Now()
	for _, issue := range issues {
		details := detailsMap[issue.ID]
		if details == nil {
			log.Printf("[sync] CONTRACT VIOLATION: GetIssueDetailsBatch returned nil error but no details for %s (%s) — deferring", issue.Identifier, issue.ID)
			w.deferDetailIssues(ctx, []issueRef{issue})
			outcome.deferred = append(outcome.deferred, issue)
			continue
		}

		clean := reconcile.PersistIssueDetails(ctx, deps, issue.ID, details, pruneCutoff)
		if !clean {
			// A collection's convert/upsert failed. The clean guard already
			// suppressed that collection's prune; here the issue must ALSO
			// keep its retry (re-enqueue for the next cycle's drain) and must
			// NOT be stamped fresh — a stamp would hide the stale rows from
			// the SWR path until the next real change.
			w.deferDetailIssues(ctx, []issueRef{issue})
			outcome.deferred = append(outcome.deferred, issue)
			continue
		}

		// Stamp detail_synced_at — the one per-issue freshness fact the SWR
		// path consults — so the FS layer doesn't immediately re-trigger
		// on-demand fetches for the data we just stored. The stamp covers all
		// detail families uniformly (comments/documents/attachments/relations):
		// it lives on the issues row, so an empty family can no longer read as
		// "never synced" (the old per-row touches could not stamp rows that
		// did not exist).
		if err := w.store.Queries().StampIssueDetailSynced(ctx, db.StampIssueDetailSyncedParams{DetailSyncedAt: db.ToNullTime(now), ID: issue.ID}); err != nil {
			log.Printf("[sync] stamp detail synced %s: %v", issue.Identifier, err)
		}
		// H-5: Remove the cleanly synced issue from the pending queue
		_ = w.store.Queries().DeletePendingDetailSync(ctx, issue.ID)
		outcome.synced = append(outcome.synced, issue)
	}
	log.Printf("[sync] batch synced details: %d clean, %d deferred", len(outcome.synced), len(outcome.deferred))
	return outcome
}

// drainPendingDetailSync processes issues that were queued for detail sync
// but skipped due to rate limiting, budget, or an earlier failure. Called at
// the start of each sync cycle. All the gates live inside syncDetails; this
// is just a batching loop that stops when an outcome reports gated (nothing
// more can sync this cycle). A gated syncDetails re-defers its batch, which
// merely re-stamps the already-pending rows' QueuedAt — harmless.
func (w *Worker) drainPendingDetailSync(ctx context.Context) {
	pending, err := w.store.Queries().ListPendingDetailSync(ctx)
	if err != nil || len(pending) == 0 {
		return
	}

	log.Printf("[sync] draining %d pending detail syncs", len(pending))

	issues := make([]issueRef, len(pending))
	for i, row := range pending {
		issues[i] = issueRef{ID: row.IssueID, Identifier: row.Identifier}
	}

	for len(issues) > 0 {
		batch := issues
		if len(batch) > detailsBatchSize {
			batch = issues[:detailsBatchSize]
		}
		issues = issues[len(batch):]

		if outcome := w.syncDetails(ctx, batch); outcome.gated {
			break
		}
	}
}
