package sync

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// mockAPIClient implements APIClient for testing
type mockAPIClient struct {
	teams           []api.Team
	issuesByTeam    map[string][]api.Issue   // teamID -> all issues (will be paginated)
	statesByTeam    map[string][]api.State   // teamID -> states
	labelsByTeam    map[string][]api.Label   // teamID -> labels
	cyclesByTeam    map[string][]api.Cycle   // teamID -> cycles
	projectsByTeam  map[string][]api.Project // teamID -> projects
	membersByTeam   map[string][]api.User    // teamID -> members
	users           []api.User
	initiatives     []api.Initiative
	milestones      map[string][]api.ProjectMilestone // projectID -> milestones
	pageSize        int
	getTeamsCalls   int32
	getIssuesCalls  int32
	simulateError   error
}

func newMockAPIClient() *mockAPIClient {
	return &mockAPIClient{
		teams:          []api.Team{},
		issuesByTeam:   make(map[string][]api.Issue),
		statesByTeam:   make(map[string][]api.State),
		labelsByTeam:   make(map[string][]api.Label),
		cyclesByTeam:   make(map[string][]api.Cycle),
		projectsByTeam: make(map[string][]api.Project),
		membersByTeam:  make(map[string][]api.User),
		milestones:     make(map[string][]api.ProjectMilestone),
		pageSize:       100,
	}
}

func (m *mockAPIClient) GetTeams(ctx context.Context) ([]api.Team, error) {
	atomic.AddInt32(&m.getTeamsCalls, 1)
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.teams, nil
}

func (m *mockAPIClient) GetViewerTeams(ctx context.Context) ([]api.Team, error) {
	atomic.AddInt32(&m.getTeamsCalls, 1)
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.teams, nil
}

func (m *mockAPIClient) GetTeamIssuesPage(ctx context.Context, teamID string, cursor string, pageSize int) ([]api.Issue, api.PageInfo, error) {
	atomic.AddInt32(&m.getIssuesCalls, 1)
	if m.simulateError != nil {
		return nil, api.PageInfo{}, m.simulateError
	}

	issues, ok := m.issuesByTeam[teamID]
	if !ok {
		return []api.Issue{}, api.PageInfo{}, nil
	}

	// Use mock's pageSize if set, otherwise use the passed pageSize
	effectivePageSize := pageSize
	if m.pageSize > 0 {
		effectivePageSize = m.pageSize
	}

	// Parse cursor to get offset
	offset := 0
	if cursor != "" {
		for i := 0; i < len(cursor); i++ {
			if cursor[i] >= '0' && cursor[i] <= '9' {
				offset = offset*10 + int(cursor[i]-'0')
			}
		}
	}

	// Get page
	end := offset + effectivePageSize
	if end > len(issues) {
		end = len(issues)
	}

	if offset >= len(issues) {
		return []api.Issue{}, api.PageInfo{}, nil
	}

	page := issues[offset:end]
	hasNext := end < len(issues)
	nextCursor := ""
	if hasNext {
		nextCursor = string(rune('0' + end))
	}

	return page, api.PageInfo{HasNextPage: hasNext, EndCursor: nextCursor}, nil
}

func (m *mockAPIClient) GetTeamStates(ctx context.Context, teamID string) ([]api.State, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.statesByTeam[teamID], nil
}

func (m *mockAPIClient) GetTeamLabels(ctx context.Context, teamID string) ([]api.Label, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.labelsByTeam[teamID], nil
}

func (m *mockAPIClient) GetTeamCycles(ctx context.Context, teamID string) ([]api.Cycle, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.cyclesByTeam[teamID], nil
}

func (m *mockAPIClient) GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.projectsByTeam[teamID], nil
}

func (m *mockAPIClient) GetTeamMembers(ctx context.Context, teamID string) ([]api.User, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.membersByTeam[teamID], nil
}

func (m *mockAPIClient) GetUsers(ctx context.Context) ([]api.User, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.users, nil
}

func (m *mockAPIClient) GetInitiatives(ctx context.Context) ([]api.Initiative, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.initiatives, nil
}

func (m *mockAPIClient) GetProjectMilestones(ctx context.Context, projectID string) ([]api.ProjectMilestone, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.milestones[projectID], nil
}

