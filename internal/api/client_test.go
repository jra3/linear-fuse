package api

import (
	"context"
	"errors"
	"testing"

	"github.com/jra3/linear-fuse/internal/testutil"
)

func TestGetTeams(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	team := testutil.FixtureTeam()
	mock.SetResponse("Teams", testutil.TeamsResponse(team))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	teams, err := client.GetTeams(context.Background())
	if err != nil {
		t.Fatalf("GetTeams failed: %v", err)
	}

	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}

	if teams[0].ID != "team-123" {
		t.Errorf("expected team ID 'team-123', got %q", teams[0].ID)
	}

	if teams[0].Key != "TST" {
		t.Errorf("expected team key 'TST', got %q", teams[0].Key)
	}
}

func TestGetIssue(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("Issue", testutil.IssueResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.GetIssue(context.Background(), "issue-123")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if result.ID != "issue-123" {
		t.Errorf("expected issue ID 'issue-123', got %q", result.ID)
	}

	if result.Identifier != "TST-123" {
		t.Errorf("expected identifier 'TST-123', got %q", result.Identifier)
	}

	if result.Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %q", result.Title)
	}

	if result.Priority != 2 {
		t.Errorf("expected priority 2, got %d", result.Priority)
	}

	if result.Assignee == nil {
		t.Error("expected assignee to be set")
	} else if result.Assignee.Email != "test@example.com" {
		t.Errorf("expected assignee email 'test@example.com', got %q", result.Assignee.Email)
	}

	if len(result.Labels.Nodes) != 2 {
		t.Errorf("expected 2 labels, got %d", len(result.Labels.Nodes))
	}
}

func TestGetTeamIssues(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue1 := testutil.FixtureIssue()
	issue2 := testutil.FixtureIssueMinimal()
	mock.SetResponse("TeamIssues", testutil.TeamIssuesResponse(issue1, issue2))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetTeamIssues(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("GetTeamIssues failed: %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}

	// Verify variables were passed correctly
	call := mock.LastCall()
	if call == nil {
		t.Fatal("expected a call to be recorded")
	}
	if call.Variables["teamId"] != "team-123" {
		t.Errorf("expected teamId 'team-123', got %v", call.Variables["teamId"])
	}
}

func TestUpdateIssue(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("UpdateIssue", testutil.UpdateIssueResponse(true))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.UpdateIssue(context.Background(), "issue-123", map[string]any{
		"title":    "Updated Title",
		"priority": 1,
	})
	if err != nil {
		t.Fatalf("UpdateIssue failed: %v", err)
	}

	// Verify the call
	call := mock.LastCall()
	if call == nil {
		t.Fatal("expected a call to be recorded")
	}

	input, ok := call.Variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("expected input to be a map, got %T", call.Variables["input"])
	}

	if input["title"] != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %v", input["title"])
	}
}

