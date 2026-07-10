package fs

import (
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// The label parse tests (round-trip fixpoint, changed-field semantics, the
// unquoted-color guard) live with the parsers in internal/marshal/label_test.go.

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