func (m *mockAPIClient) GetIssueDetails(ctx context.Context, issueID string) (*api.IssueDetails, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return &api.IssueDetails{
		Comments:  []api.Comment{},
		Documents: []api.Document{},
	}, nil
}

func (m *mockAPIClient) GetIssueDetailsBatch(ctx context.Context, issueIDs []string) (map[string]*api.IssueDetails, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	result := make(map[string]*api.IssueDetails, len(issueIDs))
	for _, id := range issueIDs {
		result[id] = &api.IssueDetails{
			Comments:  []api.Comment{},
			Documents: []api.Document{},
		}
	}
	return result, nil
}

func (m *mockAPIClient) GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return []api.Comment{}, nil
}

func (m *mockAPIClient) GetIssueDocuments(ctx context.Context, issueID string) ([]api.Document, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return []api.Document{}, nil
}

func (m *mockAPIClient) GetIssueAttachments(ctx context.Context, issueID string) ([]api.Attachment, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return []api.Attachment{}, nil
}

func (m *mockAPIClient) AuthHeader() string {
	return "Bearer test-token"
}

func TestWorkerStartStop(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}

	cfg := Config{Interval: 100 * time.Millisecond}
	worker := NewWorker(mock, store, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start worker
	worker.Start(ctx)
	if !worker.Running() {
		t.Error("Worker should be running after Start()")
	}

	// Wait for initial sync
	time.Sleep(50 * time.Millisecond)

	// Stop worker
	worker.Stop()
	if worker.Running() {
		t.Error("Worker should not be running after Stop()")
	}

	// Verify GetTeams was called at least once
	if atomic.LoadInt32(&mock.getTeamsCalls) == 0 {
		t.Error("GetTeams should have been called")
	}
}

func TestWorkerSyncAllTeams(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	mock.teams = []api.Team{
		{ID: "team-1", Key: "ENG", Name: "Engineering"},
		{ID: "team-2", Key: "DSN", Name: "Design"},
	}

	// Add issues to each team
	now := time.Now()
	mock.issuesByTeam["team-1"] = []api.Issue{
		{ID: "issue-1", Identifier: "ENG-1", Title: "Issue 1", Team: &api.Team{ID: "team-1"}, UpdatedAt: now},
		{ID: "issue-2", Identifier: "ENG-2", Title: "Issue 2", Team: &api.Team{ID: "team-1"}, UpdatedAt: now.Add(-time.Minute)},
	}
	mock.issuesByTeam["team-2"] = []api.Issue{
		{ID: "issue-3", Identifier: "DSN-1", Title: "Design Issue", Team: &api.Team{ID: "team-2"}, UpdatedAt: now},
	}

	cfg := Config{Interval: time.Hour} // Long interval, we'll call SyncNow manually
	worker := NewWorker(mock, store, cfg)

	// Trigger sync manually
	err := worker.SyncNow(ctx)
	if err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	// Verify issues were synced
	engIssues, err := store.Queries().ListTeamIssues(ctx, "team-1")
	if err != nil {
		t.Fatalf("ListTeamIssues failed: %v", err)
	}
	if len(engIssues) != 2 {
		t.Errorf("Expected 2 ENG issues, got %d", len(engIssues))
	}

	dsnIssues, err := store.Queries().ListTeamIssues(ctx, "team-2")
	if err != nil {
		t.Fatalf("ListTeamIssues failed: %v", err)
	}
	if len(dsnIssues) != 1 {
		t.Errorf("Expected 1 DSN issue, got %d", len(dsnIssues))
	}

	// Verify teams were synced
	teams, err := store.Queries().ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}
	if len(teams) != 2 {
		t.Errorf("Expected 2 teams, got %d", len(teams))
	}
}

