package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/repo"
)

// mockBudgetReporter implements BudgetReporter for testing
type mockBudgetReporter struct {
	count int
	pct   float64
}

func (m *mockBudgetReporter) BudgetSnapshot() (int, float64) {
	return m.count, m.pct
}

// fakeClock drives the Worker's clock seam (now/newTimer/newTicker) in tests
// — the worker-side analogue of ratebudget_test.go's fakeClock, plus recorded
// timer/ticker channels the test fires explicitly. The time is mutex'd
// because the run loop reads now() from its own goroutine.
type fakeClock struct {
	mu gosync.Mutex
	t  time.Time

	timerCh  chan time.Time // handed out by newTimer; the test fires it
	tickerCh chan time.Time // handed out by newTicker; the test feeds ticks
	tickerD  time.Duration  // duration requested by the last newTicker call

	// Each newTimer call reports its requested duration here (buffered, so
	// the worker never blocks on it). A receive doubles as the handshake
	// that the worker has reached — and parked on — the timer.
	timerSet chan time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		t:        time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		timerCh:  make(chan time.Time),
		tickerCh: make(chan time.Time),
		timerSet: make(chan time.Duration, 4),
	}
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func (f *fakeClock) newTimer(d time.Duration) (<-chan time.Time, func() bool) {
	f.timerSet <- d
	return f.timerCh, func() bool { return true }
}

func (f *fakeClock) newTicker(d time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	f.tickerD = d
	f.mu.Unlock()
	return f.tickerCh, func() {}
}

func (f *fakeClock) tickerInterval() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tickerD
}

// install wires the fake into a worker's clock seam.
func (f *fakeClock) install(w *Worker) {
	w.now = f.now
	w.newTimer = f.newTimer
	w.newTicker = f.newTicker
}

// mockAPIClient implements APIClient for testing
type mockAPIClient struct {
	teams            []api.Team
	issuesByTeam     map[string][]api.Issue   // teamID -> all issues (will be paginated)
	statesByTeam     map[string][]api.State   // teamID -> states
	labelsByTeam     map[string][]api.Label   // teamID -> labels
	cyclesByTeam     map[string][]api.Cycle   // teamID -> cycles
	projectsByTeam   map[string][]api.Project // teamID -> projects
	membersByTeam    map[string][]api.User    // teamID -> members
	users            []api.User
	initiatives      []api.Initiative
	projectLabels    []api.ProjectLabel
	projectLabelsErr error // if set, GetProjectLabels fails with this (catalog isolation tests)
	pageSize         int
	getTeamsCalls    int32
	getIssuesCalls   int32
	simulateError    error
	rateLimitResetAt time.Time                    // M-3: configurable reset time for adaptive backoff tests
	detailsByIssue   map[string]*api.IssueDetails // issueID -> canned details for GetIssueDetailsBatch
	detailsCalls     int32                        // number of GetIssueDetailsBatch calls (incl. failed ones)
	onDetailsBatch   func()                       // if set, runs inside GetIssueDetailsBatch (simulates writes racing the fetch)
	onTeamMetadata   func()                       // if set, runs inside GetTeamMetadata (simulates writes racing the fetch)
	onWorkspace      func()                       // if set, runs inside GetWorkspace (simulates writes racing the fetch)
	viewerErr        error                        // if set, GetViewer (the cold-start budget probe) fails with this
	getViewerCalls   int32
	projectsProbeErr error               // if set, GetTeamProjectsNewestPage fails with this (probe-error tests)
	issueIDsByTeam   map[string][]string // teamID -> authoritative bare issue IDs (the reconcile sweep's drain)
	issueIDsErr      error               // if set, GetTeamIssueIDs fails with this (all-or-nothing drain tests)
	opMu             gosync.Mutex
	opOrder          []string // call order across GetViewer/GetWorkspace/GetTeamMetadata/GetTeams/GetTeamProjectsNewestPage (probe-sequencing + lean/full cycle tests)
}

// recordOp appends op to the observed call order.
func (m *mockAPIClient) recordOp(op string) {
	m.opMu.Lock()
	m.opOrder = append(m.opOrder, op)
	m.opMu.Unlock()
}

// callOrder returns a snapshot of the observed call order.
func (m *mockAPIClient) callOrder() []string {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return append([]string(nil), m.opOrder...)
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
		pageSize:       100,
		detailsByIssue: make(map[string]*api.IssueDetails),
	}
}

func (m *mockAPIClient) GetTeams(ctx context.Context) ([]api.Team, error) {
	m.recordOp("GetTeams")
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

func (m *mockAPIClient) GetTeamMetadata(ctx context.Context, teamID string) (*api.TeamMetadata, error) {
	m.recordOp("GetTeamMetadata")
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	if m.onTeamMetadata != nil {
		m.onTeamMetadata()
	}
	return &api.TeamMetadata{
		States:   m.statesByTeam[teamID],
		Labels:   m.labelsByTeam[teamID],
		Cycles:   m.cyclesByTeam[teamID],
		Projects: m.projectsByTeam[teamID],
		Members:  m.membersByTeam[teamID],
	}, nil
}

// GetTeamProjectsNewestPage serves projectsByTeam sorted updatedAt DESC with
// offset cursors — the projects sibling of the GetTeamIssuesPage mock.
func (m *mockAPIClient) GetTeamProjectsNewestPage(ctx context.Context, teamID string, cursor string, pageSize int) ([]api.Project, api.PageInfo, error) {
	m.recordOp("GetTeamProjectsNewestPage")
	if m.projectsProbeErr != nil {
		return nil, api.PageInfo{}, m.projectsProbeErr
	}
	if m.simulateError != nil {
		return nil, api.PageInfo{}, m.simulateError
	}

	projects := append([]api.Project(nil), m.projectsByTeam[teamID]...)
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].UpdatedAt.After(projects[j].UpdatedAt)
	})

	offset := 0
	if cursor != "" {
		offset, _ = strconv.Atoi(cursor)
	}
	if offset >= len(projects) {
		return []api.Project{}, api.PageInfo{}, nil
	}
	end := offset + pageSize
	if end > len(projects) {
		end = len(projects)
	}
	page := projects[offset:end]
	hasNext := end < len(projects)
	nextCursor := ""
	if hasNext {
		nextCursor = strconv.Itoa(end)
	}
	return page, api.PageInfo{HasNextPage: hasNext, EndCursor: nextCursor}, nil
}

func (m *mockAPIClient) GetWorkspace(ctx context.Context) (*api.WorkspaceData, error) {
	m.recordOp("GetWorkspace")
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	if m.onWorkspace != nil {
		m.onWorkspace()
	}
	return &api.WorkspaceData{
		Users:       m.users,
		Initiatives: m.initiatives,
	}, nil
}

func (m *mockAPIClient) GetProjectLabels(ctx context.Context) ([]api.ProjectLabel, error) {
	m.recordOp("GetProjectLabels")
	if m.projectLabelsErr != nil {
		return nil, m.projectLabelsErr
	}
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.projectLabels, nil
}

// GetProjectMilestones removed — milestones now come inline from GetTeamProjects

func (m *mockAPIClient) GetIssueDetailsBatch(ctx context.Context, issueIDs []string) (map[string]*api.IssueDetails, error) {
	atomic.AddInt32(&m.detailsCalls, 1)
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	if m.onDetailsBatch != nil {
		m.onDetailsBatch()
	}
	result := make(map[string]*api.IssueDetails, len(issueIDs))
	for _, id := range issueIDs {
		if d, ok := m.detailsByIssue[id]; ok {
			result[id] = d
			continue
		}
		result[id] = &api.IssueDetails{
			Comments:  []api.Comment{},
			Documents: []api.Document{},
		}
	}
	return result, nil
}

func (m *mockAPIClient) GetTeamIssueIDs(ctx context.Context, teamID string) ([]string, error) {
	m.recordOp("GetTeamIssueIDs")
	if m.issueIDsErr != nil {
		return nil, m.issueIDsErr
	}
	return m.issueIDsByTeam[teamID], nil
}

func (m *mockAPIClient) AuthHeader() string {
	return "Bearer test-token"
}

func (m *mockAPIClient) GetViewer(ctx context.Context) (*api.User, error) {
	m.recordOp("GetViewer")
	atomic.AddInt32(&m.getViewerCalls, 1)
	if m.viewerErr != nil {
		return nil, m.viewerErr
	}
	return &api.User{ID: "viewer-1", Name: "Test Viewer", Email: "viewer@example.com"}, nil
}

