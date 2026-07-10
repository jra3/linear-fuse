package fs

import (
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