func TestWorkerSyncUntilUnchanged(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	baseTime := time.Now().Add(-time.Hour)

	// Pre-populate database with "old" issues
	for i := 0; i < 5; i++ {
		data := &db.IssueData{
			ID:         "old-issue-" + string(rune('A'+i)),
			Identifier: "TST-" + string(rune('1'+i)),
			Title:      "Old Issue " + string(rune('1'+i)),
			TeamID:     teamID,
			CreatedAt:  baseTime,
			UpdatedAt:  baseTime.Add(time.Duration(i) * time.Minute),
			Data:       []byte("{}"),
		}
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("setup failed: %v", err)
		}
	}

	// Update sync metadata with the last known update time
	lastUpdate := baseTime.Add(4 * time.Minute)
	if err := store.Queries().UpsertSyncMeta(ctx, db.UpsertSyncMetaParams{
		TeamID:             teamID,
		LastSyncedAt:       time.Now().Add(-10 * time.Minute),
		LastIssueUpdatedAt: db.ToNullTime(lastUpdate),
		IssueCount:         db.ToNullInt64(5),
	}); err != nil {
		t.Fatalf("setup sync meta failed: %v", err)
	}

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: teamID, Key: "TST", Name: "Test"}}

	// API returns: 2 new issues, then 3 unchanged issues
	// Worker should stop after hitting unchanged issues
	newTime := time.Now()
	mock.issuesByTeam[teamID] = []api.Issue{
		// New issues (updatedAt > lastUpdate)
		{ID: "new-1", Identifier: "TST-NEW1", Title: "New 1", Team: &api.Team{ID: teamID}, UpdatedAt: newTime},
		{ID: "new-2", Identifier: "TST-NEW2", Title: "New 2", Team: &api.Team{ID: teamID}, UpdatedAt: newTime.Add(-time.Second)},
		// Old unchanged issues (updatedAt <= lastUpdate)
		{ID: "old-issue-E", Identifier: "TST-5", Title: "Old 5", Team: &api.Team{ID: teamID}, UpdatedAt: lastUpdate},
		{ID: "old-issue-D", Identifier: "TST-4", Title: "Old 4", Team: &api.Team{ID: teamID}, UpdatedAt: lastUpdate.Add(-time.Minute)},
		{ID: "old-issue-C", Identifier: "TST-3", Title: "Old 3", Team: &api.Team{ID: teamID}, UpdatedAt: lastUpdate.Add(-2 * time.Minute)},
	}
	mock.pageSize = 10 // All in one page

	cfg := Config{Interval: time.Hour}
	worker := NewWorker(mock, store, cfg)

	// Sync
	err := worker.SyncNow(ctx)
	if err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	// Verify new issues were added
	issue1, err := store.Queries().GetIssueByIdentifier(ctx, "TST-NEW1")
	if err != nil {
		t.Errorf("New issue TST-NEW1 should exist: %v", err)
	}
	if issue1.Title != "New 1" {
		t.Errorf("Issue title mismatch: got %s", issue1.Title)
	}

	issue2, err := store.Queries().GetIssueByIdentifier(ctx, "TST-NEW2")
	if err != nil {
		t.Errorf("New issue TST-NEW2 should exist: %v", err)
	}
	if issue2.Title != "New 2" {
		t.Errorf("Issue title mismatch: got %s", issue2.Title)
	}

	// Total should be 7 (5 old + 2 new)
	issues, _ := store.Queries().ListTeamIssues(ctx, teamID)
	if len(issues) != 7 {
		t.Errorf("Expected 7 total issues, got %d", len(issues))
	}
}

func TestWorkerPagination(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: teamID, Key: "TST", Name: "Test"}}
	mock.pageSize = 2 // Small page size to test pagination

	// Create 5 issues - should require 3 pages
	now := time.Now()
	issues := make([]api.Issue, 5)
	for i := 0; i < 5; i++ {
		issues[i] = api.Issue{
			ID:         "issue-" + string(rune('A'+i)),
			Identifier: "TST-" + string(rune('1'+i)),
			Title:      "Issue " + string(rune('1'+i)),
			Team:       &api.Team{ID: teamID},
			UpdatedAt:  now.Add(-time.Duration(i) * time.Minute),
		}
	}
	mock.issuesByTeam[teamID] = issues

	cfg := Config{Interval: time.Hour}
	worker := NewWorker(mock, store, cfg)

	err := worker.SyncNow(ctx)
	if err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	// Verify all issues were synced
	dbIssues, err := store.Queries().ListTeamIssues(ctx, teamID)
	if err != nil {
		t.Fatalf("ListTeamIssues failed: %v", err)
	}
	if len(dbIssues) != 5 {
		t.Errorf("Expected 5 issues, got %d", len(dbIssues))
	}

	// Verify multiple pages were fetched
	calls := atomic.LoadInt32(&mock.getIssuesCalls)
	if calls < 3 {
		t.Errorf("Expected at least 3 GetTeamIssuesPage calls for 5 issues with pageSize 2, got %d", calls)
	}
}

