package marshal

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestMilestoneToMarkdown(t *testing.T) {
	t.Parallel()
	targetDate := "2025-06-30"

	m := &api.ProjectMilestone{
		ID:          "milestone-1",
		Name:        "Phase 1",
		Description: "Initial rollout scope.",
		TargetDate:  &targetDate,
		SortOrder:   1.5,
	}

	md, err := MilestoneToMarkdown(m)
	if err != nil {
		t.Fatalf("MilestoneToMarkdown() error: %v", err)
	}

	content := string(md)
	for _, want := range []string{
		"name: Phase 1",
		`targetDate: "2025-06-30"`,
		"sortOrder: 1.5",
		"Initial rollout scope.",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("MilestoneToMarkdown() missing %q in:\n%s", want, content)
		}
	}

	// Editable-only: the server-managed id lives in the .meta sidecar.
	keys, _ := frontmatterKeys(t, md)
	if want := []string{"name", "sortOrder", "targetDate"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("milestone .md frontmatter keys = %v, want %v (editable-only)", keys, want)
	}
}

// TestMilestoneMetaToMarkdown pins the server-managed half: the identity alone
// (api.ProjectMilestone carries no timestamps or url), frontmatter-only.
func TestMilestoneMetaToMarkdown(t *testing.T) {
	t.Parallel()
	md, err := MilestoneMetaToMarkdown(&api.ProjectMilestone{ID: "milestone-1", Name: "Phase 1"})
	if err != nil {
		t.Fatalf("MilestoneMetaToMarkdown() error: %v", err)
	}
	keys, doc := frontmatterKeys(t, md)
	if want := []string{"id"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("milestone .meta frontmatter keys = %v, want %v", keys, want)
	}
	if doc.Frontmatter["id"] != "milestone-1" {
		t.Errorf("id = %v, want milestone-1", doc.Frontmatter["id"])
	}
	if doc.Body != "" {
		t.Errorf("meta must be frontmatter-only, got body %q", doc.Body)
	}
}

func TestMilestoneToMarkdownOmitsEmptyOptionals(t *testing.T) {
	t.Parallel()
	m := &api.ProjectMilestone{
		ID:   "milestone-2",
		Name: "Bare",
	}

	md, err := MilestoneToMarkdown(m)
	if err != nil {
		t.Fatalf("MilestoneToMarkdown() error: %v", err)
	}

	content := string(md)
	for _, notWant := range []string{"targetDate:", "sortOrder:"} {
		if strings.Contains(content, notWant) {
			t.Errorf("MilestoneToMarkdown() should omit %q for zero value:\n%s", notWant, content)
		}
	}
}

func TestMarkdownToMilestoneUpdate(t *testing.T) {
	t.Parallel()
	origDate := "2025-06-30"
	original := &api.ProjectMilestone{
		ID:          "milestone-1",
		Name:        "Phase 1",
		Description: "Initial rollout scope.",
		TargetDate:  &origDate,
		SortOrder:   1.5,
	}

	t.Run("changed fields are emitted", func(t *testing.T) {
		content := []byte(`---
id: milestone-1
name: Phase 1 (revised)
targetDate: "2025-09-30"
sortOrder: 2.5
---
Revised scope.`)

		input, err := MarkdownToMilestoneUpdate(content, original)
		if err != nil {
			t.Fatalf("MarkdownToMilestoneUpdate() error: %v", err)
		}
		if input.Name == nil || *input.Name != "Phase 1 (revised)" {
			t.Errorf("Name = %v, want %q", input.Name, "Phase 1 (revised)")
		}
		if input.TargetDate == nil || *input.TargetDate != "2025-09-30" {
			t.Errorf("TargetDate = %v, want %q", input.TargetDate, "2025-09-30")
		}
		if input.SortOrder == nil || *input.SortOrder != 2.5 {
			t.Errorf("SortOrder = %v, want 2.5", input.SortOrder)
		}
		if input.Description == nil || *input.Description != "Revised scope." {
			t.Errorf("Description = %v, want %q", input.Description, "Revised scope.")
		}
	})

	t.Run("unquoted target date parses as date not string", func(t *testing.T) {
		// YAML resolves a bare 2025-09-30 to a timestamp; the parser must
		// coerce it back to YYYY-MM-DD instead of silently dropping the edit.
		content := []byte(`---
name: Phase 1
targetDate: 2025-09-30
---
Initial rollout scope.`)

		input, err := MarkdownToMilestoneUpdate(content, original)
		if err != nil {
			t.Fatalf("MarkdownToMilestoneUpdate() error: %v", err)
		}
		if input.TargetDate == nil || *input.TargetDate != "2025-09-30" {
			t.Errorf("TargetDate = %v, want %q", input.TargetDate, "2025-09-30")
		}
	})

	t.Run("removed target date clears it", func(t *testing.T) {
		content := []byte(`---
name: Phase 1
---
Initial rollout scope.`)

		input, err := MarkdownToMilestoneUpdate(content, original)
		if err != nil {
			t.Fatalf("MarkdownToMilestoneUpdate() error: %v", err)
		}
		if input.TargetDate == nil || *input.TargetDate != "" {
			t.Errorf("TargetDate = %v, want cleared (empty string)", input.TargetDate)
		}
	})
}

