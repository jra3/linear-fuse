package fs

import (
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

func TestExtractCommentBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "plain text",
			content: "This is just plain text",
			want:    "This is just plain text",
		},
		{
			name:    "plain text with whitespace",
			content: "  \n  This is text  \n  ",
			want:    "This is text",
		},
		{
			name: "with frontmatter",
			content: `---
id: comment-123
author: test@example.com
---
This is the comment body`,
			want: "This is the comment body",
		},
		{
			name: "frontmatter with multiline body",
			content: `---
id: comment-123
---
First line
Second line
Third line`,
			want: "First line\nSecond line\nThird line",
		},
		{
			name:    "empty body after frontmatter",
			content: "---\nid: test\n---\n",
			want:    "",
		},
		{
			name:    "body with whitespace after frontmatter",
			content: "---\nid: test\n---\n\n  Comment body  \n\n",
			want:    "Comment body",
		},
		{
			name:    "no closing frontmatter delimiter",
			content: "---\nid: test\nNo closing",
			want:    "---\nid: test\nNo closing",
		},
		{
			name:    "empty content",
			content: "",
			want:    "",
		},
		{
			name:    "only whitespace",
			content: "   \n\n   ",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCommentBody([]byte(tt.content))
			if got != tt.want {
				t.Errorf("extractCommentBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCommentRenderExtractRoundTrip: the rendered comment .md (pure body, no
// frontmatter — the editable-only split) extracts back to the original body,
// so a no-op save pushes nothing. And the lenient parse still strips a leading
// frontmatter block, so an agent pasting old-format content works too.
func TestCommentRenderExtractRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Now()
	originalBody := "This is my original comment body\nwith a second line"
	comment := &api.Comment{
		ID:        "comment-123",
		Body:      originalBody,
		CreatedAt: now,
		UpdatedAt: now,
		User:      &api.User{Email: "test@example.com", Name: "Test User"},
	}

	content := marshal.CommentToMarkdown(comment)
	if strings.HasPrefix(string(content), "---") {
		t.Error("comment .md must carry no frontmatter (server fields live in the .meta sidecar)")
	}
	if got := extractCommentBody(content); got != originalBody {
		t.Errorf("Round-trip failed: got %q, want %q", got, originalBody)
	}

	// Old-format paste: a frontmatter block is stripped, not treated as body.
	oldFormat := []byte("---\nid: comment-123\nauthor: test@example.com\n---\n" + originalBody + "\n")
	if got := extractCommentBody(oldFormat); got != originalBody {
		t.Errorf("Old-format extract = %q, want %q", got, originalBody)
	}
}
