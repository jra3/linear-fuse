package fs

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestProjectDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		project api.Project
		want    string
	}{
		{
			name: "simple name",
			project: api.Project{
				Name: "My Project",
				Slug: "my-project",
			},
			want: "my-project",
		},
		{
			name: "name with special chars",
			project: api.Project{
				Name: "Project: Alpha (v2)",
				Slug: "project-alpha",
			},
			want: "project-alpha-v2",
		},
		{
			name: "uppercase name",
			project: api.Project{
				Name: "API Gateway",
				Slug: "api-gateway",
			},
			want: "api-gateway",
		},
		{
			name: "name with only special chars falls back to slug",
			project: api.Project{
				Name: "!@#$%",
				Slug: "fallback-slug",
			},
			want: "fallback-slug",
		},
		{
			name: "empty name uses slug",
			project: api.Project{
				Name: "",
				Slug: "backup-slug",
			},
			want: "backup-slug",
		},
		{
			name: "name with numbers",
			project: api.Project{
				Name: "Project 2024",
				Slug: "project-2024",
			},
			want: "project-2024",
		},
		{
			name: "name with multiple spaces",
			project: api.Project{
				Name: "The   Big   Project",
				Slug: "tbp",
			},
			want: "the---big---project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectDirName(tt.project)
			if got != tt.want {
				t.Errorf("projectDirName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseUpdateContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		content    string
		wantBody   string
		wantHealth string
		wantField  string // non-empty: expect a *FieldError on this field
	}{
		{
			name:       "plain text",
			content:    "This is a status update.",
			wantBody:   "This is a status update.",
			wantHealth: "onTrack",
		},
		{
			name: "with frontmatter onTrack",
			content: `---
health: onTrack
---
All systems go!`,
			wantBody:   "All systems go!",
			wantHealth: "onTrack",
		},
		{
			name: "with frontmatter atRisk",
			content: `---
health: atRisk
---
Some delays expected.`,
			wantBody:   "Some delays expected.",
			wantHealth: "atRisk",
		},
		{
			name: "with frontmatter offTrack",
			content: `---
health: offTrack
---
Blocked by dependencies.`,
			wantBody:   "Blocked by dependencies.",
			wantHealth: "offTrack",
		},
		{
			name: "health with spaces (on track)",
			content: `---
health: "on track"
---
Update body`,
			wantBody:   "Update body",
			wantHealth: "onTrack",
		},
		{
			name: "health with hyphens (at-risk)",
			content: `---
health: at-risk
---
Body text`,
			wantBody:   "Body text",
			wantHealth: "atRisk",
		},
		{
			name: "health with quotes",
			content: `---
health: 'off-track'
---
Critical issues`,
			wantBody:   "Critical issues",
			wantHealth: "offTrack",
		},
		{
			name:       "empty content",
			content:    "",
			wantBody:   "",
			wantHealth: "onTrack",
		},
		{
			name:       "whitespace only",
			content:    "   \n\n   ",
			wantBody:   "",
			wantHealth: "onTrack",
		},
		{
			name: "frontmatter without closing delimiter",
			content: `---
health: atRisk
No closing delimiter`,
			wantBody:   "---\nhealth: atRisk\nNo closing delimiter",
			wantHealth: "onTrack",
		},
		{
			name: "multiline body",
			content: `---
health: onTrack
---
Line 1
Line 2
Line 3`,
			wantBody:   "Line 1\nLine 2\nLine 3",
			wantHealth: "onTrack",
		},
		{
			name: "unknown health rejected, not coerced to onTrack",
			content: `---
health: critical
---
Everything is on fire`,
			wantField: "health",
		},
		{
			name: "frontmatter with empty body rejected",
			content: `---
health: atRisk
---
`,
			wantField: "body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBody, gotHealth, err := parseUpdateContent([]byte(tt.content))
			if tt.wantField != "" {
				var ferr *FieldError
				if !errors.As(err, &ferr) {
					t.Fatalf("parseUpdateContent() err = %v, want *FieldError on %q", err, tt.wantField)
				}
				if ferr.Field != tt.wantField {
					t.Errorf("parseUpdateContent() FieldError.Field = %q, want %q", ferr.Field, tt.wantField)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUpdateContent() unexpected error: %v", err)
			}
			if gotBody != tt.wantBody {
				t.Errorf("parseUpdateContent() body = %q, want %q", gotBody, tt.wantBody)
			}
			if gotHealth != tt.wantHealth {
				t.Errorf("parseUpdateContent() health = %q, want %q", gotHealth, tt.wantHealth)
			}
		})
	}
}

func TestUpdateToMarkdown(t *testing.T) {
	t.Parallel()
	now := time.Now()
	update := &api.ProjectUpdate{
		ID:        "update-123",
		Health:    "onTrack",
		Body:      "Project is progressing well.",
		CreatedAt: now,
		UpdatedAt: now,
		User: &api.User{
			Email: "user@example.com",
			Name:  "Test User",
		},
	}

	content := updateMarkdown(update.ID, update.Health, update.CreatedAt, update.UpdatedAt, update.User, update.Body)
	contentStr := string(content)

	checks := []string{
		"---",
		"id: update-123",
		"health: onTrack",
		"author: user@example.com",
		"authorName: Test User",
		"Project is progressing well.",
	}

	for _, check := range checks {
		if !strings.Contains(contentStr, check) {
			t.Errorf("updateMarkdown() missing %q in:\n%s", check, contentStr)
		}
	}
}

func TestUpdateToMarkdown_NoUser(t *testing.T) {
	t.Parallel()
	now := time.Now()
	update := &api.ProjectUpdate{
		ID:        "update-456",
		Health:    "atRisk",
		Body:      "Some concerns.",
		CreatedAt: now,
		UpdatedAt: now,
		User:      nil,
	}

	content := updateMarkdown(update.ID, update.Health, update.CreatedAt, update.UpdatedAt, update.User, update.Body)
	contentStr := string(content)

	// Should not have author fields when user is nil
	if strings.Contains(contentStr, "author:") {
		t.Error("updateMarkdown() should not include author when user is nil")
	}
	if strings.Contains(contentStr, "authorName:") {
		t.Error("updateMarkdown() should not include authorName when user is nil")
	}
}
