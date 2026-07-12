package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// TestGetTeamsDrainsPages proves GetTeams drains the teams connection —
// Linear silently caps a connection without first: at 50 nodes, and this is
// the sync worker's root fetch, so page 2 must be fetched with page 1's
// cursor and both pages' nodes returned.
func TestGetTeamsDrainsPages(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	teamA := testutil.FixtureTeam()
	teamB := testutil.FixtureTeam()
	teamB["id"] = "team-456"
	teamB["key"] = "SEC"
	mock.SetResponseSequence("Teams",
		map[string]any{
			"teams": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "cursor-1"},
				"nodes":    []map[string]any{teamA},
			},
		},
		map[string]any{
			"teams": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes":    []map[string]any{teamB},
			},
		},
	)

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	teams, err := client.GetTeams(context.Background())
	if err != nil {
		t.Fatalf("GetTeams failed: %v", err)
	}

	if len(teams) != 2 {
		t.Fatalf("expected 2 teams across 2 pages, got %d", len(teams))
	}
	if teams[0].ID != "team-123" || teams[1].ID != "team-456" {
		t.Errorf("teams out of order: got %q, %q", teams[0].ID, teams[1].ID)
	}

	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if got := calls[0].Variables["after"]; got != nil {
		t.Errorf("page 1 carried after=%v, want none", got)
	}
	if got := calls[1].Variables["after"]; got != "cursor-1" {
		t.Errorf("page 2 fetched with after=%v, want cursor-1", got)
	}
}

// TestGetProjectUpdatesDrainsPages proves the updates read drains past the
// old implicit 50-cap: updates accumulate over a project's lifetime and the
// SWR refresh is upsert-only, so a capped read silently froze completeness.
func TestGetProjectUpdatesDrainsPages(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	updateA := testutil.FixtureProjectUpdate()
	updateB := testutil.FixtureProjectUpdate()
	updateB["id"] = "update-456"
	page := func(pi map[string]any, updates ...map[string]any) map[string]any {
		return map[string]any{
			"project": map[string]any{
				"projectUpdates": map[string]any{
					"pageInfo": pi,
					"nodes":    updates,
				},
			},
		}
	}
	mock.SetResponseSequence("ProjectUpdates",
		page(map[string]any{"hasNextPage": true, "endCursor": "cursor-1"}, updateA),
		page(map[string]any{"hasNextPage": false, "endCursor": ""}, updateB),
	)

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	updates, err := client.GetProjectUpdates(context.Background(), "project-123")
	if err != nil {
		t.Fatalf("GetProjectUpdates failed: %v", err)
	}

	if len(updates) != 2 {
		t.Fatalf("expected 2 updates across 2 pages, got %d", len(updates))
	}
	if updates[0].ID != "update-123" || updates[1].ID != "update-456" {
		t.Errorf("updates out of order: got %q, %q", updates[0].ID, updates[1].ID)
	}

	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if got := calls[1].Variables["after"]; got != "cursor-1" {
		t.Errorf("page 2 fetched with after=%v, want cursor-1", got)
	}
	if got := calls[1].Variables["projectId"]; got != "project-123" {
		t.Errorf("page 2 lost projectId: got %v", got)
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

	// GetTeams drains, so the GraphQL error arrives wrapped with page
	// context; the structured error must survive the wrap.
	if !strings.Contains(err.Error(), "GraphQL error: authentication failed") {
		t.Errorf("unexpected error message: %v", err)
	}
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Errorf("error does not unwrap to *GraphQLError: %v", err)
	}
}

func TestCallRecording(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("Teams", testutil.TeamsResponse())
	mock.SetResponse("Viewer", map[string]any{"viewer": testutil.FixtureUser()})

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	// Make multiple calls
	_, _ = client.GetTeams(context.Background())
	_, _ = client.GetViewer(context.Background())

	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}

	if calls[0].Operation != "Teams" {
		t.Errorf("expected first operation 'Teams', got %q", calls[0].Operation)
	}

	if calls[1].Operation != "Viewer" {
		t.Errorf("expected second operation 'Viewer', got %q", calls[1].Operation)
	}
}

func TestMockReset(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("Teams", testutil.TeamsResponse())

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	_, _ = client.GetTeams(context.Background())

	if len(mock.Calls()) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.Calls()))
	}

	mock.Reset()

	if len(mock.Calls()) != 0 {
		t.Errorf("expected 0 calls after reset, got %d", len(mock.Calls()))
	}

	// The mock now returns empty data — under the fetch null policy that is
	// a loud error, not a silent empty team list.
	_, err := client.GetTeams(context.Background())
	if err == nil || !strings.Contains(err.Error(), `"teams" missing or null`) {
		t.Fatalf("GetTeams after reset: err = %v, want missing-or-null error", err)
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
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	doc := testutil.FixtureDocument()
	mock.SetResponse("TeamDocuments", testutil.ProjectDocumentsResponse(doc))

	client := NewClient("test-api-key")
	client.SetAPIURL(mock.URL())

	docs, err := client.GetTeamDocuments(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("GetTeamDocuments failed: %v", err)
	}

	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}

	if docs[0].Title != "Test Document" {
		t.Errorf("expected title 'Test Document', got %q", docs[0].Title)
	}
}