func TestWorkerLastSync(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}

	cfg := Config{Interval: time.Hour}
	worker := NewWorker(mock, store, cfg)

	// Initially no sync
	if !worker.LastSync().IsZero() {
		t.Error("LastSync should be zero before any sync")
	}

	// Trigger sync
	err := worker.SyncNow(ctx)
	if err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	// LastSync should be recent
	if worker.LastSync().IsZero() {
		t.Error("LastSync should not be zero after sync")
	}
	if time.Since(worker.LastSync()) > time.Second {
		t.Error("LastSync should be within last second")
	}
}

func TestWorkerContextCancellation(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}

	cfg := Config{Interval: 50 * time.Millisecond}
	worker := NewWorker(mock, store, cfg)

	ctx, cancel := context.WithCancel(context.Background())

	worker.Start(ctx)
	time.Sleep(20 * time.Millisecond)

	// Cancel context should stop worker
	cancel()
	time.Sleep(100 * time.Millisecond)

	if worker.Running() {
		t.Error("Worker should stop when context is cancelled")
	}
}

func TestWorkerMultipleStartStop(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}

	cfg := Config{Interval: time.Hour}
	worker := NewWorker(mock, store, cfg)

	ctx := context.Background()

	// Start multiple times should be safe
	worker.Start(ctx)
	worker.Start(ctx) // Should be no-op

	if !worker.Running() {
		t.Error("Worker should be running")
	}

	// Stop multiple times should be safe
	worker.Stop()
	worker.Stop() // Should be no-op

	if worker.Running() {
		t.Error("Worker should not be running")
	}
}

func TestSyncMetadataTracking(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	now := time.Now()

	// Upsert sync metadata
	err := store.Queries().UpsertSyncMeta(ctx, db.UpsertSyncMetaParams{
		TeamID:             teamID,
		LastSyncedAt:       now,
		LastIssueUpdatedAt: db.ToNullTime(now.Add(-5 * time.Minute)),
		IssueCount:         db.ToNullInt64(100),
	})
	if err != nil {
		t.Fatalf("UpsertSyncMeta failed: %v", err)
	}

	// Retrieve and verify
	meta, err := store.Queries().GetSyncMeta(ctx, teamID)
	if err != nil {
		t.Fatalf("GetSyncMeta failed: %v", err)
	}

	if meta.TeamID != teamID {
		t.Errorf("TeamID mismatch: got %s, want %s", meta.TeamID, teamID)
	}
	if meta.IssueCount.Int64 != 100 {
		t.Errorf("IssueCount mismatch: got %d, want 100", meta.IssueCount.Int64)
	}
	if !meta.LastIssueUpdatedAt.Valid {
		t.Error("LastIssueUpdatedAt should be valid")
	}

	// Update with new values
	err = store.Queries().UpsertSyncMeta(ctx, db.UpsertSyncMetaParams{
		TeamID:             teamID,
		LastSyncedAt:       now.Add(time.Hour),
		LastIssueUpdatedAt: db.ToNullTime(now),
		IssueCount:         db.ToNullInt64(150),
	})
	if err != nil {
		t.Fatalf("UpsertSyncMeta update failed: %v", err)
	}

	// Verify update
	meta, _ = store.Queries().GetSyncMeta(ctx, teamID)
	if meta.IssueCount.Int64 != 150 {
		t.Errorf("Updated IssueCount mismatch: got %d, want 150", meta.IssueCount.Int64)
	}
}

