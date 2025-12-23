package fs

import (
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

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

func TestLabelToMarkdown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		label       *api.Label
		wantContain []string
	}{
		{
			name: "full label",
			label: &api.Label{
				ID:          "label-123",
				Name:        "Bug",
				Color:       "#FF0000",
				Description: "Something is broken",
			},
			wantContain: []string{
				"id: label-123",
				`name: "Bug"`,
				`color: "#FF0000"`,
				`description: "Something is broken"`,
				"# Bug",
				"**Color:** #FF0000",
				"**ID:** label-123",
				"Something is broken",
			},
		},
		{
			name: "label without description",
			label: &api.Label{
				ID:          "label-456",
				Name:        "Feature",
				Color:       "#00FF00",
				Description: "",
			},
			wantContain: []string{
				"id: label-456",
				`name: "Feature"`,
				`color: "#00FF00"`,
				"# Feature",
			},
		},
		{
			name: "label with special characters in name",
			label: &api.Label{
				ID:    "label-789",
				Name:  "Bug: Critical",
				Color: "#0000FF",
			},
			wantContain: []string{
				`name: "Bug: Critical"`,
				"# Bug: Critical",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(labelToMarkdown(tt.label))
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("labelToMarkdown() missing %q\nGot:\n%s", want, got)
				}
			}
		})
	}
}

func TestParseLabelMarkdown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		content    string
		original   *api.Label
		wantUpdate map[string]any
		wantErr    bool
	}{
		{
			name: "no changes",
			content: `---
id: label-123
name: "Bug"
color: "#FF0000"
description: "Something broken"
---
# Bug`,
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
id: label-123
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
id: label-123
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
id: label-123
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
			got, err := parseLabelMarkdown([]byte(tt.content), tt.original)

			if tt.wantErr {
				if err == nil {
					t.Error("parseLabelMarkdown() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("parseLabelMarkdown() unexpected error: %v", err)
				return
			}

			if len(got) != len(tt.wantUpdate) {
				t.Errorf("parseLabelMarkdown() returned %d fields, want %d\nGot: %v\nWant: %v",
					len(got), len(tt.wantUpdate), got, tt.wantUpdate)
			}

			for k, want := range tt.wantUpdate {
				gotVal, ok := got[k]
				if !ok {
					t.Errorf("parseLabelMarkdown() missing key %q", k)
					continue
				}
				if gotVal != want {
					t.Errorf("parseLabelMarkdown() field %q = %v, want %v", k, gotVal, want)
				}
			}
		})
	}
}

func TestParseNewLabelMarkdown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		content         string
		wantName        string
		wantColor       string
		wantDescription string
		wantErr         bool
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
			name: "name and color",
			content: `---
name: "Colored Label"
color: "#00FF00"
---`,
			wantName:        "Colored Label",
			wantColor:       "#00FF00",
			wantDescription: "",
		},
		{
			name: "unquoted values",
			content: `---
name: Unquoted Name
color: #0000FF
---`,
			wantName:  "Unquoted Name",
			wantColor: "#0000FF",
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
			name, color, desc, err := parseNewLabelMarkdown([]byte(tt.content))

			if tt.wantErr {
				if err == nil {
					t.Error("parseNewLabelMarkdown() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("parseNewLabelMarkdown() unexpected error: %v", err)
				return
			}

			if name != tt.wantName {
				t.Errorf("parseNewLabelMarkdown() name = %q, want %q", name, tt.wantName)
			}
			if color != tt.wantColor {
				t.Errorf("parseNewLabelMarkdown() color = %q, want %q", color, tt.wantColor)
			}
			if desc != tt.wantDescription {
				t.Errorf("parseNewLabelMarkdown() description = %q, want %q", desc, tt.wantDescription)
			}
		})
	}
}

func TestParseYAMLFrontmatter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    map[string]any
		wantErr bool
	}{
		{
			name: "simple frontmatter",
			content: `---
key1: value1
key2: value2
---
body`,
			want: map[string]any{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name: "quoted values",
			content: `---
name: "Quoted Value"
color: "#FF0000"
---`,
			want: map[string]any{
				"name":  "Quoted Value",
				"color": "#FF0000",
			},
		},
		{
			name: "empty values",
			content: `---
empty:
present: value
---`,
			want: map[string]any{
				"empty":   "",
				"present": "value",
			},
		},
		{
			name: "with comments",
			content: `---
# This is a comment
key: value
# Another comment
---`,
			want: map[string]any{
				"key": "value",
			},
		},
		{
			name: "whitespace handling",
			content: `---
  key1  :  value1
key2:value2
---`,
			want: map[string]any{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:    "no opening delimiter",
			content: "key: value\n---\nbody",
			wantErr: true,
		},
		{
			name:    "no closing delimiter",
			content: "---\nkey: value\nbody without closing",
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
			got, err := parseYAMLFrontmatter([]byte(tt.content))

			if tt.wantErr {
				if err == nil {
					t.Error("parseYAMLFrontmatter() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("parseYAMLFrontmatter() unexpected error: %v", err)
				return
			}

			if len(got) != len(tt.want) {
				t.Errorf("parseYAMLFrontmatter() returned %d fields, want %d\nGot: %v\nWant: %v",
					len(got), len(tt.want), got, tt.want)
			}

			for k, want := range tt.want {
				gotVal, ok := got[k]
				if !ok {
					t.Errorf("parseYAMLFrontmatter() missing key %q", k)
					continue
				}
				if gotVal != want {
					t.Errorf("parseYAMLFrontmatter() field %q = %v, want %v", k, gotVal, want)
				}
			}
		})
	}
}

func TestLabelsDirIno(t *testing.T) {
	t.Parallel()
	// Same team ID should produce same inode
	ino1 := labelsDirIno("team-123")
	ino2 := labelsDirIno("team-123")
	if ino1 != ino2 {
		t.Errorf("labelsDirIno() not stable: got %d and %d for same input", ino1, ino2)
	}

	// Different team IDs should produce different inodes
	ino3 := labelsDirIno("team-456")
	if ino1 == ino3 {
		t.Errorf("labelsDirIno() collision: got same inode %d for different teams", ino1)
	}
}

func TestLabelIno(t *testing.T) {
	t.Parallel()
	// Same label ID should produce same inode
	ino1 := labelIno("label-123")
	ino2 := labelIno("label-123")
	if ino1 != ino2 {
		t.Errorf("labelIno() not stable: got %d and %d for same input", ino1, ino2)
	}

	// Different label IDs should produce different inodes
	ino3 := labelIno("label-456")
	if ino1 == ino3 {
		t.Errorf("labelIno() collision: got same inode %d for different labels", ino1)
	}
}
