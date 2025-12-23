package repo

import (
	"context"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestMockRepository_Teams(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	// Add test teams
	repo.Teams = []api.Team{
		{ID: "team-1", Key: "ENG", Name: "Engineering"},
		{ID: "team-2", Key: "DSN", Name: "Design"},
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
		t.Errorf("Expected 'Engineering', got %q", team.Name)
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

func TestMockRepository_Issues(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	team := api.Team{ID: "team-1", Key: "TST", Name: "Test"}
	issue1 := api.Issue{
		ID:         "issue-1",
		Identifier: "TST-1",
		Title:      "Test Issue 1",
		Team:       &team,
		State:      api.State{ID: "s1", Type: "unstarted"},
	}
	issue2 := api.Issue{
		ID:         "issue-2",
		Identifier: "TST-2",
		Title:      "Test Issue 2",
		Team:       &team,
		State:      api.State{ID: "s2", Type: "completed"},
		Assignee:   &api.User{ID: "user-1"},
	}

	repo.AddIssue(issue1)
	repo.AddIssue(issue2)

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
		t.Errorf("Expected 'Test Issue 1', got %q", issue.Title)
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
		t.Errorf("Expected 'TST-2', got %q", issue.Identifier)
	}
}

func TestMockRepository_FilteredIssues(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	team := api.Team{ID: "team-1", Key: "TST"}
	user := api.User{ID: "user-1", Email: "test@example.com"}

	// Add issues with different attributes
	repo.AddIssue(api.Issue{ID: "i1", Identifier: "TST-1", Team: &team, State: api.State{ID: "s1", Type: "unstarted"}, Priority: 1})
	repo.AddIssue(api.Issue{ID: "i2", Identifier: "TST-2", Team: &team, State: api.State{ID: "s1", Type: "unstarted"}, Priority: 2, Assignee: &user})
	repo.AddIssue(api.Issue{ID: "i3", Identifier: "TST-3", Team: &team, State: api.State{ID: "s2", Type: "completed"}, Priority: 1})

	// Test GetIssuesByState
	issues, _ := repo.GetIssuesByState(ctx, "team-1", "s1")
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues in state s1, got %d", len(issues))
	}

	// Test GetIssuesByPriority
	issues, _ = repo.GetIssuesByPriority(ctx, "team-1", 1)
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues with priority 1, got %d", len(issues))
	}

	// Test GetUnassignedIssues
	issues, _ = repo.GetUnassignedIssues(ctx, "team-1")
	if len(issues) != 2 {
		t.Errorf("Expected 2 unassigned issues, got %d", len(issues))
	}

	// Test GetIssuesByAssignee
	issues, _ = repo.GetIssuesByAssignee(ctx, "team-1", "user-1")
	if len(issues) != 1 {
		t.Errorf("Expected 1 assigned issue, got %d", len(issues))
	}
}

func TestMockRepository_MyIssues(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	user := api.User{ID: "user-1", Email: "me@example.com"}
	repo.CurrentUser = &user

	team := api.Team{ID: "team-1"}

	// Add issues - 2 assigned to me, 1 completed
	repo.AddIssue(api.Issue{ID: "i1", Identifier: "TST-1", Team: &team, State: api.State{Type: "unstarted"}, Assignee: &user})
	repo.AddIssue(api.Issue{ID: "i2", Identifier: "TST-2", Team: &team, State: api.State{Type: "completed"}, Assignee: &user})
	repo.AddIssue(api.Issue{ID: "i3", Identifier: "TST-3", Team: &team, State: api.State{Type: "started"}, Assignee: &user})
	repo.AddIssue(api.Issue{ID: "i4", Identifier: "TST-4", Team: &team, State: api.State{Type: "unstarted"}}) // Not assigned

	// Test GetMyIssues
	issues, _ := repo.GetMyIssues(ctx)
	if len(issues) != 3 {
		t.Errorf("Expected 3 my issues, got %d", len(issues))
	}

	// Test GetMyActiveIssues (excludes completed)
	issues, _ = repo.GetMyActiveIssues(ctx)
	if len(issues) != 2 {
		t.Errorf("Expected 2 active issues, got %d", len(issues))
	}
}