func TestWorkerSyncTeamMetadata(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: teamID, Key: "TST", Name: "Test Team"}}

	// Add metadata for the team
	mock.statesByTeam[teamID] = []api.State{
		{ID: "state-1", Name: "Todo", Type: "unstarted"},
		{ID: "state-2", Name: "Done", Type: "completed"},
	}
	mock.labelsByTeam[teamID] = []api.Label{
		{ID: "label-1", Name: "Bug", Color: "#ff0000"},
	}
	mock.cyclesByTeam[teamID] = []api.Cycle{
		{ID: "cycle-1", Number: 1, Name: "Sprint 1"},
	}
	mock.projectsByTeam[teamID] = []api.Project{
		{ID: "project-1", Slug: "test-project", Name: "Test Project"},
	}
	mock.membersByTeam[teamID] = []api.User{
		{ID: "user-1", Email: "user1@test.com", Name: "User One"},
	}
	mock.milestones["project-1"] = []api.ProjectMilestone{
		{ID: "milestone-1", Name: "Phase 1"},
	}

	cfg := Config{Interval: time.Hour}
	worker := NewWorker(mock, store, cfg)

	err := worker.SyncNow(ctx)
	if err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	// Verify states were synced
	states, err := store.Queries().ListTeamStates(ctx, teamID)
	if err != nil {
		t.Fatalf("ListTeamStates failed: %v", err)
	}
	if len(states) != 2 {
		t.Errorf("Expected 2 states, got %d", len(states))
	}

	// Verify labels were synced
	labels, err := store.Queries().ListTeamLabels(ctx, sql.NullString{String: teamID, Valid: true})
	if err != nil {
		t.Fatalf("ListTeamLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("Expected 1 label, got %d", len(labels))
	}

	// Verify cycles were synced
	cycles, err := store.Queries().ListTeamCycles(ctx, teamID)
	if err != nil {
		t.Fatalf("ListTeamCycles failed: %v", err)
	}
	if len(cycles) != 1 {
		t.Errorf("Expected 1 cycle, got %d", len(cycles))
	}

	// Verify projects were synced
	projects, err := store.Queries().ListTeamProjects(ctx, teamID)
	if err != nil {
		t.Fatalf("ListTeamProjects failed: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("Expected 1 project, got %d", len(projects))
	}

	// Verify project milestones were synced
	milestones, err := store.Queries().ListProjectMilestones(ctx, "project-1")
	if err != nil {
		t.Fatalf("ListProjectMilestones failed: %v", err)
	}
	if len(milestones) != 1 {
		t.Errorf("Expected 1 milestone, got %d", len(milestones))
	}

	// Verify team members were synced
	members, err := store.Queries().ListTeamMembers(ctx, teamID)
	if err != nil {
		t.Fatalf("ListTeamMembers failed: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("Expected 1 team member, got %d", len(members))
	}
}

func TestWorkerSyncWorkspace(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}

	// Add workspace-level entities
	mock.users = []api.User{
		{ID: "user-1", Email: "alice@test.com", Name: "Alice", Active: true},
		{ID: "user-2", Email: "bob@test.com", Name: "Bob", Active: true},
	}
	mock.initiatives = []api.Initiative{
		{ID: "init-1", Slug: "q1-goals", Name: "Q1 Goals"},
	}

	cfg := Config{Interval: time.Hour}
	worker := NewWorker(mock, store, cfg)

	err := worker.SyncNow(ctx)
	if err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	// Verify users were synced
	users, err := store.Queries().ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("Expected 2 users, got %d", len(users))
	}

	// Verify initiatives were synced
	initiatives, err := store.Queries().ListInitiatives(ctx)
	if err != nil {
		t.Fatalf("ListInitiatives failed: %v", err)
	}
	if len(initiatives) != 1 {
		t.Errorf("Expected 1 initiative, got %d", len(initiatives))
	}
}

// Helper to open test store
func openTestStore(t *testing.T) *db.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	return store
}

// =============================================================================
// Embedded File Extraction Tests
// =============================================================================

func TestExtractFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "simple filename",
			url:      "https://uploads.linear.app/abc123/def456/screenshot.png",
			expected: "screenshot.png",
		},
		{
			name:     "filename with query params",
			url:      "https://uploads.linear.app/abc123/def456/image.jpg?token=xyz",
			expected: "image.jpg",
		},
		{
			name:     "UUID-prefixed filename",
			url:      "https://uploads.linear.app/abc123/def456/a1b2c3d4-e5f6-7890-abcd-ef1234567890-screenshot.png",
			expected: "screenshot.png",
		},
		{
			name:     "simple filename without UUID",
			url:      "https://uploads.linear.app/abc123/design.pdf",
			expected: "design.pdf",
		},
		{
			name:     "empty path segment",
			url:      "https://uploads.linear.app/",
			expected: "file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilename(tt.url)
			if got != tt.expected {
				t.Errorf("extractFilename(%q) = %q, want %q", tt.url, got, tt.expected)
			}
		})
	}
}

