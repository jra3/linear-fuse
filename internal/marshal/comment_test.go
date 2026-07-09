package marshal

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// TestCommentToMarkdown pins the editable-only contract for a comment .md: the
// body alone, no frontmatter at all. Every frontmatter field the file used to
// carry was server-managed and its edits were silently discarded.
func TestCommentToMarkdown(t *testing.T) {
	t.Parallel()
	comment := &api.Comment{
		ID:        "comment-123",
		Body:      "Line 1\nLine 2",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		User:      &api.User{Email: "test@example.com", Name: "Test User"},
	}

	got := string(CommentToMarkdown(comment))
	if got != "Line 1\nLine 2\n" {
		t.Errorf("CommentToMarkdown() = %q, want the pure body with trailing newline", got)
	}
	if strings.HasPrefix(got, "---") {
		t.Error("comment .md must carry no frontmatter (server fields live in .meta)")
	}
}

// TestCommentMetaToMarkdown pins the server-managed half: identity,
// timestamps, and authorship, frontmatter-only, empties omitted.
func TestCommentMetaToMarkdown(t *testing.T) {
	t.Parallel()
	created := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	edited := created.Add(time.Hour)
	full := &api.Comment{
		ID:        "comment-123",
		Body:      "Body text",
		CreatedAt: created,
		UpdatedAt: edited,
		EditedAt:  &edited,
		User:      &api.User{Email: "test@example.com", Name: "Test User"},
	}

	content, err := CommentMetaToMarkdown(full)
	if err != nil {
		t.Fatalf("CommentMetaToMarkdown: %v", err)
	}
	keys, doc := frontmatterKeys(t, content)
	want := []string{"author", "authorName", "created", "edited", "id", "updated"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("comment .meta frontmatter keys = %v, want %v", keys, want)
	}
	if doc.Frontmatter["author"] != "test@example.com" {
		t.Errorf("author = %v, want test@example.com", doc.Frontmatter["author"])
	}
	if doc.Body != "" {
		t.Errorf("meta must be frontmatter-only, got body %q", doc.Body)
	}

	// No user, never edited: those keys are omitted.
	content, err = CommentMetaToMarkdown(&api.Comment{ID: "comment-min", CreatedAt: created, UpdatedAt: created})
	if err != nil {
		t.Fatalf("CommentMetaToMarkdown(min): %v", err)
	}
	if keys, _ := frontmatterKeys(t, content); !reflect.DeepEqual(keys, []string{"created", "id", "updated"}) {
		t.Errorf("minimal comment .meta keys = %v, want [created id updated]", keys)
	}
}
