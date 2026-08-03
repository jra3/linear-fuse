package fs

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// collectionDir's Readdir assembly (entries) and Lookup classification
// (classify) are the branchy parts where the .meta-shadow round-trip bugs hide.
// Both are pure — exercised here without a mount, the way manifest_test pins
// dirManifest's entries/find. The mount-level behavior (open, delete,
// coherence) stays covered by the integration tests these four nodes already
// carry; in particular the delete tests exercise unlink's dirIno derivation
// end-to-end (a wrong dir inode leaves the removed item lingering in cache).

// The listing seam is satisfied structurally by both concrete listings.
var (
	_ collectionListing[api.Comment]  = indexedListing[api.Comment]{}
	_ collectionListing[api.Document] = namedListing[api.Document]{}
)

// testCollectionDir builds a collectionDir over plain strings named "<s>.md",
// enough to exercise the pure entry-assembly and classification.
func testCollectionDir() collectionDir[string] {
	return collectionDir[string]{
		trio: collectionTrio{
			kind:     "tests",
			parentID: "p1",
			onFlush:  func(context.Context, []byte) syscall.Errno { return 0 },
		},
		listing: func(items []string) collectionListing[string] {
			return namedListing[string]{items: items, nameOf: func(s string) string { return s + ".md" }}
		},
		idOf: func(s string) string { return s },
	}
}

func entryNameSet(es []fuse.DirEntry) map[string]bool {
	m := make(map[string]bool, len(es))
	for _, e := range es {
		m[e.Name] = true
	}
	return m
}

func TestCollectionDirEntries(t *testing.T) {
	t.Parallel()
	cd := testCollectionDir()

	// Empty: the trio surfaces only, no item files.
	empty := entryNameSet(cd.entries(nil))
	for _, want := range []string{"_create", ".error", ".last"} {
		if !empty[want] {
			t.Errorf("empty entries missing trio surface %q", want)
		}
	}
	if empty["a.md"] {
		t.Error("empty entries should carry no item files")
	}

	// Two items: trio + each item's .md and its .meta sidecar.
	got := entryNameSet(cd.entries([]string{"a", "b"}))
	for _, want := range []string{"_create", ".error", ".last", "a.md", "b.md", "a.meta", "b.meta"} {
		if !got[want] {
			t.Errorf("entries missing %q", want)
		}
	}
}

func TestCollectionDirClassify(t *testing.T) {
	t.Parallel()
	cd := testCollectionDir()
	items := []string{"a", "b"}

	cases := []struct {
		name string
		want lookupKind
		item string // expected item for a hit
	}{
		{"a.md", lookupFile, "a"},
		{"b.md", lookupFile, "b"},
		{"a.meta", lookupMeta, "a"},    // ".meta" shadows the ".md"
		{"z.md", lookupNotFound, ""},   // no such item
		{"z.meta", lookupNotFound, ""}, // ".meta" of a missing item
	}
	for _, tc := range cases {
		res := cd.classify(tc.name, items)
		if res.kind != tc.want {
			t.Errorf("classify(%q) kind = %v, want %v", tc.name, res.kind, tc.want)
		}
		if tc.item != "" && res.item != tc.item {
			t.Errorf("classify(%q) item = %q, want %q", tc.name, res.item, tc.item)
		}
	}
}

// TestCollectionDirResolve pins the shared ctx-ful find that Unlink and both
// Rename specs delegate to: a hit returns the item, a clean miss is (nil, nil)
// (the contract commitDelete/commitRename expect), and a fetch failure
// propagates the error. It resolves the same names classify resolves, so Rename
// can never ENOENT an entity Lookup/Unlink still find (#293).
func TestCollectionDirResolve(t *testing.T) {
	t.Parallel()
	cd := testCollectionDir()
	cd.fetch = func(context.Context) ([]string, error) { return []string{"a", "b"}, nil }

	got, err := cd.resolve(context.Background(), "a.md")
	if err != nil || got == nil || *got != "a" {
		t.Errorf("resolve(a.md) = (%v, %v), want (&\"a\", nil)", got, err)
	}

	got, err = cd.resolve(context.Background(), "z.md")
	if err != nil || got != nil {
		t.Errorf("resolve(z.md) = (%v, %v), want (nil, nil) on a clean miss", got, err)
	}

	cd.fetch = func(context.Context) ([]string, error) { return nil, errors.New("db down") }
	if _, err := cd.resolve(context.Background(), "a.md"); err == nil {
		t.Error("resolve must propagate a fetch error, not swallow it")
	}
}

