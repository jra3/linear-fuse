package fs

import (
	"context"
	"errors"
	"syscall"
	"testing"

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
