package marshal

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		wantFrontmatter map[string]any
		wantBody       string
		wantErr        bool
	}{
		{
			name:           "empty content",
			content:        "",
			wantFrontmatter: map[string]any{},
			wantBody:       "",
		},
		{
			name:           "body only - no frontmatter",
			content:        "Just a regular markdown document.\n\nWith multiple paragraphs.",
			wantFrontmatter: map[string]any{},
			wantBody:       "Just a regular markdown document.\n\nWith multiple paragraphs.",
		},
		{
			name:    "valid frontmatter with body",
			content: "---\ntitle: My Title\nstatus: Done\n---\nBody content here.",
			wantFrontmatter: map[string]any{
				"title":  "My Title",
				"status": "Done",
			},
			wantBody: "Body content here.",
		},
		{
			name:    "frontmatter with numeric values",
			content: "---\npriority: 2\nestimate: 3.5\n---\nBody",
			wantFrontmatter: map[string]any{
				"priority": 2,
				"estimate": 3.5,
			},
			wantBody: "Body",
		},
		{
			name:    "frontmatter with array",
			content: "---\nlabels:\n  - bug\n  - frontend\n---\nDescription",
			wantFrontmatter: map[string]any{
				"labels": []any{"bug", "frontend"},
			},
			wantBody: "Description",
		},
		{
			name:           "empty frontmatter",
			content:        "---\n---\nBody after empty frontmatter",
			wantFrontmatter: map[string]any{},
			wantBody:       "Body after empty frontmatter",
		},
		{
			name:    "unclosed frontmatter",
			content: "---\ntitle: Test\nNo closing delimiter",
			wantErr: true,
		},
		{
			name:    "invalid YAML in frontmatter",
			content: "---\ntitle: [invalid yaml\n---\nBody",
			wantErr: true,
		},
		{
			name:    "frontmatter with multiline body",
			content: "---\ntitle: Test\n---\nLine 1\n\nLine 2\n\nLine 3",
			wantFrontmatter: map[string]any{
				"title": "Test",
			},
			wantBody: "Line 1\n\nLine 2\n\nLine 3",
		},
		{
			name:    "frontmatter with special characters in values",
			content: "---\ntitle: \"Fix: Bug #123\"\nurl: \"https://example.com/path?a=1&b=2\"\n---\nBody",
			wantFrontmatter: map[string]any{
				"title": "Fix: Bug #123",
				"url":   "https://example.com/path?a=1&b=2",
			},
			wantBody: "Body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := Parse([]byte(tt.content))

			if tt.wantErr {
				if err == nil {
					t.Errorf("Parse() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Parse() unexpected error: %v", err)
				return
			}

			// Compare frontmatter
			if len(doc.Frontmatter) != len(tt.wantFrontmatter) {
				t.Errorf("Parse() frontmatter len = %d, want %d", len(doc.Frontmatter), len(tt.wantFrontmatter))
			}

			for k, want := range tt.wantFrontmatter {
				got, ok := doc.Frontmatter[k]
				if !ok {
					t.Errorf("Parse() missing key %q in frontmatter", k)
					continue
				}
				// Handle slice comparison
				if wantSlice, ok := want.([]any); ok {
					gotSlice, ok := got.([]any)
					if !ok {
						t.Errorf("Parse() frontmatter[%q] type = %T, want []any", k, got)
						continue
					}
					if len(gotSlice) != len(wantSlice) {
						t.Errorf("Parse() frontmatter[%q] len = %d, want %d", k, len(gotSlice), len(wantSlice))
						continue
					}
					for i, v := range wantSlice {
						if gotSlice[i] != v {
							t.Errorf("Parse() frontmatter[%q][%d] = %v, want %v", k, i, gotSlice[i], v)
						}
					}
				} else if got != want {
					t.Errorf("Parse() frontmatter[%q] = %v, want %v", k, got, want)
				}
			}

			if doc.Body != tt.wantBody {
				t.Errorf("Parse() body = %q, want %q", doc.Body, tt.wantBody)
			}
		})
	}
}

func TestRender(t *testing.T) {
	tests := []struct {
		name        string
		doc         *Document
		wantContain []string
		wantErr     bool
	}{
		{
			name: "empty document",
			doc: &Document{
				Frontmatter: map[string]any{},
				Body:        "",
			},
			wantContain: []string{},
		},
		{
			name: "body only",
			doc: &Document{
				Frontmatter: map[string]any{},
				Body:        "Just body content",
			},
			wantContain: []string{"Just body content"},
		},
		{
			name: "frontmatter and body",
			doc: &Document{
				Frontmatter: map[string]any{
					"title":  "Test Title",
					"status": "In Progress",
				},
				Body: "Description here",
			},
			wantContain: []string{"---", "title: Test Title", "status: In Progress", "---", "Description here"},
		},
		{
			name: "frontmatter with array",
			doc: &Document{
				Frontmatter: map[string]any{
					"labels": []string{"bug", "backend"},
				},
				Body: "Body",
			},
			wantContain: []string{"---", "labels:", "- bug", "- backend", "---"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.doc)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Render() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Render() unexpected error: %v", err)
				return
			}

			result := string(got)
			for _, want := range tt.wantContain {
				if !strings.Contains(result, want) {
					t.Errorf("Render() result missing %q\nGot:\n%s", want, result)
				}
			}
		})
	}
}

func TestParseRenderRoundtrip(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "simple document",
			content: "---\ntitle: Test\nstatus: Done\n---\nBody content",
		},
		{
			name:    "document with multiline body",
			content: "---\ntitle: Test\n---\nLine 1\n\nLine 2\n\nLine 3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse
			doc, err := Parse([]byte(tt.content))
			if err != nil {
				t.Fatalf("Parse() error: %v", err)
			}

			// Render
			rendered, err := Render(doc)
			if err != nil {
				t.Fatalf("Render() error: %v", err)
			}

			// Parse again
			doc2, err := Parse(rendered)
			if err != nil {
				t.Fatalf("Parse() after render error: %v", err)
			}

			// Check frontmatter preserved
			if len(doc.Frontmatter) != len(doc2.Frontmatter) {
				t.Errorf("Roundtrip frontmatter len changed: %d -> %d", len(doc.Frontmatter), len(doc2.Frontmatter))
			}

			for k, v := range doc.Frontmatter {
				if doc2.Frontmatter[k] != v {
					t.Errorf("Roundtrip frontmatter[%q] changed: %v -> %v", k, v, doc2.Frontmatter[k])
				}
			}

			// Check body preserved
			if doc.Body != doc2.Body {
				t.Errorf("Roundtrip body changed: %q -> %q", doc.Body, doc2.Body)
			}
		})
	}
}
