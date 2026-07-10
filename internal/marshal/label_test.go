package marshal

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// TestLabelToMarkdown pins the editable-only contract for a label .md: name,
// color, description — every field editable, empty body. The id (which the old
// render leaked into the frontmatter and a generated prose body) lives in the
// .meta sidecar.
func TestLabelToMarkdown(t *testing.T) {
	t.Parallel()
	label := &api.Label{
		ID:          "label-123",
		Name:        "Bug: Critical",
		Color:       "#FF0000",
		Description: "Something is broken",
	}

	content, err := LabelToMarkdown(label)
	if err != nil {
		t.Fatalf("LabelToMarkdown: %v", err)
	}
	keys, doc := frontmatterKeys(t, content)
	if want := []string{"color", "description", "name"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("label .md frontmatter keys = %v, want %v (editable-only)", keys, want)
	}
	// Hostile values (colon in the name, # in the color) survive the YAML
	// round-trip — the reason the render routes through the marshal seam.
	if doc.Frontmatter["name"] != "Bug: Critical" {
		t.Errorf("name = %v, want the hostile name intact", doc.Frontmatter["name"])
	}
	if doc.Frontmatter["color"] != "#FF0000" {
		t.Errorf("color = %v, want #FF0000", doc.Frontmatter["color"])
	}
	if doc.Body != "" {
		t.Errorf("label .md body = %q, want empty (the old generated prose moved to .meta)", doc.Body)
	}

	// An empty description still renders its key (it is editable; the empty
	// value invites filling it in), matching the old contract.
	content, err = LabelToMarkdown(&api.Label{ID: "l2", Name: "Feature", Color: "#00FF00"})
	if err != nil {
		t.Fatalf("LabelToMarkdown(no description): %v", err)
	}
	if keys, _ := frontmatterKeys(t, content); !reflect.DeepEqual(keys, []string{"color", "description", "name"}) {
		t.Errorf("label .md keys without description = %v, want all three", keys)
	}
}

// TestLabelMetaToMarkdown pins the server-managed half: the identity, plus the
// owning team for a team-scoped label (omitted for a workspace label).
func TestLabelMetaToMarkdown(t *testing.T) {
	t.Parallel()
	content, err := LabelMetaToMarkdown(&api.Label{ID: "label-123", Name: "Bug", Team: &api.Team{ID: "team-1"}})
	if err != nil {
		t.Fatalf("LabelMetaToMarkdown: %v", err)
	}
	keys, doc := frontmatterKeys(t, content)
	if want := []string{"id", "team"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("team label .meta frontmatter keys = %v, want %v", keys, want)
	}
	if doc.Body != "" {
		t.Errorf("meta must be frontmatter-only, got body %q", doc.Body)
	}

	// Workspace label: no team edge, no team key.
	content, err = LabelMetaToMarkdown(&api.Label{ID: "label-ws", Name: "Bug"})
	if err != nil {
		t.Fatalf("LabelMetaToMarkdown(workspace): %v", err)
	}
	if keys, _ := frontmatterKeys(t, content); !reflect.DeepEqual(keys, []string{"id"}) {
		t.Errorf("workspace label .meta keys = %v, want [id]", keys)
	}
}

// TestLabelRenderParseRoundTrip pins parse(render(label)) as a fixpoint: the
// editable-only render (LabelToMarkdown) parses back with zero changes, so a
// no-op save pushes nothing. This is the test that caught the single-quote
// gap: yaml.v3 renders '#FF0000' single-quoted, and the old hand parser only
// stripped double quotes, so every re-save "changed" the color to a
// quote-wrapped corruption — real YAML on both sides kills that class of bug.
func TestLabelRenderParseRoundTrip(t *testing.T) {
	t.Parallel()
	labels := []*api.Label{
		{ID: "label-123", Name: "Bug", Color: "#FF0000", Description: "Something is broken"},
		{ID: "label-456", Name: "Feature", Color: "#00FF00"},
		{ID: "label-789", Name: "Bug: Critical", Color: "#0000FF"},
	}
	for _, label := range labels {
		t.Run(label.Name, func(t *testing.T) {
			content, err := LabelToMarkdown(label)
			if err != nil {
				t.Fatalf("LabelToMarkdown: %v", err)
			}
			if strings.Contains(string(content), "id:") {
				t.Errorf("label .md leaks the server-managed id (it lives in .meta):\n%s", content)
			}
			// The render must emit the color quoted ('#FF0000'), or the parse
			// side's comment guard below would reject our own output.
			if !strings.Contains(string(content), "'"+label.Color+"'") {
				t.Errorf("render did not single-quote the color:\n%s", content)
			}
			update, err := MarkdownToLabelUpdate(content, label)
			if err != nil {
				t.Fatalf("MarkdownToLabelUpdate: %v", err)
			}
			if len(update) != 0 {
				t.Errorf("Roundtrip produced unexpected changes: %v (rendered:\n%s)", update, content)
			}
		})
	}
}

