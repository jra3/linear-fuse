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
