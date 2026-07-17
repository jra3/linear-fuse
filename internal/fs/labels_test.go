package fs

import (
	"context"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// The label parse tests (round-trip fixpoint, changed-field semantics, the
// unquoted-color guard) live with the parsers in internal/marshal/label_test.go.

// TestLabelEditPersists drives LabelFileNode.Flush at the store level: editing a
// label's color + description must land in SQLite while the untouched name
// survives. It runs off a private store (not the shared integration mount)
// because a label edit invalidates the by/label filtered view — see the note in
// integration/edit_persist_test.go.
func TestLabelEditPersists(t *testing.T) {
	lfs, store := linkTestLFS(t)
	const teamID = "team-1"
	orig := api.Label{ID: "lbl-1", Name: "EditProbe", Color: "#ff0000", Description: "orig desc"}
	if err := lfs.UpsertLabel(context.Background(), teamID, orig); err != nil {
		t.Fatalf("seed label: %v", err)
	}

	n := &LabelFileNode{BaseNode: BaseNode{lfs: lfs}, label: orig, teamID: teamID}
	edited := orig
	edited.Color = "#00ff00"
	edited.Description = "updated desc QQQ"
	content, err := marshal.LabelToMarkdown(&edited)
	if err != nil {
		t.Fatalf("render label: %v", err)
	}
	n.content = content
	n.dirty = true

	if errno := n.Flush(context.Background(), nil); errno != 0 {
		t.Fatalf("label Flush errno = %v, want 0", errno)
	}

	got, err := store.Queries().GetLabel(context.Background(), "lbl-1")
	if err != nil {
		t.Fatalf("GetLabel: %v", err)
	}
	if got.Color.String != "#00ff00" {
		t.Errorf("color did not persist: got %q, want #00ff00", got.Color.String)
	}
	if got.Description.String != "updated desc QQQ" {
		t.Errorf("description did not persist: got %q", got.Description.String)
	}
	if got.Name != "EditProbe" {
		t.Errorf("untouched name changed: got %q, want EditProbe", got.Name)
	}
}

func TestLabelFilename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		label api.Label
		want  string
	}{
		{
			name:  "simple name",
			label: api.Label{Name: "Bug"},
			want:  "Bug.md",
		},
		{
			name:  "name with spaces",
			label: api.Label{Name: "Critical Bug"},
			want:  "Critical-Bug.md",
		},
		{
			name:  "name with multiple spaces",
			label: api.Label{Name: "High Priority Task"},
			want:  "High-Priority-Task.md",
		},
		{
			name:  "name with slash",
			label: api.Label{Name: "Bug/Frontend"},
			want:  "Bug-Frontend.md",
		},
		{
			name:  "name with spaces and slashes",
			label: api.Label{Name: "Priority / High"},
			want:  "Priority---High.md",
		},
		{
			name:  "empty name",
			label: api.Label{Name: ""},
			want:  ".md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := labelFilename(tt.label)
			if got != tt.want {
				t.Errorf("labelFilename() = %q, want %q", got, tt.want)
			}
		})
	}
}