func TestMockRepository_Search(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	team := api.Team{ID: "team-1"}

	repo.AddIssue(api.Issue{ID: "i1", Identifier: "TST-1", Title: "Fix login bug", Description: "Users cannot login", Team: &team})
	repo.AddIssue(api.Issue{ID: "i2", Identifier: "TST-2", Title: "Add dashboard", Description: "New feature", Team: &team})
	repo.AddIssue(api.Issue{ID: "i3", Identifier: "TST-3", Title: "Update login page", Description: "Redesign", Team: &team})

	// Test SearchIssues
	results, _ := repo.SearchIssues(ctx, "login")
	if len(results) != 2 {
		t.Errorf("Expected 2 results for 'login', got %d", len(results))
	}

	// Test SearchTeamIssues
	results, _ = repo.SearchTeamIssues(ctx, "team-1", "dashboard")
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'dashboard', got %d", len(results))
	}

	// Test case-insensitive search
	results, _ = repo.SearchIssues(ctx, "LOGIN")
	if len(results) != 2 {
		t.Errorf("Expected 2 results for 'LOGIN' (case-insensitive), got %d", len(results))
	}
}

func TestMockRepository_States(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	repo.States["team-1"] = []api.State{
		{ID: "s1", Name: "Todo", Type: "unstarted"},
		{ID: "s2", Name: "In Progress", Type: "started"},
		{ID: "s3", Name: "Done", Type: "completed"},
	}

	// Test GetTeamStates
	states, _ := repo.GetTeamStates(ctx, "team-1")
	if len(states) != 3 {
		t.Errorf("Expected 3 states, got %d", len(states))
	}

	// Test GetStateByName
	state, _ := repo.GetStateByName(ctx, "team-1", "In Progress")
	if state == nil {
		t.Fatal("Expected state, got nil")
	}
	if state.Type != "started" {
		t.Errorf("Expected type 'started', got %q", state.Type)
	}

	// Test GetStateByName - not found
	state, _ = repo.GetStateByName(ctx, "team-1", "NotFound")
	if state != nil {
		t.Error("Expected nil for non-existent state")
	}
}

func TestMockRepository_Labels(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	repo.Labels["team-1"] = []api.Label{
		{ID: "l1", Name: "Bug", Color: "#ff0000"},
		{ID: "l2", Name: "Feature", Color: "#00ff00"},
	}

	// Test GetTeamLabels
	labels, _ := repo.GetTeamLabels(ctx, "team-1")
	if len(labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(labels))
	}

	// Test GetLabelByName
	label, _ := repo.GetLabelByName(ctx, "team-1", "Bug")
	if label == nil {
		t.Fatal("Expected label, got nil")
	}
	if label.Color != "#ff0000" {
		t.Errorf("Expected color '#ff0000', got %q", label.Color)
	}
}

func TestMockRepository_Users(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	repo.Users = []api.User{
		{ID: "u1", Name: "Alice", Email: "alice@example.com"},
		{ID: "u2", Name: "Bob", Email: "bob@example.com"},
	}

	// Test GetUsers
	users, _ := repo.GetUsers(ctx)
	if len(users) != 2 {
		t.Errorf("Expected 2 users, got %d", len(users))
	}

	// Test GetUserByID
	user, _ := repo.GetUserByID(ctx, "u1")
	if user == nil || user.Name != "Alice" {
		t.Error("Expected Alice")
	}

	// Test GetUserByEmail
	user, _ = repo.GetUserByEmail(ctx, "bob@example.com")
	if user == nil || user.ID != "u2" {
		t.Error("Expected Bob")
	}
}