func (m *mockAPIClient) RateLimitResetAt() time.Time {
	return m.rateLimitResetAt
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

	// Stop worker. Stop blocks on run()'s doneCh, and run() always completes
	// the probe and the initial sync cycle before it can observe stopCh — so
	// GetTeams has fired by the time Stop returns, no settling sleep needed.
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

	// Cancel context should stop worker. run() closes doneCh on every exit
	// (after clearing running), so a blocking read is the synchronization
	// point — the old deadline poll flaked on loaded CI runners where the
	// in-flight initial sync outlived the grace period.
	cancel()
	<-worker.doneCh

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
		{
			ID: "project-1", Slug: "test-project", Name: "Test Project",
			Milestones: &api.ProjectMilestones{
				Nodes: []api.ProjectMilestone{
					{ID: "milestone-1", Name: "Phase 1"},
				},
			},
		},
	}
	mock.membersByTeam[teamID] = []api.User{
		{ID: "user-1", Email: "user1@test.com", Name: "User One"},
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

// TestWorkerSyncWorkspaceSurfacesUpsertFailure: a workspace pass whose
// upserts fail must return an error, not report success (the errs
// aggregation was once dead code — every failure path logged and continued,
// so a fully-failed pass returned nil).
func TestWorkerSyncWorkspaceSurfacesUpsertFailure(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()

	mock := newMockAPIClient()
	mock.users = []api.User{{ID: "user-1", Email: "alice@test.com", Name: "Alice", Active: true}}
	mock.initiatives = []api.Initiative{{ID: "init-1", Slug: "q1-goals", Name: "Q1 Goals"}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	// Closing the store makes every upsert fail while the fetch succeeds.
	store.Close()

	err := worker.syncWorkspace(ctx)
	if err == nil {
		t.Fatal("syncWorkspace returned nil with every upsert failing")
	}
	for _, want := range []string{"upsert user alice@test.com", "upsert initiative q1-goals"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
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
// Phase 3 & 4 Sync Fix Tests (H-1, H-5, M-3)
// =============================================================================

// TestViewerCacheRoundTrip verifies H-1: SetViewerUserID / GetViewerUserID round-trip
// and that the singleton constraint holds (only one row ever exists).
func TestViewerCacheRoundTrip(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Before any write, GetViewerUserID should return sql.ErrNoRows
	_, err := store.Queries().GetViewerUserID(ctx)
	if err == nil {
		t.Error("expected error when viewer cache is empty, got nil")
	}

	// Set a viewer
	now := time.Now()
	err = store.Queries().SetViewerUserID(ctx, db.SetViewerUserIDParams{
		UserID:   "user-1",
		SyncedAt: now,
	})
	if err != nil {
		t.Fatalf("SetViewerUserID failed: %v", err)
	}

	// Read it back
	id, err := store.Queries().GetViewerUserID(ctx)
	if err != nil {
		t.Fatalf("GetViewerUserID failed: %v", err)
	}
	if id != "user-1" {
		t.Errorf("GetViewerUserID = %q, want %q", id, "user-1")
	}

	// Upsert a different user — singleton constraint should replace the row
	err = store.Queries().SetViewerUserID(ctx, db.SetViewerUserIDParams{
		UserID:   "user-2",
		SyncedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("SetViewerUserID (overwrite) failed: %v", err)
	}

	// Should now return the new value
	id, err = store.Queries().GetViewerUserID(ctx)
	if err != nil {
		t.Fatalf("GetViewerUserID (after overwrite) failed: %v", err)
	}
	if id != "user-2" {
		t.Errorf("GetViewerUserID after overwrite = %q, want %q", id, "user-2")
	}
}

// TestPendingDetailSyncQueueAndDrain verifies H-5: issues skipped due to rate limiting
// are persisted in pending_detail_sync and drained on the next sync cycle.
func TestPendingDetailSyncQueueAndDrain(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	// Return a rate-limit error so syncIssueDetailsBatch persists to the pending queue
	mock.simulateError = fmt.Errorf("rate limit exceeded")

	cfg := Config{Interval: time.Hour}
	worker := NewWorker(mock, store, cfg)

	issues := []issueRef{
		{ID: "issue-1", Identifier: "TST-1"},
		{ID: "issue-2", Identifier: "TST-2"},
	}

	// Call syncDetails directly; the rate-limit error should queue the issues
	outcome := worker.syncDetails(ctx, issues)
	if !outcome.gated {
		t.Error("rate-limit fetch failure should gate the outcome")
	}

	// Verify both issues landed in the pending queue
	pending, err := store.Queries().ListPendingDetailSync(ctx)
	if err != nil {
		t.Fatalf("ListPendingDetailSync failed: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending issues, got %d", len(pending))
	}

	// Clear the simulated error and reset the rate-limit expiry so the drain runs
	mock.simulateError = nil
	worker.rateLimitMu.Lock()
	worker.rateLimitExpiry = time.Time{}
	worker.rateLimitMu.Unlock()

	// Drain the pending queue
	worker.drainPendingDetailSync(ctx)

	// Pending queue should now be empty (DeletePendingDetailSync called per issue)
	pending, err = store.Queries().ListPendingDetailSync(ctx)
	if err != nil {
		t.Fatalf("ListPendingDetailSync (after drain) failed: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected 0 pending issues after drain, got %d", len(pending))
	}
}

// TestDetailsSyncPrunesStaleRows: the details sync must delete rows Linear no
// longer returns — a comment deleted in Linear, or a phantom left by a delete
// whose SQLite forget failed (the store is the listing source of truth, so an
// unpruned phantom resurrects the file forever). Rows the fetch DID return are
// re-stamped and must survive.
func TestDetailsSyncPrunesStaleRows(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	live := api.Comment{ID: "comment-live", Body: "still on Linear", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	phantom := api.Comment{ID: "comment-phantom", Body: "deleted on Linear, forget failed", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	for _, c := range []api.Comment{live, phantom} {
		params, err := db.APICommentToDBComment(c, "issue-1")
		if err != nil {
			t.Fatalf("convert comment: %v", err)
		}
		// Backdate synced_at so both predate the sync's prune cutoff.
		params.SyncedAt = time.Now().Add(-time.Minute)
		if err := store.Queries().UpsertComment(ctx, params); err != nil {
			t.Fatalf("seed comment: %v", err)
		}
	}

	mock := newMockAPIClient()
	mock.detailsByIssue["issue-1"] = &api.IssueDetails{Comments: []api.Comment{live}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	worker.syncDetails(ctx, []issueRef{{ID: "issue-1", Identifier: "TST-1"}})

	comments, err := store.Queries().ListIssueComments(ctx, "issue-1")
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 1 || comments[0].ID != "comment-live" {
		ids := []string{}
		for _, c := range comments {
			ids = append(ids, c.ID)
		}
		t.Errorf("after details sync comments = %v, want [comment-live] (phantom pruned, live retained)", ids)
	}
}

// TestDetailsSyncPruneSparesMidFetchCreates: a comment created through FUSE
// while the details fetch is in flight is absent from the fetch response but
// must survive pruning — its synced_at postdates the pre-fetch cutoff. This is
// the guarantee the cutoff exists for; a naive "delete everything not in the
// response" would eat the freshly-created comment.
func TestDetailsSyncPruneSparesMidFetchCreates(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	// The fetch returns no comments for issue-1…
	mock.detailsByIssue["issue-1"] = &api.IssueDetails{Comments: []api.Comment{}}
	// …but while it is "in flight", a comment lands through the FUSE write path.
	mock.onDetailsBatch = func() {
		params, err := db.APICommentToDBComment(api.Comment{ID: "comment-raced", Body: "created mid-fetch", CreatedAt: time.Now(), UpdatedAt: time.Now()}, "issue-1")
		if err != nil {
			t.Errorf("convert raced comment: %v", err)
			return
		}
		if err := store.Queries().UpsertComment(ctx, params); err != nil {
			t.Errorf("upsert raced comment: %v", err)
		}
	}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	worker.syncDetails(ctx, []issueRef{{ID: "issue-1", Identifier: "TST-1"}})

	comments, err := store.Queries().ListIssueComments(ctx, "issue-1")
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 1 || comments[0].ID != "comment-raced" {
		t.Errorf("comments = %v, want the mid-fetch create to survive pruning", comments)
	}
}

// TestDetailsSyncFullPageSkipsPrune: a full page (IssueDetailsPageSize rows)
// may be truncated by the API's page cap, so pruning against it could delete
// real rows — the guard must skip pruning entirely.
func TestDetailsSyncFullPageSkipsPrune(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// A row beyond the page cap: real on Linear, just not in the fetched page.
	beyond, err := db.APICommentToDBComment(api.Comment{ID: "comment-beyond-page", Body: "real, past the cap", CreatedAt: time.Now(), UpdatedAt: time.Now()}, "issue-1")
	if err != nil {
		t.Fatalf("convert comment: %v", err)
	}
	beyond.SyncedAt = time.Now().Add(-time.Minute)
	if err := store.Queries().UpsertComment(ctx, beyond); err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	full := make([]api.Comment, api.IssueDetailsPageSize)
	for i := range full {
		full[i] = api.Comment{ID: fmt.Sprintf("comment-%03d", i), Body: "page filler", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	}
	mock := newMockAPIClient()
	mock.detailsByIssue["issue-1"] = &api.IssueDetails{Comments: full}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	worker.syncDetails(ctx, []issueRef{{ID: "issue-1", Identifier: "TST-1"}})

	comments, err := store.Queries().ListIssueComments(ctx, "issue-1")
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != api.IssueDetailsPageSize+1 {
		t.Errorf("comments = %d, want %d — a full (possibly truncated) page must not prune", len(comments), api.IssueDetailsPageSize+1)
	}
}

// TestDetailsSyncPrunesStaleDocsAndAttachments guards the document/attachment
// wiring of the near-identical collection specs the details sync now routes
// through reconcile.PersistIssueDetails: a mis-wired items slice or issueID
// (e.g. handing details.Comments to the document spec, or the wrong id to a
// prune) would leave a stale row un-pruned or a live one deleted. Comments have
// their own prune test above; this covers the other two collections end-to-end.
func TestDetailsSyncPrunesStaleDocsAndAttachments(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	old := time.Now().Add(-time.Minute)
	docIssue := &api.Issue{ID: "issue-1"} // documents key their issue_id off document.Issue.ID

	liveDoc := api.Document{ID: "doc-live", SlugID: "slug-live", Title: "Live", Issue: docIssue, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	staleDoc := api.Document{ID: "doc-stale", SlugID: "slug-stale", Title: "Stale", Issue: docIssue, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	for _, d := range []api.Document{liveDoc, staleDoc} {
		params, err := db.APIDocumentToDBDocument(d)
		if err != nil {
			t.Fatalf("convert document: %v", err)
		}
		params.SyncedAt = old
		if err := store.Queries().UpsertDocument(ctx, params); err != nil {
			t.Fatalf("seed document: %v", err)
		}
	}

	liveAtt := api.Attachment{ID: "att-live", Title: "Live", URL: "https://x/live", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	staleAtt := api.Attachment{ID: "att-stale", Title: "Stale", URL: "https://x/stale", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	for _, a := range []api.Attachment{liveAtt, staleAtt} {
		params, err := db.APIAttachmentToDBAttachment(a, "issue-1")
		if err != nil {
			t.Fatalf("convert attachment: %v", err)
		}
		params.SyncedAt = old
		if err := store.Queries().UpsertAttachment(ctx, params); err != nil {
			t.Fatalf("seed attachment: %v", err)
		}
	}

	// The fetch returns only the live row for each collection.
	mock := newMockAPIClient()
	mock.detailsByIssue["issue-1"] = &api.IssueDetails{
		Documents:   []api.Document{liveDoc},
		Attachments: []api.Attachment{liveAtt},
	}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	worker.syncDetails(ctx, []issueRef{{ID: "issue-1", Identifier: "TST-1"}})

	docs, err := store.Queries().ListIssueDocuments(ctx, sql.NullString{String: "issue-1", Valid: true})
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != "doc-live" {
		got := []string{}
		for _, d := range docs {
			got = append(got, d.ID)
		}
		t.Errorf("documents = %v, want [doc-live] (stale pruned, live retained)", got)
	}

	atts, err := store.Queries().ListIssueAttachments(ctx, "issue-1")
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(atts) != 1 || atts[0].ID != "att-live" {
		got := []string{}
		for _, a := range atts {
			got = append(got, a.ID)
		}
		t.Errorf("attachments = %v, want [att-live] (stale pruned, live retained)", got)
	}
}

// TestSetRateLimitedAdaptiveBackoff verifies M-3: when the API client reports a non-zero
// future RateLimitResetAt(), setRateLimited() uses that time (+ 5s buffer) instead of the
// 15-min default. The pinned fake clock makes the arithmetic exact — no tolerance window.
func TestSetRateLimitedAdaptiveBackoff(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	clock := newFakeClock()
	mock := newMockAPIClient()
	serverResetAt := clock.now().Add(30 * time.Minute)
	mock.rateLimitResetAt = serverResetAt

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	clock.install(worker)

	worker.setRateLimited()

	if want := serverResetAt.Add(5 * time.Second); !worker.rateLimitExpiry.Equal(want) {
		t.Errorf("rateLimitExpiry = %v, want exactly %v (server reset + 5s buffer)", worker.rateLimitExpiry, want)
	}
}

// TestSetRateLimitedFallback verifies M-3: with no usable server reset —
// zero (never reported) or already past — setRateLimited() falls back to the
// 15-minute fixed backoff, exactly, on the pinned fake clock.
func TestSetRateLimitedFallback(t *testing.T) {
	t.Parallel()

	cases := map[string]func(now time.Time) time.Time{
		"zero reset": func(time.Time) time.Time { return time.Time{} },
		"past reset": func(now time.Time) time.Time { return now.Add(-time.Minute) },
	}
	for name, resetAt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t)
			defer store.Close()

			clock := newFakeClock()
			mock := newMockAPIClient()
			mock.rateLimitResetAt = resetAt(clock.now())

			worker := NewWorker(mock, store, Config{Interval: time.Hour})
			clock.install(worker)

			worker.setRateLimited()

			if want := clock.now().Add(15 * time.Minute); !worker.rateLimitExpiry.Equal(want) {
				t.Errorf("rateLimitExpiry = %v, want exactly %v (the 15-minute fallback)", worker.rateLimitExpiry, want)
			}
		})
	}
}

// TestIsRateLimitedFlipsWhenClockCrossesExpiry: the seam's most basic win —
// isRateLimited() is a pure now-vs-expiry comparison, so advancing the fake
// clock across rateLimitExpiry must flip it false with zero real waiting.
func TestIsRateLimitedFlipsWhenClockCrossesExpiry(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	clock := newFakeClock()
	mock := newMockAPIClient() // zero reset → the 15-minute fallback backoff
	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	clock.install(worker)

	worker.setRateLimited()
	if !worker.isRateLimited() {
		t.Fatal("isRateLimited() = false immediately after setRateLimited()")
	}

	clock.advance(15*time.Minute - time.Second)
	if !worker.isRateLimited() {
		t.Error("isRateLimited() = false one second before expiry, want true")
	}

	clock.advance(2 * time.Second)
	if worker.isRateLimited() {
		t.Error("isRateLimited() = true after the clock crossed expiry, want false")
	}
}

// =============================================================================
// Cold-Start Budget Probe Tests
// =============================================================================

// TestProbeSeedsBudgetBeforeFirstSync verifies the ordering guarantee of the
// cold-start probe: the cheap viewer query completes (seeding the client's
// rate budget from its response headers) BEFORE the worker issues any
// expensive work (workspace, teams, metadata, issues).
func TestProbeSeedsBudgetBeforeFirstSync(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	worker.Start(ctx)
	// Stop blocks on run()'s doneCh, and run() completes the probe and the
	// initial sync cycle before it can observe stopCh — so by the time Stop
	// returns, GetTeams has fired and the call order is settled (no poll).
	worker.Stop()

	if atomic.LoadInt32(&mock.getTeamsCalls) == 0 {
		t.Fatal("initial sync never called GetTeams")
	}

	order := mock.callOrder()
	if len(order) == 0 || order[0] != "GetViewer" {
		t.Fatalf("call order = %v, want GetViewer (the budget probe) strictly first", order)
	}
	if atomic.LoadInt32(&mock.getViewerCalls) != 1 {
		t.Errorf("GetViewer calls = %d, want exactly 1 probe", atomic.LoadInt32(&mock.getViewerCalls))
	}
}

// TestProbeRateLimitedDelaysSyncStart verifies the exhausted-account path:
// when the probe itself reports RATELIMITED, the worker marks itself
// rate-limited (honoring the budget's server-reported reset) and delays the
// entire sync start instead of bursting into the wall — and shutdown during
// that delay exits cleanly without firing a post-stop sync cycle.
func TestProbeRateLimitedDelaysSyncStart(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	clock := newFakeClock()
	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}
	mock.viewerErr = errors.New("GraphQL error: RATELIMITED: rate limit exceeded")
	// The budget's server-reported reset (seeded by the probe response's
	// headers in production) is an hour out.
	mock.rateLimitResetAt = clock.now().Add(time.Hour)

	worker := NewWorker(mock, store, Config{Interval: 10 * time.Millisecond})
	clock.install(worker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	worker.Start(ctx)

	// The probe parks on the injected delay timer — the handshake that
	// replaced the old 200ms "ample time to misbehave" sleep. Nothing ever
	// fires the fake timer, so any sync activity from here IS the bug. With
	// the clock pinned the requested delay is exact: reset − now + 5s buffer.
	if got, want := <-clock.timerSet, time.Hour+5*time.Second; got != want {
		t.Errorf("probe delay = %v, want exactly %v", got, want)
	}

	if got := atomic.LoadInt32(&mock.getTeamsCalls); got != 0 {
		t.Errorf("GetTeams calls during rate-limited probe delay = %d, want 0", got)
	}
	if !worker.isRateLimited() {
		t.Error("worker should report rate-limited after a RATELIMITED probe")
	}

	// Stop must interrupt the delay; no sync cycle may fire on the way out.
	worker.Stop()
	if order := mock.callOrder(); len(order) != 1 || order[0] != "GetViewer" {
		t.Errorf("call order after stop = %v, want just the probe [GetViewer]", order)
	}
}

// TestProbeFailureProceeds verifies that a non-rate-limit probe failure
// (network down, bad auth) does not block sync: those failures repeat in
// syncAllTeams and are handled there.
func TestProbeFailureProceeds(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}
	mock.viewerErr = errors.New("connection refused")

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	worker.Start(ctx)
	// Stop blocks until run() exits; a probe failure that (correctly)
	// proceeds will have completed the initial sync by then.
	worker.Stop()

	if atomic.LoadInt32(&mock.getTeamsCalls) == 0 {
		t.Fatal("sync never proceeded past a non-rate-limit probe failure")
	}
	if worker.isRateLimited() {
		t.Error("a non-rate-limit probe failure must not mark the worker rate-limited")
	}
}

// TestProbeBudgetRateLimitedWaitAndResume drives probeBudget's RATELIMITED
// path directly on the fake timer: the requested wait must be exactly the
// backoff-to-expiry duration, and firing the timer resumes sync (returns
// true) — zero real waiting.
func TestProbeBudgetRateLimitedWaitAndResume(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	clock := newFakeClock()
	mock := newMockAPIClient()
	mock.viewerErr = errors.New("GraphQL error: RATELIMITED: rate limit exceeded")
	mock.rateLimitResetAt = clock.now().Add(30 * time.Minute)

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	clock.install(worker)

	result := make(chan bool, 1)
	go func() { result <- worker.probeBudget(context.Background()) }()

	// The probe parked on the fake timer; the clock is pinned, so the
	// requested wait is exactly reset − now + 5s buffer.
	if got, want := <-clock.timerSet, 30*time.Minute+5*time.Second; got != want {
		t.Errorf("probe wait = %v, want exactly %v", got, want)
	}

	// Fire the timer: the backoff "elapsed", sync must proceed.
	clock.timerCh <- time.Time{}
	if !<-result {
		t.Error("probeBudget = false after the delay timer fired, want true (sync proceeds)")
	}
}

// TestProbeBudgetStopInterruptsWait: Stop (stopCh closing) must interrupt the
// probe's backoff wait and return false so run() exits without firing a
// post-stop sync cycle. The fake timer never fires — the only way out is the
// stop channel.
func TestProbeBudgetStopInterruptsWait(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	clock := newFakeClock()
	mock := newMockAPIClient()
	mock.viewerErr = errors.New("GraphQL error: RATELIMITED: rate limit exceeded")
	mock.rateLimitResetAt = clock.now().Add(30 * time.Minute)

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	clock.install(worker)

	result := make(chan bool, 1)
	go func() { result <- worker.probeBudget(context.Background()) }()

	<-clock.timerSet     // the probe is parked on the fake timer
	close(worker.stopCh) // what Stop() does (in-package; run() isn't live here)
	if <-result {
		t.Error("probeBudget = true, want false when Stop interrupts the backoff wait")
	}
}

// TestRunLoopTickFiresSyncCycle: the run loop's cadence rides the injected
// ticker — feeding one tick on the fake channel fires a full sync cycle, no
// real interval elapses.
func TestRunLoopTickFiresSyncCycle(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	clock := newFakeClock()
	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	clock.install(worker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	worker.Start(ctx)
	// The unbuffered send completes only once the loop is parked in its
	// select — i.e. after the initial sync — and hands it exactly one tick.
	clock.tickerCh <- time.Time{}
	// Stop blocks until run() exits, and the tick's sync cycle runs to
	// completion before the loop can re-enter the select and observe stopCh.
	worker.Stop()

	if d := clock.tickerInterval(); d != time.Hour {
		t.Errorf("run loop ticker constructed with %v, want the configured interval %v", d, time.Hour)
	}
	if calls := atomic.LoadInt32(&mock.getTeamsCalls); calls != 2 {
		t.Errorf("GetTeams calls = %d, want exactly 2 (initial sync + the injected tick)", calls)
	}
}

// =============================================================================
// Lean/Full Cycle Taxonomy Tests (#242)
// =============================================================================

// opsDuring runs fn and returns only the ops the mock recorded during it —
// the per-cycle window the lean/full assertions are made against.
func opsDuring(m *mockAPIClient, fn func()) []string {
	before := len(m.callOrder())
	fn()
	return m.callOrder()[before:]
}

// containsOp reports whether op appears in ops.
func containsOp(ops []string, op string) bool {
	for _, o := range ops {
		if o == op {
			return true
		}
	}
	return false
}

// assertCycleOps asserts one cycle's fetch classes: a full cycle issues both
// GetWorkspace and GetTeamMetadata (whose complete projects drain makes the
// probe redundant there), a lean cycle issues neither but runs the projects
// change-detection probe instead; every non-skipped cycle issues GetTeams
// (the cheap team enumeration the issues sync needs either way).
func assertCycleOps(t *testing.T, label string, ops []string, wantFull bool) {
	t.Helper()
	if !containsOp(ops, "GetTeams") {
		t.Errorf("%s: ops = %v, want GetTeams in every non-skipped cycle", label, ops)
	}
	for _, op := range []string{"GetWorkspace", "GetTeamMetadata"} {
		if got := containsOp(ops, op); got != wantFull {
			t.Errorf("%s: ops = %v, %s present = %v, want %v", label, ops, op, got, wantFull)
		}
	}
	if got := containsOp(ops, "GetTeamProjectsNewestPage"); got != !wantFull {
		t.Errorf("%s: ops = %v, GetTeamProjectsNewestPage present = %v, want %v (probe rides lean cycles only)",
			label, ops, got, !wantFull)
	}
}

// cycleTestWorker builds the standard lean/full-cycle fixture: one team with
// metadata, a fake clock, a 2-minute cycle interval and a 10-minute full-sync
// interval.
func cycleTestWorker(t *testing.T, store *db.Store) (*Worker, *mockAPIClient, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}
	mock.statesByTeam["team-1"] = []api.State{{ID: "state-1", Name: "Todo", Type: "unstarted"}}
	worker := NewWorker(mock, store, Config{Interval: 2 * time.Minute, FullSyncInterval: 10 * time.Minute})
	clock.install(worker)
	return worker, mock, clock
}

// TestScheduledCyclesLeanUntilFullIntervalElapses scripts the steady-state
// cadence: a cold-start full cycle, then lean cycles (no workspace or
// team-metadata fetches) every 2 minutes until 10 minutes have elapsed since
// the persisted full-cycle timestamp, at which point the cycle runs full
// again and re-arms the window.
func TestScheduledCyclesLeanUntilFullIntervalElapses(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	worker, mock, clock := cycleTestWorker(t, store)

	// Cycle 1: cold start — no persisted timestamp — runs full.
	ops := opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("cycle 1: %v", err)
		}
	})
	assertCycleOps(t, "cycle 1 (cold start)", ops, true)

	// The full cycle persisted its timestamp — at the fake clock's now, via
	// the clock seam, readable from the store.
	stamped, err := store.Queries().GetSyncSchedule(ctx, scheduleKeyFullCycle)
	if err != nil {
		t.Fatalf("GetSyncSchedule after full cycle: %v", err)
	}
	if !stamped.Equal(clock.now()) {
		t.Errorf("persisted full-cycle timestamp = %v, want %v (w.now() at stamp time)", stamped, clock.now())
	}

	// Cycles 2-5 (+2m…+8m): inside the window — lean, issues only.
	for i := 2; i <= 5; i++ {
		clock.advance(2 * time.Minute)
		ops := opsDuring(mock, func() {
			if err := worker.syncAllTeams(ctx); err != nil {
				t.Fatalf("cycle %d: %v", i, err)
			}
		})
		assertCycleOps(t, fmt.Sprintf("cycle %d (in-window)", i), ops, false)
	}

	// Cycle 6 (+10m): the full-sync interval has elapsed — full again.
	clock.advance(2 * time.Minute)
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("cycle 6: %v", err)
		}
	})
	assertCycleOps(t, "cycle 6 (interval elapsed)", ops, true)

	// Cycle 7 (+12m): the full cycle re-armed the window — lean again.
	clock.advance(2 * time.Minute)
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("cycle 7: %v", err)
		}
	})
	assertCycleOps(t, "cycle 7 (window re-armed)", ops, false)
}

