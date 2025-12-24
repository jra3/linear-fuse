package marshal

import (
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestIssueToMarkdown(t *testing.T) {
	t.Parallel()
	baseTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	updateTime := time.Date(2025, 1, 16, 14, 0, 0, 0, time.UTC)
	dueDate := "2025-02-01"
	estimate := 5.0

	tests := []struct {
		name        string
		issue       *api.Issue
		wantContain []string
		wantErr     bool
	}{
		{
			name: "full issue with all fields",
			issue: &api.Issue{
				ID:          "issue-123",
				Identifier:  "ENG-456",
				Title:       "Fix authentication bug",
				Description: "Users can't log in with SSO.",
				State:       api.State{ID: "state-1", Name: "In Progress", Type: "started"},
				Assignee:    &api.User{ID: "user-1", Name: "Alice", Email: "alice@example.com"},
				Priority:    2, // high
				Labels: api.Labels{Nodes: []api.Label{
					{ID: "label-1", Name: "bug", Color: "#FF0000"},
					{ID: "label-2", Name: "backend", Color: "#00FF00"},
				}},
				DueDate:   &dueDate,
				Estimate:  &estimate,
				CreatedAt: baseTime,
				UpdatedAt: updateTime,
				URL:       "https://linear.app/team/issue/ENG-456",
				Team:      &api.Team{ID: "team-1", Key: "ENG", Name: "Engineering"},
				Project:   &api.Project{ID: "proj-1", Name: "Q1 Launch"},
			},
			wantContain: []string{
				"id: issue-123",
				"identifier: ENG-456",
				"title: Fix authentication bug",
				"status: In Progress",
				"priority: high",
				"assignee: alice@example.com",
				"due: \"2025-02-01\"",
				"estimate: 5",
				"team: ENG",
				"project: Q1 Launch",
				"- bug",
				"- backend",
				"Users can't log in with SSO.",
			},
		},
		{
			name: "minimal issue",
			issue: &api.Issue{
				ID:          "issue-min",
				Identifier:  "ENG-1",
				Title:       "Simple task",
				Description: "",
				State:       api.State{ID: "state-1", Name: "Backlog"},
				Priority:    0, // none
				Labels:      api.Labels{Nodes: []api.Label{}},
				CreatedAt:   baseTime,
				UpdatedAt:   baseTime,
				URL:         "https://linear.app/team/issue/ENG-1",
			},
			wantContain: []string{
				"id: issue-min",
				"identifier: ENG-1",
				"title: Simple task",
				"status: Backlog",
				"priority: none",
				"# Simple task", // Auto-generated body
			},
		},
		{
			name: "issue with no assignee",
			issue: &api.Issue{
				ID:         "issue-no-assign",
				Identifier: "ENG-2",
				Title:      "Unassigned task",
				State:      api.State{ID: "state-1", Name: "Todo"},
				Priority:   3, // medium
				Labels:     api.Labels{Nodes: []api.Label{}},
				CreatedAt:  baseTime,
				UpdatedAt:  baseTime,
				URL:        "https://linear.app/team/issue/ENG-2",
			},
			wantContain: []string{
				"title: Unassigned task",
				"priority: medium",
			},
		},
		{
			name: "issue with special characters in title",
			issue: &api.Issue{
				ID:         "issue-special",
				Identifier: "ENG-3",
				Title:      "Fix: Bug #123 & handle \"quotes\"",
				State:      api.State{ID: "state-1", Name: "Todo"},
				Priority:   0,
				Labels:     api.Labels{Nodes: []api.Label{}},
				CreatedAt:  baseTime,
				UpdatedAt:  baseTime,
				URL:        "https://linear.app/team/issue/ENG-3",
			},
			wantContain: []string{
				"ENG-3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IssueToMarkdown(tt.issue)

			if tt.wantErr {
				if err == nil {
					t.Errorf("IssueToMarkdown() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("IssueToMarkdown() unexpected error: %v", err)
				return
			}

			result := string(got)
			for _, want := range tt.wantContain {
				if !strings.Contains(result, want) {
					t.Errorf("IssueToMarkdown() missing %q\nGot:\n%s", want, result)
				}
			}
		})
	}
}

func TestMarkdownToIssueUpdate(t *testing.T) {
	t.Parallel()
	baseTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	dueDate := "2025-02-01"
	estimate := 5.0

	original := &api.Issue{
		ID:          "issue-123",
		Identifier:  "ENG-456",
		Title:       "Original Title",
		Description: "Original description",
		State:       api.State{ID: "state-1", Name: "Todo", Type: "unstarted"},
		Assignee:    &api.User{ID: "user-1", Name: "Alice", Email: "alice@example.com"},
		Priority:    2, // high
		Labels: api.Labels{Nodes: []api.Label{
			{ID: "label-1", Name: "bug"},
		}},
		DueDate:   &dueDate,
		Estimate:  &estimate,
		CreatedAt: baseTime,
		UpdatedAt: baseTime,
		URL:       "https://linear.app/team/issue/ENG-456",
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
title: Original Title
status: Todo
priority: high
assignee: alice@example.com
due: "2025-02-01"
estimate: 5
labels:
  - bug
---
Original description`,
			wantUpdate: map[string]any{},
		},
		{
			name: "title changed",
			content: `---
title: New Title
status: Todo
priority: high
assignee: alice@example.com
due: "2025-02-01"
estimate: 5
labels:
  - bug
---
Original description`,
			wantUpdate: map[string]any{
				"title": "New Title",
			},
		},
		{
			name: "status changed",
			content: `---
title: Original Title
status: In Progress
priority: high
assignee: alice@example.com
due: "2025-02-01"
estimate: 5
labels:
  - bug
---
Original description`,
			wantUpdate: map[string]any{
				"stateId": "In Progress", // Will be resolved to actual ID
			},
		},
		{
			name: "priority changed",
			content: `---
title: Original Title
status: Todo
priority: urgent
assignee: alice@example.com
due: "2025-02-01"
estimate: 5
labels:
  - bug
---
Original description`,
			wantUpdate: map[string]any{
				"priority": 1,
			},
		},
		{
			name: "assignee changed",
			content: `---
title: Original Title
status: Todo
priority: high
assignee: bob@example.com
due: "2025-02-01"
estimate: 5
labels:
  - bug
---
Original description`,
			wantUpdate: map[string]any{
				"assigneeId": "bob@example.com",
			},
		},
		{
			name: "description changed",
			content: `---
title: Original Title
status: Todo
priority: high
assignee: alice@example.com
due: "2025-02-01"
estimate: 5
labels:
  - bug
---
New description with more details.`,
			wantUpdate: map[string]any{
				"description": "New description with more details.",
			},
		},
		{
			name: "due date changed",
			content: `---
title: Original Title
status: Todo
priority: high
assignee: alice@example.com
due: "2025-03-15"
estimate: 5
labels:
  - bug
---
Original description`,
			wantUpdate: map[string]any{
				"dueDate": "2025-03-15",
			},
		},
		{
			name: "due date removed",
			content: `---
title: Original Title
status: Todo
priority: high
assignee: alice@example.com
estimate: 5
labels:
  - bug
---
Original description`,
			wantUpdate: map[string]any{
				"dueDate": nil,
			},
		},
		{
			name: "estimate changed",
			content: `---
title: Original Title
status: Todo
priority: high
assignee: alice@example.com
due: "2025-02-01"
estimate: 8
labels:
  - bug
---
Original description`,
			wantUpdate: map[string]any{
				"estimate": 8,
			},
		},
		{
			name: "estimate removed",
			content: `---
title: Original Title
status: Todo
priority: high
assignee: alice@example.com
due: "2025-02-01"
labels:
  - bug
---
Original description`,
			wantUpdate: map[string]any{
				"estimate": nil,
			},
		},
		{
			name: "labels changed",
			content: `---
title: Original Title
status: Todo
priority: high
assignee: alice@example.com
due: "2025-02-01"
estimate: 5
labels:
  - bug
  - frontend
---
Original description`,
			wantUpdate: map[string]any{
				"labelIds": []string{"bug", "frontend"},
			},
		},
		{
			name: "labels removed",
			content: `---
title: Original Title
status: Todo
priority: high
assignee: alice@example.com
due: "2025-02-01"
estimate: 5
---
Original description`,
			wantUpdate: map[string]any{
				"labelIds": []string{},
			},
		},
		{
			name: "multiple changes",
			content: `---
title: New Title
status: Done
priority: low
assignee: bob@example.com
---
New description`,
			wantUpdate: map[string]any{
				"title":       "New Title",
				"stateId":     "Done",
				"priority":    4,
				"assigneeId":  "bob@example.com",
				"description": "New description",
				"dueDate":     nil,
				"estimate":    nil,
				"labelIds":    []string{},
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
			got, err := MarkdownToIssueUpdate([]byte(tt.content), original)

			if tt.wantErr {
				if err == nil {
					t.Errorf("MarkdownToIssueUpdate() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("MarkdownToIssueUpdate() unexpected error: %v", err)
				return
			}

			// Check expected fields
			if len(got) != len(tt.wantUpdate) {
				t.Errorf("MarkdownToIssueUpdate() returned %d fields, want %d\nGot: %v\nWant: %v",
					len(got), len(tt.wantUpdate), got, tt.wantUpdate)
			}

			for k, want := range tt.wantUpdate {
				gotVal, ok := got[k]
				if !ok {
					t.Errorf("MarkdownToIssueUpdate() missing key %q", k)
					continue
				}

				// Handle slice comparison
				if wantSlice, ok := want.([]string); ok {
					gotSlice, ok := gotVal.([]string)
					if !ok {
						t.Errorf("MarkdownToIssueUpdate() field %q type = %T, want []string", k, gotVal)
						continue
					}
					if len(gotSlice) != len(wantSlice) {
						t.Errorf("MarkdownToIssueUpdate() field %q len = %d, want %d", k, len(gotSlice), len(wantSlice))
						continue
					}
					for i, v := range wantSlice {
						if gotSlice[i] != v {
							t.Errorf("MarkdownToIssueUpdate() field %q[%d] = %q, want %q", k, i, gotSlice[i], v)
						}
					}
				} else if gotVal != want {
					t.Errorf("MarkdownToIssueUpdate() field %q = %v, want %v", k, gotVal, want)
				}
			}
		})
	}
}

func TestMarkdownToIssueUpdateNoAssignee(t *testing.T) {
	t.Parallel()
	baseTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	// Original issue with no assignee
	original := &api.Issue{
		ID:          "issue-123",
		Identifier:  "ENG-456",
		Title:       "Original Title",
		Description: "Original description",
		State:       api.State{ID: "state-1", Name: "Todo"},
		Assignee:    nil,
		Priority:    2,
		Labels:      api.Labels{Nodes: []api.Label{}},
		CreatedAt:   baseTime,
		UpdatedAt:   baseTime,
	}

	// Add assignee
	content := `---
title: Original Title
status: Todo
priority: high
assignee: alice@example.com
---
Original description`

	got, err := MarkdownToIssueUpdate([]byte(content), original)
	if err != nil {
		t.Fatalf("MarkdownToIssueUpdate() error: %v", err)
	}

	if got["assigneeId"] != "alice@example.com" {
		t.Errorf("Expected assigneeId to be alice@example.com, got %v", got["assigneeId"])
	}
}

func TestIssueToMarkdownWithAttachments(t *testing.T) {
	t.Parallel()
	baseTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name        string
		issue       *api.Issue
		attachments []api.Attachment
		wantContain []string
		wantMissing []string
	}{
		{
			name: "issue with github PR attachment",
			issue: &api.Issue{
				ID:          "issue-123",
				Identifier:  "ENG-456",
				Title:       "Fix bug",
				Description: "Description here",
				State:       api.State{ID: "state-1", Name: "In Progress"},
				Priority:    2,
				Labels:      api.Labels{Nodes: []api.Label{}},
				CreatedAt:   baseTime,
				UpdatedAt:   baseTime,
				URL:         "https://linear.app/team/issue/ENG-456",
			},
			attachments: []api.Attachment{
				{
					ID:         "attach-1",
					Title:      "feat: Fix auth flow",
					URL:        "https://github.com/org/repo/pull/456",
					SourceType: "github",
				},
			},
			wantContain: []string{
				"links:",
				"type: github",
				"feat: Fix auth flow", // YAML may use single or double quotes
				"url: https://github.com/org/repo/pull/456",
			},
		},
		{
			name: "issue with multiple attachments",
			issue: &api.Issue{
				ID:          "issue-multi",
				Identifier:  "ENG-789",
				Title:       "Integration work",
				Description: "Connecting services",
				State:       api.State{ID: "state-1", Name: "Todo"},
				Priority:    3,
				Labels:      api.Labels{Nodes: []api.Label{}},
				CreatedAt:   baseTime,
				UpdatedAt:   baseTime,
				URL:         "https://linear.app/team/issue/ENG-789",
			},
			attachments: []api.Attachment{
				{
					ID:         "attach-1",
					Title:      "PR: Add API endpoint",
					URL:        "https://github.com/org/repo/pull/100",
					SourceType: "github",
				},
				{
					ID:         "attach-2",
					Title:      "Discussion thread",
					URL:        "https://slack.com/archives/C123/p456",
					SourceType: "slack",
				},
			},
			wantContain: []string{
				"links:",
				"type: github",
				"type: slack",
				"url: https://github.com/org/repo/pull/100",
				"url: https://slack.com/archives/C123/p456",
			},
		},
		{
			name: "issue without attachments - no links field",
			issue: &api.Issue{
				ID:          "issue-no-attach",
				Identifier:  "ENG-999",
				Title:       "Simple task",
				Description: "No attachments",
				State:       api.State{ID: "state-1", Name: "Backlog"},
				Priority:    0,
				Labels:      api.Labels{Nodes: []api.Label{}},
				CreatedAt:   baseTime,
				UpdatedAt:   baseTime,
				URL:         "https://linear.app/team/issue/ENG-999",
			},
			attachments: []api.Attachment{}, // Empty attachments
			wantContain: []string{
				"identifier: ENG-999",
			},
			wantMissing: []string{
				"links:", // Should NOT have links field when no attachments
			},
		},
		{
			name: "issue with nil attachments - no links field",
			issue: &api.Issue{
				ID:          "issue-nil-attach",
				Identifier:  "ENG-888",
				Title:       "Another task",
				Description: "Nil attachments",
				State:       api.State{ID: "state-1", Name: "Backlog"},
				Priority:    0,
				Labels:      api.Labels{Nodes: []api.Label{}},
				CreatedAt:   baseTime,
				UpdatedAt:   baseTime,
				URL:         "https://linear.app/team/issue/ENG-888",
			},
			attachments: nil, // Nil attachments
			wantContain: []string{
				"identifier: ENG-888",
			},
			wantMissing: []string{
				"links:",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IssueToMarkdown(tt.issue, tt.attachments...)
			if err != nil {
				t.Fatalf("IssueToMarkdown() error: %v", err)
			}

			result := string(got)

			// Check expected content is present
			for _, want := range tt.wantContain {
				if !strings.Contains(result, want) {
					t.Errorf("IssueToMarkdown() missing %q\nGot:\n%s", want, result)
				}
			}

			// Check unwanted content is absent
			for _, notWant := range tt.wantMissing {
				if strings.Contains(result, notWant) {
					t.Errorf("IssueToMarkdown() should not contain %q\nGot:\n%s", notWant, result)
				}
			}
		})
	}
}

func TestStringSlicesEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{
			name: "equal slices same order",
			a:    []string{"a", "b", "c"},
			b:    []string{"a", "b", "c"},
			want: true,
		},
		{
			name: "equal slices different order",
			a:    []string{"c", "a", "b"},
			b:    []string{"a", "b", "c"},
			want: true,
		},
		{
			name: "different lengths",
			a:    []string{"a", "b"},
			b:    []string{"a", "b", "c"},
			want: false,
		},
		{
			name: "different elements",
			a:    []string{"a", "b", "c"},
			b:    []string{"a", "b", "d"},
			want: false,
		},
		{
			name: "empty slices",
			a:    []string{},
			b:    []string{},
			want: true,
		},
		{
			name: "nil slices",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "one nil one empty",
			a:    nil,
			b:    []string{},
			want: true,
		},
		{
			name: "duplicates in both",
			a:    []string{"a", "a", "b"},
			b:    []string{"a", "b", "a"},
			want: true,
		},
		{
			name: "different duplicate counts",
			a:    []string{"a", "a", "b"},
			b:    []string{"a", "b", "b"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stringSlicesEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("stringSlicesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
