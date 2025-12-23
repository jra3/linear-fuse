package repo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

func setupTestDB(t *testing.T) (*db.Store, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}
	return store, func() { store.Close() }
}

func TestSQLiteRepository_Teams(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test teams
	team1 := api.Team{ID: "team-1", Key: "ENG", Name: "Engineering", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	team2 := api.Team{ID: "team-2", Key: "DSN", Name: "Design", CreatedAt: time.Now(), UpdatedAt: time.Now()}

	err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team1))
	if err != nil {
		t.Fatalf("Failed to insert team1: %v", err)
	}
	err = store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team2))
	if err != nil {
		t.Fatalf("Failed to insert team2: %v", err)
	}

	// Test GetTeams
	teams, err := repo.GetTeams(ctx)
	if err != nil {
		t.Fatalf("GetTeams failed: %v", err)
	}
	if len(teams) != 2 {
		t.Errorf("Expected 2 teams, got %d", len(teams))
	}

	// Test GetTeamByKey
	team, err := repo.GetTeamByKey(ctx, "ENG")
	if err != nil {
		t.Fatalf("GetTeamByKey failed: %v", err)
	}
	if team == nil {
		t.Fatal("Expected team, got nil")
	}
	if team.Name != "Engineering" {
		t.Errorf("Expected team name 'Engineering', got %q", team.Name)
	}

	// Test GetTeamByKey - not found
	team, err = repo.GetTeamByKey(ctx, "NOTFOUND")
	if err != nil {
		t.Fatalf("GetTeamByKey failed: %v", err)
	}
	if team != nil {
		t.Error("Expected nil for non-existent team")
	}
}