func TestDetectMIMEType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		filename string
		expected string
	}{
		{"image.png", "image/png"},
		{"photo.jpg", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"animation.gif", "image/gif"},
		{"icon.webp", "image/webp"},
		{"logo.svg", "image/svg+xml"},
		{"document.pdf", "application/pdf"},
		{"report.doc", "application/msword"},
		{"report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"data.xls", "application/vnd.ms-excel"},
		{"data.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"archive.zip", "application/zip"},
		{"video.mp4", "video/mp4"},
		{"video.mov", "video/quicktime"},
		{"audio.mp3", "audio/mpeg"},
		{"unknown.xyz", "application/octet-stream"},
		{"noextension", "application/octet-stream"},
		{"IMAGE.PNG", "image/png"}, // Case insensitive
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := detectMIMEType(tt.filename)
			if got != tt.expected {
				t.Errorf("detectMIMEType(%q) = %q, want %q", tt.filename, got, tt.expected)
			}
		})
	}
}

func TestLinearCDNPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name:    "single URL in markdown",
			content: "Check out this image: ![screenshot](https://uploads.linear.app/abc123/def456/screenshot.png)",
			expected: []string{
				"https://uploads.linear.app/abc123/def456/screenshot.png",
			},
		},
		{
			name: "multiple URLs",
			content: `Here are the designs:
![design1](https://uploads.linear.app/org1/file1/design1.png)
![design2](https://uploads.linear.app/org2/file2/design2.jpg)`,
			expected: []string{
				"https://uploads.linear.app/org1/file1/design1.png",
				"https://uploads.linear.app/org2/file2/design2.jpg",
			},
		},
		{
			name:     "URL with query params",
			content:  "Image: https://uploads.linear.app/abc/def/image.png?token=xyz123",
			expected: []string{"https://uploads.linear.app/abc/def/image.png?token=xyz123"},
		},
		{
			name:     "no Linear CDN URLs",
			content:  "Regular text with https://example.com/image.png",
			expected: nil,
		},
		{
			name:     "empty content",
			content:  "",
			expected: nil,
		},
		{
			name:    "URL in angle brackets",
			content: "See <https://uploads.linear.app/abc/def/file.pdf>",
			expected: []string{
				"https://uploads.linear.app/abc/def/file.pdf",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := linearCDNPattern.FindAllString(tt.content, -1)

			if len(got) != len(tt.expected) {
				t.Errorf("Found %d URLs, want %d\nGot: %v\nWant: %v", len(got), len(tt.expected), got, tt.expected)
				return
			}

			for i, url := range tt.expected {
				if got[i] != url {
					t.Errorf("URL[%d] = %q, want %q", i, got[i], url)
				}
			}
		})
	}
}

func TestExtractAndStoreEmbeddedFiles(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	cfg := Config{Interval: time.Hour}
	worker := NewWorker(mock, store, cfg)

	ctx := context.Background()
	issueID := "test-issue-123"

	// Content with embedded files
	// - First file: markdown with display name becomes the filename
	// - Second file: bare URL, filename extracted from path
	content := `Here's a screenshot of the bug:
![bug-screenshot.png](https://uploads.linear.app/workspace1/issue1/bug-screenshot.png)

And here's the design spec:
https://uploads.linear.app/workspace1/issue1/design-spec.pdf`

	worker.extractAndStoreEmbeddedFiles(ctx, issueID, content, "description")

	// Verify files were stored
	files, err := store.Queries().ListIssueEmbeddedFiles(ctx, issueID)
	if err != nil {
		t.Fatalf("ListIssueEmbeddedFiles failed: %v", err)
	}

	if len(files) != 2 {
		t.Errorf("Expected 2 embedded files, got %d", len(files))
	}

	// Verify file details
	foundPNG := false
	foundPDF := false
	for _, f := range files {
		if f.Filename == "bug-screenshot.png" {
			foundPNG = true
			if f.MimeType.String != "image/png" {
				t.Errorf("Expected MIME type image/png, got %s", f.MimeType.String)
			}
			if f.Source != "description" {
				t.Errorf("Expected source 'description', got %s", f.Source)
			}
		}
		if f.Filename == "design-spec.pdf" {
			foundPDF = true
			if f.MimeType.String != "application/pdf" {
				t.Errorf("Expected MIME type application/pdf, got %s", f.MimeType.String)
			}
		}
	}

	if !foundPNG {
		t.Error("Did not find bug-screenshot.png in stored files")
	}
	if !foundPDF {
		t.Error("Did not find design-spec.pdf in stored files")
	}
}
