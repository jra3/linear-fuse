package fs

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/config"
)

func testLFS(t *testing.T) *LinearFS {
	t.Helper()
	cfg := &config.Config{APIKey: "test-key", Cache: config.CacheConfig{TTL: 100 * time.Millisecond, MaxEntries: 100}}
	lfs, err := NewLinearFS(cfg, false)
	if err != nil {
		t.Fatalf("NewLinearFS failed: %v", err)
	}
	t.Cleanup(func() { lfs.Close() })
	return lfs
}

// TestNodeAttrFill pins the field mapping: atime/mtime report updatedAt, ctime
// reports createdAt, uid/gid come from the mount, and the mode carries through.
func TestNodeAttrFill(t *testing.T) {
	t.Parallel()
	lfs := testLFS(t)
	created := time.Unix(1_700_000_000, 0)
	updated := time.Unix(1_700_009_999, 0)

	na := nodeAttr{mode: 0755 | syscall.S_IFDIR, size: 42, created: created, updated: updated}
	b := &BaseNode{lfs: lfs}
	var attr fuse.Attr
	na.fill(&attr, b)

	if attr.Mode != na.mode {
		t.Errorf("mode = %o, want %o", attr.Mode, na.mode)
	}
	if attr.Size != 42 {
		t.Errorf("size = %d, want 42", attr.Size)
	}
	if attr.Uid != lfs.uid || attr.Gid != lfs.gid {
		t.Errorf("owner = %d/%d, want %d/%d", attr.Uid, attr.Gid, lfs.uid, lfs.gid)
	}
	if int64(attr.Mtime) != updated.Unix() {
		t.Errorf("mtime = %d, want %d (updatedAt)", attr.Mtime, updated.Unix())
	}
	if int64(attr.Atime) != updated.Unix() {
		t.Errorf("atime = %d, want %d (updatedAt)", attr.Atime, updated.Unix())
	}
	if int64(attr.Ctime) != created.Unix() {
		t.Errorf("ctime = %d, want %d (createdAt)", attr.Ctime, created.Unix())
	}
}

// TestNodeAttrZeroTimeStaysEpoch guards the nonZeroTime handling: a zero time
// must render as a zero attr, never a wrapped far-future timestamp.
func TestNodeAttrZeroTimeStaysEpoch(t *testing.T) {
	t.Parallel()
	lfs := testLFS(t)
	na := nodeAttr{mode: 0755 | syscall.S_IFDIR}
	b := &BaseNode{lfs: lfs}
	var attr fuse.Attr
	na.fill(&attr, b)
	if attr.Mtime != 0 || attr.Ctime != 0 || attr.Atime != 0 {
		t.Errorf("zero times must stay 0, got atime=%d mtime=%d ctime=%d", attr.Atime, attr.Mtime, attr.Ctime)
	}
}

// TestNodeAttrAtimeOverride pins the one deliberate exception to atime==updated:
// a non-zero atime field overrides it (the cycle directory tier reports
// atime=EndsAt with mtime/ctime=StartsAt, since api.Cycle has no
// created/updated fields), while mtime/ctime stay on updated/created.
func TestNodeAttrAtimeOverride(t *testing.T) {
	t.Parallel()
	lfs := testLFS(t)
	starts := time.Unix(1_700_000_000, 0)
	ends := time.Unix(1_700_500_000, 0)

	na := nodeAttr{mode: 0755 | syscall.S_IFDIR, created: starts, updated: starts, atime: ends}
	var attr fuse.Attr
	na.fill(&attr, &BaseNode{lfs: lfs})

	if int64(attr.Atime) != ends.Unix() {
		t.Errorf("atime = %d, want %d (override)", attr.Atime, ends.Unix())
	}
	if int64(attr.Mtime) != starts.Unix() || int64(attr.Ctime) != starts.Unix() {
		t.Errorf("mtime/ctime = %d/%d, want %d (updated/created untouched by the override)", attr.Mtime, attr.Ctime, starts.Unix())
	}
}

// dirAttrNode is every constructed directory node: it carries a nodeAttr and
// reports it through the attrNode mixin's promoted methods. fillAttr is the
// Lookup-answer path (what newDirInode uses); Getattr is the stat path.
type dirAttrNode interface {
	setAttr(nodeAttr)
	fillAttr(*fuse.Attr)
	Getattr(context.Context, fs.FileHandle, *fuse.AttrOut) syscall.Errno
}

// TestDirNodeLookupGetattrAgree is the anti-drift guarantee in executable form:
// the attributes a directory node's Lookup answer carries must equal what a
// later Getattr reports, for every constructed directory kind. This is the
// regression proof for the time.Now()/wrong-ctime bug that had a stat disagree
// with — and reshuffle against — its own listing. Because both paths render the
// one stored nodeAttr, a node that hand-wrote a divergent Getattr fails here.
func TestDirNodeLookupGetattrAgree(t *testing.T) {
	t.Parallel()
	lfs := testLFS(t)
	created := time.Unix(1_650_000_000, 0)
	updated := time.Unix(1_650_050_000, 0)
	na := dirAttr(created, updated)

	nodes := map[string]dirAttrNode{
		"comments":            &CommentsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"docs":                &DocsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"attachments":         &AttachmentsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"relations":           &RelationsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"children":            &ChildrenNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"milestones":          &MilestonesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"updates":             &UpdatesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"labels":              &LabelsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"initiative-updates":  &InitiativeUpdatesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"initiative-projects": &InitiativeProjectsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		// The three entity directories, folded onto attrNode by the dir manifest.
		"issue-dir":      &IssueDirectoryNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"project-dir":    &ProjectNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"initiative-dir": &InitiativeNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		// The view/entity directory kinds normalized onto attrNode: the four
		// top-level containers, the team/user entity dirs, and the team's view
		// subdirectories (previously hand-rolled time.Now() blocks).
		"teams-root":       &TeamsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"users-root":       &UsersNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"my-root":          &MyNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"initiatives-root": &InitiativesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"team-dir":         &TeamNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"user-dir":         &UserNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"issues":           &IssuesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"projects":         &ProjectsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"cycles":           &CyclesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"cycle-dir":        &CycleDirNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"recent":           &RecentNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"by-root":          &FilterRootNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"by-category":      &FilterCategoryNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"by-value":         &FilterValueNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
		"my-issues":        &MyIssuesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}},
	}

	for name, node := range nodes {
		t.Run(name, func(t *testing.T) {
			node.setAttr(na)

			var lookup fuse.Attr
			node.fillAttr(&lookup)

			var stat fuse.AttrOut
			if errno := node.Getattr(context.Background(), nil, &stat); errno != 0 {
				t.Fatalf("Getattr errno = %d", errno)
			}

			if lookup != stat.Attr {
				t.Errorf("Lookup answer and Getattr disagree:\n lookup=%+v\n getattr=%+v", lookup, stat.Attr)
			}
			if int64(stat.Attr.Mtime) != updated.Unix() || int64(stat.Attr.Ctime) != created.Unix() {
				t.Errorf("times not deterministic: mtime=%d ctime=%d want mtime=%d ctime=%d",
					stat.Attr.Mtime, stat.Attr.Ctime, updated.Unix(), created.Unix())
			}
		})
	}
}