// TestSyncNowAlwaysRunsFull: an explicit sync request runs full even when the
// persisted full-cycle timestamp is fresh (a scheduled cycle at the same
// instant would run lean).
func TestSyncNowAlwaysRunsFull(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	worker, mock, clock := cycleTestWorker(t, store)

	// Arm the window with a cold-start full cycle, then move barely inside it.
	if err := worker.syncAllTeams(ctx); err != nil {
		t.Fatalf("arming full cycle: %v", err)
	}
	clock.advance(2 * time.Minute)

	// A scheduled cycle here is lean…
	ops := opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("scheduled cycle: %v", err)
		}
	})
	assertCycleOps(t, "scheduled cycle mid-window", ops, false)

	// …but SyncNow at the very same instant is full, unconditionally.
	ops = opsDuring(mock, func() {
		if err := worker.SyncNow(ctx); err != nil {
			t.Fatalf("SyncNow: %v", err)
		}
	})
	assertCycleOps(t, "SyncNow mid-window", ops, true)
}

// TestRestartHonorsPersistedFullCycleTimestamp: the persistence requirement.
// A fresh Worker over a store carrying a fresh full-cycle timestamp starts
// lean (a restart mid-window must not force an extra full cycle), while a
// fresh Worker over a fresh store starts full (cold start).
func TestRestartHonorsPersistedFullCycleTimestamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("fresh store starts full", func(t *testing.T) {
		t.Parallel()
		store := openTestStore(t)
		defer store.Close()

		worker, mock, _ := cycleTestWorker(t, store)
		ops := opsDuring(mock, func() {
			if err := worker.syncAllTeams(ctx); err != nil {
				t.Fatalf("cold-start cycle: %v", err)
			}
		})
		assertCycleOps(t, "cold start", ops, true)
	})

	t.Run("fresh persisted timestamp starts lean", func(t *testing.T) {
		t.Parallel()
		store := openTestStore(t)
		defer store.Close()

		// "Previous process": a full cycle stamps the schedule, then exits.
		prev, _, prevClock := cycleTestWorker(t, store)
		if err := prev.syncAllTeams(ctx); err != nil {
			t.Fatalf("previous process full cycle: %v", err)
		}

		// "Restart" 2 minutes later: a brand-new Worker over the same store.
		worker, mock, clock := cycleTestWorker(t, store)
		clock.advance(prevClock.now().Sub(clock.now()) + 2*time.Minute)
		ops := opsDuring(mock, func() {
			if err := worker.syncAllTeams(ctx); err != nil {
				t.Fatalf("post-restart cycle: %v", err)
			}
		})
		assertCycleOps(t, "restart mid-window", ops, false)
	})
}

