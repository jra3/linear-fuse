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
		name           string
		issue          *api.Issue
		wantContain    []string
		wantNotContain []string
		wantErr        bool
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
				"team: ENG", // editable since #429 — the move surface
				"title: Fix authentication bug",
				"status: In Progress",
				"priority: high",
				"assignee: alice@example.com",
				"due: \"2025-02-01\"",
				"estimate: 5",
				"project: Q1 Launch",
				"- bug",
				"- backend",
				"Users can't log in with SSO.",
			},
			wantNotContain: []string{
				"id: issue-123", // server field -> issue.meta
				"identifier: ENG-456",
				"url:",
				"updated:",
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
				"title: Simple task",
				"status: Backlog",
				"priority: none",
				"# Simple task", // Auto-generated body
			},
			wantNotContain: []string{"id:", "identifier:", "url:"},
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
				"Bug #123", // title text survives into the auto-generated body
			},
			wantNotContain: []string{"identifier: ENG-3", "url:"},
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
			for _, notWant := range tt.wantNotContain {
				if strings.Contains(result, notWant) {
					t.Errorf("IssueToMarkdown() should not contain %q (belongs in issue.meta)\nGot:\n%s", notWant, result)
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

// TestIssueMetaToMarkdown covers the read-only issue.meta surface: identity
// fields plus external-link attachments (which moved out of issue.md in #150).
func TestIssueMetaToMarkdown(t *testing.T) {
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
			got, err := IssueMetaToMarkdown(tt.issue, tt.attachments...)
			if err != nil {
				t.Fatalf("IssueMetaToMarkdown() error: %v", err)
			}

			result := string(got)

			// Check expected content is present
			for _, want := range tt.wantContain {
				if !strings.Contains(result, want) {
					t.Errorf("IssueMetaToMarkdown() missing %q\nGot:\n%s", want, result)
				}
			}

			// Check unwanted content is absent
			for _, notWant := range tt.wantMissing {
				if strings.Contains(result, notWant) {
					t.Errorf("IssueMetaToMarkdown() should not contain %q\nGot:\n%s", notWant, result)
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

func TestMarkdownToIssueCreate(t *testing.T) {
	t.Parallel()
	content := []byte("---\n" +
		"title: New Thing\n" +
		"status: In Progress\n" +
		"priority: high\n" +
		"assignee: alice@example.com\n" +
		"labels: [Bug, Backend]\n" +
		"project: Q1 Launch\n" +
		"parent: ENG-1\n" +
		"estimate: 3\n" +
		"due: \"2026-02-01\"\n" +
		"id: should-be-ignored\n" + // read-only key ignored tolerantly
		"---\n" +
		"Body text.\n")

	got, err := MarkdownToIssueCreate(content)
	if err != nil {
		t.Fatalf("MarkdownToIssueCreate error: %v", err)
	}
	// Relational fields carry names for the resolver; scalars are typed.
	checks := map[string]any{
		"title":      "New Thing",
		"stateId":    "In Progress",
		"priority":   2, // high
		"assigneeId": "alice@example.com",
		"projectId":  "Q1 Launch",
		"parentId":   "ENG-1",
		"dueDate":    "2026-02-01",
	}
	for k, want := range checks {
		if got[k] != want {
			t.Errorf("field %q = %v, want %v", k, got[k], want)
		}
	}
	if desc, _ := got["description"].(string); strings.TrimSpace(desc) != "Body text." {
		t.Errorf("description = %q, want %q", desc, "Body text.")
	}
	if labels, ok := got["labelIds"].([]string); !ok || len(labels) != 2 {
		t.Errorf("labelIds = %v, want [Bug Backend]", got["labelIds"])
	}
	if _, ok := got["id"]; ok {
		t.Error("read-only key 'id' should be ignored, not passed to create input")
	}
}

func TestMarkdownToIssueCreateInvalidPriority(t *testing.T) {
	t.Parallel()
	_, err := MarkdownToIssueCreate([]byte("---\ntitle: X\npriority: critical\n---\nbody\n"))
	if err == nil {
		t.Fatal("expected error for invalid priority")
	}
	if !strings.HasPrefix(err.Error(), "priority:") {
		t.Errorf("error should be priority-prefixed for .error normalization, got: %v", err)
	}
}

func TestMarkdownToIssueCreateCoercesScalars(t *testing.T) {
	t.Parallel()
	// Unquoted due parses as time.Time; priority/estimate as numbers; title as int.
	// None of these must be silently dropped (the #148 failure mode).
	content := []byte("---\ntitle: 12345\ndue: 2026-02-01\npriority: 2\nestimate: 3\n---\nbody\n")
	got, err := MarkdownToIssueCreate(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["title"] != "12345" {
		t.Errorf("title = %v, want coerced \"12345\"", got["title"])
	}
	if got["dueDate"] != "2026-02-01" {
		t.Errorf("dueDate = %v, want \"2026-02-01\" (unquoted date coerced, not dropped)", got["dueDate"])
	}
	if got["priority"] != 2 {
		t.Errorf("priority = %v, want numeric 2 (not dropped)", got["priority"])
	}
	if got["estimate"] != 3 {
		t.Errorf("estimate = %v (type %T), want int 3", got["estimate"], got["estimate"])
	}
}

// TestMarkdownToIssueCreateCoercesRelationalAndLabels guards the review finding
// that the "coerce, don't drop" fix reached only title/priority/due/estimate,
// leaving labels and the name-bearing scalar fields to bare `.(string)` drops.
func TestMarkdownToIssueCreateCoercesRelationalAndLabels(t *testing.T) {
	t.Parallel()

	// A bare (non-list) label and a numeric-looking label must both survive.
	got, err := MarkdownToIssueCreate([]byte("---\ntitle: X\nlabels: Bug\n---\nbody\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if labels, ok := got["labelIds"].([]string); !ok || len(labels) != 1 || labels[0] != "Bug" {
		t.Errorf("bare `labels: Bug` = %v, want [Bug] (dropped instead of coerced)", got["labelIds"])
	}
	got, err = MarkdownToIssueCreate([]byte("---\ntitle: X\nlabels: [Bug, 2026]\n---\nbody\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if labels, ok := got["labelIds"].([]string); !ok || len(labels) != 2 || labels[1] != "2026" {
		t.Errorf("`labels: [Bug, 2026]` = %v, want [Bug 2026] (numeric label dropped)", got["labelIds"])
	}

	// A numeric cycle/status name must not be silently dropped.
	got, err = MarkdownToIssueCreate([]byte("---\ntitle: X\nstatus: 7\ncycle: 42\n---\nbody\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["stateId"] != "7" {
		t.Errorf("numeric status = %v, want coerced \"7\" (not dropped)", got["stateId"])
	}
	if got["cycleId"] != "42" {
		t.Errorf("numeric cycle = %v, want coerced \"42\" (not dropped)", got["cycleId"])
	}
}

// TestMarkdownToIssueCreateNumericPriorityRange guards that an out-of-range
// numeric priority is rejected with a priority-prefixed EINVAL (not passed
// through to the API as an opaque EIO).
func TestMarkdownToIssueCreateNumericPriorityRange(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"99", "-1", "2.5"} {
		_, err := MarkdownToIssueCreate([]byte("---\ntitle: X\npriority: " + bad + "\n---\nbody\n"))
		if err == nil {
			t.Errorf("priority: %s should be rejected as out-of-range", bad)
			continue
		}
		if !strings.HasPrefix(err.Error(), "priority:") {
			t.Errorf("priority: %s error should be priority-prefixed, got: %v", bad, err)
		}
	}
	// In-range integers still pass.
	if _, err := MarkdownToIssueCreate([]byte("---\ntitle: X\npriority: 4\n---\nbody\n")); err != nil {
		t.Errorf("priority: 4 should be valid, got: %v", err)
	}
}

// TestMarkdownToIssueUpdateCoercesScalars guards that the edit path (not just
// create) coerces wrong-typed scalars instead of silently ignoring the edit —
// the #148 silent no-op on the highest-traffic surface.
func TestMarkdownToIssueUpdateCoercesScalars(t *testing.T) {
	t.Parallel()
	original := &api.Issue{Title: "Old", Description: "body"}

	// Unquoted due (time.Time), numeric title, and numeric priority must apply.
	content := []byte("---\ntitle: 2026\npriority: 3\ndue: 2026-03-15\n---\nbody\n")
	update, err := MarkdownToIssueUpdate(content, original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if update["title"] != "2026" {
		t.Errorf("title = %v, want coerced \"2026\" (edit silently dropped)", update["title"])
	}
	if update["dueDate"] != "2026-03-15" {
		t.Errorf("dueDate = %v, want \"2026-03-15\" (unquoted date dropped on edit)", update["dueDate"])
	}
	if update["priority"] != 3 {
		t.Errorf("priority = %v, want 3 (numeric priority dropped on edit)", update["priority"])
	}
}

// TestMarkdownToIssueUpdateQuotedEstimateDoesNotZero guards the worst update-path
// finding: a quoted `estimate: "3"` matched neither int nor float and zeroed the
// estimate on Linear. It must now coerce (or, if unparseable, leave untouched).
func TestMarkdownToIssueUpdateQuotedEstimateDoesNotZero(t *testing.T) {
	t.Parallel()
	three := float64(3)
	original := &api.Issue{Title: "X", Estimate: &three, Description: "body"}

	// Quoted numeric string coerces to the same value → no update emitted.
	update, err := MarkdownToIssueUpdate([]byte("---\ntitle: X\nestimate: \"3\"\n---\nbody\n"), original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := update["estimate"]; ok {
		t.Errorf("estimate = %v, want no change (quoted \"3\" == 3, must not zero)", v)
	}

	// An unparseable estimate leaves the field untouched rather than zeroing it.
	update, err = MarkdownToIssueUpdate([]byte("---\ntitle: X\nestimate: abc\n---\nbody\n"), original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := update["estimate"]; ok {
		t.Errorf("estimate = %v, want no change for unparseable value (must not zero)", v)
	}
}

// TestMarkdownToIssueUpdateEmptyDescriptionNoop guards that a byte-identical
// rewrite of an empty-description issue does not push the synthesized
// `# <Title>` placeholder back as a real description (the byte-stable-write
// contract).
func TestMarkdownToIssueUpdateEmptyDescriptionNoop(t *testing.T) {
	t.Parallel()
	original := &api.Issue{Title: "Fix thing", Description: ""}

	// IssueToMarkdown renders "# Fix thing\n" as the body for an empty description.
	rendered, err := IssueToMarkdown(original)
	if err != nil {
		t.Fatalf("IssueToMarkdown error: %v", err)
	}
	update, err := MarkdownToIssueUpdate(rendered, original)
	if err != nil {
		t.Fatalf("MarkdownToIssueUpdate error: %v", err)
	}
	if v, ok := update["description"]; ok {
		t.Errorf("no-op rewrite emitted description=%q, want no update (placeholder must not persist)", v)
	}
}

// TestIssueRoundtrip pins parse(render(issue)) as a fixpoint across every
// editable field at once: rendering issue.md and diffing it back against the
// same issue must report zero changes. A field rendered and parsed
// asymmetrically surfaces here as a phantom update — the failure that
// read-your-writes verification would otherwise misreport as a server-side
// divergence (or that would silently rewrite the field on every no-op save).
func TestIssueRoundtrip(t *testing.T) {
	t.Parallel()
	dueDate := "2025-02-01"
	estimate := 5.0

	tests := []struct {
		name  string
		issue *api.Issue
	}{
		{
			name: "fully populated",
			issue: &api.Issue{
				ID:          "issue-123",
				Identifier:  "ENG-456",
				Title:       "Fix authentication bug",
				Description: "Users can't log in with SSO.\n\n## Repro\n\n1. Open the login page.",
				State:       api.State{ID: "state-1", Name: "In Progress", Type: "started"},
				Assignee:    &api.User{ID: "user-1", Name: "Alice", Email: "alice@example.com"},
				Priority:    2, // high
				Labels: api.Labels{Nodes: []api.Label{
					{ID: "label-1", Name: "bug", Color: "#FF0000"},
					{ID: "label-2", Name: "backend", Color: "#00FF00"},
				}},
				DueDate:          &dueDate,
				Estimate:         &estimate,
				Project:          &api.Project{ID: "proj-1", Name: "Q1 Launch"},
				ProjectMilestone: &api.ProjectMilestone{ID: "milestone-1", Name: "Phase 1"},
				Parent:           &api.ParentIssue{ID: "issue-100", Identifier: "ENG-100"},
				Cycle:            &api.IssueCycle{ID: "cycle-1", Name: "Sprint 42", Number: 42},
			},
		},
		{
			name: "minimal (empty description placeholder)",
			issue: &api.Issue{
				ID:         "issue-min",
				Identifier: "ENG-1",
				Title:      "Simple task",
				State:      api.State{ID: "state-1", Name: "Backlog"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := IssueToMarkdown(tt.issue)
			if err != nil {
				t.Fatalf("IssueToMarkdown() error: %v", err)
			}

			update, err := MarkdownToIssueUpdate(md, tt.issue)
			if err != nil {
				t.Fatalf("MarkdownToIssueUpdate() error: %v", err)
			}

			if len(update) != 0 {
				t.Errorf("Roundtrip produced unexpected changes: %v (rendered:\n%s)", update, md)
			}
		})
	}
}

// TestIssueScalarFieldsWiring pins the declarative field table's contract (#227):
// each editable scalar field maps its frontmatter key to the exact API key the
// render/update/create loops emit, with the right removal semantics. As a
// change-detector over the single source of field truth, it catches a typo'd
// apiKey, a dropped field, or a wrong removable flag — the drift class the table
// exists to eliminate.
func TestIssueScalarFieldsWiring(t *testing.T) {
	t.Parallel()
	wantAPIKey := map[string]string{
		"title":     "title",
		"team":      "teamId",
		"status":    "stateId",
		"assignee":  "assigneeId",
		"due":       "dueDate",
		"parent":    "parentId",
		"project":   "projectId",
		"milestone": "projectMilestoneId",
		"cycle":     "cycleId",
	}
	seen := map[string]bool{}
	for _, f := range issueScalarFields {
		if seen[f.yamlKey] {
			t.Errorf("duplicate yamlKey %q in issueScalarFields", f.yamlKey)
		}
		seen[f.yamlKey] = true

		want, ok := wantAPIKey[f.yamlKey]
		if !ok {
			t.Errorf("unexpected field %q in table (update wantAPIKey if intentional)", f.yamlKey)
			continue
		}
		if f.apiKey != want {
			t.Errorf("field %q apiKey = %q, want %q", f.yamlKey, f.apiKey, want)
		}
		// title, team, and status have no removal semantics (an issue always has
		// each of the three); everything else clears on absence.
		wantRemovable := f.yamlKey != "title" && f.yamlKey != "team" && f.yamlKey != "status"
		if f.removable != wantRemovable {
			t.Errorf("field %q removable = %v, want %v", f.yamlKey, f.removable, wantRemovable)
		}
	}
	for k := range wantAPIKey {
		if !seen[k] {
			t.Errorf("expected field %q missing from issueScalarFields", k)
		}
	}
}

// TestIssueTeamIsEditableNotMeta pins the file each field lives in after #429:
// team moved OUT of the read-only issue.meta and INTO the editable issue.md,
// because moving an issue between teams is a normal Linear operation the
// filesystem should express. The two assertions are one claim — a field lives in
// exactly one file — and a regression that duplicated it would give a writer two
// places to edit one value, only one of which is read.
func TestIssueTeamIsEditableNotMeta(t *testing.T) {
	t.Parallel()
	issue := &api.Issue{
		ID:         "issue-1",
		Identifier: "ENG-1",
		Title:      "Move me",
		State:      api.State{ID: "state-1", Name: "Todo"},
		Team:       &api.Team{ID: "team-1", Key: "ENG", Name: "Engineering"},
		CreatedAt:  time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
	}

	md, err := IssueToMarkdown(issue)
	if err != nil {
		t.Fatalf("IssueToMarkdown: %v", err)
	}
	if !strings.Contains(string(md), "team: ENG") {
		t.Errorf("issue.md does not carry the editable team key:\n%s", md)
	}

	meta, err := IssueMetaToMarkdown(issue)
	if err != nil {
		t.Fatalf("IssueMetaToMarkdown: %v", err)
	}
	if strings.Contains(string(meta), "team:") {
		t.Errorf("issue.meta still carries team, which is now editable in issue.md:\n%s", meta)
	}
}

// TestMarkdownToIssueUpdateTeam pins the move's diff behavior: the team key is
// emitted under teamId ONLY when it differs from the issue's current team, and
// an absent key never clears it. The rendered value is the key, so an unchanged
// round-trip of a rendered file must produce no update — otherwise every save of
// any field would also re-send a team move.
func TestMarkdownToIssueUpdateTeam(t *testing.T) {
	t.Parallel()
	original := &api.Issue{
		ID:         "issue-1",
		Identifier: "AGT-15",
		Title:      "Move me",
		State:      api.State{ID: "state-1", Name: "Todo"},
		Team:       &api.Team{ID: "team-agt", Key: "AGT", Name: "Agents"},
	}

	cases := []struct {
		name    string
		content string
		want    any // the expected update["teamId"], or nil for "absent"
	}{
		{
			name:    "same team is not a change",
			content: "---\ntitle: Move me\nteam: AGT\nstatus: Todo\n---\nbody",
			want:    nil,
		},
		{
			name:    "different team emits the key for resolution",
			content: "---\ntitle: Move me\nteam: SPY\nstatus: Todo\n---\nbody",
			want:    "SPY",
		},
		{
			// team is non-removable: an issue always belongs to exactly one team,
			// so an absent key means "unchanged", never "clear it".
			name:    "absent team never clears",
			content: "---\ntitle: Move me\nstatus: Todo\n---\nbody",
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			update, err := MarkdownToIssueUpdate([]byte(tc.content), original)
			if err != nil {
				t.Fatalf("MarkdownToIssueUpdate: %v", err)
			}
			got, present := update["teamId"]
			if tc.want == nil {
				if present {
					t.Errorf("update carries teamId = %v, want it absent", got)
				}
				return
			}
			if !present {
				t.Fatalf("update has no teamId, want %q", tc.want)
			}
			if got != tc.want {
				t.Errorf("teamId = %v, want %v (the KEY, resolved to an ID downstream)", got, tc.want)
			}
		})
	}
}

// TestMarkdownToIssueUpdateRejectsUnrecognizedKey pins the #426 contract: a key
// issue.md does not accept is a rejected write, not an accepted one that quietly
// applies nothing. Before this, an unknown key took a third path the failure
// model has no room for — exit 0, empty .error, no mutation, and the key gone on
// the next fresh render — so `teem: SPY` read as a successful team move.
func TestMarkdownToIssueUpdateRejectsUnrecognizedKey(t *testing.T) {
	t.Parallel()
	original := &api.Issue{
		ID: "issue-1", Identifier: "AGT-5", Title: "Original",
		Team:  &api.Team{ID: "team-agt", Key: "AGT", Name: "Agents"},
		State: api.State{ID: "state-1", Name: "Todo"},
	}

	cases := []struct {
		name        string
		content     string
		wantField   string
		wantMessage []string // substrings the .error must carry
	}{
		{
			name:        "typo'd editable key",
			content:     "---\ntitle: Original\nassigne: alice@example.com\n---\nbody",
			wantField:   "assigne",
			wantMessage: []string{"unknown field", "assignee"}, // the list names the real key
		},
		{
			name:      "wholly unknown key",
			content:   "---\ntitle: Original\nfoo: bar\n---\nbody",
			wantField: "foo",
			// The accepted-field list is the whole remedy: it tells the writer
			// what they may have meant without a second failed write.
			wantMessage: []string{"unknown field", "issue.md accepts:", "title", "cycle"},
		},
		{
			name:      "several unknown keys are all named",
			content:   "---\ntitle: Original\nzeta: 1\nteem: SPY\n---\nbody",
			wantField: "teem", // sorted, so the reported field is stable
			// One rejected write must name every key to fix; otherwise a writer
			// with two typos pays two round trips to learn about them.
			wantMessage: []string{"unknown field", `also unrecognized: "zeta"`},
		},
		{
			name:      "server-managed key says where the field lives",
			content:   "---\ntitle: Original\nidentifier: AGT-5\n---\nbody",
			wantField: "identifier",
			// Recognized-but-read-only is a different mistake from a typo: the
			// writer named a real field, just not one issue.md owns.
			wantMessage: []string{"read-only field", "issue.meta"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			update, err := MarkdownToIssueUpdate([]byte(tc.content), original)
			if err == nil {
				t.Fatalf("MarkdownToIssueUpdate accepted %q, want a rejection (update: %v)", tc.content, update)
			}
			if update != nil {
				t.Errorf("rejected write returned a non-nil update %v — nothing may be applied from a rejected document", update)
			}
			ferr, ok := err.(*FieldError)
			if !ok {
				t.Fatalf("error is %T, want *FieldError so .error renders Field/Value/Error and fs maps it to EINVAL", err)
			}
			if ferr.Field != tc.wantField {
				t.Errorf("FieldError.Field = %q, want %q", ferr.Field, tc.wantField)
			}
			for _, want := range tc.wantMessage {
				if !strings.Contains(ferr.Message, want) {
					t.Errorf("message %q does not carry %q", ferr.Message, want)
				}
			}
		})
	}
}

// TestMarkdownToIssueCreateRejectsUnrecognizedKey covers the create half of the
// same contract. The split is deliberate: _create IGNORES server-managed keys
// (a spec pasted from a rendered issue.md + issue.meta carries them, and the
// server assigns those fields at birth — TestMarkdownToIssueCreate pins that),
// but a key in neither set is a typo, and an issue born unassigned because of
// `assigne:` is exactly the silent drop #426 is about.
func TestMarkdownToIssueCreateRejectsUnrecognizedKey(t *testing.T) {
	t.Parallel()

	if _, err := MarkdownToIssueCreate([]byte("---\ntitle: New\nassigne: alice@example.com\n---\nbody")); err == nil {
		t.Error("MarkdownToIssueCreate accepted a typo'd key, want a rejection")
	} else if ferr, ok := err.(*FieldError); !ok {
		t.Errorf("error is %T, want *FieldError", err)
	} else if ferr.Field != "assigne" {
		t.Errorf("FieldError.Field = %q, want %q", ferr.Field, "assigne")
	}

	// Read-only keys stay tolerated here — this is the one place the two paths
	// differ, so it is asserted rather than left to the other test's fixture.
	if _, err := MarkdownToIssueCreate([]byte("---\ntitle: New\nid: from-a-pasted-meta\nurl: https://linear.app/x\n---\nbody")); err != nil {
		t.Errorf("MarkdownToIssueCreate rejected server-managed keys: %v — create ignores them by design", err)
	}
}

// TestRenderedIssueSurvivesTheKeyGuard is the round-trip safety net for the
// guard: every key IssueToMarkdown renders must be a key MarkdownToIssueUpdate
// accepts. A guard that rejects the file the filesystem itself produced would
// make a no-op re-save fail, which is a worse bug than the one it fixes.
func TestRenderedIssueSurvivesTheKeyGuard(t *testing.T) {
	t.Parallel()
	dueDate := "2026-02-01"
	estimate := 5.0
	issue := &api.Issue{
		ID: "issue-1", Identifier: "ENG-1", Title: "Everything set",
		Description:      "body",
		Team:             &api.Team{ID: "team-1", Key: "ENG", Name: "Engineering"},
		State:            api.State{ID: "state-1", Name: "Todo"},
		Assignee:         &api.User{ID: "u1", Email: "alice@example.com"},
		Priority:         2,
		Labels:           api.Labels{Nodes: []api.Label{{ID: "l1", Name: "bug"}}},
		DueDate:          &dueDate,
		Estimate:         &estimate,
		Parent:           &api.ParentIssue{ID: "p1", Identifier: "ENG-100"},
		Project:          &api.Project{ID: "pr1", Name: "Q1 Launch"},
		ProjectMilestone: &api.ProjectMilestone{ID: "m1", Name: "Phase 1"},
		Cycle:            &api.IssueCycle{ID: "c1", Name: "Sprint 42"},
	}

	md, err := IssueToMarkdown(issue)
	if err != nil {
		t.Fatalf("IssueToMarkdown: %v", err)
	}
	if _, err := MarkdownToIssueUpdate(md, issue); err != nil {
		t.Fatalf("the guard rejected a rendered issue.md — a no-op re-save would now fail: %v\n%s", err, md)
	}
}

// TestIssueMetaKeysCoverTheRenderedSidecar keeps issueMetaKeys complete by
// rendering rather than by memory: a field added to IssueMetaToMarkdown must be
// RECOGNIZED by the guard, or naming it in issue.md reports "unknown field"
// when the honest answer is "read-only, it lives in issue.meta".
func TestIssueMetaKeysCoverTheRenderedSidecar(t *testing.T) {
	t.Parallel()
	baseTime := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	started, completed, canceled, archived := baseTime, baseTime, baseTime, baseTime
	issue := &api.Issue{
		ID: "issue-1", Identifier: "ENG-1", Title: "Everything set",
		URL:         "https://linear.app/team/issue/ENG-1",
		BranchName:  "eng-1-everything-set",
		Creator:     &api.User{ID: "u1", Email: "alice@example.com"},
		CreatedAt:   baseTime,
		UpdatedAt:   baseTime,
		StartedAt:   &started,
		CompletedAt: &completed,
		CanceledAt:  &canceled,
		ArchivedAt:  &archived,
		Relations: api.IssueRelations{Nodes: []api.IssueRelation{
			{Type: "blocks", RelatedIssue: &api.ParentIssue{ID: "issue-2", Identifier: "ENG-2"}},
		}},
	}
	md, err := IssueMetaToMarkdown(issue, api.Attachment{ID: "a1", Title: "PR", URL: "https://example.com", SourceType: "github"})
	if err != nil {
		t.Fatalf("IssueMetaToMarkdown: %v", err)
	}

	doc, err := Parse(md)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Frontmatter) < 10 {
		t.Fatalf("meta render produced only %d keys — the fixture stopped populating fields", len(doc.Frontmatter))
	}
	for key := range doc.Frontmatter {
		if !issueMetaKeys[key] {
			t.Errorf("issue.meta renders %q but issueMetaKeys does not list it — writing it to issue.md would report 'unknown field' instead of naming the sidecar", key)
		}
	}
}

// TestClearedEstimateConverges pins the idempotency of a cleared estimate: a
// clear must be a value the round trip can represent, so the save after it is a
// no-op rather than the same clear again.
//
// Zero is deliberately NOT treated as "no estimate" on either side. Linear teams
// can permit zero-point estimates (issueEstimationAllowZero, rendered into
// team.md as estimate_allow_zero), so a `0` is a value a reader must be able to
// see and a writer must be able to keep. Both directions therefore ask the same
// question — is the pointer nil? — which is what makes a clear settle: it stores
// null, the next render emits no key, and the next diff finds nothing to remove.
//
// A backend (or a test double) that answered a clear with `0` instead of null
// would break that convergence, which is why the mock mutator models a clear as
// nil — see TestClearedEstimateRoundTripsAsNil in internal/testutil/mockmutation.
func TestClearedEstimateConverges(t *testing.T) {
	cleared := 0.0
	real := 5.0

	t.Run("a zero estimate is rendered", func(t *testing.T) {
		out, err := IssueToMarkdown(&api.Issue{Title: "T", Estimate: &cleared})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if !strings.Contains(string(out), "estimate: 0") {
			t.Errorf("a zero-point estimate was not rendered; a team that allows them would "+
				"find the value invisible on the mount:\n%s", out)
		}
	})

	t.Run("no key against a cleared (nil) estimate is no change", func(t *testing.T) {
		update, err := MarkdownToIssueUpdate([]byte("---\ntitle: T\n---\nbody\n"),
			&api.Issue{Title: "T", Description: "body"})
		if err != nil {
			t.Fatalf("diff: %v", err)
		}
		if _, present := update["estimate"]; present {
			t.Errorf("saving a document with no estimate against an already-cleared estimate "+
				"emitted %#v — the clear never converges and every save re-sends it", update)
		}
	})

	t.Run("no key against a real estimate still clears", func(t *testing.T) {
		update, err := MarkdownToIssueUpdate([]byte("---\ntitle: T\n---\nbody\n"),
			&api.Issue{Title: "T", Description: "body", Estimate: &real})
		if err != nil {
			t.Fatalf("diff: %v", err)
		}
		got, present := update["estimate"]
		if !present || got != nil {
			t.Errorf("removing the estimate key must still clear a real estimate, got %#v", update)
		}
	})
}
