package fs

import (
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
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

func TestCommentToMarkdown(t *testing.T) {
	t.Parallel()
	now := time.Now()
	edited := now.Add(time.Hour)

	tests := []struct {
		name        string
		comment     *api.Comment
		wantContain []string
	}{
		{
			name: "basic comment",
			comment: &api.Comment{
				ID:        "comment-123",
				Body:      "This is the comment",
				CreatedAt: now,
				UpdatedAt: now,
			},
			wantContain: []string{
				"---",
				"id: comment-123",
				"created:",
				"updated:",
				"---",
				"This is the comment",
			},
		},
		{
			name: "comment with author",
			comment: &api.Comment{
				ID:        "comment-456",
				Body:      "Author comment",
				CreatedAt: now,
				UpdatedAt: now,
				User: &api.User{
					Email: "test@example.com",
					Name:  "Test User",
				},
			},
			wantContain: []string{
				"author: test@example.com",
				"authorName: Test User",
				"Author comment",
			},
		},
		{
			name: "comment with edited time",
			comment: &api.Comment{
				ID:        "comment-789",
				Body:      "Edited comment",
				CreatedAt: now,
				UpdatedAt: edited,
				EditedAt:  &edited,
			},
			wantContain: []string{
				"edited:",
				"Edited comment",
			},
		},
		{
			name: "multiline comment body",
			comment: &api.Comment{
				ID:        "comment-abc",
				Body:      "Line 1\nLine 2\nLine 3",
				CreatedAt: now,
				UpdatedAt: now,
			},
			wantContain: []string{
				"Line 1\nLine 2\nLine 3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(commentToMarkdown(tt.comment))
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("commentToMarkdown() missing %q\nGot:\n%s", want, got)
				}
			}
		})
	}
}

func TestCommentToMarkdown_HasValidYAML(t *testing.T) {
	t.Parallel()
	now := time.Now()
	comment := &api.Comment{
		ID:        "comment-123",
		Body:      "Test body",
		CreatedAt: now,
		UpdatedAt: now,
		User: &api.User{
			Email: "user@example.com",
			Name:  "User Name",
		},
	}

	content := commentToMarkdown(comment)

	// Should start with frontmatter
	if !strings.HasPrefix(string(content), "---\n") {
		t.Error("Comment should start with YAML frontmatter")
	}

	// Should have closing frontmatter
	if !strings.Contains(string(content), "\n---\n") {
		t.Error("Comment should have closing frontmatter delimiter")
	}

	// Body should come after frontmatter
	parts := strings.Split(string(content), "---")
	if len(parts) < 3 {
		t.Error("Expected frontmatter and body sections")
	}
}

func TestCommentToMarkdown_RoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Now()
	originalBody := "This is my original comment body"
	comment := &api.Comment{
		ID:        "comment-123",
		Body:      originalBody,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Convert to markdown
	content := commentToMarkdown(comment)

	// Extract body back
	extractedBody := extractCommentBody(content)

	// Should get original body back
	if extractedBody != originalBody {
		t.Errorf("Round-trip failed: got %q, want %q", extractedBody, originalBody)
	}
}
