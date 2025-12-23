// Package sync implements background synchronization of Linear issues to SQLite.
//
// The sync strategy is "sync until unchanged": fetch issues ordered by updatedAt DESC
// and stop when we hit issues that haven't changed since our last sync. This allows
// efficient incremental updates without fetching all issues on every sync.
package sync

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// APIClient defines the interface for API operations needed by the sync worker
type APIClient interface {
	// Teams
	GetTeams(ctx context.Context) ([]api.Team, error)
	GetTeamIssuesPage(ctx context.Context, teamID string, cursor string, pageSize int) ([]api.Issue, api.PageInfo, error)

	// Team metadata
	GetTeamStates(ctx context.Context, teamID string) ([]api.State, error)
	GetTeamLabels(ctx context.Context, teamID string) ([]api.Label, error)
	GetTeamCycles(ctx context.Context, teamID string) ([]api.Cycle, error)
	GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error)
	GetTeamMembers(ctx context.Context, teamID string) ([]api.User, error)

	// Workspace-level entities
	GetUsers(ctx context.Context) ([]api.User, error)
	GetInitiatives(ctx context.Context) ([]api.Initiative, error)

	// Project details
	GetProjectMilestones(ctx context.Context, projectID string) ([]api.ProjectMilestone, error)
}

// Worker handles background synchronization of Linear issues to SQLite
type Worker struct {
	client   APIClient
	store    *db.Store
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
	mu       sync.RWMutex
	running  bool
	lastSync time.Time
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
		client:   client,
		store:    store,
		interval: cfg.Interval,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
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
	TeamID       string
	IssuesAdded  int
	IssuesUpdated int
	PagesFetched int
	Duration     time.Duration
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
	if err != nil {
		return err
	}

	// Update sync metadata
	count, _ := w.store.Queries().GetTeamIssueCount(ctx, team.ID)
	latestUpdatedAtRaw, _ := w.store.Queries().GetLatestTeamIssueUpdatedAt(ctx, team.ID)

	var lastIssueUpdatedAt time.Time
	if latestUpdatedAtRaw != nil {
		// MAX() returns different types depending on the driver
		switch v := latestUpdatedAtRaw.(type) {
		case time.Time:
			lastIssueUpdatedAt = v
		case string:
			lastIssueUpdatedAt, _ = time.Parse(time.RFC3339, v)
		}
	}

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

			if isNew {
				added++
			} else {
				updated++
			}
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

// syncWorkspace syncs workspace-level entities: users and initiatives
func (w *Worker) syncWorkspace(ctx context.Context) error {
	var errs []error

	// Sync users
	if err := w.syncUsers(ctx); err != nil {
		errs = append(errs, fmt.Errorf("users: %w", err))
	}

	// Sync initiatives
	if err := w.syncInitiatives(ctx); err != nil {
		errs = append(errs, fmt.Errorf("initiatives: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("workspace sync errors: %v", errs)
	}
	return nil
}

func (w *Worker) syncUsers(ctx context.Context) error {
	users, err := w.client.GetUsers(ctx)
	if err != nil {
		return err
	}

	for _, user := range users {
		params, err := db.APIUserToDBUser(user)
		if err != nil {
			log.Printf("[sync] convert user %s failed: %v", user.Email, err)
			continue
		}
		if err := w.store.Queries().UpsertUser(ctx, params); err != nil {
			log.Printf("[sync] upsert user %s failed: %v", user.Email, err)
		}
	}

	log.Printf("[sync] synced %d users", len(users))
	return nil
}

func (w *Worker) syncInitiatives(ctx context.Context) error {
	initiatives, err := w.client.GetInitiatives(ctx)
	if err != nil {
		return err
	}

	for _, initiative := range initiatives {
		params, err := db.APIInitiativeToDBInitiative(initiative)
		if err != nil {
			log.Printf("[sync] convert initiative %s failed: %v", initiative.Slug, err)
			continue
		}
		if err := w.store.Queries().UpsertInitiative(ctx, params); err != nil {
			log.Printf("[sync] upsert initiative %s failed: %v", initiative.Slug, err)
			continue
		}

		// Sync initiative-project associations
		if err := w.syncInitiativeProjects(ctx, initiative); err != nil {
			log.Printf("[sync] sync initiative %s projects failed: %v", initiative.Slug, err)
		}
	}

	log.Printf("[sync] synced %d initiatives", len(initiatives))
	return nil
}

func (w *Worker) syncInitiativeProjects(ctx context.Context, initiative api.Initiative) error {
	// Get projects from the initiative's Projects field
	for _, project := range initiative.Projects.Nodes {
		if err := w.store.Queries().UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
			InitiativeID: initiative.ID,
			ProjectID:    project.ID,
			SyncedAt:     db.Now(),
		}); err != nil {
			return fmt.Errorf("upsert initiative-project %s-%s: %w", initiative.ID, project.ID, err)
		}
	}
	return nil
}

// =============================================================================
// Team Metadata Sync
// =============================================================================

// syncTeamMetadata syncs all metadata for a team: states, labels, cycles, projects, members
func (w *Worker) syncTeamMetadata(ctx context.Context, team api.Team) error {
	var errs []error

	// Sync states
	if err := w.syncTeamStates(ctx, team.ID); err != nil {
		errs = append(errs, fmt.Errorf("states: %w", err))
	}

	// Sync labels
	if err := w.syncTeamLabels(ctx, team.ID); err != nil {
		errs = append(errs, fmt.Errorf("labels: %w", err))
	}

	// Sync cycles
	if err := w.syncTeamCycles(ctx, team.ID); err != nil {
		errs = append(errs, fmt.Errorf("cycles: %w", err))
	}

	// Sync projects (includes milestones)
	if err := w.syncTeamProjects(ctx, team.ID); err != nil {
		errs = append(errs, fmt.Errorf("projects: %w", err))
	}

	// Sync team members
	if err := w.syncTeamMembers(ctx, team.ID); err != nil {
		errs = append(errs, fmt.Errorf("members: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("team %s metadata errors: %v", team.ID, errs)
	}
	return nil
}

func (w *Worker) syncTeamStates(ctx context.Context, teamID string) error {
	states, err := w.client.GetTeamStates(ctx, teamID)
	if err != nil {
		return err
	}

	for _, state := range states {
		params, err := db.APIStateToDBState(state, teamID)
		if err != nil {
			log.Printf("[sync] convert state %s failed: %v", state.Name, err)
			continue
		}
		if err := w.store.Queries().UpsertState(ctx, params); err != nil {
			log.Printf("[sync] upsert state %s failed: %v", state.Name, err)
		}
	}

	return nil
}

func (w *Worker) syncTeamLabels(ctx context.Context, teamID string) error {
	labels, err := w.client.GetTeamLabels(ctx, teamID)
	if err != nil {
		return err
	}

	for _, label := range labels {
		params, err := db.APILabelToDBLabel(label, teamID)
		if err != nil {
			log.Printf("[sync] convert label %s failed: %v", label.Name, err)
			continue
		}
		if err := w.store.Queries().UpsertLabel(ctx, params); err != nil {
			log.Printf("[sync] upsert label %s failed: %v", label.Name, err)
		}
	}

	return nil
}

func (w *Worker) syncTeamCycles(ctx context.Context, teamID string) error {
	cycles, err := w.client.GetTeamCycles(ctx, teamID)
	if err != nil {
		return err
	}

	for _, cycle := range cycles {
		params, err := db.APICycleToDBCycle(cycle, teamID)
		if err != nil {
			log.Printf("[sync] convert cycle %s failed: %v", cycle.Name, err)
			continue
		}
		if err := w.store.Queries().UpsertCycle(ctx, params); err != nil {
			log.Printf("[sync] upsert cycle %s failed: %v", cycle.Name, err)
		}
	}

	return nil
}

func (w *Worker) syncTeamProjects(ctx context.Context, teamID string) error {
	projects, err := w.client.GetTeamProjects(ctx, teamID)
	if err != nil {
		return err
	}

	for _, project := range projects {
		params, err := db.APIProjectToDBProject(project)
		if err != nil {
			log.Printf("[sync] convert project %s failed: %v", project.Slug, err)
			continue
		}
		if err := w.store.Queries().UpsertProject(ctx, params); err != nil {
			log.Printf("[sync] upsert project %s failed: %v", project.Slug, err)
			continue
		}

		// Upsert project-team association
		if err := w.store.Queries().UpsertProjectTeam(ctx, db.UpsertProjectTeamParams{
			ProjectID: project.ID,
			TeamID:    teamID,
			SyncedAt:  db.Now(),
		}); err != nil {
			log.Printf("[sync] upsert project-team %s-%s failed: %v", project.ID, teamID, err)
		}

		// Sync project milestones
		if err := w.syncProjectMilestones(ctx, project.ID); err != nil {
			log.Printf("[sync] sync project %s milestones failed: %v", project.Slug, err)
		}
	}

	return nil
}

func (w *Worker) syncProjectMilestones(ctx context.Context, projectID string) error {
	milestones, err := w.client.GetProjectMilestones(ctx, projectID)
	if err != nil {
		return err
	}

	for _, milestone := range milestones {
		params, err := db.APIProjectMilestoneToDBMilestone(milestone, projectID)
		if err != nil {
			log.Printf("[sync] convert milestone %s failed: %v", milestone.Name, err)
			continue
		}
		if err := w.store.Queries().UpsertProjectMilestone(ctx, params); err != nil {
			log.Printf("[sync] upsert milestone %s failed: %v", milestone.Name, err)
		}
	}

	return nil
}

func (w *Worker) syncTeamMembers(ctx context.Context, teamID string) error {
	members, err := w.client.GetTeamMembers(ctx, teamID)
	if err != nil {
		return err
	}

	for _, member := range members {
		// Ensure user exists in users table
		params, err := db.APIUserToDBUser(member)
		if err != nil {
			log.Printf("[sync] convert member %s failed: %v", member.Email, err)
			continue
		}
		if err := w.store.Queries().UpsertUser(ctx, params); err != nil {
			log.Printf("[sync] upsert member user %s failed: %v", member.Email, err)
			continue
		}

		// Upsert team membership
		if err := w.store.Queries().UpsertTeamMember(ctx, db.UpsertTeamMemberParams{
			TeamID:   teamID,
			UserID:   member.ID,
			SyncedAt: db.Now(),
		}); err != nil {
			log.Printf("[sync] upsert team member %s failed: %v", member.Email, err)
		}
	}

	return nil
}