// TestBudgetSkippedCycleLeavesFullCycleDue: a budget-skipped cycle runs
// nothing, so it must not stamp the persisted timestamp — the full sync stays
// due and fires on the next unblocked cycle rather than silently stretching
// the metadata staleness bound.
func TestBudgetSkippedCycleLeavesFullCycleDue(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	worker, mock, _ := cycleTestWorker(t, store)
	budget := &mockBudgetReporter{count: 1300, pct: 87.0} // >80% — skip
	worker.SetBudgetReporter(budget)

	ops := opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("budget-skipped cycle: %v", err)
		}
	})
	if len(ops) != 0 {
		t.Errorf("budget-skipped cycle issued ops %v, want none", ops)
	}
	if _, err := store.Queries().GetSyncSchedule(ctx, scheduleKeyFullCycle); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetSyncSchedule after skipped cycle: err = %v, want sql.ErrNoRows (no stamp)", err)
	}

	// Budget recovers — the still-due full cycle fires.
	budget.count, budget.pct = 100, 10.0
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("unblocked cycle: %v", err)
		}
	})
	assertCycleOps(t, "first unblocked cycle", ops, true)
}

// =============================================================================
// Issue-ID Reconcile Sweep Tests (#245)
// =============================================================================

// issueReconcileFixture builds the sweep fixture on top of cycleTestWorker: a
// real SQLiteRepository (nil api client — the drain comes through the mock
// APIClient seam) wired as the worker's IssueIDReconciler, plus two seeded
// issues on team-1, "issue-keep" (present in the drained ID set) and
// "issue-gone" (absent — deleted in Linear), with a comment and a document on
// issue-gone to assert the detail-row cleanup.
func issueReconcileFixture(t *testing.T, store *db.Store) (*Worker, *mockAPIClient, *fakeClock) {
	t.Helper()
	ctx := context.Background()
	worker, mock, clock := cycleTestWorker(t, store)

	rep := repo.NewSQLiteRepository(store, nil)
	t.Cleanup(rep.Close)
	worker.SetIssueIDReconciler(rep)

	team := api.Team{ID: "team-1", Key: "TST", Name: "Test"}
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	now := clock.now()
	for _, id := range []string{"issue-keep", "issue-gone"} {
		issue := api.Issue{
			ID: id, Identifier: id, Title: id, Team: &team,
			State:     api.State{ID: "state-1", Name: "Todo", Type: "unstarted"},
			CreatedAt: now, UpdatedAt: now,
		}
		data, err := db.APIIssueToDBIssue(issue)
		if err != nil {
			t.Fatalf("convert %s: %v", id, err)
		}
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if err := store.Queries().UpsertComment(ctx, db.UpsertCommentParams{
		ID: "c-gone", IssueID: "issue-gone", Body: "bye",
		CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	if err := store.Queries().UpsertDocument(ctx, db.UpsertDocumentParams{
		ID: "d-gone", SlugID: "d-gone", Title: "Doc",
		IssueID:  sql.NullString{String: "issue-gone", Valid: true},
		SyncedAt: now, Data: []byte("{}"),
	}); err != nil {
		t.Fatalf("seed document: %v", err)
	}

	mock.issueIDsByTeam = map[string][]string{"team-1": {"issue-keep"}}
	return worker, mock, clock
}

// TestIssueIDReconcileSweepDeletesMissingIssues: the sweep's core promise. An
// issue present locally but absent from the drained authoritative ID set is
// deleted (with its detail rows) by the hourly sweep riding the sync cycle,
// without any read touching it; the schedule then holds the sweep off until
// the hour elapses.
func TestIssueIDReconcileSweepDeletesMissingIssues(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	q := store.Queries()

	worker, mock, clock := issueReconcileFixture(t, store)

	// Cycle 1: no persisted sweep timestamp — the sweep is due and runs.
	ops := opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("cycle 1: %v", err)
		}
	})
	if !containsOp(ops, "GetTeamIssueIDs") {
		t.Errorf("cycle 1 ops = %v, want GetTeamIssueIDs (sweep due on missing schedule row)", ops)
	}

	// issue-gone and its detail rows are deleted; issue-keep survives.
	if _, err := q.GetIssueByID(ctx, "issue-gone"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("issue-gone still present after sweep: err = %v, want sql.ErrNoRows", err)
	}
	if got, _ := q.ListIssueComments(ctx, "issue-gone"); len(got) != 0 {
		t.Errorf("issue-gone comments not cleaned up: %d remain", len(got))
	}
	if got, _ := q.ListIssueDocuments(ctx, sql.NullString{String: "issue-gone", Valid: true}); len(got) != 0 {
		t.Errorf("issue-gone documents not cleaned up: %d remain", len(got))
	}
	if _, err := q.GetIssueByID(ctx, "issue-keep"); err != nil {
		t.Errorf("issue-keep was deleted by the sweep: %v", err)
	}

	// The complete sweep stamped its schedule at w.now(), via the clock seam.
	stamped, err := q.GetSyncSchedule(ctx, scheduleKeyIssueIDReconcile)
	if err != nil {
		t.Fatalf("GetSyncSchedule after sweep: %v", err)
	}
	if !stamped.Equal(clock.now()) {
		t.Errorf("persisted sweep timestamp = %v, want %v", stamped, clock.now())
	}

	// Cycles inside the hour don't re-sweep…
	clock.advance(2 * time.Minute)
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("in-window cycle: %v", err)
		}
	})
	if containsOp(ops, "GetTeamIssueIDs") {
		t.Errorf("in-window cycle ops = %v, want no GetTeamIssueIDs (sweep not due)", ops)
	}

	// …and the first cycle past the hour does.
	clock.advance(issueReconcileInterval)
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("post-hour cycle: %v", err)
		}
	})
	if !containsOp(ops, "GetTeamIssueIDs") {
		t.Errorf("post-hour cycle ops = %v, want GetTeamIssueIDs (hourly bound elapsed)", ops)
	}
}

