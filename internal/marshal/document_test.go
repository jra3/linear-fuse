package marshal

import (
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestDocumentToMarkdown(t *testing.T) {
	t.Parallel()
	baseTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	updateTime := time.Date(2025, 1, 16, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		doc         *api.Document
		wantContain []string
		wantErr     bool
	}{
		{
			name: "full document with all fields",
			doc: &api.Document{
				ID:        "doc-123",
				Title:     "Technical Requirements",
				Content:   "# Overview\n\nThis is the technical spec.",
				SlugID:    "technical-requirements-abc123",
				URL:       "https://linear.app/docs/technical-requirements",
				Icon:      "📄",
				Color:     "#3B82F6",
				CreatedAt: baseTime,
				UpdatedAt: updateTime,
				Creator:   &api.User{ID: "user-1", Name: "Alice", Email: "alice@example.com"},
			},
			wantContain: []string{
				"id: doc-123",
				"title: Technical Requirements",
				"url: https://linear.app/docs/technical-requirements",
				"slug: technical-requirements-abc123",
				"icon:", // YAML may use different quote styles for emoji
				"color:",
				"creator: alice@example.com",
				"# Overview",
				"This is the technical spec.",
			},
		},
		{
			name: "minimal document",
			doc: &api.Document{
				ID:        "doc-min",
				Title:     "Simple Doc",
				Content:   "",
				CreatedAt: baseTime,
				UpdatedAt: baseTime,
			},
			wantContain: []string{
				"id: doc-min",
				"title: Simple Doc",
				"# Simple Doc", // Auto-generated body
			},
		},
		{
			name: "document without creator",
			doc: &api.Document{
				ID:        "doc-no-creator",
				Title:     "No Creator Doc",
				Content:   "Some content",
				CreatedAt: baseTime,
				UpdatedAt: baseTime,
			},
			wantContain: []string{
				"title: No Creator Doc",
				"Some content",
			},
		},
		{
			name: "document without optional fields",
			doc: &api.Document{
				ID:        "doc-basic",
				Title:     "Basic Doc",
				Content:   "Content here",
				URL:       "https://example.com/doc",
				CreatedAt: baseTime,
				UpdatedAt: baseTime,
			},
			wantContain: []string{
				"title: Basic Doc",
				"Content here",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DocumentToMarkdown(tt.doc)

			if tt.wantErr {
				if err == nil {
					t.Errorf("DocumentToMarkdown() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("DocumentToMarkdown() unexpected error: %v", err)
				return
			}

			result := string(got)
			for _, want := range tt.wantContain {
				if !strings.Contains(result, want) {
					t.Errorf("DocumentToMarkdown() missing %q\nGot:\n%s", want, result)
				}
			}
		})
	}
}

func TestMarkdownToDocumentUpdate(t *testing.T) {
	t.Parallel()
	baseTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	original := &api.Document{
		ID:        "doc-123",
		Title:     "Original Title",
		Content:   "Original content",
		SlugID:    "original-slug",
		URL:       "https://linear.app/docs/original",
		Icon:      "📄",
		Color:     "#3B82F6",
		CreatedAt: baseTime,
		UpdatedAt: baseTime,
		Creator:   &api.User{ID: "user-1", Name: "Alice", Email: "alice@example.com"},
	}

	tests := []struct {
		name       string
		content    string
		wantUpdate map[string]any
		wantErr    bool
	}{
		{
			name: "no changes",
			content: `---
id: doc-123
title: Original Title
url: https://linear.app/docs/original
icon: "📄"
color: "#3B82F6"
---
Original content`,
			wantUpdate: map[string]any{},
		},
		{
			name: "title changed",
			content: `---
title: New Title
---
Original content`,
			wantUpdate: map[string]any{
				"title": "New Title",
			},
		},
		{
			name: "content changed",
			content: `---
title: Original Title
---
New content with updates.`,
			wantUpdate: map[string]any{
				"content": "New content with updates.",
			},
		},
		{
			name: "icon changed",
			content: `---
title: Original Title
icon: "🚀"
---
Original content`,
			wantUpdate: map[string]any{
				"icon": "🚀",
			},
		},
		{
			name: "color changed",
			content: `---
title: Original Title
color: "#FF0000"
---
Original content`,
			wantUpdate: map[string]any{
				"color": "#FF0000",
			},
		},
		{
			name: "multiple changes",
			content: `---
title: Updated Title
icon: "📝"
color: "#00FF00"
---
Updated content here.`,
			wantUpdate: map[string]any{
				"title":   "Updated Title",
				"icon":    "📝",
				"color":   "#00FF00",
				"content": "Updated content here.",
			},
		},
		{
			name:    "invalid markdown",
			content: "---\ntitle: [invalid\n---\nbody",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MarkdownToDocumentUpdate([]byte(tt.content), original)

			if tt.wantErr {
				if err == nil {
					t.Errorf("MarkdownToDocumentUpdate() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("MarkdownToDocumentUpdate() unexpected error: %v", err)
				return
			}

			if len(got) != len(tt.wantUpdate) {
				t.Errorf("MarkdownToDocumentUpdate() returned %d fields, want %d\nGot: %v\nWant: %v",
					len(got), len(tt.wantUpdate), got, tt.wantUpdate)
			}

			for k, want := range tt.wantUpdate {
				gotVal, ok := got[k]
				if !ok {
					t.Errorf("MarkdownToDocumentUpdate() missing key %q", k)
					continue
				}
				if gotVal != want {
					t.Errorf("MarkdownToDocumentUpdate() field %q = %v, want %v", k, gotVal, want)
				}
			}
		})
	}
}

func TestParseNewDocument(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		content   string
		wantTitle string
		wantBody  string
		wantErr   bool
	}{
		{
			name: "title from frontmatter",
			content: `---
title: My Document
---
This is the body content.`,
			wantTitle: "My Document",
			wantBody:  "This is the body content.",
		},
		{
			name:      "title from heading",
			content:   "# Document Title\n\nBody content here.",
			wantTitle: "Document Title",
			wantBody:  "Body content here.",
		},
		{
			name:      "title from heading with multiple lines",
			content:   "# Document Title\n\nFirst paragraph.\n\nSecond paragraph.",
			wantTitle: "Document Title",
			wantBody:  "First paragraph.\n\nSecond paragraph.",
		},
		{
			name:      "title from first line when no heading",
			content:   "This is the first line\n\nMore content.",
			wantTitle: "This is the first line",
			wantBody:  "This is the first line\n\nMore content.",
		},
		{
			name:      "long first line truncated",
			content:   "This is a very long first line that exceeds fifty characters and should be truncated to fit\n\nMore content.",
			wantTitle: "This is a very long first line that exceeds fifty ...",
			wantBody:  "This is a very long first line that exceeds fifty characters and should be truncated to fit\n\nMore content.",
		},
		{
			name:      "empty content uses Untitled",
			content:   "",
			wantTitle: "Untitled",
			wantBody:  "",
		},
		{
			name:      "whitespace only uses Untitled",
			content:   "\n\n  \n",
			wantTitle: "Untitled",
			wantBody:  "\n\n  \n",
		},
		{
			name: "frontmatter title takes precedence over heading",
			content: `---
title: Frontmatter Title
---
# Heading Title

Body content.`,
			wantTitle: "Frontmatter Title",
			wantBody:  "# Heading Title\n\nBody content.",
		},
		{
			name: "empty frontmatter title falls back to heading",
			content: `---
title: ""
---
# Heading Title

Body.`,
			wantTitle: "Heading Title",
			wantBody:  "Body.",
		},
		{
			name:    "invalid frontmatter",
			content: "---\ntitle: [invalid\n---\nbody",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTitle, gotBody, err := ParseNewDocument([]byte(tt.content))

			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseNewDocument() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("ParseNewDocument() unexpected error: %v", err)
				return
			}

			if gotTitle != tt.wantTitle {
				t.Errorf("ParseNewDocument() title = %q, want %q", gotTitle, tt.wantTitle)
			}

			if gotBody != tt.wantBody {
				t.Errorf("ParseNewDocument() body = %q, want %q", gotBody, tt.wantBody)
			}
		})
	}
}

func TestDocumentRoundtrip(t *testing.T) {
	t.Parallel()
	baseTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	original := &api.Document{
		ID:        "doc-123",
		Title:     "Test Document",
		Content:   "# Introduction\n\nThis is the content.\n\n## Section 2\n\nMore content here.",
		SlugID:    "test-document",
		URL:       "https://linear.app/docs/test",
		Icon:      "📝",
		Color:     "#3B82F6",
		CreatedAt: baseTime,
		UpdatedAt: baseTime,
		Creator:   &api.User{ID: "user-1", Name: "Alice", Email: "alice@example.com"},
	}

	// Convert to markdown
	md, err := DocumentToMarkdown(original)
	if err != nil {
		t.Fatalf("DocumentToMarkdown() error: %v", err)
	}

	// Parse back
	update, err := MarkdownToDocumentUpdate(md, original)
	if err != nil {
		t.Fatalf("MarkdownToDocumentUpdate() error: %v", err)
	}

	// Should have no changes
	if len(update) != 0 {
		t.Errorf("Roundtrip produced unexpected changes: %v", update)
	}
}
