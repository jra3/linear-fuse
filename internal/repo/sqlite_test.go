package repo

import (
	"context"
	"database/sql"
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

func TestSQLiteRepository_IssueChildren(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert parent issue
	parentIssue := api.Issue{
		ID:         "parent-1",
		Identifier: "TST-1",
		Title:      "Parent Issue",
		Team:       &team,
		State:      api.State{ID: "state-1", Name: "Todo", Type: "unstarted"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	parentData, _ := db.APIIssueToDBIssue(parentIssue)
	store.Queries().UpsertIssue(ctx, parentData.ToUpsertParams())

	// Insert child issues
	child1 := api.Issue{
		ID:         "child-1",
		Identifier: "TST-2",
		Title:      "Child Issue 1",
		Team:       &team,
		State:      api.State{ID: "state-1", Name: "Todo", Type: "unstarted"},
		Parent:     &api.ParentIssue{ID: "parent-1", Identifier: "TST-1"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	child2 := api.Issue{
		ID:         "child-2",
		Identifier: "TST-3",
		Title:      "Child Issue 2",
		Team:       &team,
		State:      api.State{ID: "state-1", Name: "Todo", Type: "unstarted"},
		Parent:     &api.ParentIssue{ID: "parent-1", Identifier: "TST-1"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	childData1, _ := db.APIIssueToDBIssue(child1)
	childData2, _ := db.APIIssueToDBIssue(child2)
	store.Queries().UpsertIssue(ctx, childData1.ToUpsertParams())
	store.Queries().UpsertIssue(ctx, childData2.ToUpsertParams())

	// Test GetIssueChildren
	children, err := repo.GetIssueChildren(ctx, "parent-1")
	if err != nil {
		t.Fatalf("GetIssueChildren failed: %v", err)
	}
	if len(children) != 2 {
		t.Errorf("Expected 2 children, got %d", len(children))
	}
}

func TestSQLiteRepository_IssuesByLabel(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert label
	label := api.Label{ID: "label-1", Name: "Bug", Color: "#ff0000"}
	labelParams, _ := db.APILabelToDBLabel(label, "team-1")
	store.Queries().UpsertLabel(ctx, labelParams)

	// Insert issues with labels (labels are stored in JSON data field)
	issue1 := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "Bug Issue",
		Team:       &team,
		State:      api.State{ID: "state-1"},
		Labels:     api.Labels{Nodes: []api.Label{label}},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	issue2 := api.Issue{
		ID:         "issue-2",
		Identifier: "TST-2",
		Title:      "No Label Issue",
		Team:       &team,
		State:      api.State{ID: "state-1"},
		Labels:     api.Labels{Nodes: []api.Label{}},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	issueData1, _ := db.APIIssueToDBIssue(issue1)
	issueData2, _ := db.APIIssueToDBIssue(issue2)
	store.Queries().UpsertIssue(ctx, issueData1.ToUpsertParams())
	store.Queries().UpsertIssue(ctx, issueData2.ToUpsertParams())

	// Test GetIssuesByLabel (labels are stored in issue JSON data)
	issues, err := repo.GetIssuesByLabel(ctx, "team-1", "label-1")
	if err != nil {
		t.Fatalf("GetIssuesByLabel failed: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("Expected 1 issue with label, got %d", len(issues))
	}
}

func TestSQLiteRepository_IssuesByProject(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert project
	project := api.Project{ID: "project-1", Name: "Project Alpha", Slug: "alpha", State: "started", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	projectParams, _ := db.APIProjectToDBProject(project)
	store.Queries().UpsertProject(ctx, projectParams)

	// Insert issues with project
	issue := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "Project Issue",
		Team:       &team,
		State:      api.State{ID: "state-1"},
		Project:    &project,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	issueData, _ := db.APIIssueToDBIssue(issue)
	store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams())

	// Test GetIssuesByProject
	issues, err := repo.GetIssuesByProject(ctx, "project-1")
	if err != nil {
		t.Fatalf("GetIssuesByProject failed: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("Expected 1 issue in project, got %d", len(issues))
	}
}

func TestSQLiteRepository_IssuesByCycle(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert cycle
	cycle := api.Cycle{ID: "cycle-1", Number: 1, Name: "Sprint 1", StartsAt: time.Now(), EndsAt: time.Now().Add(14 * 24 * time.Hour)}
	cycleParams, _ := db.APICycleToDBCycle(cycle, "team-1")
	store.Queries().UpsertCycle(ctx, cycleParams)

	// Insert issue with cycle
	issueCycle := api.IssueCycle{ID: "cycle-1", Number: 1, Name: "Sprint 1"}
	issue := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "Cycle Issue",
		Team:       &team,
		State:      api.State{ID: "state-1"},
		Cycle:      &issueCycle,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	issueData, _ := db.APIIssueToDBIssue(issue)
	store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams())

	// Test GetIssuesByCycle
	issues, err := repo.GetIssuesByCycle(ctx, "cycle-1")
	if err != nil {
		t.Fatalf("GetIssuesByCycle failed: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("Expected 1 issue in cycle, got %d", len(issues))
	}
}

func TestSQLiteRepository_MyIssues(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert users
	user1 := api.User{ID: "user-1", Name: "Me", Email: "me@example.com", Active: true}
	user2 := api.User{ID: "user-2", Name: "Other", Email: "other@example.com", Active: true}
	userParams1, _ := db.APIUserToDBUser(user1)
	userParams2, _ := db.APIUserToDBUser(user2)
	store.Queries().UpsertUser(ctx, userParams1)
	store.Queries().UpsertUser(ctx, userParams2)

	// Set current user
	repo.SetCurrentUser(&user1)

	// Insert issues
	myIssue := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "My Issue",
		Team:       &team,
		State:      api.State{ID: "state-1", Type: "started"},
		Assignee:   &user1,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	otherIssue := api.Issue{
		ID:         "issue-2",
		Identifier: "TST-2",
		Title:      "Other Issue",
		Team:       &team,
		State:      api.State{ID: "state-1", Type: "started"},
		Assignee:   &user2,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	myIssueData, _ := db.APIIssueToDBIssue(myIssue)
	otherIssueData, _ := db.APIIssueToDBIssue(otherIssue)
	store.Queries().UpsertIssue(ctx, myIssueData.ToUpsertParams())
	store.Queries().UpsertIssue(ctx, otherIssueData.ToUpsertParams())

	// Test GetMyIssues
	issues, err := repo.GetMyIssues(ctx)
	if err != nil {
		t.Fatalf("GetMyIssues failed: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("Expected 1 issue assigned to me, got %d", len(issues))
	}
	if len(issues) > 0 && issues[0].ID != "issue-1" {
		t.Errorf("Expected issue ID 'issue-1', got %q", issues[0].ID)
	}
}

func TestSQLiteRepository_MyActiveIssues(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert user and set as current
	user := api.User{ID: "user-1", Name: "Me", Email: "me@example.com", Active: true}
	userParams, _ := db.APIUserToDBUser(user)
	store.Queries().UpsertUser(ctx, userParams)
	repo.SetCurrentUser(&user)

	// Insert issues with different states
	activeIssue := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "Active Issue",
		Team:       &team,
		State:      api.State{ID: "state-1", Type: "started"},
		Assignee:   &user,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	completedIssue := api.Issue{
		ID:         "issue-2",
		Identifier: "TST-2",
		Title:      "Completed Issue",
		Team:       &team,
		State:      api.State{ID: "state-2", Type: "completed"},
		Assignee:   &user,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	canceledIssue := api.Issue{
		ID:         "issue-3",
		Identifier: "TST-3",
		Title:      "Canceled Issue",
		Team:       &team,
		State:      api.State{ID: "state-3", Type: "canceled"},
		Assignee:   &user,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	activeData, _ := db.APIIssueToDBIssue(activeIssue)
	completedData, _ := db.APIIssueToDBIssue(completedIssue)
	canceledData, _ := db.APIIssueToDBIssue(canceledIssue)
	store.Queries().UpsertIssue(ctx, activeData.ToUpsertParams())
	store.Queries().UpsertIssue(ctx, completedData.ToUpsertParams())
	store.Queries().UpsertIssue(ctx, canceledData.ToUpsertParams())

	// Test GetMyActiveIssues - should only return non-completed, non-canceled
	issues, err := repo.GetMyActiveIssues(ctx)
	if err != nil {
		t.Fatalf("GetMyActiveIssues failed: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("Expected 1 active issue, got %d", len(issues))
	}
}

func TestSQLiteRepository_UserIssues(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert user
	user := api.User{ID: "user-1", Name: "User", Email: "user@example.com", Active: true}
	userParams, _ := db.APIUserToDBUser(user)
	store.Queries().UpsertUser(ctx, userParams)

	// Insert issues
	issue := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "User Issue",
		Team:       &team,
		State:      api.State{ID: "state-1"},
		Assignee:   &user,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	issueData, _ := db.APIIssueToDBIssue(issue)
	store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams())

	// Test GetUserIssues
	issues, err := repo.GetUserIssues(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetUserIssues failed: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("Expected 1 issue for user, got %d", len(issues))
	}
}

func TestSQLiteRepository_TeamMembers(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert users
	user1 := api.User{ID: "user-1", Name: "Alice", Email: "alice@example.com", Active: true}
	user2 := api.User{ID: "user-2", Name: "Bob", Email: "bob@example.com", Active: true}
	userParams1, _ := db.APIUserToDBUser(user1)
	userParams2, _ := db.APIUserToDBUser(user2)
	store.Queries().UpsertUser(ctx, userParams1)
	store.Queries().UpsertUser(ctx, userParams2)

	// Add team memberships
	store.Queries().UpsertTeamMember(ctx, db.UpsertTeamMemberParams{
		TeamID:   "team-1",
		UserID:   "user-1",
		SyncedAt: time.Now(),
	})
	store.Queries().UpsertTeamMember(ctx, db.UpsertTeamMemberParams{
		TeamID:   "team-1",
		UserID:   "user-2",
		SyncedAt: time.Now(),
	})

	// Test GetTeamMembers
	members, err := repo.GetTeamMembers(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeamMembers failed: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("Expected 2 team members, got %d", len(members))
	}
}

func TestSQLiteRepository_Milestones(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert project
	project := api.Project{ID: "project-1", Name: "Project", Slug: "project", State: "started", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	projectParams, _ := db.APIProjectToDBProject(project)
	store.Queries().UpsertProject(ctx, projectParams)

	// Insert milestones
	targetDate := "2024-03-31"
	milestone1 := api.ProjectMilestone{ID: "ms-1", Name: "Alpha", Description: "Alpha release", TargetDate: &targetDate, SortOrder: 1.0}
	milestone2 := api.ProjectMilestone{ID: "ms-2", Name: "Beta", Description: "Beta release", TargetDate: &targetDate, SortOrder: 2.0}

	ms1Params, _ := db.APIProjectMilestoneToDBMilestone(milestone1, "project-1")
	ms2Params, _ := db.APIProjectMilestoneToDBMilestone(milestone2, "project-1")
	store.Queries().UpsertProjectMilestone(ctx, ms1Params)
	store.Queries().UpsertProjectMilestone(ctx, ms2Params)

	// Test GetProjectMilestones
	milestones, err := repo.GetProjectMilestones(ctx, "project-1")
	if err != nil {
		t.Fatalf("GetProjectMilestones failed: %v", err)
	}
	if len(milestones) != 2 {
		t.Errorf("Expected 2 milestones, got %d", len(milestones))
	}

	// Test GetMilestoneByName
	milestone, err := repo.GetMilestoneByName(ctx, "project-1", "Alpha")
	if err != nil {
		t.Fatalf("GetMilestoneByName failed: %v", err)
	}
	if milestone == nil {
		t.Fatal("Expected milestone, got nil")
	}
	if milestone.Name != "Alpha" {
		t.Errorf("Expected milestone name 'Alpha', got %q", milestone.Name)
	}

	// Test not found
	milestone, err = repo.GetMilestoneByName(ctx, "project-1", "NotFound")
	if err != nil {
		t.Fatalf("GetMilestoneByName failed: %v", err)
	}
	if milestone != nil {
		t.Error("Expected nil for non-existent milestone")
	}
}

func TestSQLiteRepository_Comments(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team and issue
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	issue := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "Test Issue",
		Team:       &team,
		State:      api.State{ID: "state-1"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	issueData, _ := db.APIIssueToDBIssue(issue)
	store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams())

	// Insert comments
	user := api.User{ID: "user-1", Name: "Commenter", Email: "commenter@example.com"}
	comment1 := api.Comment{ID: "comment-1", Body: "First comment", CreatedAt: time.Now(), UpdatedAt: time.Now(), User: &user}
	comment2 := api.Comment{ID: "comment-2", Body: "Second comment", CreatedAt: time.Now(), UpdatedAt: time.Now(), User: &user}

	c1Params, _ := db.APICommentToDBComment(comment1, "issue-1")
	c2Params, _ := db.APICommentToDBComment(comment2, "issue-1")
	store.Queries().UpsertComment(ctx, c1Params)
	store.Queries().UpsertComment(ctx, c2Params)

	// Test GetIssueComments
	comments, err := repo.GetIssueComments(ctx, "issue-1")
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("Expected 2 comments, got %d", len(comments))
	}

	// Test GetCommentByID
	comment, err := repo.GetCommentByID(ctx, "comment-1")
	if err != nil {
		t.Fatalf("GetCommentByID failed: %v", err)
	}
	if comment == nil {
		t.Fatal("Expected comment, got nil")
	}
	if comment.Body != "First comment" {
		t.Errorf("Expected body 'First comment', got %q", comment.Body)
	}

	// Test not found
	comment, err = repo.GetCommentByID(ctx, "not-found")
	if err != nil {
		t.Fatalf("GetCommentByID failed: %v", err)
	}
	if comment != nil {
		t.Error("Expected nil for non-existent comment")
	}
}

func TestSQLiteRepository_IssueDocuments(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team and issue
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	issue := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "Test Issue",
		Team:       &team,
		State:      api.State{ID: "state-1"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	issueData, _ := db.APIIssueToDBIssue(issue)
	store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams())

	// Insert documents
	user := api.User{ID: "user-1", Name: "Author", Email: "author@example.com"}
	doc := api.Document{
		ID:        "doc-1",
		Title:     "Test Doc",
		Content:   "Document content",
		SlugID:    "test-doc",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Creator:   &user,
		Issue:     &api.Issue{ID: "issue-1"},
	}
	docParams, _ := db.APIDocumentToDBDocument(doc)
	store.Queries().UpsertDocument(ctx, docParams)

	// Test GetIssueDocuments
	docs, err := repo.GetIssueDocuments(ctx, "issue-1")
	if err != nil {
		t.Fatalf("GetIssueDocuments failed: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("Expected 1 document, got %d", len(docs))
	}

	// Test GetDocumentBySlug
	doc2, err := repo.GetDocumentBySlug(ctx, "test-doc")
	if err != nil {
		t.Fatalf("GetDocumentBySlug failed: %v", err)
	}
	if doc2 == nil {
		t.Fatal("Expected document, got nil")
	}
	if doc2.Title != "Test Doc" {
		t.Errorf("Expected title 'Test Doc', got %q", doc2.Title)
	}
}

func TestSQLiteRepository_ProjectDocuments(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert project
	project := api.Project{ID: "project-1", Name: "Project", Slug: "project", State: "started", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	projectParams, _ := db.APIProjectToDBProject(project)
	store.Queries().UpsertProject(ctx, projectParams)

	// Insert document
	user := api.User{ID: "user-1", Name: "Author", Email: "author@example.com"}
	doc := api.Document{
		ID:        "doc-1",
		Title:     "Project Doc",
		Content:   "Project document content",
		SlugID:    "project-doc",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Creator:   &user,
		Project:   &api.Project{ID: "project-1"},
	}
	docParams, _ := db.APIDocumentToDBDocument(doc)
	store.Queries().UpsertDocument(ctx, docParams)

	// Test GetProjectDocuments
	docs, err := repo.GetProjectDocuments(ctx, "project-1")
	if err != nil {
		t.Fatalf("GetProjectDocuments failed: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("Expected 1 document, got %d", len(docs))
	}
}

func TestSQLiteRepository_Initiatives(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert initiatives
	initiative := api.Initiative{
		ID:          "init-1",
		Name:        "Test Initiative",
		Slug:        "test-initiative",
		Description: "A test initiative",
		Status:      "active",
		Color:       "#0000ff",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	initParams, _ := db.APIInitiativeToDBInitiative(initiative)
	store.Queries().UpsertInitiative(ctx, initParams)

	// Test GetInitiatives
	initiatives, err := repo.GetInitiatives(ctx)
	if err != nil {
		t.Fatalf("GetInitiatives failed: %v", err)
	}
	if len(initiatives) != 1 {
		t.Errorf("Expected 1 initiative, got %d", len(initiatives))
	}

	// Test GetInitiativeBySlug
	init, err := repo.GetInitiativeBySlug(ctx, "test-initiative")
	if err != nil {
		t.Fatalf("GetInitiativeBySlug failed: %v", err)
	}
	if init == nil {
		t.Fatal("Expected initiative, got nil")
	}
	if init.Name != "Test Initiative" {
		t.Errorf("Expected name 'Test Initiative', got %q", init.Name)
	}

	// Test not found
	init, err = repo.GetInitiativeBySlug(ctx, "not-found")
	if err != nil {
		t.Fatalf("GetInitiativeBySlug failed: %v", err)
	}
	if init != nil {
		t.Error("Expected nil for non-existent initiative")
	}
}

func TestSQLiteRepository_InitiativeProjects(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert initiative
	initiative := api.Initiative{
		ID:        "init-1",
		Name:      "Test Initiative",
		Slug:      "test-initiative",
		Status:    "active",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	initParams, _ := db.APIInitiativeToDBInitiative(initiative)
	store.Queries().UpsertInitiative(ctx, initParams)

	// Insert project
	project := api.Project{ID: "project-1", Name: "Project", Slug: "project", State: "started", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	projectParams, _ := db.APIProjectToDBProject(project)
	store.Queries().UpsertProject(ctx, projectParams)

	// Link project to initiative
	store.Queries().UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
		InitiativeID: "init-1",
		ProjectID:    "project-1",
		SyncedAt:     time.Now(),
	})

	// Test GetInitiativeProjects
	projects, err := repo.GetInitiativeProjects(ctx, "init-1")
	if err != nil {
		t.Fatalf("GetInitiativeProjects failed: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("Expected 1 project, got %d", len(projects))
	}
}

func TestSQLiteRepository_ProjectUpdates(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert project
	project := api.Project{ID: "project-1", Name: "Project", Slug: "project", State: "started", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	projectParams, _ := db.APIProjectToDBProject(project)
	store.Queries().UpsertProject(ctx, projectParams)

	// Insert project update
	user := api.User{ID: "user-1", Name: "User", Email: "user@example.com"}
	update := api.ProjectUpdate{
		ID:        "update-1",
		Body:      "Sprint completed",
		Health:    "onTrack",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		User:      &user,
	}
	updateParams, _ := db.APIProjectUpdateToDBUpdate(update, "project-1")
	store.Queries().UpsertProjectUpdate(ctx, updateParams)

	// Test GetProjectUpdates
	updates, err := repo.GetProjectUpdates(ctx, "project-1")
	if err != nil {
		t.Fatalf("GetProjectUpdates failed: %v", err)
	}
	if len(updates) != 1 {
		t.Errorf("Expected 1 update, got %d", len(updates))
	}
	if updates[0].Health != "onTrack" {
		t.Errorf("Expected health 'onTrack', got %q", updates[0].Health)
	}
}

func TestSQLiteRepository_InitiativeUpdates(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert initiative
	initiative := api.Initiative{
		ID:        "init-1",
		Name:      "Test Initiative",
		Slug:      "test-initiative",
		Status:    "active",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	initParams, _ := db.APIInitiativeToDBInitiative(initiative)
	store.Queries().UpsertInitiative(ctx, initParams)

	// Insert initiative update
	user := api.User{ID: "user-1", Name: "User", Email: "user@example.com"}
	update := api.InitiativeUpdate{
		ID:        "update-1",
		Body:      "Initiative on track",
		Health:    "onTrack",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		User:      &user,
	}
	updateParams, _ := db.APIInitiativeUpdateToDBUpdate(update, "init-1")
	store.Queries().UpsertInitiativeUpdate(ctx, updateParams)

	// Test GetInitiativeUpdates
	updates, err := repo.GetInitiativeUpdates(ctx, "init-1")
	if err != nil {
		t.Fatalf("GetInitiativeUpdates failed: %v", err)
	}
	if len(updates) != 1 {
		t.Errorf("Expected 1 update, got %d", len(updates))
	}
}

func TestSQLiteRepository_StoreAndClose(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)

	// Test Store returns the store
	s := repo.Store()
	if s != store {
		t.Error("Store() should return the underlying store")
	}

	// Test Close (doesn't return error)
	repo.Close()
}

func TestSQLiteRepository_SetStalenessThreshold(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()

	// Default threshold should be 5 minutes
	if repo.stalenessThreshold != 5*time.Minute {
		t.Errorf("Expected default threshold of 5m, got %v", repo.stalenessThreshold)
	}

	// Set custom threshold
	repo.SetStalenessThreshold(10 * time.Minute)
	if repo.stalenessThreshold != 10*time.Minute {
		t.Errorf("Expected threshold of 10m, got %v", repo.stalenessThreshold)
	}

	// Set to 0
	repo.SetStalenessThreshold(0)
	if repo.stalenessThreshold != 0 {
		t.Errorf("Expected threshold of 0, got %v", repo.stalenessThreshold)
	}
}

func TestSQLiteRepository_ParseTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input interface{}
		want  bool // whether result should be zero
	}{
		{"nil", nil, true},
		{"time.Time", time.Now(), false},
		{"string RFC3339", "2024-01-15T10:30:00Z", false},
		{"empty string", "", true},
		{"invalid string", "not a date", true},
		{"int (unsupported)", 12345, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseTime(tt.input)
			if tt.want && !result.IsZero() {
				t.Errorf("Expected zero time for %v, got %v", tt.input, result)
			}
			if !tt.want && result.IsZero() {
				t.Errorf("Expected non-zero time for %v, got zero", tt.input)
			}
		})
	}
}

func TestSQLiteRepository_GetIssueByID_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	issue, err := repo.GetIssueByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetIssueByID should not error on not found: %v", err)
	}
	if issue != nil {
		t.Error("Expected nil for non-existent issue")
	}
}

func TestSQLiteRepository_GetIssueByIdentifier_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	issue, err := repo.GetIssueByIdentifier(ctx, "NOTFOUND-999")
	if err != nil {
		t.Fatalf("GetIssueByIdentifier should not error on not found: %v", err)
	}
	if issue != nil {
		t.Error("Expected nil for non-existent issue")
	}
}

func TestSQLiteRepository_GetProjectBySlug_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	project, err := repo.GetProjectBySlug(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetProjectBySlug should not error on not found: %v", err)
	}
	if project != nil {
		t.Error("Expected nil for non-existent project")
	}
}

func TestSQLiteRepository_GetProjectByID_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	project, err := repo.GetProjectByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetProjectByID should not error on not found: %v", err)
	}
	if project != nil {
		t.Error("Expected nil for non-existent project")
	}
}

func TestSQLiteRepository_GetDocumentBySlug_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	doc, err := repo.GetDocumentBySlug(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetDocumentBySlug should not error on not found: %v", err)
	}
	if doc != nil {
		t.Error("Expected nil for non-existent document")
	}
}

func TestSQLiteRepository_GetLabelByName_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	label, err := repo.GetLabelByName(ctx, "team-1", "NonexistentLabel")
	if err != nil {
		t.Fatalf("GetLabelByName should not error on not found: %v", err)
	}
	if label != nil {
		t.Error("Expected nil for non-existent label")
	}
}

func TestSQLiteRepository_GetUserByID_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	user, err := repo.GetUserByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetUserByID should not error on not found: %v", err)
	}
	if user != nil {
		t.Error("Expected nil for non-existent user")
	}
}

func TestSQLiteRepository_GetUserByEmail_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	user, err := repo.GetUserByEmail(ctx, "nonexistent@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail should not error on not found: %v", err)
	}
	if user != nil {
		t.Error("Expected nil for non-existent user")
	}
}

func TestSQLiteRepository_GetCycleByName_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	cycle, err := repo.GetCycleByName(ctx, "team-1", "NonexistentCycle")
	if err != nil {
		t.Fatalf("GetCycleByName should not error on not found: %v", err)
	}
	if cycle != nil {
		t.Error("Expected nil for non-existent cycle")
	}
}

func TestSQLiteRepository_GetStateByName_NotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test not found
	state, err := repo.GetStateByName(ctx, "team-1", "NonexistentState")
	if err != nil {
		t.Fatalf("GetStateByName should not error on not found: %v", err)
	}
	if state != nil {
		t.Error("Expected nil for non-existent state")
	}
}

func TestSQLiteRepository_GetIssuesByLabel_LabelNotFound(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Test with non-existent label - should return empty slice, not error
	issues, err := repo.GetIssuesByLabel(ctx, "team-1", "nonexistent-label")
	if err != nil {
		t.Fatalf("GetIssuesByLabel should not error for non-existent label: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("Expected 0 issues for non-existent label, got %d", len(issues))
	}
}

func TestSQLiteRepository_MyCreatedIssues(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test team
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team))

	// Insert users
	user1 := api.User{ID: "user-1", Name: "Me", Email: "me@example.com", Active: true}
	user2 := api.User{ID: "user-2", Name: "Other", Email: "other@example.com", Active: true}
	userParams1, _ := db.APIUserToDBUser(user1)
	userParams2, _ := db.APIUserToDBUser(user2)
	store.Queries().UpsertUser(ctx, userParams1)
	store.Queries().UpsertUser(ctx, userParams2)

	// Set current user
	repo.SetCurrentUser(&user1)

	// Insert issues - one created by me, one by other
	myCreatedIssue := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "My Created Issue",
		Team:       &team,
		State:      api.State{ID: "state-1", Type: "unstarted"},
		Creator:    &user1,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	otherCreatedIssue := api.Issue{
		ID:         "issue-2",
		Identifier: "TST-2",
		Title:      "Other Created Issue",
		Team:       &team,
		State:      api.State{ID: "state-1", Type: "unstarted"},
		Creator:    &user2,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	myData, _ := db.APIIssueToDBIssue(myCreatedIssue)
	otherData, _ := db.APIIssueToDBIssue(otherCreatedIssue)
	store.Queries().UpsertIssue(ctx, myData.ToUpsertParams())
	store.Queries().UpsertIssue(ctx, otherData.ToUpsertParams())

	// Test GetMyCreatedIssues
	issues, err := repo.GetMyCreatedIssues(ctx)
	if err != nil {
		t.Fatalf("GetMyCreatedIssues failed: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("Expected 1 issue created by me, got %d", len(issues))
	}
}

func TestSQLiteRepository_MyCreatedIssues_NoCurrentUser(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Don't set current user - should return empty slice
	issues, err := repo.GetMyCreatedIssues(ctx)
	if err != nil {
		t.Fatalf("GetMyCreatedIssues failed: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("Expected 0 issues when no current user, got %d", len(issues))
	}
}

func TestSQLiteRepository_MyIssues_NoCurrentUser(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Don't set current user - should return empty slice
	issues, err := repo.GetMyIssues(ctx)
	if err != nil {
		t.Fatalf("GetMyIssues failed: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("Expected 0 issues when no current user, got %d", len(issues))
	}
}

func TestSQLiteRepository_MyActiveIssues_NoCurrentUser(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Don't set current user - should return empty slice
	issues, err := repo.GetMyActiveIssues(ctx)
	if err != nil {
		t.Fatalf("GetMyActiveIssues failed: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("Expected 0 issues when no current user, got %d", len(issues))
	}
}

func TestSQLiteRepository_TriggerBackgroundRefresh_NoClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create repo without API client
	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()

	// Should be a no-op with nil client
	called := false
	repo.triggerBackgroundRefresh("test-key", func(ctx context.Context) error {
		called = true
		return nil
	})

	// Give a moment for any goroutine to run
	time.Sleep(10 * time.Millisecond)

	if called {
		t.Error("triggerBackgroundRefresh should not call refreshFn when client is nil")
	}
}

func TestSQLiteRepository_MaybeRefreshComments_NoClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create repo without API client
	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()

	// Should be a no-op - no panic
	repo.maybeRefreshComments("issue-1", true)
	repo.maybeRefreshComments("issue-2", false)
}

func TestSQLiteRepository_MaybeRefreshIssueDocuments_NoClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create repo without API client
	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()

	// Should be a no-op - no panic
	repo.maybeRefreshIssueDocuments("issue-1", true)
	repo.maybeRefreshIssueDocuments("issue-2", false)
}

func TestSQLiteRepository_MaybeRefreshProjectDocuments_NoClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create repo without API client
	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()

	// Should be a no-op - no panic
	repo.maybeRefreshProjectDocuments("project-1", true)
	repo.maybeRefreshProjectDocuments("project-2", false)
}

func TestSQLiteRepository_MaybeRefreshProjectUpdates_NoClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create repo without API client
	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()

	// Should be a no-op - no panic
	repo.maybeRefreshProjectUpdates("project-1", true)
	repo.maybeRefreshProjectUpdates("project-2", false)
}

func TestSQLiteRepository_MaybeRefreshInitiativeUpdates_NoClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create repo without API client
	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()

	// Should be a no-op - no panic
	repo.maybeRefreshInitiativeUpdates("init-1", true)
	repo.maybeRefreshInitiativeUpdates("init-2", false)
}

// =============================================================================
// Attachment Tests
// =============================================================================

func TestSQLiteRepository_Attachments(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	// Insert test attachment
	attachment := api.Attachment{
		ID:         "attach-1",
		Title:      "GitHub PR #123",
		URL:        "https://github.com/org/repo/pull/123",
		SourceType: "github",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	issueID := "issue-123"
	params, err := db.APIAttachmentToDBAttachment(attachment, issueID)
	if err != nil {
		t.Fatalf("APIAttachmentToDBAttachment failed: %v", err)
	}
	err = store.Queries().UpsertAttachment(ctx, params)
	if err != nil {
		t.Fatalf("UpsertAttachment failed: %v", err)
	}

	// Test GetIssueAttachments
	attachments, err := repo.GetIssueAttachments(ctx, issueID)
	if err != nil {
		t.Fatalf("GetIssueAttachments failed: %v", err)
	}
	if len(attachments) != 1 {
		t.Errorf("Expected 1 attachment, got %d", len(attachments))
	}
	if len(attachments) > 0 {
		if attachments[0].Title != "GitHub PR #123" {
			t.Errorf("Expected title 'GitHub PR #123', got %q", attachments[0].Title)
		}
		if attachments[0].SourceType != "github" {
			t.Errorf("Expected sourceType 'github', got %q", attachments[0].SourceType)
		}
	}

	// Test GetIssueAttachments - no attachments
	attachments, err = repo.GetIssueAttachments(ctx, "nonexistent-issue")
	if err != nil {
		t.Fatalf("GetIssueAttachments failed: %v", err)
	}
	if len(attachments) != 0 {
		t.Errorf("Expected 0 attachments, got %d", len(attachments))
	}
}

func TestSQLiteRepository_EmbeddedFiles(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	issueID := "issue-456"

	// Insert test embedded files
	err := store.Queries().UpsertEmbeddedFile(ctx, db.UpsertEmbeddedFileParams{
		ID:       "file-1",
		IssueID:  issueID,
		Url:      "https://uploads.linear.app/workspace/file1/screenshot.png",
		Filename: "screenshot.png",
		MimeType: sql.NullString{String: "image/png", Valid: true},
		Source:   "description",
		CreatedAt: time.Now(),
		SyncedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertEmbeddedFile failed: %v", err)
	}

	err = store.Queries().UpsertEmbeddedFile(ctx, db.UpsertEmbeddedFileParams{
		ID:       "file-2",
		IssueID:  issueID,
		Url:      "https://uploads.linear.app/workspace/file2/design.pdf",
		Filename: "design.pdf",
		MimeType: sql.NullString{String: "application/pdf", Valid: true},
		Source:   "comment:abc123",
		CreatedAt: time.Now(),
		SyncedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertEmbeddedFile failed: %v", err)
	}

	// Test GetIssueEmbeddedFiles
	files, err := repo.GetIssueEmbeddedFiles(ctx, issueID)
	if err != nil {
		t.Fatalf("GetIssueEmbeddedFiles failed: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("Expected 2 embedded files, got %d", len(files))
	}

	// Verify file contents
	for _, f := range files {
		if f.Filename == "screenshot.png" {
			if f.MimeType != "image/png" {
				t.Errorf("Expected MIME type 'image/png', got %q", f.MimeType)
			}
		}
		if f.Filename == "design.pdf" {
			if f.Source != "comment:abc123" {
				t.Errorf("Expected source 'comment:abc123', got %q", f.Source)
			}
		}
	}

	// Test GetIssueEmbeddedFiles - no files
	files, err = repo.GetIssueEmbeddedFiles(ctx, "nonexistent-issue")
	if err != nil {
		t.Fatalf("GetIssueEmbeddedFiles failed: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("Expected 0 files, got %d", len(files))
	}
}

func TestSQLiteRepository_UpdateEmbeddedFileCache(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()

	issueID := "issue-789"
	fileID := "file-cache-test"

	// Insert test embedded file
	err := store.Queries().UpsertEmbeddedFile(ctx, db.UpsertEmbeddedFileParams{
		ID:       fileID,
		IssueID:  issueID,
		Url:      "https://uploads.linear.app/workspace/test/image.png",
		Filename: "image.png",
		MimeType: sql.NullString{String: "image/png", Valid: true},
		Source:   "description",
		CreatedAt: time.Now(),
		SyncedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertEmbeddedFile failed: %v", err)
	}

	// Update cache path
	cachePath := "/tmp/linearfs/cache/file-cache-test"
	fileSize := int64(12345)

	err = repo.UpdateEmbeddedFileCache(ctx, fileID, cachePath, fileSize)
	if err != nil {
		t.Fatalf("UpdateEmbeddedFileCache failed: %v", err)
	}

	// Verify the update
	files, err := repo.GetIssueEmbeddedFiles(ctx, issueID)
	if err != nil {
		t.Fatalf("GetIssueEmbeddedFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	if files[0].CachePath != cachePath {
		t.Errorf("Expected cache path %q, got %q", cachePath, files[0].CachePath)
	}
	if files[0].FileSize != fileSize {
		t.Errorf("Expected file size %d, got %d", fileSize, files[0].FileSize)
	}
}

func TestSQLiteRepository_MaybeRefreshAttachments_NoClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create repo without API client
	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()

	// Should be a no-op - no panic
	repo.maybeRefreshAttachments("issue-1", true)
	repo.maybeRefreshAttachments("issue-2", false)
}