// TestIssueIDReconcileDrainFailureDeletesNothingAndStaysDue: the
// all-or-nothing contract at the worker seam. A failed drain — a plain error
// or an api.ErrBudget deferral — deletes nothing and withholds the schedule
// stamp, so the very next cycle retries the sweep.
func TestIssueIDReconcileDrainFailureDeletesNothingAndStaysDue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		drainErr error
	}{
		{"generic drain error", errors.New("boom")},
		{"budget deferral", fmt.Errorf("paginate: %w", api.ErrBudget)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t)
			defer store.Close()
			ctx := context.Background()
			q := store.Queries()

			worker, mock, clock := issueReconcileFixture(t, store)
			mock.issueIDsErr = tc.drainErr

			// Cycle 1: the sweep runs but its drain fails.
			ops := opsDuring(mock, func() {
				if err := worker.syncAllTeams(ctx); err != nil {
					t.Fatalf("failing cycle: %v", err)
				}
			})
			if !containsOp(ops, "GetTeamIssueIDs") {
				t.Fatalf("failing cycle ops = %v, want GetTeamIssueIDs attempt", ops)
			}

			// Nothing deleted, no stamp — the sweep stays due.
			if _, err := q.GetIssueByID(ctx, "issue-gone"); err != nil {
				t.Errorf("failed drain deleted issue-gone: %v", err)
			}
			if got, _ := q.ListIssueComments(ctx, "issue-gone"); len(got) != 1 {
				t.Errorf("failed drain touched detail rows: %d comments remain, want 1", len(got))
			}
			if _, err := q.GetSyncSchedule(ctx, scheduleKeyIssueIDReconcile); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("GetSyncSchedule after failed sweep: err = %v, want sql.ErrNoRows (no stamp)", err)
			}

			// The drain recovers — the very next cycle retries and deletes.
			mock.issueIDsErr = nil
			clock.advance(2 * time.Minute)
			ops = opsDuring(mock, func() {
				if err := worker.syncAllTeams(ctx); err != nil {
					t.Fatalf("retry cycle: %v", err)
				}
			})
			if !containsOp(ops, "GetTeamIssueIDs") {
				t.Fatalf("retry cycle ops = %v, want GetTeamIssueIDs (sweep still due)", ops)
			}
			if _, err := q.GetIssueByID(ctx, "issue-gone"); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("issue-gone still present after retry sweep: err = %v", err)
			}
			if stamped, err := q.GetSyncSchedule(ctx, scheduleKeyIssueIDReconcile); err != nil || !stamped.Equal(clock.now()) {
				t.Errorf("retry sweep stamp = %v (err %v), want %v", stamped, err, clock.now())
			}
		})
	}
}