func TestMockRepository_Projects(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	repo.Projects["team-1"] = []api.Project{
		{ID: "p1", Name: "Project Alpha", Slug: "alpha"},
		{ID: "p2", Name: "Project Beta", Slug: "beta"},
	}

	// Test GetTeamProjects
	projects, _ := repo.GetTeamProjects(ctx, "team-1")
	if len(projects) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(projects))
	}

	// Test GetProjectBySlug
	project, _ := repo.GetProjectBySlug(ctx, "alpha")
	if project == nil || project.Name != "Project Alpha" {
		t.Error("Expected Project Alpha")
	}

	// Test GetProjectByID
	project, _ = repo.GetProjectByID(ctx, "p2")
	if project == nil || project.Slug != "beta" {
		t.Error("Expected beta project")
	}
}

func TestMockRepository_Cycles(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	repo.Cycles["team-1"] = []api.Cycle{
		{ID: "c1", Number: 1, Name: "Sprint 1"},
		{ID: "c2", Number: 2, Name: "Sprint 2"},
	}

	// Test GetTeamCycles
	cycles, _ := repo.GetTeamCycles(ctx, "team-1")
	if len(cycles) != 2 {
		t.Errorf("Expected 2 cycles, got %d", len(cycles))
	}

	// Test GetCycleByName
	cycle, _ := repo.GetCycleByName(ctx, "team-1", "Sprint 1")
	if cycle == nil || cycle.Number != 1 {
		t.Error("Expected Sprint 1")
	}
}

func TestMockRepository_Comments(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	repo.Comments["issue-1"] = []api.Comment{
		{ID: "c1", Body: "First comment", CreatedAt: time.Now()},
		{ID: "c2", Body: "Second comment", CreatedAt: time.Now()},
	}

	// Test GetIssueComments
	comments, _ := repo.GetIssueComments(ctx, "issue-1")
	if len(comments) != 2 {
		t.Errorf("Expected 2 comments, got %d", len(comments))
	}

	// Test GetCommentByID
	comment, _ := repo.GetCommentByID(ctx, "c1")
	if comment == nil || comment.Body != "First comment" {
		t.Error("Expected first comment")
	}
}

func TestMockRepository_Documents(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	repo.Documents["issue-1"] = []api.Document{
		{ID: "d1", Title: "Design Doc", SlugID: "design-doc"},
	}
	repo.Documents["project-1"] = []api.Document{
		{ID: "d2", Title: "README", SlugID: "readme"},
	}

	// Test GetIssueDocuments
	docs, _ := repo.GetIssueDocuments(ctx, "issue-1")
	if len(docs) != 1 {
		t.Errorf("Expected 1 document, got %d", len(docs))
	}

	// Test GetProjectDocuments
	docs, _ = repo.GetProjectDocuments(ctx, "project-1")
	if len(docs) != 1 {
		t.Errorf("Expected 1 document, got %d", len(docs))
	}

	// Test GetDocumentBySlug
	doc, _ := repo.GetDocumentBySlug(ctx, "design-doc")
	if doc == nil || doc.Title != "Design Doc" {
		t.Error("Expected Design Doc")
	}
}

func TestMockRepository_Initiatives(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	repo.Initiatives = []api.Initiative{
		{ID: "init-1", Name: "Q1 Goals", Slug: "q1-goals"},
		{ID: "init-2", Name: "Platform", Slug: "platform"},
	}
	repo.InitiativeProjects["init-1"] = []api.Project{
		{ID: "p1", Name: "Project A"},
	}

	// Test GetInitiatives
	initiatives, _ := repo.GetInitiatives(ctx)
	if len(initiatives) != 2 {
		t.Errorf("Expected 2 initiatives, got %d", len(initiatives))
	}

	// Test GetInitiativeBySlug
	init, _ := repo.GetInitiativeBySlug(ctx, "q1-goals")
	if init == nil || init.Name != "Q1 Goals" {
		t.Error("Expected Q1 Goals")
	}

	// Test GetInitiativeProjects
	projects, _ := repo.GetInitiativeProjects(ctx, "init-1")
	if len(projects) != 1 {
		t.Errorf("Expected 1 project, got %d", len(projects))
	}
}

