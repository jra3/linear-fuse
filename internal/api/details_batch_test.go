package api

import (
	"context"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/testutil"
)

// GetIssueDetailsBatch's contract is all-or-nothing: a nil-error return
// guarantees a non-nil entry for every requested ID. Sync-worker prunes rely
// on it — a silently-missing or null alias decoded as empty collections would
// prune a live issue's details.

func detailsPayload(commentIDs ...string) map[string]any {
	comments := []map[string]any{}
	for _, id := range commentIDs {
		comments = append(comments, map[string]any{"id": id, "body": "text"})
	}
	empty := map[string]any{"nodes": []map[string]any{}}
	return map[string]any{
		"comments":         map[string]any{"nodes": comments},
		"documents":        empty,
		"attachments":      empty,
		"relations":        empty,
		"inverseRelations": empty,
	}
}

func TestGetIssueDetailsBatchAllAliasesPresent(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("IssueDetailsBatch", map[string]any{
		"i0": detailsPayload("comment-1"),
		"i1": detailsPayload(),
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	details, err := c.GetIssueDetailsBatch(context.Background(), []string{"issue-a", "issue-b"})
	if err != nil {
		t.Fatalf("GetIssueDetailsBatch: %v", err)
	}
	if len(details) != 2 {
		t.Fatalf("details = %d entries, want 2", len(details))
	}
	for _, id := range []string{"issue-a", "issue-b"} {
		if details[id] == nil {
			t.Fatalf("details[%s] is nil, want non-nil for every requested ID", id)
		}
	}
	if len(details["issue-a"].Comments) != 1 || details["issue-a"].Comments[0].ID != "comment-1" {
		t.Errorf("issue-a comments = %+v, want [comment-1]", details["issue-a"].Comments)
	}
}

func TestGetIssueDetailsBatchMissingAliasFails(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	// i1 is absent from the response entirely.
	mock.SetResponse("IssueDetailsBatch", map[string]any{
		"i0": detailsPayload(),
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	details, err := c.GetIssueDetailsBatch(context.Background(), []string{"issue-a", "issue-b"})
	if err == nil {
		t.Fatal("expected error for missing alias, got nil")
	}
	if !strings.Contains(err.Error(), "issue-b") {
		t.Errorf("error = %q, want it to name issue-b", err)
	}
	if details != nil {
		t.Errorf("details = %v, want nil map on error", details)
	}
}

func TestGetIssueDetailsBatchNullAliasFails(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	// A null alias is how GraphQL reports an issue that doesn't exist. It must
	// not decode into an empty payload whose collections all look "complete".
	mock.SetResponse("IssueDetailsBatch", map[string]any{
		"i0": detailsPayload(),
		"i1": nil,
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	details, err := c.GetIssueDetailsBatch(context.Background(), []string{"issue-a", "issue-b"})
	if err == nil {
		t.Fatal("expected error for null alias, got nil")
	}
	if !strings.Contains(err.Error(), "issue-b") {
		t.Errorf("error = %q, want it to name issue-b", err)
	}
	if !strings.Contains(err.Error(), "null") {
		t.Errorf("error = %q, want it to say the alias was null", err)
	}
	if details != nil {
		t.Errorf("details = %v, want nil map on error", details)
	}
}

func TestGetIssueDetailsBatchDecodeFailureFails(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	// comments is a string where an object is expected: decode failure.
	broken := detailsPayload()
	broken["comments"] = "not-a-connection"
	mock.SetResponse("IssueDetailsBatch", map[string]any{
		"i0": detailsPayload(),
		"i1": broken,
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	details, err := c.GetIssueDetailsBatch(context.Background(), []string{"issue-a", "issue-b"})
	if err == nil {
		t.Fatal("expected error for undecodable alias, got nil")
	}
	if !strings.Contains(err.Error(), "issue-b") {
		t.Errorf("error = %q, want it to name issue-b", err)
	}
	if details != nil {
		t.Errorf("details = %v, want nil map on error", details)
	}
}