// TestIssueIDReconcileScheduleHonoredAcrossRestart: persistence. A fresh
// Worker (and fresh repo) over the same store with a fresh sweep timestamp
// does not re-sweep; once the hour elapses, it does.
func TestIssueIDReconcileScheduleHonoredAcrossRestart(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// "Previous process": one cycle runs the sweep and stamps the schedule.
	prevWorker, prevMock, prevClock := issueReconcileFixture(t, store)
	ops := opsDuring(prevMock, func() {
		if err := prevWorker.syncAllTeams(ctx); err != nil {
			t.Fatalf("previous process cycle: %v", err)
		}
	})
	if !containsOp(ops, "GetTeamIssueIDs") {
		t.Fatalf("previous process ops = %v, want GetTeamIssueIDs", ops)
	}

	// "Restart" 2 minutes later: a brand-new Worker and repo over the same
	// store. The persisted stamp holds the sweep off.
	worker, mock, clock := cycleTestWorker(t, store)
	rep := repo.NewSQLiteRepository(store, nil)
	t.Cleanup(rep.Close)
	worker.SetIssueIDReconciler(rep)
	mock.issueIDsByTeam = map[string][]string{"team-1": {"issue-keep"}}

	clock.advance(prevClock.now().Sub(clock.now()) + 2*time.Minute)
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("post-restart cycle: %v", err)
		}
	})
	if containsOp(ops, "GetTeamIssueIDs") {
		t.Errorf("post-restart cycle ops = %v, want no GetTeamIssueIDs (persisted stamp honored)", ops)
	}

	// Past the hourly bound the restarted process sweeps again.
	clock.advance(issueReconcileInterval)
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("post-hour cycle: %v", err)
		}
	})
	if !containsOp(ops, "GetTeamIssueIDs") {
		t.Errorf("post-hour cycle ops = %v, want GetTeamIssueIDs", ops)
	}
}

// =============================================================================
// Budget Gate Tests
// =============================================================================

func TestSyncAllTeamsSkipsWhenBudgetExceeded(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}

	cfg := Config{Interval: 1 * time.Hour} // won't tick
	worker := NewWorker(mock, store, cfg)
	worker.SetBudgetReporter(&mockBudgetReporter{count: 1300, pct: 87.0}) // >80%

	err := worker.syncAllTeams(context.Background())
	if err != nil {
		t.Fatalf("syncAllTeams should succeed (skip), got: %v", err)
	}

	// GetTeams should NOT have been called since we skipped
	if atomic.LoadInt32(&mock.getTeamsCalls) != 0 {
		t.Errorf("expected 0 GetTeams calls when budget exceeded, got %d", mock.getTeamsCalls)
	}
}

func TestSyncAllTeamsProceedsWhenBudgetOK(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}

	cfg := Config{Interval: 1 * time.Hour}
	worker := NewWorker(mock, store, cfg)
	worker.SetBudgetReporter(&mockBudgetReporter{count: 500, pct: 33.0}) // <80%

	_ = worker.syncAllTeams(context.Background())

	if atomic.LoadInt32(&mock.getTeamsCalls) == 0 {
		t.Error("expected GetTeams to be called when budget is OK")
	}
}

// TestSyncDetailsDefersWhenBudgetHigh: the budget gate — above the defer
// threshold, syncDetails must not spend an API call; the whole batch lands in
// pending_detail_sync (deferred) and the outcome is gated so a draining loop
// stops.
func TestSyncDetailsDefersWhenBudgetHigh(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	cfg := Config{Interval: 1 * time.Hour}
	worker := NewWorker(mock, store, cfg)
	worker.SetBudgetReporter(&mockBudgetReporter{count: 1100, pct: 73.0}) // >70%

	issues := []issueRef{
		{ID: "issue-1", Identifier: "TST-1"},
		{ID: "issue-2", Identifier: "TST-2"},
	}

	outcome := worker.syncDetails(context.Background(), issues)

	if !outcome.gated {
		t.Error("budget gate should report gated")
	}
	if len(outcome.deferred) != 2 || len(outcome.synced) != 0 {
		t.Errorf("outcome = %d deferred / %d synced, want 2 / 0", len(outcome.deferred), len(outcome.synced))
	}
	if calls := atomic.LoadInt32(&mock.detailsCalls); calls != 0 {
		t.Errorf("expected 0 GetIssueDetailsBatch calls when budget exceeded, got %d", calls)
	}

	// Should have been queued to pending_detail_sync, not API-called
	pending, err := store.Queries().ListPendingDetailSync(context.Background())
	if err != nil {
		t.Fatalf("ListPendingDetailSync failed: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending detail syncs, got %d", len(pending))
	}
}