func TestMarkdownToLabelUpdate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		content    string
		original   *api.Label
		wantUpdate map[string]any
		wantErr    bool
		wantField  string // non-empty: expect a *FieldError on this field
	}{
		{
			name: "no changes",
			content: `---
name: "Bug"
color: "#FF0000"
description: "Something broken"
---`,
			original: &api.Label{
				ID:          "label-123",
				Name:        "Bug",
				Color:       "#FF0000",
				Description: "Something broken",
			},
			wantUpdate: map[string]any{},
		},
		{
			name: "name changed",
			content: `---
name: "Critical Bug"
color: "#FF0000"
description: ""
---`,
			original: &api.Label{
				ID:    "label-123",
				Name:  "Bug",
				Color: "#FF0000",
			},
			wantUpdate: map[string]any{
				"name": "Critical Bug",
			},
		},
		{
			name: "color changed",
			content: `---
name: "Bug"
color: "#00FF00"
description: ""
---`,
			original: &api.Label{
				ID:    "label-123",
				Name:  "Bug",
				Color: "#FF0000",
			},
			wantUpdate: map[string]any{
				"color": "#00FF00",
			},
		},
		{
			name: "description changed",
			content: `---
name: "Bug"
color: "#FF0000"
description: "New description"
---`,
			original: &api.Label{
				ID:          "label-123",
				Name:        "Bug",
				Color:       "#FF0000",
				Description: "",
			},
			wantUpdate: map[string]any{
				"description": "New description",
			},
		},
		{
			name: "multiple fields changed",
			content: `---
name: "New Name"
color: "#0000FF"
description: "New desc"
---`,
			original: &api.Label{
				ID:          "label-123",
				Name:        "Old Name",
				Color:       "#FF0000",
				Description: "Old desc",
			},
			wantUpdate: map[string]any{
				"name":        "New Name",
				"color":       "#0000FF",
				"description": "New desc",
			},
		},
		{
			// Absent keys are untouched fields: no change, no error.
			name: "absent keys leave fields alone",
			content: `---
name: "Bug"
---`,
			original: &api.Label{
				ID:          "label-123",
				Name:        "Bug",
				Color:       "#FF0000",
				Description: "Something broken",
			},
			wantUpdate: map[string]any{},
		},
		{
			// The YAML comment trap: unquoted `color: #FF0000` parses the value
			// as a comment — key present, value nil. Rejected with quoting
			// guidance instead of silently dropping the edit.
			name: "unquoted hex color rejected",
			content: `---
name: "Bug"
color: #00FF00
---`,
			original: &api.Label{
				ID:    "label-123",
				Name:  "Bug",
				Color: "#FF0000",
			},
			wantField: "color",
		},
		{
			name:    "no frontmatter",
			content: "Just some text without frontmatter",
			original: &api.Label{
				ID:   "label-123",
				Name: "Bug",
			},
			wantErr: true,
		},
		{
			name:    "unterminated frontmatter",
			content: "---\nname: Bug\nNo closing delimiter",
			original: &api.Label{
				ID:   "label-123",
				Name: "Bug",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MarkdownToLabelUpdate([]byte(tt.content), tt.original)

			if tt.wantField != "" {
				var ferr *FieldError
				if !errors.As(err, &ferr) {
					t.Fatalf("MarkdownToLabelUpdate() err = %v, want *FieldError on %q", err, tt.wantField)
				}
				if ferr.Field != tt.wantField {
					t.Errorf("MarkdownToLabelUpdate() FieldError.Field = %q, want %q", ferr.Field, tt.wantField)
				}
				return
			}
			if tt.wantErr {
				if err == nil {
					t.Error("MarkdownToLabelUpdate() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("MarkdownToLabelUpdate() unexpected error: %v", err)
				return
			}

			if !reflect.DeepEqual(got, tt.wantUpdate) {
				t.Errorf("MarkdownToLabelUpdate() = %v, want %v", got, tt.wantUpdate)
			}
		})
	}
}

func TestParseNewLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		content         string
		wantName        string
		wantColor       string
		wantDescription string
		wantErr         bool
		wantField       string // non-empty: expect a *FieldError on this field
	}{
		{
			name: "full label",
			content: `---
name: "New Label"
color: "#FF0000"
description: "A new label"
---`,
			wantName:        "New Label",
			wantColor:       "#FF0000",
			wantDescription: "A new label",
		},
		{
			name: "name only",
			content: `---
name: "Simple Label"
---`,
			wantName:        "Simple Label",
			wantColor:       "",
			wantDescription: "",
		},
		{
			name: "quoted color",
			content: `---
name: "Colored Label"
color: '#00FF00'
---`,
			wantName:  "Colored Label",
			wantColor: "#00FF00",
		},
		{
			// Unquoted names are fine YAML; an unquoted hex color is not — it
			// parses as a comment and trips the guard.
			name: "unquoted hex color rejected",
			content: `---
name: Unquoted Name
color: #0000FF
---`,
			wantField: "color",
		},
		{
			name:    "no frontmatter",
			content: "Just text",
			wantErr: true,
		},
		{
			name:    "empty content",
			content: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, color, desc, err := ParseNewLabel([]byte(tt.content))

			if tt.wantField != "" {
				var ferr *FieldError
				if !errors.As(err, &ferr) {
					t.Fatalf("ParseNewLabel() err = %v, want *FieldError on %q", err, tt.wantField)
				}
				if ferr.Field != tt.wantField {
					t.Errorf("ParseNewLabel() FieldError.Field = %q, want %q", ferr.Field, tt.wantField)
				}
				return
			}
			if tt.wantErr {
				if err == nil {
					t.Error("ParseNewLabel() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("ParseNewLabel() unexpected error: %v", err)
				return
			}

			if name != tt.wantName {
				t.Errorf("ParseNewLabel() name = %q, want %q", name, tt.wantName)
			}
			if color != tt.wantColor {
				t.Errorf("ParseNewLabel() color = %q, want %q", color, tt.wantColor)
			}
			if desc != tt.wantDescription {
				t.Errorf("ParseNewLabel() description = %q, want %q", desc, tt.wantDescription)
			}
		})
	}
}