// TestMilestoneRoundtrip pins parse(render(milestone)) as a fixpoint: rendering
// milestone.md and parsing it back must report zero changes, or every no-op
// save would push a phantom update (and read-your-writes verification would
// flag a divergence that is really a marshal asymmetry).
func TestMilestoneRoundtrip(t *testing.T) {
	t.Parallel()
	targetDate := "2025-06-30"

	tests := []struct {
		name      string
		milestone *api.ProjectMilestone
	}{
		{
			name: "fully populated",
			milestone: &api.ProjectMilestone{
				ID:          "milestone-1",
				Name:        "Phase 1",
				Description: "Initial rollout scope.\n\nWith a second paragraph.",
				TargetDate:  &targetDate,
				SortOrder:   1.5,
			},
		},
		{
			name: "minimal",
			milestone: &api.ProjectMilestone{
				ID:   "milestone-2",
				Name: "Bare",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := MilestoneToMarkdown(tt.milestone)
			if err != nil {
				t.Fatalf("MilestoneToMarkdown() error: %v", err)
			}

			input, err := MarkdownToMilestoneUpdate(md, tt.milestone)
			if err != nil {
				t.Fatalf("MarkdownToMilestoneUpdate() error: %v", err)
			}

			if input != (api.ProjectMilestoneUpdateInput{}) {
				t.Errorf("Roundtrip produced unexpected changes: %+v (rendered:\n%s)", input, md)
			}
		})
	}
}

func TestParseNewMilestone(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		content  string
		wantName string
		wantDesc string
	}{
		{
			name:     "name and description",
			content:  "Phase 1\nInitial milestone",
			wantName: "Phase 1",
			wantDesc: "Initial milestone",
		},
		{
			name:     "name only",
			content:  "Phase 1\n",
			wantName: "Phase 1",
			wantDesc: "",
		},
		{
			name:     "multiline description",
			content:  "Phase 1\nLine one\nLine two",
			wantName: "Phase 1",
			wantDesc: "Line one\nLine two",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, desc := ParseNewMilestone([]byte(tt.content))
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if desc != tt.wantDesc {
				t.Errorf("description = %q, want %q", desc, tt.wantDesc)
			}
		})
	}
}

func TestValidateMilestoneUpdate(t *testing.T) {
	t.Parallel()
	empty := ""
	if err := ValidateMilestoneUpdate(api.ProjectMilestoneUpdateInput{Name: &empty}); err == nil {
		t.Error("expected error for empty name")
	}
	name := "Phase 1"
	if err := ValidateMilestoneUpdate(api.ProjectMilestoneUpdateInput{Name: &name}); err != nil {
		t.Errorf("unexpected error for valid name: %v", err)
	}
}