func TestSyncDetailsSyncsWhenBudgetOK(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	cfg := Config{Interval: 1 * time.Hour}
	worker := NewWorker(mock, store, cfg)
	worker.SetBudgetReporter(&mockBudgetReporter{count: 300, pct: 20.0}) // <70%

	issues := []issueRef{
		{ID: "issue-1", Identifier: "TST-1"},
	}

	outcome := worker.syncDetails(context.Background(), issues)

	if outcome.gated {
		t.Error("a clean sync should not gate")
	}
	if len(outcome.synced) != 1 || len(outcome.deferred) != 0 {
		t.Errorf("outcome = %d synced / %d deferred, want 1 / 0", len(outcome.synced), len(outcome.deferred))
	}

	// Should NOT be in pending queue (was synced directly)
	pending, err := store.Queries().ListPendingDetailSync(context.Background())
	if err != nil {
		t.Fatalf("ListPendingDetailSync failed: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected 0 pending detail syncs (direct sync), got %d", len(pending))
	}
}

// seedIssueRow inserts a bare issues row so the detail_synced_at stamp (an
// UPDATE on issues) has a row to land on — in real flow the entity sync
// upserts the issue before its details ever sync.
func seedIssueRow(t *testing.T, store *db.Store, issueID, identifier string) {
	t.Helper()
	data := &db.IssueData{
		ID:         issueID,
		Identifier: identifier,
		Title:      identifier,
		TeamID:     "team-1",
		CreatedAt:  db.Now(),
		UpdatedAt:  db.Now(),
		Data:       []byte("{}"),
	}
	if err := store.Queries().UpsertIssue(context.Background(), data.ToUpsertParams()); err != nil {
		t.Fatalf("seed issue %s: %v", issueID, err)
	}
}

// detailSyncedAt reads back an issue's detail_synced_at stamp.
func detailSyncedAt(t *testing.T, store *db.Store, issueID string) sql.NullTime {
	t.Helper()
	fresh, err := store.Queries().GetIssueDetailFreshness(context.Background(), issueID)
	if err != nil {
		t.Fatalf("GetIssueDetailFreshness %s: %v", issueID, err)
	}
	return fresh.DetailSyncedAt
}

// TestSyncDetailsCleanIssueStampedAndDequeued: the happy half of the ledger —
// a cleanly persisted issue gets its detail_synced_at stamp (the one per-issue
// detail-freshness fact), is removed from pending_detail_sync, and is reported
// in outcome.synced. The issue here has ZERO comments/docs/attachments — under
// the old per-row touches an all-empty issue could never be stamped fresh (an
// UPDATE cannot stamp rows that do not exist), the root of the refetch loop.
func TestSyncDetailsCleanIssueStampedAndDequeued(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Pre-enqueue the issue so the dequeue is observable.
	if err := store.Queries().UpsertPendingDetailSync(ctx, db.UpsertPendingDetailSyncParams{
		IssueID: "issue-1", Identifier: "TST-1", QueuedAt: db.Now(),
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	seedIssueRow(t, store, "issue-1", "TST-1")
	if got := detailSyncedAt(t, store, "issue-1"); got.Valid {
		t.Fatalf("detail_synced_at = %v before any detail sync, want NULL", got.Time)
	}

	mock := newMockAPIClient() // default: empty (clean) details
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	outcome := worker.syncDetails(ctx, []issueRef{{ID: "issue-1", Identifier: "TST-1"}})

	if outcome.gated {
		t.Error("clean sync should not gate")
	}
	if len(outcome.synced) != 1 || outcome.synced[0].ID != "issue-1" {
		t.Errorf("outcome.synced = %v, want [issue-1]", outcome.synced)
	}
	if len(outcome.deferred) != 0 {
		t.Errorf("outcome.deferred = %v, want empty", outcome.deferred)
	}

	// Stamped: detail_synced_at set even though every family is empty.
	if got := detailSyncedAt(t, store, "issue-1"); !got.Valid {
		t.Error("detail_synced_at still NULL — clean issue was not stamped")
	}

	// Dequeued from pending_detail_sync.
	pending, err := store.Queries().ListPendingDetailSync(ctx)
	if err != nil {
		t.Fatalf("ListPendingDetailSync: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected clean issue dequeued, but %d still pending", len(pending))
	}
}

// TestSyncDetailsUncleanIssueDeferredNotStamped: the masked-staleness hazard —
// an issue whose persist was unclean (one collection's upsert failed) must NOT
// be stamped fresh (a stamp would hide its stale rows from the SWR path) and
// must NOT lose its retry (it stays in pending_detail_sync). The failure is
// injected as pure data: a relation with RelatedIssue == nil fails the
// relations collection's upsert closure.
func TestSyncDetailsUncleanIssueDeferredNotStamped(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	seedIssueRow(t, store, "issue-1", "TST-1")
	seedIssueRow(t, store, "issue-2", "TST-2")

	mock := newMockAPIClient()
	mock.detailsByIssue["issue-1"] = &api.IssueDetails{
		Relations: []api.IssueRelation{{ID: "rel-broken", Type: "blocks", RelatedIssue: nil, CreatedAt: time.Now(), UpdatedAt: time.Now()}},
	}
	// issue-2 rides in the same batch and is clean — the ledger is per issue.
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	outcome := worker.syncDetails(ctx, []issueRef{
		{ID: "issue-1", Identifier: "TST-1"},
		{ID: "issue-2", Identifier: "TST-2"},
	})

	if outcome.gated {
		t.Error("a per-issue unclean persist should not gate the batch")
	}
	if len(outcome.deferred) != 1 || outcome.deferred[0].ID != "issue-1" {
		t.Errorf("outcome.deferred = %v, want [issue-1]", outcome.deferred)
	}
	if len(outcome.synced) != 1 || outcome.synced[0].ID != "issue-2" {
		t.Errorf("outcome.synced = %v, want [issue-2]", outcome.synced)
	}

	// NOT stamped: the unclean issue's detail_synced_at stays NULL (stale)...
	if got := detailSyncedAt(t, store, "issue-1"); got.Valid {
		t.Errorf("detail_synced_at = %v — unclean issue was stamped, masking staleness", got.Time)
	}
	// ...while the clean batchmate IS stamped.
	if got := detailSyncedAt(t, store, "issue-2"); !got.Valid {
		t.Error("detail_synced_at still NULL for issue-2 — clean issue was not stamped")
	}

	// NOT dequeued: the issue keeps its retry.
	pending, err := store.Queries().ListPendingDetailSync(ctx)
	if err != nil {
		t.Fatalf("ListPendingDetailSync: %v", err)
	}
	if len(pending) != 1 || pending[0].IssueID != "issue-1" {
		ids := []string{}
		for _, p := range pending {
			ids = append(ids, p.IssueID)
		}
		t.Errorf("pending = %v, want [issue-1] (unclean issue re-enqueued, clean one not)", ids)
	}
}

// TestUnchangedSyncDoesNotMaskStaleHistory: the history-staleness mask fix.
// The deleted touch-on-unchanged block re-stamped an unchanged issue's history
// cache fresh every sync cycle — but history is never worker-fetched
// (SWR-only), so a history cached BEFORE the issue's last update was masked
// fresh forever and history.md served pre-update history indefinitely. A sync
// pass over an unchanged issue must leave the history cache's synced_at alone
// so the SWR comparison (updated_at > synced_at) can still see it is stale.
func TestUnchangedSyncDoesNotMaskStaleHistory(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	issueUpdated := db.Now().Add(-time.Hour)

	// The issue exists locally and is already up to date.
	data := &db.IssueData{
		ID: "issue-1", Identifier: "TST-1", Title: "Issue", TeamID: teamID,
		CreatedAt: issueUpdated.Add(-time.Hour), UpdatedAt: issueUpdated,
		Data: []byte("{}"),
	}
	if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if err := store.Queries().UpsertSyncMeta(ctx, db.UpsertSyncMetaParams{
		TeamID:             teamID,
		LastSyncedAt:       db.Now().Add(-10 * time.Minute),
		LastIssueUpdatedAt: db.ToNullTime(issueUpdated),
		IssueCount:         db.ToNullInt64(1),
	}); err != nil {
		t.Fatalf("seed sync meta: %v", err)
	}

	// History cached BEFORE the issue's last update — genuinely stale.
	staleHistorySyncedAt := issueUpdated.Add(-30 * time.Minute)
	if err := store.Queries().UpsertIssueHistoryCache(ctx, db.UpsertIssueHistoryCacheParams{
		IssueID: "issue-1", SyncedAt: staleHistorySyncedAt, Data: []byte("[]"),
	}); err != nil {
		t.Fatalf("seed history cache: %v", err)
	}

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: teamID, Key: "TST", Name: "Test"}}
	mock.issuesByTeam[teamID] = []api.Issue{
		{ID: "issue-1", Identifier: "TST-1", Title: "Issue", Team: &api.Team{ID: teamID}, UpdatedAt: issueUpdated},
	}

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	if err := worker.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}

	cache, err := store.Queries().GetIssueHistoryCache(ctx, "issue-1")
	if err != nil {
		t.Fatalf("GetIssueHistoryCache: %v", err)
	}
	if !cache.SyncedAt.Equal(staleHistorySyncedAt) {
		t.Errorf("history cache synced_at = %v, want untouched %v — an unchanged-issue sync pass masked stale history as fresh", cache.SyncedAt, staleHistorySyncedAt)
	}
}

