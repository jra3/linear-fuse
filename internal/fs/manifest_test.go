package fs

import (
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// TestDirManifestRoundTrip is the anti-drift guarantee for an entity directory's
// static children: because entries() (Readdir) and find() (Lookup) are both pure
// projections of one children slice, every name a directory lists must be one it
// can also open. It also pins the exact child set per directory as a change-
// detector — adding or removing a static child is a conscious edit here. The
// build closures are captured but never invoked, so no mount is needed.
func TestDirManifestRoundTrip(t *testing.T) {
	t.Parallel()
	lfs := testLFS(t)
	created := time.Unix(1_650_000_000, 0)
	updated := time.Unix(1_650_050_000, 0)

	issueDir := &IssueDirectoryNode{
		attrNode:   attrNode{BaseNode: BaseNode{lfs: lfs}},
		entityCell: entityCell[api.Issue]{val: api.Issue{ID: "i1", Identifier: "ENG-1", CreatedAt: created, UpdatedAt: updated}},
	}
	projectDir := &ProjectNode{
		attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}},
		project:  api.Project{ID: "p1", CreatedAt: created, UpdatedAt: updated},
	}
	initiativeDir := &InitiativeNode{
		attrNode:   attrNode{BaseNode: BaseNode{lfs: lfs}},
		entityCell: entityCell[api.Initiative]{val: api.Initiative{ID: "n1", CreatedAt: created, UpdatedAt: updated}},
	}

	cases := []struct {
		name string
		m    *dirManifest
		want []string // exact child names, in Readdir order
	}{
		{
			name: "issue",
			m:    issueDir.manifest(),
			want: []string{"issue.md", "issue.meta", "history.md", ".error", ".last",
				"comments", "docs", "children", "attachments", "relations"},
		},
		{
			name: "project",
			m:    projectDir.manifest(),
			want: []string{"project.md", "project.meta", ".error", "docs", "updates", "milestones", "links"},
		},
		{
			name: "initiative",
			m:    initiativeDir.manifest(),
			want: []string{"initiative.md", "initiative.meta", ".error", "docs", "projects", "updates", "links"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := tc.m.entries()

			// Exact child set and order — the change-detector.
			var got []string
			for _, e := range entries {
				got = append(got, e.Name)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("entries() = %v\n            want %v", got, tc.want)
			}

			// No duplicate dirents.
			seen := make(map[string]bool, len(entries))
			for _, e := range entries {
				if seen[e.Name] {
					t.Errorf("duplicate dirent %q", e.Name)
				}
				seen[e.Name] = true
			}

			// Totality: every listed name resolves via find(), with the same mode.
			for _, e := range entries {
				child, ok := tc.m.find(e.Name)
				if !ok {
					t.Errorf("entries() lists %q but find() cannot resolve it (listed⇔openable violated)", e.Name)
					continue
				}
				if child.mode != e.Mode {
					t.Errorf("mode mismatch for %q: entries()=%#o find()=%#o", e.Name, e.Mode, child.mode)
				}
				if child.build == nil {
					t.Errorf("child %q has no build closure", e.Name)
				}
			}

			// A name that isn't a static child misses (so Lookup falls to the tail).
			if _, ok := tc.m.find("definitely-not-a-child"); ok {
				t.Error("find() matched a nonexistent name")
			}
		})
	}
}

// TestDirManifestSubdirsAreDirents guards the mode split: subdirs list as
// S_IFDIR, every file/meta/error/last as S_IFREG.
func TestDirManifestSubdirsAreDirents(t *testing.T) {
	t.Parallel()
	lfs := testLFS(t)
	issueDir := &IssueDirectoryNode{
		attrNode:   attrNode{BaseNode: BaseNode{lfs: lfs}},
		entityCell: entityCell[api.Issue]{val: api.Issue{ID: "i1", Identifier: "ENG-1"}},
	}
	dirs := map[string]bool{"comments": true, "docs": true, "children": true, "attachments": true, "relations": true}
	for _, e := range issueDir.manifest().entries() {
		wantDir := dirs[e.Name]
		isDir := e.Mode&syscall.S_IFDIR != 0
		if wantDir != isDir {
			t.Errorf("%q: isDir=%v want %v (mode %#o)", e.Name, isDir, wantDir, e.Mode)
		}
	}
}
