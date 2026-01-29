package fs

import (
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestDocsDirIno(t *testing.T) {
	t.Parallel()
	// Same parent ID should produce same inode
	ino1 := docsDirIno("parent-123")
	ino2 := docsDirIno("parent-123")
	if ino1 != ino2 {
		t.Errorf("docsDirIno() not stable: got %d and %d for same input", ino1, ino2)
	}

	// Different parent IDs should produce different inodes
	ino3 := docsDirIno("parent-456")
	if ino1 == ino3 {
		t.Errorf("docsDirIno() collision: got same inode %d for different parents", ino1)
	}
}

func TestDocumentIno(t *testing.T) {
	t.Parallel()
	// Same document ID should produce same inode
	ino1 := documentIno("doc-123")
	ino2 := documentIno("doc-123")
	if ino1 != ino2 {
		t.Errorf("documentIno() not stable: got %d and %d for same input", ino1, ino2)
	}

	// Different document IDs should produce different inodes
	ino3 := documentIno("doc-456")
	if ino1 == ino3 {
		t.Errorf("documentIno() collision: got same inode %d for different documents", ino1)
	}
}

func TestDocumentFilename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  api.Document
		want string
	}{
		{
			name: "with slugId",
			doc: api.Document{
				SlugID: "my-document",
				Title:  "My Document",
			},
			want: "my-document.md",
		},
		{
			name: "without slugId uses title",
			doc: api.Document{
				SlugID: "",
				Title:  "My Document",
			},
			want: "my-document.md",
		},
		{
			name: "title with spaces",
			doc: api.Document{
				SlugID: "",
				Title:  "My New Document",
			},
			want: "my-new-document.md",
		},
		{
			name: "title with slashes",
			doc: api.Document{
				SlugID: "",
				Title:  "Feature/Backend",
			},
			want: "feature-backend.md",
		},
		{
			name: "title with mixed case",
			doc: api.Document{
				SlugID: "",
				Title:  "API Documentation",
			},
			want: "api-documentation.md",
		},
		{
			name: "empty title and slugId",
			doc: api.Document{
				SlugID: "",
				Title:  "",
			},
			want: ".md",
		},
		{
			name: "slugId takes precedence",
			doc: api.Document{
				SlugID: "custom-slug",
				Title:  "Different Title",
			},
			want: "custom-slug.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := documentFilename(tt.doc)
			if got != tt.want {
				t.Errorf("documentFilename() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDocsNode_parentID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		node      DocsNode
		wantID    string
	}{
		{
			name:   "issue docs",
			node:   DocsNode{issueID: "issue-123"},
			wantID: "issue-123",
		},
		{
			name:   "team docs",
			node:   DocsNode{teamID: "team-456"},
			wantID: "team-456",
		},
		{
			name:   "project docs",
			node:   DocsNode{projectID: "project-789"},
			wantID: "project-789",
		},
		{
			name:   "issue takes precedence",
			node:   DocsNode{issueID: "issue-1", teamID: "team-2", projectID: "project-3"},
			wantID: "issue-1",
		},
		{
			name:   "team over project",
			node:   DocsNode{teamID: "team-2", projectID: "project-3"},
			wantID: "team-2",
		},
		{
			name:   "empty node",
			node:   DocsNode{},
			wantID: "",
		},
	}

	for i := range tests {
		tt := &tests[i]
		t.Run(tt.name, func(t *testing.T) {
			got := tt.node.parentID()
			if got != tt.wantID {
				t.Errorf("parentID() = %q, want %q", got, tt.wantID)
			}
		})
	}
}
