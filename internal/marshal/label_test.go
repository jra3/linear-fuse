package marshal

import (
	"reflect"
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