func TestSQLiteRepository_Issues(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test Team", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))
	if err != nil {
		t.Fatalf("Failed to insert team: %v", err)
	}

	// Insert test issues
	issue1 := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "Test Issue 1",
		Team:       &team,
		State:      api.State{ID: "state-1", Name: "Todo", Type: "unstarted"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	issue2 := api.Issue{
		ID:         "issue-2",
		Identifier: "TST-2",
		Title:      "Test Issue 2",
		Team:       &team,
		State:      api.State{ID: "state-2", Name: "Done", Type: "completed"},
		Assignee:   &api.User{ID: "user-1", Email: "test@example.com"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	issueData1, _ := db.APIIssueToDBIssue(issue1)
	issueData2, _ := db.APIIssueToDBIssue(issue2)
	store.Queries().UpsertIssue(ctx, issueData1.ToUpsertParams())
	store.Queries().UpsertIssue(ctx, issueData2.ToUpsertParams())

	// Test GetTeamIssues
	issues, err := repo.GetTeamIssues(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeamIssues failed: %v", err)
	}
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues, got %d", len(issues))
	}

	// Test GetIssueByIdentifier
	issue, err := repo.GetIssueByIdentifier(ctx, "TST-1")
	if err != nil {
		t.Fatalf("GetIssueByIdentifier failed: %v", err)
	}
	if issue == nil {
		t.Fatal("Expected issue, got nil")
	}
	if issue.Title != "Test Issue 1" {
		t.Errorf("Expected title 'Test Issue 1', got %q", issue.Title)
	}

	// Test GetIssueByID
	issue, err = repo.GetIssueByID(ctx, "issue-2")
	if err != nil {
		t.Fatalf("GetIssueByID failed: %v", err)
	}
	if issue == nil {
		t.Fatal("Expected issue, got nil")
	}
	if issue.Identifier != "TST-2" {
		t.Errorf("Expected identifier 'TST-2', got %q", issue.Identifier)
	}
}

func TestSQLiteRepository_FilteredIssues(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test data
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert states
	state1Params, _ := db.APIStateToDBState(api.State{ID: "state-1", Name: "Todo", Type: "unstarted"}, "team-1")
	state2Params, _ := db.APIStateToDBState(api.State{ID: "state-2", Name: "Done", Type: "completed"}, "team-1")
	store.Queries().UpsertState(ctx, state1Params)
	store.Queries().UpsertState(ctx, state2Params)

	// Insert issues with different states, priorities, and assignees
	issues := []api.Issue{
		{ID: "i1", Identifier: "TST-1", Title: "Issue 1", Team: &team, State: api.State{ID: "state-1", Type: "unstarted"}, Priority: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "i2", Identifier: "TST-2", Title: "Issue 2", Team: &team, State: api.State{ID: "state-1", Type: "unstarted"}, Priority: 2, Assignee: &api.User{ID: "user-1"}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "i3", Identifier: "TST-3", Title: "Issue 3", Team: &team, State: api.State{ID: "state-2", Type: "completed"}, Priority: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	for _, issue := range issues {
		data, _ := db.APIIssueToDBIssue(issue)
		store.Queries().UpsertIssue(ctx, data.ToUpsertParams())
	}

	// Test GetIssuesByState
	stateIssues, err := repo.GetIssuesByState(ctx, "team-1", "state-1")
	if err != nil {
		t.Fatalf("GetIssuesByState failed: %v", err)
	}
	if len(stateIssues) != 2 {
		t.Errorf("Expected 2 issues in state-1, got %d", len(stateIssues))
	}

	// Test GetIssuesByPriority
	priorityIssues, err := repo.GetIssuesByPriority(ctx, "team-1", 1)
	if err != nil {
		t.Fatalf("GetIssuesByPriority failed: %v", err)
	}
	if len(priorityIssues) != 2 {
		t.Errorf("Expected 2 issues with priority 1, got %d", len(priorityIssues))
	}

	// Test GetUnassignedIssues
	unassigned, err := repo.GetUnassignedIssues(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetUnassignedIssues failed: %v", err)
	}
	if len(unassigned) != 2 {
		t.Errorf("Expected 2 unassigned issues, got %d", len(unassigned))
	}

	// Test GetIssuesByAssignee
	assigned, err := repo.GetIssuesByAssignee(ctx, "team-1", "user-1")
	if err != nil {
		t.Fatalf("GetIssuesByAssignee failed: %v", err)
	}
	if len(assigned) != 1 {
		t.Errorf("Expected 1 assigned issue, got %d", len(assigned))
	}
}

func TestSQLiteRepository_States(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert states
	states := []api.State{
		{ID: "s1", Name: "Backlog", Type: "backlog"},
		{ID: "s2", Name: "Todo", Type: "unstarted"},
		{ID: "s3", Name: "In Progress", Type: "started"},
		{ID: "s4", Name: "Done", Type: "completed"},
	}
	for _, state := range states {
		params, _ := db.APIStateToDBState(state, "team-1")
		store.Queries().UpsertState(ctx, params)
	}

	// Test GetTeamStates
	result, err := repo.GetTeamStates(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeamStates failed: %v", err)
	}
	if len(result) != 4 {
		t.Errorf("Expected 4 states, got %d", len(result))
	}

	// Test GetStateByName
	state, err := repo.GetStateByName(ctx, "team-1", "In Progress")
	if err != nil {
		t.Fatalf("GetStateByName failed: %v", err)
	}
	if state == nil {
		t.Fatal("Expected state, got nil")
	}
	if state.Type != "started" {
		t.Errorf("Expected type 'started', got %q", state.Type)
	}
}

func TestSQLiteRepository_Labels(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert labels
	labels := []api.Label{
		{ID: "l1", Name: "Bug", Color: "#ff0000"},
		{ID: "l2", Name: "Feature", Color: "#00ff00"},
	}
	for _, label := range labels {
		params, _ := db.APILabelToDBLabel(label, "team-1")
		store.Queries().UpsertLabel(ctx, params)
	}

	// Test GetTeamLabels
	result, err := repo.GetTeamLabels(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeamLabels failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(result))
	}

	// Test GetLabelByName
	label, err := repo.GetLabelByName(ctx, "team-1", "Bug")
	if err != nil {
		t.Fatalf("GetLabelByName failed: %v", err)
	}
	if label == nil {
		t.Fatal("Expected label, got nil")
	}
	if label.Color != "#ff0000" {
		t.Errorf("Expected color '#ff0000', got %q", label.Color)
	}
}

func TestSQLiteRepository_Users(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert users
	users := []api.User{
		{ID: "u1", Name: "Alice", Email: "alice@example.com", Active: true},
		{ID: "u2", Name: "Bob", Email: "bob@example.com", Active: true},
	}
	for _, user := range users {
		params, _ := db.APIUserToDBUser(user)
		store.Queries().UpsertUser(ctx, params)
	}

	// Test GetUsers
	result, err := repo.GetUsers(ctx)
	if err != nil {
		t.Fatalf("GetUsers failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 users, got %d", len(result))
	}

	// Test GetUserByID
	user, err := repo.GetUserByID(ctx, "u1")
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if user == nil {
		t.Fatal("Expected user, got nil")
	}
	if user.Name != "Alice" {
		t.Errorf("Expected name 'Alice', got %q", user.Name)
	}

	// Test GetUserByEmail
	user, err = repo.GetUserByEmail(ctx, "bob@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail failed: %v", err)
	}
	if user == nil {
		t.Fatal("Expected user, got nil")
	}
	if user.ID != "u2" {
		t.Errorf("Expected ID 'u2', got %q", user.ID)
	}
}

func TestSQLiteRepository_Cycles(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert cycles
	cycles := []api.Cycle{
		{ID: "c1", Number: 1, Name: "Sprint 1", StartsAt: time.Now(), EndsAt: time.Now().Add(14 * 24 * time.Hour)},
		{ID: "c2", Number: 2, Name: "Sprint 2", StartsAt: time.Now().Add(14 * 24 * time.Hour), EndsAt: time.Now().Add(28 * 24 * time.Hour)},
	}
	for _, cycle := range cycles {
		params, _ := db.APICycleToDBCycle(cycle, "team-1")
		store.Queries().UpsertCycle(ctx, params)
	}

	// Test GetTeamCycles
	result, err := repo.GetTeamCycles(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeamCycles failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 cycles, got %d", len(result))
	}

	// Test GetCycleByName
	cycle, err := repo.GetCycleByName(ctx, "team-1", "Sprint 1")
	if err != nil {
		t.Fatalf("GetCycleByName failed: %v", err)
	}
	if cycle == nil {
		t.Fatal("Expected cycle, got nil")
	}
	if cycle.Number != 1 {
		t.Errorf("Expected number 1, got %d", cycle.Number)
	}
}

func TestSQLiteRepository_Projects(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert projects
	projects := []api.Project{
		{ID: "p1", Name: "Project Alpha", Slug: "alpha", State: "started", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "p2", Name: "Project Beta", Slug: "beta", State: "planned", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	for _, project := range projects {
		params, _ := db.APIProjectToDBProject(project)
		store.Queries().UpsertProject(ctx, params)
		// Link to team
		store.Queries().UpsertProjectTeam(ctx, db.UpsertProjectTeamParams{
			ProjectID: project.ID,
			TeamID:    "team-1",
			SyncedAt:  time.Now(),
		})
	}

	// Test GetTeamProjects
	result, err := repo.GetTeamProjects(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeamProjects failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(result))
	}

	// Test GetProjectBySlug
	project, err := repo.GetProjectBySlug(ctx, "alpha")
	if err != nil {
		t.Fatalf("GetProjectBySlug failed: %v", err)
	}
	if project == nil {
		t.Fatal("Expected project, got nil")
	}
	if project.Name != "Project Alpha" {
		t.Errorf("Expected name 'Project Alpha', got %q", project.Name)
	}

	// Test GetProjectByID
	project, err = repo.GetProjectByID(ctx, "p2")
	if err != nil {
		t.Fatalf("GetProjectByID failed: %v", err)
	}
	if project == nil {
		t.Fatal("Expected project, got nil")
	}
	if project.Slug != "beta" {
		t.Errorf("Expected slug 'beta', got %q", project.Slug)
	}
}

func TestSQLiteRepository_Search(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team and issues
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	issues := []api.Issue{
		{ID: "i1", Identifier: "TST-1", Title: "Fix login bug", Description: "Users cannot login", Team: &team, State: api.State{ID: "s1"}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "i2", Identifier: "TST-2", Title: "Add dashboard", Description: "Create new dashboard", Team: &team, State: api.State{ID: "s1"}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "i3", Identifier: "TST-3", Title: "Update login page", Description: "Redesign", Team: &team, State: api.State{ID: "s1"}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	for _, issue := range issues {
		data, _ := db.APIIssueToDBIssue(issue)
		store.Queries().UpsertIssue(ctx, data.ToUpsertParams())
	}

	// Test SearchIssues
	results, err := repo.SearchIssues(ctx, "login")
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 results for 'login', got %d", len(results))
	}

	// Test SearchTeamIssues
	results, err = repo.SearchTeamIssues(ctx, "team-1", "dashboard")
	if err != nil {
		t.Fatalf("SearchTeamIssues failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'dashboard', got %d", len(results))
	}
}

func TestSQLiteRepository_CurrentUser(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Initially nil
	user, err := repo.GetCurrentUser(ctx)
	if err != nil {
		t.Fatalf("GetCurrentUser failed: %v", err)
	}
	if user != nil {
		t.Error("Expected nil user initially")
	}

	// Set current user
	testUser := &api.User{ID: "u1", Name: "Test User", Email: "test@example.com"}
	repo.SetCurrentUser(testUser)

	// Now should return the user
	user, err = repo.GetCurrentUser(ctx)
	if err != nil {
		t.Fatalf("GetCurrentUser failed: %v", err)
	}
	if user == nil {
		t.Fatal("Expected user, got nil")
	}
	if user.ID != "u1" {
		t.Errorf("Expected user ID 'u1', got %q", user.ID)
	}
}