// TestCollectionDirItemFileTarget pins where an editor's atomic save is allowed
// to land inside a dynamic collection (#438). The tail itself (EXDEV, scratch
// lookup, adopt/consume/invalidate) is renameSave's and is covered in
// renamesave_test.go; what is collection-specific — and what the entity
// directories have no analogue of — is that a destination can be EITHER an
// existing item to replace OR a name that does not exist yet, which is a create.
// A wrong destination must refuse before any mutation and say why in .error, so
// the scratch buffer stays intact for a corrected rename.
func TestCollectionDirItemFileTarget(t *testing.T) {
	t.Parallel()

	// buildFile is reached only on the replace branch, and only through
	// flushOnto; returning an errno from it stops before the inode is touched
	// (no mount here) while still proving the branch ran.
	const buildProbe = syscall.EPERM

	newTarget := func(items ...string) (collectionDir[string], *LinearFS) {
		lfs := &LinearFS{writeFeedback: newWriteFeedback(nil)}
		cd := testCollectionDir()
		cd.lfs = lfs
		cd.noun = "test item"
		cd.fetch = func(context.Context) ([]string, error) { return items, nil }
		cd.buildFile = func(context.Context, string, string, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
			return nil, buildProbe
		}
		return cd, lfs
	}
	errKey := collectionErrorKey("tests", "p1")

	t.Run("existing name replaces that item", func(t *testing.T) {
		created := false
		cd, lfs := newTarget("a")

		target, errno := cd.itemFileTarget(func(context.Context, []byte) syscall.Errno {
			created = true
			return 0
		})(context.Background(), "a.md.tmp.1", "a.md")
		if errno != 0 {
			t.Fatalf("errno = %v, want 0 (a.md names an existing item)", errno)
		}
		// The save routes through the item's own edit path, not the create
		// trigger — otherwise an atomic save over a doc would mint a duplicate.
		if got := target.flush(context.Background(), []byte("x")); got != buildProbe {
			t.Errorf("flush reached %v, want the item's own node build (%v)", got, buildProbe)
		}
		if created {
			t.Error("replacing an existing item must not run the create trigger")
		}
		// The item's inode and entity are editFlush's business: it upserts the
		// fresh entity to SQLite and lists the inode in its own coherence set, so
		// naming them again here could only drift from that.
		if target.adopt != nil || target.fileIno != 0 {
			t.Errorf("target = {adopt:%v fileIno:%d}, want both empty for a collection item",
				target.adopt != nil, target.fileIno)
		}
		if lfs.GetWriteError(errKey) != nil {
			t.Error("an accepted destination must record no .error")
		}
	})

	t.Run("unknown name creates", func(t *testing.T) {
		created := false
		cd, _ := newTarget("a")

		target, errno := cd.itemFileTarget(func(ctx context.Context, b []byte) syscall.Errno {
			created = true
			return 0
		})(context.Background(), "new.md.tmp.1", "new.md")
		if errno != 0 {
			t.Fatalf("errno = %v, want 0 (a new .md name is a create)", errno)
		}
		if got := target.flush(context.Background(), []byte("x")); got != 0 || !created {
			t.Errorf("flush = %v, created = %v; want the create trigger to run", got, created)
		}
	})

	t.Run("non-md destination is refused", func(t *testing.T) {
		cd, lfs := newTarget("a")

		_, errno := cd.itemFileTarget(nil)(context.Background(), "a.md.tmp.1", "a.md.tmp.2")
		if errno != syscall.EINVAL {
			t.Fatalf("errno = %v, want EINVAL", errno)
		}
		werr := lfs.GetWriteError(errKey)
		if werr == nil {
			t.Fatal("a refused destination must explain itself in .error")
		}
		// The message has to name both the rejected rename and the naming rule;
		// the errno alone cannot carry either.
		for _, want := range []string{"rename a.md.tmp.1 -> a.md.tmp.2", "<name>.md"} {
			if !strings.Contains(werr.Message, want) {
				t.Errorf(".error %q does not mention %q", werr.Message, want)
			}
		}
	})

	t.Run("unknown name without a create surface is refused", func(t *testing.T) {
		cd, lfs := newTarget("a")

		_, errno := cd.itemFileTarget(nil)(context.Background(), "new.md.tmp.1", "new.md")
		if errno != syscall.ENOENT {
			t.Fatalf("errno = %v, want ENOENT", errno)
		}
		if lfs.GetWriteError(errKey) == nil {
			t.Error("a refused destination must explain itself in .error")
		}
	})

	t.Run("unreadable listing refuses rather than guessing", func(t *testing.T) {
		cd, lfs := newTarget()
		cd.fetch = func(context.Context) ([]string, error) { return nil, errors.New("db down") }

		_, errno := cd.itemFileTarget(func(context.Context, []byte) syscall.Errno { return 0 })(
			context.Background(), "a.md.tmp.1", "a.md")
		// Without the listing, replace and create are indistinguishable — and
		// guessing create would duplicate the entity.
		if errno != syscall.EIO {
			t.Fatalf("errno = %v, want EIO", errno)
		}
		if lfs.GetWriteError(errKey) == nil {
			t.Error("a refused destination must explain itself in .error")
		}
	})
}