func TestMockRepository_StatusUpdates(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	repo.ProjectUpdates["project-1"] = []api.ProjectUpdate{
		{ID: "pu1", Body: "On track", Health: "onTrack"},
	}
	repo.InitiativeUpdates["init-1"] = []api.InitiativeUpdate{
		{ID: "iu1", Body: "Making progress", Health: "onTrack"},
	}

	// Test GetProjectUpdates
	updates, _ := repo.GetProjectUpdates(ctx, "project-1")
	if len(updates) != 1 {
		t.Errorf("Expected 1 project update, got %d", len(updates))
	}

	// Test GetInitiativeUpdates
	initUpdates, _ := repo.GetInitiativeUpdates(ctx, "init-1")
	if len(initUpdates) != 1 {
		t.Errorf("Expected 1 initiative update, got %d", len(initUpdates))
	}
}

func TestMockRepository_IssueChildren(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	team := api.Team{ID: "team-1"}
	parent := api.Issue{ID: "parent-1", Identifier: "TST-1", Team: &team}
	child1 := api.Issue{ID: "child-1", Identifier: "TST-2", Team: &team, Parent: &api.ParentIssue{ID: "parent-1"}}
	child2 := api.Issue{ID: "child-2", Identifier: "TST-3", Team: &team, Parent: &api.ParentIssue{ID: "parent-1"}}
	other := api.Issue{ID: "other-1", Identifier: "TST-4", Team: &team}

	repo.AddIssue(parent)
	repo.AddIssue(child1)
	repo.AddIssue(child2)
	repo.AddIssue(other)

	// Test GetIssueChildren
	children, _ := repo.GetIssueChildren(ctx, "parent-1")
	if len(children) != 2 {
		t.Errorf("Expected 2 children, got %d", len(children))
	}
}

func TestMockRepository_IssuesByProjectAndCycle(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	team := api.Team{ID: "team-1"}
	project := api.Project{ID: "project-1", Name: "Test Project"}
	cycle := api.IssueCycle{ID: "cycle-1", Name: "Sprint 1"}

	repo.AddIssue(api.Issue{ID: "i1", Identifier: "TST-1", Team: &team, Project: &project})
	repo.AddIssue(api.Issue{ID: "i2", Identifier: "TST-2", Team: &team, Project: &project, Cycle: &cycle})
	repo.AddIssue(api.Issue{ID: "i3", Identifier: "TST-3", Team: &team, Cycle: &cycle})
	repo.AddIssue(api.Issue{ID: "i4", Identifier: "TST-4", Team: &team})

	// Test GetIssuesByProject
	issues, _ := repo.GetIssuesByProject(ctx, "project-1")
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues in project, got %d", len(issues))
	}

	// Test GetIssuesByCycle
	issues, _ = repo.GetIssuesByCycle(ctx, "cycle-1")
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues in cycle, got %d", len(issues))
	}
}

func TestMockRepository_IssuesByLabel(t *testing.T) {
	t.Parallel()
	repo := NewMockRepository()
	ctx := context.Background()

	team := api.Team{ID: "team-1"}
	bugLabel := api.Label{ID: "label-bug", Name: "Bug"}
	featureLabel := api.Label{ID: "label-feature", Name: "Feature"}

	repo.AddIssue(api.Issue{
		ID: "i1", Identifier: "TST-1", Team: &team,
		Labels: api.Labels{Nodes: []api.Label{bugLabel}},
	})
	repo.AddIssue(api.Issue{
		ID: "i2", Identifier: "TST-2", Team: &team,
		Labels: api.Labels{Nodes: []api.Label{bugLabel, featureLabel}},
	})
	repo.AddIssue(api.Issue{
		ID: "i3", Identifier: "TST-3", Team: &team,
		Labels: api.Labels{Nodes: []api.Label{featureLabel}},
	})

	// Test GetIssuesByLabel
	issues, _ := repo.GetIssuesByLabel(ctx, "team-1", "label-bug")
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues with bug label, got %d", len(issues))
	}

	issues, _ = repo.GetIssuesByLabel(ctx, "team-1", "label-feature")
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues with feature label, got %d", len(issues))
	}
}

// Test interface compliance at compile time
var _ Repository = (*MockRepository)(nil)