// TestRateLimitResetHeaderParsed verifies the per-axis reset headers are
// parsed as epoch MILLISECONDS (Linear's actual unit) and surfaced through
// RateLimitResetAt so the sync worker can use them for adaptive backoff.
func TestRateLimitResetHeaderParsed(t *testing.T) {
	t.Parallel()

	resetMs := time.Now().Add(60 * time.Second).UnixMilli()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Complexity-Reset", strconv.FormatInt(resetMs, 10))
		w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(resetMs, 10))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"teams": {"pageInfo": {"hasNextPage": false, "endCursor": ""}, "nodes": []}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)

	// Before any request, RateLimitResetAt should be zero
	if !client.RateLimitResetAt().IsZero() {
		t.Error("expected zero RateLimitResetAt before any request")
	}

	_, _ = client.GetTeams(context.Background())

	got := client.RateLimitResetAt()
	expected := time.UnixMilli(resetMs)
	if !got.Equal(expected) {
		t.Errorf("RateLimitResetAt() = %v, want %v", got, expected)
	}
}

// TestViewerProbeSeedsBudget verifies the cold-start probe contract end to
// end at the client level: before any response the budget is unseen (nothing
// gates), and one cheap GetViewer whose headers report a nearly-exhausted
// complexity window seeds the budget so the NEXT expensive query is deferred
// without ever reaching the server. This is the hole the sync worker's probe
// closes: without a first observed response, a cold start's burst would all
// admit un-gated.
func TestViewerProbeSeedsBudget(t *testing.T) {
	t.Parallel()

	resetMs := time.Now().Add(30 * time.Minute).UnixMilli()
	var requests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("X-Complexity", "1")
		w.Header().Set("X-RateLimit-Complexity-Limit", "3000000")
		w.Header().Set("X-RateLimit-Complexity-Remaining", "1000") // nearly exhausted
		w.Header().Set("X-RateLimit-Complexity-Reset", strconv.FormatInt(resetMs, 10))
		w.Header().Set("X-RateLimit-Requests-Limit", "2500")
		w.Header().Set("X-RateLimit-Requests-Remaining", "2400")
		w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(resetMs, 10))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"viewer": {"id": "user-1", "name": "Probe", "email": "p@example.com"}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)

	// Unseen budget: nothing gates yet.
	if client.LowBudget() {
		t.Fatal("LowBudget() = true before any response; an unseen budget must not gate")
	}

	viewer, err := client.GetViewer(context.Background())
	if err != nil {
		t.Fatalf("GetViewer failed: %v", err)
	}
	if viewer.ID != "user-1" {
		t.Errorf("viewer.ID = %q, want user-1", viewer.ID)
	}

	// The probe's headers seeded the budget: 1000 complexity remaining is
	// under every read tier's reserve, so expensive work now defers.
	if !client.LowBudget() {
		t.Error("LowBudget() = false after probe reported 1000/3000000 complexity remaining")
	}
	if _, err := client.GetTeams(context.Background()); err == nil || !strings.Contains(err.Error(), "deferred") {
		t.Errorf("GetTeams after exhausted probe: err = %v, want budget deferral", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1 (the deferred query must not reach the server)", got)
	}
	if got, want := client.RateLimitResetAt(), time.UnixMilli(resetMs); !got.Equal(want) {
		t.Errorf("RateLimitResetAt() = %v, want %v (seeded by the probe)", got, want)
	}
}

// TestRateLimitResetHeaderMissing verifies that when no reset headers are
// sent, RateLimitResetAt() remains zero.
func TestRateLimitResetHeaderMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No X-RateLimit-Reset header
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"teams": {"pageInfo": {"hasNextPage": false, "endCursor": ""}, "nodes": []}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)

	_, _ = client.GetTeams(context.Background())

	if !client.RateLimitResetAt().IsZero() {
		t.Errorf("RateLimitResetAt() = %v, want zero when header is absent", client.RateLimitResetAt())
	}
}

// =============================================================================
// Circuit Breaker Tests
// =============================================================================

func TestCircuitBreakerTripsAfterConsecutiveErrors(t *testing.T) {
	t.Parallel()

	// Use a listener on a port that refuses connections to simulate network errors
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close() // Close immediately so connections are refused

	client := NewClient("test-api-key")
	client.SetAPIURL("http://" + addr)

	// Make requests until circuit breaker trips (threshold = 5)
	for i := 0; i < circuitBreakerThreshold; i++ {
		_, _ = client.GetTeams(context.Background())
	}

	// Circuit should now be open
	if client.circuitOpenUntil.Load() == 0 {
		t.Fatal("expected circuit breaker to be open after consecutive errors")
	}

	// Next request should fail immediately with circuit breaker error
	_, err = client.GetTeams(context.Background())
	if err == nil || !strings.Contains(err.Error(), "circuit breaker open") {
		t.Errorf("expected circuit breaker error, got: %v", err)
	}
}

func TestCircuitBreakerResetsOnSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"teams": {"pageInfo": {"hasNextPage": false, "endCursor": ""}, "nodes": []}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)

	// Simulate some consecutive errors
	client.consecutiveErrors.Store(3)

	// Successful request should reset the counter
	_, _ = client.GetTeams(context.Background())

	if client.consecutiveErrors.Load() != 0 {
		t.Errorf("expected consecutive errors to reset to 0, got %d", client.consecutiveErrors.Load())
	}
}

func TestCircuitBreakerCooldownAllowsProbe(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"teams": {"pageInfo": {"hasNextPage": false, "endCursor": ""}, "nodes": []}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)

	// Set circuit open but with expired cooldown (in the past)
	client.circuitOpenUntil.Store(time.Now().Add(-1 * time.Second).Unix())

	// Should allow a probe request through (cooldown expired)
	_, err := client.GetTeams(context.Background())
	if err != nil {
		t.Errorf("expected probe request to succeed after cooldown, got: %v", err)
	}

	// Circuit should be closed now
	if client.circuitOpenUntil.Load() != 0 {
		t.Error("expected circuit to be closed after successful probe")
	}
}

// =============================================================================
// Mutation Priority Tests
// =============================================================================

// TestMutationPriorityReservesBudgetForWrites verifies the reserve ladder
// in Client.query: under a drained complexity budget a background read
// defers, while a mutation (reserve 0) still flows.
func TestMutationPriorityReservesBudgetForWrites(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"teams": {"pageInfo": {"hasNextPage": false, "endCursor": ""}, "nodes": []}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)

	// Drain the budget: 20k complexity left of 3M — under every read
	// tier's reserve floor, but enough for a write (reserve 0).
	client.budget.mu.Lock()
	client.budget.complexity = window{
		name: "complexity", limit: 3000000, remaining: 20000,
		resetAt: time.Now().Add(time.Hour), seen: true,
	}
	client.budget.mu.Unlock()

	// Non-mutation query should defer under the reserve floor
	var result struct {
		Teams struct {
			Nodes []struct{} `json:"nodes"`
		} `json:"teams"`
	}
	err := client.query(context.Background(), "query Teams { teams { nodes { id } } }", nil, &result)
	if err == nil || !strings.Contains(err.Error(), "deferred") {
		t.Errorf("expected read to be deferred under drained budget, got: %v", err)
	}

	// Mutation should still be allowed through
	err = client.query(context.Background(), "mutation UpdateIssue($id: String!) { issueUpdate(id: $id) { success } }", nil, &result)
	if err != nil && strings.Contains(err.Error(), "deferred") {
		t.Errorf("mutation should bypass the read reserves, got: %v", err)
	}
}

func TestClient_LowBudget(t *testing.T) {
	c := NewClient("test-key")
	// A fresh budget has observed nothing — unseen axes never gate.
	if c.LowBudget() {
		t.Error("LowBudget true before any response observed")
	}
	// Drain the complexity window below the list-tier reserve.
	c.budget.mu.Lock()
	c.budget.complexity = window{
		name: "complexity", limit: 3000000, remaining: 100000,
		resetAt: time.Now().Add(time.Hour), seen: true,
	}
	c.budget.mu.Unlock()
	if !c.LowBudget() {
		t.Error("LowBudget false with remaining under the list reserve")
	}
}

func TestClient_GetTeamIssueIDs(t *testing.T) {
	// Two pages: proves the migrated method drains the connection through
	// the paginate seam (not just the first page) and threads the cursor.
	var secondPageCursor string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), `"after":"c1"`) {
			secondPageCursor = "c1"
			_, _ = w.Write([]byte(`{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"i4"},{"id":"i5"}]}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[{"id":"i1"},{"id":"i2"},{"id":"i3"}]}}}}`))
	}))
	defer server.Close()

	c := NewClient("test-key")
	c.SetAPIURL(server.URL)

	ids, err := c.GetTeamIssueIDs(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Join(ids, ","); got != "i1,i2,i3,i4,i5" {
		t.Errorf("got %q, want i1,i2,i3,i4,i5", got)
	}
	if secondPageCursor != "c1" {
		t.Error("second page was not fetched with the page-1 cursor")
	}
}

func TestClient_GetWorkspaceProjectIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"projects":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"p1"},{"id":"p2"}]}}}`))
	}))
	defer server.Close()

	c := NewClient("test-key")
	c.SetAPIURL(server.URL)

	ids, err := c.GetWorkspaceProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Join(ids, ","); got != "p1,p2" {
		t.Errorf("got %q, want p1,p2", got)
	}
}

func TestClient_GetWorkspaceInitiativeIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"initiatives":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"i1"}]}}}`))
	}))
	defer server.Close()

	c := NewClient("test-key")
	c.SetAPIURL(server.URL)

	ids, err := c.GetWorkspaceInitiativeIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Join(ids, ","); got != "i1" {
		t.Errorf("got %q, want i1", got)
	}
}