// TestSyncDetailsFetchFailureDefersAll: a non-rate-limit batch fetch failure
// must defer every issue to pending_detail_sync (the old code logged and
// returned, silently dropping the worker-side retry for team-sync-sourced
// issues) and gate the outcome.
func TestSyncDetailsFetchFailureDefersAll(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	mock.simulateError = errors.New("boom: internal server error")
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	outcome := worker.syncDetails(ctx, []issueRef{
		{ID: "issue-1", Identifier: "TST-1"},
		{ID: "issue-2", Identifier: "TST-2"},
	})

	if !outcome.gated {
		t.Error("fetch failure should gate the outcome")
	}
	if len(outcome.deferred) != 2 || len(outcome.synced) != 0 {
		t.Errorf("outcome = %d deferred / %d synced, want 2 / 0", len(outcome.deferred), len(outcome.synced))
	}
	if worker.isRateLimited() {
		t.Error("a non-rate-limit failure must not set the rate-limit backoff")
	}

	pending, err := store.Queries().ListPendingDetailSync(ctx)
	if err != nil {
		t.Fatalf("ListPendingDetailSync: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected both issues deferred to pending after fetch failure, got %d", len(pending))
	}
}

// TestDrainStopsWhenGated: the drain loop must stop at the first gated
// outcome instead of burning an API call per remaining batch — with more than
// one batch pending and a persistently failing fetch, exactly one
// GetIssueDetailsBatch call is made.
func TestDrainStopsWhenGated(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Two batches' worth of pending issues.
	for i := 0; i < detailsBatchSize+1; i++ {
		if err := store.Queries().UpsertPendingDetailSync(ctx, db.UpsertPendingDetailSyncParams{
			IssueID:    fmt.Sprintf("issue-%02d", i),
			Identifier: fmt.Sprintf("TST-%02d", i),
			QueuedAt:   db.Now(),
		}); err != nil {
			t.Fatalf("seed pending: %v", err)
		}
	}

	mock := newMockAPIClient()
	mock.simulateError = errors.New("boom: internal server error") // non-rate-limit → gate 4 every time
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	worker.drainPendingDetailSync(ctx)

	if calls := atomic.LoadInt32(&mock.detailsCalls); calls != 1 {
		t.Errorf("GetIssueDetailsBatch called %d times, want 1 — drain must stop on a gated outcome", calls)
	}

	// Nothing was lost: every issue is still pending.
	pending, err := store.Queries().ListPendingDetailSync(ctx)
	if err != nil {
		t.Fatalf("ListPendingDetailSync: %v", err)
	}
	if len(pending) != detailsBatchSize+1 {
		t.Errorf("pending = %d, want %d (gated batches keep their retry)", len(pending), detailsBatchSize+1)
	}
}

func TestBudgetExceedsWithNilReporter(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	cfg := Config{Interval: 1 * time.Hour}
	worker := NewWorker(mock, store, cfg)
	// No budget reporter set

	// Should return false (safe default)
	if worker.budgetExceeds(50.0) {
		t.Error("budgetExceeds should return false with nil reporter")
	}
}

// TestDetailsSyncPersistsAndPrunesRelations: relations fetched with an
// issue's details are persisted (closing the gap where only the FUSE create
// handler wrote them, so UI-created relations never appeared as .rel files),
// phantoms owned by the issue are pruned on a clean short page, and inverse
// rows — owned by the OTHER issue — are upserted from this end but never
// pruned by this issue's sync (they're outside its completeness set).
func TestDetailsSyncPersistsAndPrunesRelations(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	live := api.IssueRelation{ID: "rel-live", Type: "blocks", RelatedIssue: &api.ParentIssue{ID: "issue-2", Identifier: "TST-2"}, CreatedAt: now, UpdatedAt: now}
	inverse := api.IssueRelation{ID: "rel-inverse", Type: "related", Issue: &api.ParentIssue{ID: "issue-3", Identifier: "TST-3"}, CreatedAt: now, UpdatedAt: now}

	// Seed a phantom owned by issue-1 (its relation was deleted in Linear's
	// UI) and a stale row owned by a different issue — the prune must eat
	// only the former.
	phantom := db.IssueRelationUpsertParams(api.IssueRelation{ID: "rel-phantom", Type: "blocks", CreatedAt: now, UpdatedAt: now}, "issue-1", "issue-9")
	phantom.SyncedAt = now.Add(-time.Minute)
	if err := store.Queries().UpsertIssueRelation(ctx, phantom); err != nil {
		t.Fatalf("seed phantom: %v", err)
	}
	other := db.IssueRelationUpsertParams(api.IssueRelation{ID: "rel-other", Type: "blocks", CreatedAt: now, UpdatedAt: now}, "issue-4", "issue-1")
	other.SyncedAt = now.Add(-time.Minute)
	if err := store.Queries().UpsertIssueRelation(ctx, other); err != nil {
		t.Fatalf("seed other-owned row: %v", err)
	}

	mock := newMockAPIClient()
	mock.detailsByIssue["issue-1"] = &api.IssueDetails{
		Relations:        []api.IssueRelation{live},
		InverseRelations: []api.IssueRelation{inverse},
	}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	worker.syncDetails(ctx, []issueRef{{ID: "issue-1", Identifier: "TST-1"}})

	// Outgoing: the live relation persisted, the phantom pruned.
	rels, err := store.Queries().ListIssueRelations(ctx, "issue-1")
	if err != nil {
		t.Fatalf("list relations: %v", err)
	}
	if len(rels) != 1 || rels[0].ID != "rel-live" {
		ids := []string{}
		for _, r := range rels {
			ids = append(ids, r.ID)
		}
		t.Errorf("relations of issue-1 = %v, want [rel-live] (phantom pruned)", ids)
	}

	// Inverse: stored from its owner's perspective (issue_id = the other side).
	invRels, err := store.Queries().ListIssueRelations(ctx, "issue-3")
	if err != nil || len(invRels) != 1 {
		t.Fatalf("inverse relation not persisted: err=%v n=%d", err, len(invRels))
	}
	if invRels[0].ID != "rel-inverse" || invRels[0].RelatedIssueID != "issue-1" {
		t.Errorf("inverse row stored as %s (%s->%s), want rel-inverse issue-3->issue-1",
			invRels[0].ID, invRels[0].IssueID, invRels[0].RelatedIssueID)
	}

	// The stale row owned by issue-4 is outside issue-1's completeness set.
	otherRels, err := store.Queries().ListIssueRelations(ctx, "issue-4")
	if err != nil || len(otherRels) != 1 || otherRels[0].ID != "rel-other" {
		t.Errorf("issue-1's sync pruned a row owned by issue-4: err=%v rels=%v", err, otherRels)
	}
}

// TestDetailsSyncRelationUpsertFailureSuppressesPrune: the clean guard — a
// malformed relation (no relatedIssue) fails its upsert, marking the
// collection unclean, so the prune is suppressed and a stale row survives
// rather than being wrongly deleted against a partial write-set.
func TestDetailsSyncRelationUpsertFailureSuppressesPrune(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	stale := db.IssueRelationUpsertParams(api.IssueRelation{ID: "rel-stale", Type: "blocks", CreatedAt: now, UpdatedAt: now}, "issue-1", "issue-9")
	stale.SyncedAt = now.Add(-time.Minute)
	if err := store.Queries().UpsertIssueRelation(ctx, stale); err != nil {
		t.Fatalf("seed stale: %v", err)
	}

	mock := newMockAPIClient()
	mock.detailsByIssue["issue-1"] = &api.IssueDetails{
		Relations: []api.IssueRelation{{ID: "rel-broken", Type: "blocks", RelatedIssue: nil, CreatedAt: now, UpdatedAt: now}},
	}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	worker.syncDetails(ctx, []issueRef{{ID: "issue-1", Identifier: "TST-1"}})

	rels, err := store.Queries().ListIssueRelations(ctx, "issue-1")
	if err != nil || len(rels) != 1 || rels[0].ID != "rel-stale" {
		t.Errorf("unclean relation sync must suppress the prune, but rel-stale is gone: err=%v rels=%v", err, rels)
	}
}
