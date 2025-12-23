package fs

import (
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestInitiativeUpdatesDirIno(t *testing.T) {
	t.Parallel()
	// Same initiative ID should produce same inode
	ino1 := initiativeUpdatesDirIno("initiative-123")
	ino2 := initiativeUpdatesDirIno("initiative-123")
	if ino1 != ino2 {
		t.Errorf("initiativeUpdatesDirIno() not stable: got %d and %d for same input", ino1, ino2)
	}

	// Different initiative IDs should produce different inodes
	ino3 := initiativeUpdatesDirIno("initiative-456")
	if ino1 == ino3 {
		t.Errorf("initiativeUpdatesDirIno() collision: got same inode %d for different initiatives", ino1)
	}
}

func TestInitiativeDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		initiative api.Initiative
		want       string
	}{
		{
			name: "simple name",
			initiative: api.Initiative{
				Name: "Q4 Goals",
				ID:   "init-123",
			},
			want: "q4-goals",
		},
		{
			name: "name with special chars",
			initiative: api.Initiative{
				Name: "Strategy: 2024 (Final)",
				ID:   "init-456",
			},
			want: "strategy-2024-final",
		},
		{
			name: "uppercase name",
			initiative: api.Initiative{
				Name: "API Modernization",
				ID:   "init-789",
			},
			want: "api-modernization",
		},
		{
			name: "name with only special chars falls back to ID",
			initiative: api.Initiative{
				Name: "!@#$%",
				ID:   "fallback-id",
			},
			want: "fallback-id",
		},
		{
			name: "empty name uses ID",
			initiative: api.Initiative{
				Name: "",
				ID:   "backup-id",
			},
			want: "backup-id",
		},
		{
			name: "name with numbers",
			initiative: api.Initiative{
				Name: "Initiative 2024",
				ID:   "init-2024",
			},
			want: "initiative-2024",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := initiativeDirName(tt.initiative)
			if got != tt.want {
				t.Errorf("initiativeDirName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInitiativeProjectDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		project api.InitiativeProject
		want    string
	}{
		{
			name: "simple name",
			project: api.InitiativeProject{
				Name: "Project Alpha",
				Slug: "project-alpha",
				ID:   "proj-123",
			},
			want: "project-alpha",
		},
		{
			name: "name with special chars",
			project: api.InitiativeProject{
				Name: "Project: Beta (v2)",
				Slug: "project-beta",
				ID:   "proj-456",
			},
			want: "project-beta-v2",
		},
		{
			name: "name sanitizes to empty uses slug",
			project: api.InitiativeProject{
				Name: "!@#",
				Slug: "fallback-slug",
				ID:   "proj-789",
			},
			want: "fallback-slug",
		},
		{
			name: "empty name and slug uses ID",
			project: api.InitiativeProject{
				Name: "",
				Slug: "",
				ID:   "fallback-id",
			},
			want: "fallback-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := initiativeProjectDirName(tt.project)
			if got != tt.want {
				t.Errorf("initiativeProjectDirName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseInitiativeUpdateContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		content    string
		wantBody   string
		wantHealth string
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
			name:       "empty content",
			content:    "",
			wantBody:   "",
			wantHealth: "onTrack",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBody, gotHealth := parseInitiativeUpdateContent([]byte(tt.content))
			if gotBody != tt.wantBody {
				t.Errorf("parseInitiativeUpdateContent() body = %q, want %q", gotBody, tt.wantBody)
			}
			if gotHealth != tt.wantHealth {
				t.Errorf("parseInitiativeUpdateContent() health = %q, want %q", gotHealth, tt.wantHealth)
			}
		})
	}
}

func TestInitiativeUpdateToMarkdown(t *testing.T) {
	t.Parallel()
	now := time.Now()
	update := &api.InitiativeUpdate{
		ID:        "update-123",
		Health:    "onTrack",
		Body:      "Initiative is progressing well.",
		CreatedAt: now,
		UpdatedAt: now,
		User: &api.User{
			Email: "user@example.com",
			Name:  "Test User",
		},
	}

	content := initiativeUpdateToMarkdown(update)
	contentStr := string(content)

	checks := []string{
		"---",
		"id: update-123",
		"health: onTrack",
		"author: user@example.com",
		"authorName: Test User",
		"Initiative is progressing well.",
	}

	for _, check := range checks {
		if !strings.Contains(contentStr, check) {
			t.Errorf("initiativeUpdateToMarkdown() missing %q in:\n%s", check, contentStr)
		}
	}
}

func TestInitiativeUpdateToMarkdown_NoUser(t *testing.T) {
	t.Parallel()
	now := time.Now()
	update := &api.InitiativeUpdate{
		ID:        "update-456",
		Health:    "atRisk",
		Body:      "Some concerns.",
		CreatedAt: now,
		UpdatedAt: now,
		User:      nil,
	}

	content := initiativeUpdateToMarkdown(update)
	contentStr := string(content)

	// Should not have author fields when user is nil
	if strings.Contains(contentStr, "author:") {
		t.Error("initiativeUpdateToMarkdown() should not include author when user is nil")
	}
	if strings.Contains(contentStr, "authorName:") {
		t.Error("initiativeUpdateToMarkdown() should not include authorName when user is nil")
	}
}

func TestInitiativeInfoNode_GenerateContent(t *testing.T) {
	t.Parallel()
	now := time.Now()
	node := &InitiativeInfoNode{
		initiative: api.Initiative{
			ID:          "init-123",
			Name:        "Q4 Goals",
			Slug:        "q4-goals",
			URL:         "https://linear.app/org/initiative/q4-goals",
			Status:      "In Progress",
			Color:       "#ff0000",
			Icon:        "target",
			Description: "Our Q4 objectives",
			CreatedAt:   now,
			UpdatedAt:   now,
			Owner: &api.User{
				ID:    "owner-123",
				Name:  "John Doe",
				Email: "john@example.com",
			},
			Projects: api.InitiativeProjects{
				Nodes: []api.InitiativeProject{
					{ID: "proj-1", Name: "Project 1", Slug: "proj-1"},
				},
			},
		},
	}

	content := node.generateContent()
	contentStr := string(content)

	checks := []string{
		"id: init-123",
		"name: \"Q4 Goals\"",
		"slug: q4-goals",
		"status: In Progress",
		"owner:",
		"john@example.com",
		"projects:",
		"# Q4 Goals",
		"Our Q4 objectives",
	}

	for _, check := range checks {
		if !strings.Contains(contentStr, check) {
			t.Errorf("generateContent() missing %q in:\n%s", check, contentStr)
		}
	}
}