func TestUpdateIssueFailure(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("UpdateIssue", testutil.UpdateIssueResponse(false))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.UpdateIssue(context.Background(), "issue-123", map[string]any{
		"title": "Updated Title",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateComment(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	comment := testutil.FixtureComment()
	mock.SetResponse("CreateComment", testutil.CreateCommentResponse(comment))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.CreateComment(context.Background(), "issue-123", "Test comment body")
	if err != nil {
		t.Fatalf("CreateComment failed: %v", err)
	}

	if result.ID != "comment-123" {
		t.Errorf("expected comment ID 'comment-123', got %q", result.ID)
	}

	// Verify variables
	call := mock.LastCall()
	if call.Variables["issueId"] != "issue-123" {
		t.Errorf("expected issueId 'issue-123', got %v", call.Variables["issueId"])
	}
	if call.Variables["body"] != "Test comment body" {
		t.Errorf("expected body 'Test comment body', got %v", call.Variables["body"])
	}
}

func TestGraphQLError(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetError("Teams", errors.New("authentication failed"))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	_, err := client.GetTeams(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if err.Error() != "GraphQL error: authentication failed" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGetTeamStates(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("TeamStates", testutil.TeamStatesResponse())

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	states, err := client.GetTeamStates(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("GetTeamStates failed: %v", err)
	}

	if len(states) != 5 {
		t.Errorf("expected 5 states, got %d", len(states))
	}

	// Verify state types
	stateTypes := make(map[string]bool)
	for _, s := range states {
		stateTypes[s.Type] = true
	}
	expected := []string{"backlog", "unstarted", "started", "completed", "canceled"}
	for _, st := range expected {
		if !stateTypes[st] {
			t.Errorf("missing state type %q", st)
		}
	}
}

func TestGetTeamLabels(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("TeamLabels", testutil.TeamLabelsResponse(
		testutil.FixtureLabel("Bug"),
		testutil.FixtureLabel("Feature"),
	))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.GetTeamLabels(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("GetTeamLabels failed: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 labels, got %d", len(result))
	}
}

func TestGetUsers(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("Users", testutil.UsersResponse(
		testutil.FixtureUser(),
		map[string]any{"id": "user-456", "name": "Other User", "email": "other@example.com", "active": true},
	))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.GetUsers(context.Background())
	if err != nil {
		t.Fatalf("GetUsers failed: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 users, got %d", len(result))
	}
}

func TestGetIssueComments(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	comment := testutil.FixtureComment()
	mock.SetResponse("IssueComments", testutil.IssueCommentsResponse(comment))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.GetIssueComments(context.Background(), "issue-123")
	if err != nil {
		t.Fatalf("GetIssueComments failed: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(result))
	}

	if result[0].Body != "This is a test comment" {
		t.Errorf("expected body 'This is a test comment', got %q", result[0].Body)
	}
}

func TestGetIssueDocuments(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	doc := testutil.FixtureDocument()
	mock.SetResponse("IssueDocuments", testutil.IssueDocumentsResponse(doc))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.GetIssueDocuments(context.Background(), "issue-123")
	if err != nil {
		t.Fatalf("GetIssueDocuments failed: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 document, got %d", len(result))
	}

	if result[0].Title != "Test Document" {
		t.Errorf("expected title 'Test Document', got %q", result[0].Title)
	}
}

func TestCallRecording(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("Teams", testutil.TeamsResponse())
	mock.SetResponse("Users", testutil.UsersResponse())

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	// Make multiple calls
	client.GetTeams(context.Background())
	client.GetUsers(context.Background())

	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}

	if calls[0].Operation != "Teams" {
		t.Errorf("expected first operation 'Teams', got %q", calls[0].Operation)
	}

	if calls[1].Operation != "Users" {
		t.Errorf("expected second operation 'Users', got %q", calls[1].Operation)
	}
}

func TestMockReset(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("Teams", testutil.TeamsResponse())

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	client.GetTeams(context.Background())

	if len(mock.Calls()) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.Calls()))
	}

	mock.Reset()

	if len(mock.Calls()) != 0 {
		t.Errorf("expected 0 calls after reset, got %d", len(mock.Calls()))
	}

	// Should return empty data now
	teams, err := client.GetTeams(context.Background())
	if err != nil {
		t.Fatalf("GetTeams failed: %v", err)
	}

	if len(teams) != 0 {
		t.Errorf("expected 0 teams after reset, got %d", len(teams))
	}
}

func TestGetTeamIssuesByStatus(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("TeamIssuesByStatus", testutil.FilteredIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetTeamIssuesByStatus(context.Background(), "team-123", "In Progress")
	if err != nil {
		t.Fatalf("GetTeamIssuesByStatus failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	call := mock.LastCall()
	if call.Variables["teamId"] != "team-123" {
		t.Errorf("expected teamId 'team-123', got %v", call.Variables["teamId"])
	}
	if call.Variables["statusName"] != "In Progress" {
		t.Errorf("expected statusName 'In Progress', got %v", call.Variables["statusName"])
	}
}

func TestGetTeamIssuesByPriority(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("TeamIssuesByPriority", testutil.IssuesByPriorityResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetTeamIssuesByPriority(context.Background(), "team-123", 2)
	if err != nil {
		t.Fatalf("GetTeamIssuesByPriority failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	call := mock.LastCall()
	if call.Variables["teamId"] != "team-123" {
		t.Errorf("expected teamId 'team-123', got %v", call.Variables["teamId"])
	}
	if call.Variables["priority"] != float64(2) {
		t.Errorf("expected priority 2, got %v", call.Variables["priority"])
	}
}

func TestGetTeamIssuesByLabel(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("TeamIssuesByLabel", testutil.FilteredIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetTeamIssuesByLabel(context.Background(), "team-123", "Bug")
	if err != nil {
		t.Fatalf("GetTeamIssuesByLabel failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	call := mock.LastCall()
	if call.Variables["labelName"] != "Bug" {
		t.Errorf("expected labelName 'Bug', got %v", call.Variables["labelName"])
	}
}

func TestGetTeamIssuesByAssignee(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("TeamIssuesByAssignee", testutil.FilteredIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetTeamIssuesByAssignee(context.Background(), "team-123", "user-456")
	if err != nil {
		t.Fatalf("GetTeamIssuesByAssignee failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	call := mock.LastCall()
	if call.Variables["assigneeId"] != "user-456" {
		t.Errorf("expected assigneeId 'user-456', got %v", call.Variables["assigneeId"])
	}
}

func TestGetTeamIssuesUnassigned(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssueMinimal()
	mock.SetResponse("TeamIssuesUnassigned", testutil.FilteredIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetTeamIssuesUnassigned(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("GetTeamIssuesUnassigned failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	call := mock.LastCall()
	if call.Variables["teamId"] != "team-123" {
		t.Errorf("expected teamId 'team-123', got %v", call.Variables["teamId"])
	}
}

func TestGetMyIssues(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("MyIssues", testutil.MyIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetMyIssues(context.Background())
	if err != nil {
		t.Fatalf("GetMyIssues failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	if issues[0].ID != "issue-123" {
		t.Errorf("expected issue ID 'issue-123', got %q", issues[0].ID)
	}
}

func TestGetMyCreatedIssues(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("MyCreatedIssues", testutil.MyCreatedIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetMyCreatedIssues(context.Background())
	if err != nil {
		t.Fatalf("GetMyCreatedIssues failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
}

func TestGetMyActiveIssues(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("MyActiveIssues", testutil.MyIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetMyActiveIssues(context.Background())
	if err != nil {
		t.Fatalf("GetMyActiveIssues failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
}

func TestArchiveIssue(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("ArchiveIssue", testutil.ArchiveIssueResponse(true))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.ArchiveIssue(context.Background(), "issue-123")
	if err != nil {
		t.Fatalf("ArchiveIssue failed: %v", err)
	}

	call := mock.LastCall()
	if call.Variables["id"] != "issue-123" {
		t.Errorf("expected id 'issue-123', got %v", call.Variables["id"])
	}
}

func TestArchiveIssueFailure(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("ArchiveIssue", testutil.ArchiveIssueResponse(false))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.ArchiveIssue(context.Background(), "issue-123")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateIssue(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("CreateIssue", testutil.CreateIssueResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.CreateIssue(context.Background(), map[string]any{
		"teamId": "team-123",
		"title":  "New Issue",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	if result.ID != "issue-123" {
		t.Errorf("expected issue ID 'issue-123', got %q", result.ID)
	}
}

func TestGetTeamProjects(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	project := testutil.FixtureProject()
	mock.SetResponse("TeamProjects", testutil.TeamProjectsResponse(project))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	projects, err := client.GetTeamProjects(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("GetTeamProjects failed: %v", err)
	}

	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}

	if projects[0].Name != "Test Project" {
		t.Errorf("expected project name 'Test Project', got %q", projects[0].Name)
	}
}

func TestGetProjectIssues(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := map[string]any{
		"id":         "issue-proj-123",
		"identifier": "TST-999",
		"title":      "Project Issue",
		"state":      testutil.FixtureState("started"),
	}
	mock.SetResponse("ProjectIssues", testutil.ProjectIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetProjectIssues(context.Background(), "project-123")
	if err != nil {
		t.Fatalf("GetProjectIssues failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	call := mock.LastCall()
	if call.Variables["projectId"] != "project-123" {
		t.Errorf("expected projectId 'project-123', got %v", call.Variables["projectId"])
	}
}

func TestGetProjectMilestones(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	milestone := testutil.FixtureProjectMilestone()
	mock.SetResponse("ProjectMilestones", testutil.ProjectMilestonesResponse(milestone))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	milestones, err := client.GetProjectMilestones(context.Background(), "project-123")
	if err != nil {
		t.Fatalf("GetProjectMilestones failed: %v", err)
	}

	if len(milestones) != 1 {
		t.Fatalf("expected 1 milestone, got %d", len(milestones))
	}

	if milestones[0].Name != "Alpha Release" {
		t.Errorf("expected milestone name 'Alpha Release', got %q", milestones[0].Name)
	}
}

func TestGetProjectUpdates(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	update := testutil.FixtureProjectUpdate()
	mock.SetResponse("ProjectUpdates", testutil.ProjectUpdatesResponse(update))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	updates, err := client.GetProjectUpdates(context.Background(), "project-123")
	if err != nil {
		t.Fatalf("GetProjectUpdates failed: %v", err)
	}

	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	if updates[0].Health != "onTrack" {
		t.Errorf("expected health 'onTrack', got %q", updates[0].Health)
	}
}

func TestCreateProjectUpdate(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	update := testutil.FixtureProjectUpdate()
	mock.SetResponse("CreateProjectUpdate", testutil.CreateProjectUpdateResponse(update))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.CreateProjectUpdate(context.Background(), "project-123", "Sprint completed", "onTrack")
	if err != nil {
		t.Fatalf("CreateProjectUpdate failed: %v", err)
	}

	if result.ID != "update-123" {
		t.Errorf("expected update ID 'update-123', got %q", result.ID)
	}

	call := mock.LastCall()
	if call.Variables["projectId"] != "project-123" {
		t.Errorf("expected projectId 'project-123', got %v", call.Variables["projectId"])
	}
}

func TestGetTeamCycles(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	cycle := testutil.FixtureCycle()
	mock.SetResponse("TeamCycles", testutil.TeamCyclesResponse(cycle))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	cycles, err := client.GetTeamCycles(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("GetTeamCycles failed: %v", err)
	}

	if len(cycles) != 1 {
		t.Fatalf("expected 1 cycle, got %d", len(cycles))
	}

	if cycles[0].Number != 42 {
		t.Errorf("expected cycle number 42, got %d", cycles[0].Number)
	}
}

func TestGetCycleIssues(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureCycleIssue()
	mock.SetResponse("CycleIssues", testutil.CycleIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetCycleIssues(context.Background(), "cycle-123")
	if err != nil {
		t.Fatalf("GetCycleIssues failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	call := mock.LastCall()
	if call.Variables["cycleId"] != "cycle-123" {
		t.Errorf("expected cycleId 'cycle-123', got %v", call.Variables["cycleId"])
	}
}

func TestCreateLabel(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	label := testutil.FixtureLabel("NewLabel")
	mock.SetResponse("CreateLabel", testutil.CreateLabelResponse(label))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.CreateLabel(context.Background(), map[string]any{
		"teamId": "team-123",
		"name":   "NewLabel",
		"color":  "#ff0000",
	})
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}

	if result.Name != "NewLabel" {
		t.Errorf("expected label name 'NewLabel', got %q", result.Name)
	}
}

func TestUpdateLabel(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	label := testutil.FixtureLabel("UpdatedLabel")
	mock.SetResponse("UpdateLabel", testutil.UpdateLabelResponse(label))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.UpdateLabel(context.Background(), "label-123", map[string]any{
		"name": "UpdatedLabel",
	})
	if err != nil {
		t.Fatalf("UpdateLabel failed: %v", err)
	}

	if result.Name != "UpdatedLabel" {
		t.Errorf("expected label name 'UpdatedLabel', got %q", result.Name)
	}
}

func TestDeleteLabel(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("DeleteLabel", testutil.DeleteLabelResponse(true))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.DeleteLabel(context.Background(), "label-123")
	if err != nil {
		t.Fatalf("DeleteLabel failed: %v", err)
	}

	call := mock.LastCall()
	if call.Variables["id"] != "label-123" {
		t.Errorf("expected id 'label-123', got %v", call.Variables["id"])
	}
}

func TestDeleteLabelFailure(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("DeleteLabel", testutil.DeleteLabelResponse(false))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.DeleteLabel(context.Background(), "label-123")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUpdateComment(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	comment := testutil.FixtureComment()
	comment["body"] = "Updated comment body"
	mock.SetResponse("UpdateComment", testutil.UpdateCommentResponse(comment))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.UpdateComment(context.Background(), "comment-123", "Updated comment body")
	if err != nil {
		t.Fatalf("UpdateComment failed: %v", err)
	}

	if result.Body != "Updated comment body" {
		t.Errorf("expected body 'Updated comment body', got %q", result.Body)
	}
}

func TestDeleteComment(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("DeleteComment", testutil.DeleteCommentResponse(true))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.DeleteComment(context.Background(), "comment-123")
	if err != nil {
		t.Fatalf("DeleteComment failed: %v", err)
	}

	call := mock.LastCall()
	if call.Variables["id"] != "comment-123" {
		t.Errorf("expected id 'comment-123', got %v", call.Variables["id"])
	}
}

func TestDeleteCommentFailure(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("DeleteComment", testutil.DeleteCommentResponse(false))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.DeleteComment(context.Background(), "comment-123")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateDocument(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	doc := testutil.FixtureDocument()
	mock.SetResponse("CreateDocument", testutil.CreateDocumentResponse(doc))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.CreateDocument(context.Background(), map[string]any{
		"issueId": "issue-123",
		"title":   "New Doc",
		"content": "Document content",
	})
	if err != nil {
		t.Fatalf("CreateDocument failed: %v", err)
	}

	if result.Title != "Test Document" {
		t.Errorf("expected title 'Test Document', got %q", result.Title)
	}
}

func TestUpdateDocument(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	doc := testutil.FixtureDocument()
	doc["title"] = "Updated Document"
	mock.SetResponse("UpdateDocument", testutil.UpdateDocumentResponse(doc))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.UpdateDocument(context.Background(), "doc-123", map[string]any{
		"title": "Updated Document",
	})
	if err != nil {
		t.Fatalf("UpdateDocument failed: %v", err)
	}

	if result.Title != "Updated Document" {
		t.Errorf("expected title 'Updated Document', got %q", result.Title)
	}
}

func TestDeleteDocument(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("DeleteDocument", testutil.DeleteDocumentResponse(true))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.DeleteDocument(context.Background(), "doc-123")
	if err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}
}

func TestDeleteDocumentFailure(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("DeleteDocument", testutil.DeleteDocumentResponse(false))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	err := client.DeleteDocument(context.Background(), "doc-123")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetInitiatives(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	initiative := testutil.FixtureInitiative()
	mock.SetResponse("Initiatives", testutil.InitiativesResponse(initiative))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	initiatives, err := client.GetInitiatives(context.Background())
	if err != nil {
		t.Fatalf("GetInitiatives failed: %v", err)
	}

	if len(initiatives) != 1 {
		t.Fatalf("expected 1 initiative, got %d", len(initiatives))
	}

	if initiatives[0].Name != "Test Initiative" {
		t.Errorf("expected name 'Test Initiative', got %q", initiatives[0].Name)
	}
}

func TestGetInitiativeUpdates(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	update := testutil.FixtureInitiativeUpdate()
	mock.SetResponse("InitiativeUpdates", testutil.InitiativeUpdatesResponse(update))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	updates, err := client.GetInitiativeUpdates(context.Background(), "initiative-123")
	if err != nil {
		t.Fatalf("GetInitiativeUpdates failed: %v", err)
	}

	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	if updates[0].Health != "onTrack" {
		t.Errorf("expected health 'onTrack', got %q", updates[0].Health)
	}
}

func TestCreateInitiativeUpdate(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	update := testutil.FixtureInitiativeUpdate()
	mock.SetResponse("CreateInitiativeUpdate", testutil.CreateInitiativeUpdateResponse(update))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	result, err := client.CreateInitiativeUpdate(context.Background(), "initiative-123", "Status update", "onTrack")
	if err != nil {
		t.Fatalf("CreateInitiativeUpdate failed: %v", err)
	}

	if result.ID != "init-update-123" {
		t.Errorf("expected update ID 'init-update-123', got %q", result.ID)
	}
}

func TestGetProjectDocuments(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	doc := testutil.FixtureDocument()
	mock.SetResponse("ProjectDocuments", testutil.ProjectDocumentsResponse(doc))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	docs, err := client.GetProjectDocuments(context.Background(), "project-123")
	if err != nil {
		t.Fatalf("GetProjectDocuments failed: %v", err)
	}

	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}

	if docs[0].Title != "Test Document" {
		t.Errorf("expected title 'Test Document', got %q", docs[0].Title)
	}
}

func TestGetTeamDocuments(t *testing.T) {
	t.Parallel()

	client := NewClient("test-api-key")

	// GetTeamDocuments always returns empty (Linear API doesn't support team-level docs)
	docs, err := client.GetTeamDocuments(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("GetTeamDocuments failed: %v", err)
	}

	if len(docs) != 0 {
		t.Errorf("expected 0 documents, got %d", len(docs))
	}
}

func TestGetUserIssues(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	issue := testutil.FixtureIssue()
	mock.SetResponse("UserIssues", testutil.UserIssuesResponse(issue))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	issues, err := client.GetUserIssues(context.Background(), "user-123")
	if err != nil {
		t.Fatalf("GetUserIssues failed: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	call := mock.LastCall()
	if call.Variables["userId"] != "user-123" {
		t.Errorf("expected userId 'user-123', got %v", call.Variables["userId"])
	}
}

func TestGetTeamMembers(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	user := testutil.FixtureUser()
	mock.SetResponse("TeamMembers", testutil.TeamMembersResponse(user))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	members, err := client.GetTeamMembers(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("GetTeamMembers failed: %v", err)
	}

	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}

	if members[0].Email != "test@example.com" {
		t.Errorf("expected email 'test@example.com', got %q", members[0].Email)
	}
}
