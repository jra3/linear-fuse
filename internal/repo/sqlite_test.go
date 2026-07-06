package repo

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
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
	if err := store.Queries().UpsertIssue(ctx, issueData1.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertIssue(ctx, issueData2.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert states
	state1Params, _ := db.APIStateToDBState(api.State{ID: "state-1", Name: "Todo", Type: "unstarted"}, "team-1")
	state2Params, _ := db.APIStateToDBState(api.State{ID: "state-2", Name: "Done", Type: "completed"}, "team-1")
	if err := store.Queries().UpsertState(ctx, state1Params); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertState(ctx, state2Params); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert issues with different states, priorities, and assignees
	issues := []api.Issue{
		{ID: "i1", Identifier: "TST-1", Title: "Issue 1", Team: &team, State: api.State{ID: "state-1", Type: "unstarted"}, Priority: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "i2", Identifier: "TST-2", Title: "Issue 2", Team: &team, State: api.State{ID: "state-1", Type: "unstarted"}, Priority: 2, Assignee: &api.User{ID: "user-1"}, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "i3", Identifier: "TST-3", Title: "Issue 3", Team: &team, State: api.State{ID: "state-2", Type: "completed"}, Priority: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	for _, issue := range issues {
		data, _ := db.APIIssueToDBIssue(issue)
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("setup: %v", err)
		}
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
		if err := store.Queries().UpsertState(ctx, params); err != nil {
			t.Fatalf("setup: %v", err)
		}
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
		label.Team = &api.Team{ID: "team-1"}
		params, _ := db.APILabelToDBLabel(label)
		if err := store.Queries().UpsertLabel(ctx, params); err != nil {
			t.Fatalf("setup: %v", err)
		}
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
		if err := store.Queries().UpsertUser(ctx, params); err != nil {
			t.Fatalf("setup: %v", err)
		}
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
		if err := store.Queries().UpsertCycle(ctx, params); err != nil {
			t.Fatalf("setup: %v", err)
		}
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
		if err := store.Queries().UpsertProject(ctx, params); err != nil {
			t.Fatalf("setup: %v", err)
		}
		// Link to team
		if err := store.Queries().UpsertProjectTeam(ctx, db.UpsertProjectTeamParams{
			ProjectID: project.ID,
			TeamID:    "team-1",
			SyncedAt:  time.Now(),
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, parentData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, childData1.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertIssue(ctx, childData2.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert label
	label := api.Label{ID: "label-1", Name: "Bug", Color: "#ff0000", Team: &api.Team{ID: "team-1"}}
	labelParams, _ := db.APILabelToDBLabel(label)
	if err := store.Queries().UpsertLabel(ctx, labelParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, issueData1.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertIssue(ctx, issueData2.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert project
	project := api.Project{ID: "project-1", Name: "Project Alpha", Slug: "alpha", State: "started", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	projectParams, _ := db.APIProjectToDBProject(project)
	if err := store.Queries().UpsertProject(ctx, projectParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert cycle
	cycle := api.Cycle{ID: "cycle-1", Number: 1, Name: "Sprint 1", StartsAt: time.Now(), EndsAt: time.Now().Add(14 * 24 * time.Hour)}
	cycleParams, _ := db.APICycleToDBCycle(cycle, "team-1")
	if err := store.Queries().UpsertCycle(ctx, cycleParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert users
	user1 := api.User{ID: "user-1", Name: "Me", Email: "me@example.com", Active: true}
	user2 := api.User{ID: "user-2", Name: "Other", Email: "other@example.com", Active: true}
	userParams1, _ := db.APIUserToDBUser(user1)
	userParams2, _ := db.APIUserToDBUser(user2)
	if err := store.Queries().UpsertUser(ctx, userParams1); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertUser(ctx, userParams2); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, myIssueData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertIssue(ctx, otherIssueData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert user and set as current
	user := api.User{ID: "user-1", Name: "Me", Email: "me@example.com", Active: true}
	userParams, _ := db.APIUserToDBUser(user)
	if err := store.Queries().UpsertUser(ctx, userParams); err != nil {
		t.Fatalf("setup: %v", err)
	}
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
	if err := store.Queries().UpsertIssue(ctx, activeData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertIssue(ctx, completedData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertIssue(ctx, canceledData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert user
	user := api.User{ID: "user-1", Name: "User", Email: "user@example.com", Active: true}
	userParams, _ := db.APIUserToDBUser(user)
	if err := store.Queries().UpsertUser(ctx, userParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert users
	user1 := api.User{ID: "user-1", Name: "Alice", Email: "alice@example.com", Active: true}
	user2 := api.User{ID: "user-2", Name: "Bob", Email: "bob@example.com", Active: true}
	userParams1, _ := db.APIUserToDBUser(user1)
	userParams2, _ := db.APIUserToDBUser(user2)
	if err := store.Queries().UpsertUser(ctx, userParams1); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertUser(ctx, userParams2); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Add team memberships
	if err := store.Queries().UpsertTeamMember(ctx, db.UpsertTeamMemberParams{
		TeamID:   "team-1",
		UserID:   "user-1",
		SyncedAt: time.Now(),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertTeamMember(ctx, db.UpsertTeamMemberParams{
		TeamID:   "team-1",
		UserID:   "user-2",
		SyncedAt: time.Now(),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertProject(ctx, projectParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert milestones
	targetDate := "2024-03-31"
	milestone1 := api.ProjectMilestone{ID: "ms-1", Name: "Alpha", Description: "Alpha release", TargetDate: &targetDate, SortOrder: 1.0}
	milestone2 := api.ProjectMilestone{ID: "ms-2", Name: "Beta", Description: "Beta release", TargetDate: &targetDate, SortOrder: 2.0}

	ms1Params, _ := db.APIProjectMilestoneToDBMilestone(milestone1, "project-1")
	ms2Params, _ := db.APIProjectMilestoneToDBMilestone(milestone2, "project-1")
	if err := store.Queries().UpsertProjectMilestone(ctx, ms1Params); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertProjectMilestone(ctx, ms2Params); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert comments
	user := api.User{ID: "user-1", Name: "Commenter", Email: "commenter@example.com"}
	comment1 := api.Comment{ID: "comment-1", Body: "First comment", CreatedAt: time.Now(), UpdatedAt: time.Now(), User: &user}
	comment2 := api.Comment{ID: "comment-2", Body: "Second comment", CreatedAt: time.Now(), UpdatedAt: time.Now(), User: &user}

	c1Params, _ := db.APICommentToDBComment(comment1, "issue-1")
	c2Params, _ := db.APICommentToDBComment(comment2, "issue-1")
	if err := store.Queries().UpsertComment(ctx, c1Params); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertComment(ctx, c2Params); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertDocument(ctx, docParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertProject(ctx, projectParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertDocument(ctx, docParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertInitiative(ctx, initParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertInitiative(ctx, initParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert project
	project := api.Project{ID: "project-1", Name: "Project", Slug: "project", State: "started", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	projectParams, _ := db.APIProjectToDBProject(project)
	if err := store.Queries().UpsertProject(ctx, projectParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Link project to initiative
	if err := store.Queries().UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
		InitiativeID: "init-1",
		ProjectID:    "project-1",
		SyncedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertProject(ctx, projectParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertProjectUpdate(ctx, updateParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertInitiative(ctx, initParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertInitiativeUpdate(ctx, updateParams); err != nil {
		t.Fatalf("setup: %v", err)
	}

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

	// Default threshold should be 5 minutes (2.5× the 2-minute sync interval)
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
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Insert users
	user1 := api.User{ID: "user-1", Name: "Me", Email: "me@example.com", Active: true}
	user2 := api.User{ID: "user-2", Name: "Other", Email: "other@example.com", Active: true}
	userParams1, _ := db.APIUserToDBUser(user1)
	userParams2, _ := db.APIUserToDBUser(user2)
	if err := store.Queries().UpsertUser(ctx, userParams1); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertUser(ctx, userParams2); err != nil {
		t.Fatalf("setup: %v", err)
	}

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
	if err := store.Queries().UpsertIssue(ctx, myData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertIssue(ctx, otherData.ToUpsertParams()); err != nil {
		t.Fatalf("setup: %v", err)
	}

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

func TestSQLiteRepository_MaybeRefreshIssueDetails_NoClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create repo without API client
	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()

	// Should be a no-op - no panic
	repo.MaybeRefreshIssueDetails("issue-1")
	repo.MaybeRefreshIssueDetails("issue-2")
}

// The four Get*Documents/Get*Updates read paths must be safe no-ops in fixture
// mode (nil client): maybeRefresh short-circuits, so the read returns whatever
// is cached without touching the API. Exercised through the real Get* methods
// now that the per-entity maybeRefresh* wrappers fold into one helper.
func TestSQLiteRepository_StaleReadPaths_NoClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()
	ctx := context.Background()

	if _, err := repo.GetProjectDocuments(ctx, "project-1"); err != nil {
		t.Errorf("GetProjectDocuments (nil client): %v", err)
	}
	if _, err := repo.GetInitiativeDocuments(ctx, "init-1"); err != nil {
		t.Errorf("GetInitiativeDocuments (nil client): %v", err)
	}
	if _, err := repo.GetProjectUpdates(ctx, "project-1"); err != nil {
		t.Errorf("GetProjectUpdates (nil client): %v", err)
	}
	if _, err := repo.GetInitiativeUpdates(ctx, "init-1"); err != nil {
		t.Errorf("GetInitiativeUpdates (nil client): %v", err)
	}
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

// TestSQLiteRepository_MaybeRefreshAttachments_NoClient removed — covered by
// TestSQLiteRepository_MaybeRefreshIssueDetails_NoClient (consolidated refresh)

// =============================================================================
// Background Refresh Timeout & Semaphore Tests
// =============================================================================

func TestTriggerBackgroundRefresh_Timeout(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	// Use a very short timeout for testing
	origTimeout := refreshTimeout
	// We can't modify the const, but we can test the behavior by using a
	// client that would block. Instead, test that the context has a deadline.

	// Create a repo with a non-nil client to enable refreshes
	client := api.NewClient("test-key")
	repoWithClient := NewSQLiteRepository(store, client)
	defer repoWithClient.Close()

	// Track whether refresh was called and whether context had deadline
	called := make(chan bool, 1)
	repoWithClient.triggerBackgroundRefresh("test-timeout", func(ctx context.Context) error {
		_, hasDeadline := ctx.Deadline()
		called <- hasDeadline
		return nil
	})

	select {
	case hasDeadline := <-called:
		if !hasDeadline {
			t.Error("expected refresh context to have a deadline (timeout)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh function was never called")
	}

	_ = repo
	_ = origTimeout
}

func TestTriggerBackgroundRefresh_SemaphoreDropsExcess(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	client := api.NewClient("test-key")
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	// Fill the semaphore with blocking refreshes
	blocker := make(chan struct{})
	for i := 0; i < maxConcurrentRefreshes; i++ {
		key := fmt.Sprintf("blocker-%d", i)
		repo.triggerBackgroundRefresh(key, func(ctx context.Context) error {
			<-blocker // block until released
			return nil
		})
	}

	// Give goroutines a moment to start
	time.Sleep(50 * time.Millisecond)

	// This refresh should be dropped (semaphore full)
	dropped := true
	repo.triggerBackgroundRefresh("should-be-dropped", func(ctx context.Context) error {
		dropped = false
		return nil
	})

	// Give a moment for it to potentially execute
	time.Sleep(50 * time.Millisecond)

	if !dropped {
		t.Error("expected excess refresh to be dropped when semaphore is full")
	}

	// Clean up: release all blockers
	close(blocker)
}

func TestTriggerBackgroundRefresh_DeduplicatesByKey(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	client := api.NewClient("test-key")
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	callCount := int32(0)
	blocker := make(chan struct{})

	// First call should start
	repo.triggerBackgroundRefresh("same-key", func(ctx context.Context) error {
		atomic.AddInt32(&callCount, 1)
		<-blocker
		return nil
	})

	time.Sleep(50 * time.Millisecond)

	// Second call with same key should be deduplicated
	repo.triggerBackgroundRefresh("same-key", func(ctx context.Context) error {
		atomic.AddInt32(&callCount, 1)
		return nil
	})

	time.Sleep(50 * time.Millisecond)
	close(blocker)
	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("expected 1 call (deduplicated), got %d", callCount)
	}
}

func TestSetCatchUpMode(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)

	if repo.stalenessThreshold != defaultStalenessThreshold {
		t.Fatalf("expected default staleness %v, got %v", defaultStalenessThreshold, repo.stalenessThreshold)
	}

	repo.SetCatchUpMode(true)
	if repo.stalenessThreshold != catchUpStaleness {
		t.Errorf("expected catch-up staleness %v, got %v", catchUpStaleness, repo.stalenessThreshold)
	}

	repo.SetCatchUpMode(false)
	if repo.stalenessThreshold != defaultStalenessThreshold {
		t.Errorf("expected default staleness %v after disabling catch-up, got %v", defaultStalenessThreshold, repo.stalenessThreshold)
	}
}

func TestIsEntityNotFound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unrelated error", fmt.Errorf("connection refused"), false},
		{"linear not-found wrapped", fmt.Errorf("GraphQL error: Entity not found: Issue"), true},
		{"raw not-found", fmt.Errorf("Entity not found"), true},
		{"rate limit", fmt.Errorf("rate limit wait cancelled: context canceled"), false},
	}
	for _, c := range cases {
		if got := isEntityNotFound(c.err); got != c.want {
			t.Errorf("%s: isEntityNotFound(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

func TestDeleteOrphanIssue(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()
	now := time.Now()
	const issueID = "orphan-1"
	const otherID = "keeper-1"

	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: now, UpdatedAt: now}
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("setup team: %v", err)
	}

	// Seed two issues — only the orphan should be deleted; the keeper stays.
	for _, id := range []string{issueID, otherID} {
		issue := api.Issue{
			ID: id, Identifier: id, Title: id, Team: &team,
			State: api.State{ID: "s1", Name: "Todo", Type: "unstarted"},
			CreatedAt: now, UpdatedAt: now,
		}
		data, _ := db.APIIssueToDBIssue(issue)
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}

	// Seed every sub-resource type for the orphan.
	q := store.Queries()
	mustExec := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	mustExec("comment", q.UpsertComment(ctx, db.UpsertCommentParams{
		ID: "c1", IssueID: issueID, Body: "hi", CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("document", q.UpsertDocument(ctx, db.UpsertDocumentParams{
		ID: "d1", SlugID: "d1", Title: "Doc",
		IssueID: sql.NullString{String: issueID, Valid: true},
		SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("attachment", q.UpsertAttachment(ctx, db.UpsertAttachmentParams{
		ID: "a1", IssueID: issueID, Title: "Att", Url: "https://e", Metadata: []byte("{}"), SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("embedded", q.UpsertEmbeddedFile(ctx, db.UpsertEmbeddedFileParams{
		ID: "e1", IssueID: issueID, Url: "https://e", Filename: "x", Source: "comment", CreatedAt: now, SyncedAt: now,
	}))
	mustExec("relation", q.UpsertIssueRelation(ctx, db.UpsertIssueRelationParams{
		ID: "r1", IssueID: issueID, RelatedIssueID: otherID, Type: "related", SyncedAt: now,
	}))
	mustExec("history", q.UpsertIssueHistoryCache(ctx, db.UpsertIssueHistoryCacheParams{
		IssueID: issueID, SyncedAt: now, Data: []byte("[]"),
	}))
	mustExec("pending", q.UpsertPendingDetailSync(ctx, db.UpsertPendingDetailSyncParams{
		IssueID: issueID, Identifier: issueID, QueuedAt: now,
	}))
	// Also seed a sibling resource on the keeper to confirm we don't clobber it.
	mustExec("keeper comment", q.UpsertComment(ctx, db.UpsertCommentParams{
		ID: "c-keep", IssueID: otherID, Body: "stay", CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}))

	repo.deleteOrphanIssue(ctx, issueID)

	// Orphan rows are gone.
	if got, _ := q.ListIssueComments(ctx, issueID); len(got) != 0 {
		t.Errorf("orphan comments not deleted: %d remain", len(got))
	}
	if got, _ := q.ListIssueDocuments(ctx, sql.NullString{String: issueID, Valid: true}); len(got) != 0 {
		t.Errorf("orphan documents not deleted: %d remain", len(got))
	}
	if got, _ := q.ListIssueAttachments(ctx, issueID); len(got) != 0 {
		t.Errorf("orphan attachments not deleted: %d remain", len(got))
	}
	if got, _ := q.ListIssueEmbeddedFiles(ctx, issueID); len(got) != 0 {
		t.Errorf("orphan embedded files not deleted: %d remain", len(got))
	}
	if got, _ := q.ListIssueRelations(ctx, issueID); len(got) != 0 {
		t.Errorf("orphan relations not deleted: %d remain", len(got))
	}
	if _, err := q.GetIssueHistoryCache(ctx, issueID); err != sql.ErrNoRows {
		t.Errorf("orphan history cache not deleted: err=%v", err)
	}
	if got, _ := q.ListPendingDetailSync(ctx); len(got) != 0 {
		t.Errorf("orphan pending sync not deleted: %d remain", len(got))
	}
	if _, err := q.GetIssueByID(ctx,issueID); err != sql.ErrNoRows {
		t.Errorf("orphan issue itself not deleted: err=%v", err)
	}

	// Keeper survives.
	if _, err := q.GetIssueByID(ctx,otherID); err != nil {
		t.Errorf("keeper issue was accidentally deleted: %v", err)
	}
	if got, _ := q.ListIssueComments(ctx, otherID); len(got) != 1 {
		t.Errorf("keeper comment was clobbered: %d remain", len(got))
	}
}

func TestDeleteOrphanProject(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()
	now := time.Now()
	const projectID = "proj-orphan"
	const otherID = "proj-keep"

	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: now, UpdatedAt: now}
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	q := store.Queries()
	mustExec := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	// Seed both projects.
	for _, id := range []string{projectID, otherID} {
		mustExec("project", q.UpsertProject(ctx, db.UpsertProjectParams{
			ID: id, SlugID: id, Name: id, SyncedAt: now, Data: []byte("{}"),
		}))
	}
	// Sub-resources on the orphan.
	mustExec("project-team", q.UpsertProjectTeam(ctx, db.UpsertProjectTeamParams{
		ProjectID: projectID, TeamID: "team-1", SyncedAt: now,
	}))
	mustExec("project-doc", q.UpsertDocument(ctx, db.UpsertDocumentParams{
		ID: "pd1", SlugID: "pd1", Title: "Doc",
		ProjectID: sql.NullString{String: projectID, Valid: true},
		SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("project-update", q.UpsertProjectUpdate(ctx, db.UpsertProjectUpdateParams{
		ID: "pu1", ProjectID: projectID, Body: "ok", CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("project-milestone", q.UpsertProjectMilestone(ctx, db.UpsertProjectMilestoneParams{
		ID: "pm1", ProjectID: projectID, Name: "MS", SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("initiative-project link", q.UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
		InitiativeID: "init-1", ProjectID: projectID, SyncedAt: now,
	}))
	// Sub-resources on the keeper.
	mustExec("keeper doc", q.UpsertDocument(ctx, db.UpsertDocumentParams{
		ID: "pd-keep", SlugID: "pd-keep", Title: "Keep",
		ProjectID: sql.NullString{String: otherID, Valid: true},
		SyncedAt: now, Data: []byte("{}"),
	}))

	repo.deleteOrphanProject(ctx, projectID)

	// Orphan gone.
	if _, err := q.GetProject(ctx, projectID); err != sql.ErrNoRows {
		t.Errorf("orphan project not deleted: err=%v", err)
	}
	if got, _ := q.ListProjectTeamIDs(ctx, projectID); len(got) != 0 {
		t.Errorf("orphan project-team links not deleted: %d remain", len(got))
	}
	if got, _ := q.ListProjectDocuments(ctx, sql.NullString{String: projectID, Valid: true}); len(got) != 0 {
		t.Errorf("orphan project docs not deleted: %d remain", len(got))
	}
	if got, _ := q.ListProjectUpdates(ctx, projectID); len(got) != 0 {
		t.Errorf("orphan project updates not deleted: %d remain", len(got))
	}
	if got, _ := q.ListProjectMilestones(ctx, projectID); len(got) != 0 {
		t.Errorf("orphan milestones not deleted: %d remain", len(got))
	}
	if got, _ := q.ListProjectInitiativeIDs(ctx, projectID); len(got) != 0 {
		t.Errorf("orphan initiative-project links not deleted: %d remain", len(got))
	}
	// Keeper survives.
	if _, err := q.GetProject(ctx, otherID); err != nil {
		t.Errorf("keeper project was deleted: %v", err)
	}
	if got, _ := q.ListProjectDocuments(ctx, sql.NullString{String: otherID, Valid: true}); len(got) != 1 {
		t.Errorf("keeper doc clobbered: %d remain", len(got))
	}
}

func TestDeleteOrphanInitiative(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()
	now := time.Now()
	const initID = "init-orphan"
	const otherID = "init-keep"

	q := store.Queries()
	mustExec := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	for _, id := range []string{initID, otherID} {
		mustExec("initiative", q.UpsertInitiative(ctx, db.UpsertInitiativeParams{
			ID: id, SlugID: id, Name: id, SyncedAt: now, Data: []byte("{}"),
		}))
	}
	mustExec("init-doc", q.UpsertDocument(ctx, db.UpsertDocumentParams{
		ID: "id1", SlugID: "id1", Title: "Doc",
		InitiativeID: sql.NullString{String: initID, Valid: true},
		SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("init-update", q.UpsertInitiativeUpdate(ctx, db.UpsertInitiativeUpdateParams{
		ID: "iu1", InitiativeID: initID, Body: "ok", CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("init-project link", q.UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
		InitiativeID: initID, ProjectID: "some-proj", SyncedAt: now,
	}))
	// Keeper sub-resource.
	mustExec("keeper update", q.UpsertInitiativeUpdate(ctx, db.UpsertInitiativeUpdateParams{
		ID: "iu-keep", InitiativeID: otherID, Body: "keep", CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}))

	repo.deleteOrphanInitiative(ctx, initID)

	if _, err := q.GetInitiative(ctx, initID); err != sql.ErrNoRows {
		t.Errorf("orphan initiative not deleted: err=%v", err)
	}
	if got, _ := q.ListInitiativeDocuments(ctx, sql.NullString{String: initID, Valid: true}); len(got) != 0 {
		t.Errorf("orphan init docs not deleted: %d remain", len(got))
	}
	if got, _ := q.ListInitiativeUpdates(ctx, initID); len(got) != 0 {
		t.Errorf("orphan init updates not deleted: %d remain", len(got))
	}
	if got, _ := q.ListInitiativeProjectIDs(ctx, initID); len(got) != 0 {
		t.Errorf("orphan init-project links not deleted: %d remain", len(got))
	}
	if _, err := q.GetInitiative(ctx, otherID); err != nil {
		t.Errorf("keeper initiative was deleted: %v", err)
	}
	if got, _ := q.ListInitiativeUpdates(ctx, otherID); len(got) != 1 {
		t.Errorf("keeper init update clobbered: %d remain", len(got))
	}
}

func TestMaybeScheduleReconcile_ColdStart(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	client := api.NewClient("test-key") // non-nil so the trigger isn't skipped
	// Point at an unreachable address so the goroutine's API calls fail
	// fast with a connection error rather than hitting Linear's production
	// API with an invalid key.
	client.SetAPIURL("http://127.0.0.1:1/")
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	// Cold start: lastReconcileAt is zero, so the first call should schedule.
	repo.maybeScheduleReconcile()

	// Wait for the scheduled goroutine to finish. reconcilePending is
	// atomic.Bool — safe to poll without the mutex. The stub runReconcile
	// clears it via defer when done.
	for i := 0; i < 100; i++ {
		if !repo.reconcilePending.Load() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if repo.reconcilePending.Load() {
		t.Fatal("scheduled reconcile goroutine did not finish in time")
	}

	// Now safe to inspect lastReconcileAt under the mutex.
	repo.reconcileMu.Lock()
	lastAt := repo.lastReconcileAt
	repo.reconcileMu.Unlock()
	if lastAt.IsZero() {
		t.Fatal("trigger did not fire on cold start (lastReconcileAt still zero)")
	}
}

func TestMaybeScheduleReconcile_CooldownGate(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	client := api.NewClient("test-key")
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	// Simulate a recent reconcile.
	repo.reconcileMu.Lock()
	repo.lastReconcileAt = time.Now()
	repo.reconcileMu.Unlock()

	// Should not fire while within cooldown.
	repo.maybeScheduleReconcile()
	if repo.reconcilePending.Load() {
		t.Error("trigger fired despite cooldown")
	}
}

func TestMaybeScheduleReconcile_NilClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	repo.maybeScheduleReconcile() // must not panic
	if repo.reconcilePending.Load() {
		t.Error("trigger fired with nil client")
	}
}

func TestSetDiff(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		local, api    []string
		wantOrphanIDs []string
	}{
		{"all present", []string{"a", "b"}, []string{"a", "b", "c"}, nil},
		{"one missing", []string{"a", "b", "c"}, []string{"a", "c"}, []string{"b"}},
		{"all missing", []string{"a", "b"}, []string{}, []string{"a", "b"}},
		{"empty local", []string{}, []string{"a"}, nil},
	}
	for _, c := range cases {
		got := setDiff(c.local, c.api)
		// Order-independent compare.
		gotSet := make(map[string]bool, len(got))
		for _, id := range got {
			gotSet[id] = true
		}
		if len(gotSet) != len(c.wantOrphanIDs) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.wantOrphanIDs)
			continue
		}
		for _, want := range c.wantOrphanIDs {
			if !gotSet[want] {
				t.Errorf("%s: missing %q in %v", c.name, want, got)
			}
		}
	}
}

func TestReconcileIssuesForTeam_DeletesOrphans(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()
	now := time.Now()

	team := api.Team{ID: "team-1", Key: "TST", Name: "T", CreatedAt: now, UpdatedAt: now}
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	// Seed three local issues; "alive" stays on API, "gone" and "alsogone" do not.
	for _, id := range []string{"alive", "gone", "alsogone"} {
		issue := api.Issue{
			ID: id, Identifier: id, Title: id, Team: &team,
			State: api.State{ID: "s1", Name: "Todo", Type: "unstarted"},
			CreatedAt: now, UpdatedAt: now,
		}
		data, _ := db.APIIssueToDBIssue(issue)
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	// Authoritative list from "Linear": only "alive" exists.
	deleted := repo.reconcileIssuesForTeam(ctx, "team-1", []string{"alive"})
	if deleted != 2 {
		t.Errorf("got deleted=%d, want 2", deleted)
	}

	if _, err := store.Queries().GetIssueByID(ctx, "alive"); err != nil {
		t.Errorf("alive issue was deleted: %v", err)
	}
	if _, err := store.Queries().GetIssueByID(ctx, "gone"); err != sql.ErrNoRows {
		t.Errorf("gone issue still present: err=%v", err)
	}
	if _, err := store.Queries().GetIssueByID(ctx, "alsogone"); err != sql.ErrNoRows {
		t.Errorf("alsogone issue still present: err=%v", err)
	}
}

// TestReconcileAgainst_DeletesOrphans drives the shared diff-and-delete seam
// that reconcileIssuesForTeam, reconcileProjects, and reconcileInitiatives
// now route through — with closures, no live client or store. This is the
// coverage projects/initiatives never had (their fetch needed a real client).
func TestReconcileAgainst_DeletesOrphans(t *testing.T) {
	t.Parallel()
	repo := &SQLiteRepository{}
	ctx := context.Background()

	var deleted []string
	n := repo.reconcileAgainst(ctx, "test",
		[]string{"alive", "also-alive"}, // authoritative set
		func() ([]string, error) {
			return []string{"alive", "gone", "also-alive", "gone2"}, nil
		},
		func(_ context.Context, id string) { deleted = append(deleted, id) },
	)

	if n != 2 {
		t.Errorf("deleted count = %d, want 2", n)
	}
	want := map[string]bool{"gone": true, "gone2": true}
	for _, id := range deleted {
		if !want[id] {
			t.Errorf("deleted %q, which is in the authoritative set", id)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Errorf("orphans not deleted: %v", want)
	}
}

// TestStaleSince pins the staleness rule the four Get*Documents/Get*Updates
// read paths share via maybeRefresh — the parseTime/threshold comparison that
// has historically hidden timezone bugs.
func TestStaleSince(t *testing.T) {
	t.Parallel()
	const threshold = time.Hour
	cases := []struct {
		name     string
		syncedAt interface{}
		err      error
		want     bool
	}{
		{"query error is stale", nil, fmt.Errorf("boom"), true},
		{"never synced (nil) is stale", nil, nil, true},
		{"recently synced is fresh", time.Now(), nil, false},
		{"older than threshold is stale", time.Now().Add(-2 * threshold), nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := staleSince(c.syncedAt, c.err, threshold); got != c.want {
				t.Errorf("staleSince(%v, %v) = %v, want %v", c.syncedAt, c.err, got, c.want)
			}
		})
	}
}

// TestMaybeRefreshNilClientNoop: in fixture mode (nil client) maybeRefresh must
// short-circuit before even querying syncedAt — no refresh, no query.
func TestMaybeRefreshNilClientNoop(t *testing.T) {
	t.Parallel()
	repo := &SQLiteRepository{} // nil client
	queried := false
	repo.maybeRefresh("k",
		func() (interface{}, error) { queried = true; return nil, nil },
		func(_ context.Context) error { return nil },
	)
	if queried {
		t.Error("maybeRefresh queried syncedAt with a nil client; want a no-op")
	}
}

// TestReconcileAgainst_LocalQueryErrorDeletesNothing: a failed local read must
// delete nothing — an empty/partial local set must never read as "everything
// is an orphan".
func TestReconcileAgainst_LocalQueryErrorDeletesNothing(t *testing.T) {
	t.Parallel()
	repo := &SQLiteRepository{}
	ctx := context.Background()

	called := false
	n := repo.reconcileAgainst(ctx, "test",
		[]string{"alive"},
		func() ([]string, error) { return nil, fmt.Errorf("db down") },
		func(_ context.Context, _ string) { called = true },
	)
	if n != 0 || called {
		t.Errorf("deleted=%d called=%v, want 0 deletes on local-query error", n, called)
	}
}
