package fs

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
)

// listingDir's Readdir assembly (readdir) and Lookup classification (resolve)
// are the branchy parts it newly owns for attachments/relations/links: the trio
// + nameOf projection, the on-fetch-error Readdir policy, the optional
// preFilter, and the EIO-vs-ENOENT symmetry. Both are exercised here without a
// mount, the way collectiondir_test pins collectionDir's entries/classify. The
// mount-level behavior (trio short-circuit, build, delete coherence) stays
// covered by the integration tests these three nodes already carry.

// The infoListing seam is satisfied structurally by all three concrete listings.
var (
	_ infoListing[attachmentEntry] = attachmentListing{}
	_ infoListing[relationEntry]   = relationListing{}
	_ infoListing[linkEntry]       = linkListing{}
)

// fakeEntry is a minimal listing entry: just a name.
type fakeEntry struct{ name string }

// fakeListing is an in-memory infoListing[fakeEntry] with an optional fetch
// error, standing in for the repo-backed listings so the pure orchestration is
// testable on literals.
type fakeListing struct {
	names []string
	err   error
}

func (l fakeListing) entries() []fakeEntry {
	out := make([]fakeEntry, len(l.names))
	for i, n := range l.names {
		out[i] = fakeEntry{name: n}
	}
	return out
}

func (l fakeListing) find(name string) (fakeEntry, bool) {
	for _, e := range l.entries() {
		if e.name == name {
			return e, true
		}
	}
	return fakeEntry{}, false
}

// testListingDir builds a listingDir[fakeEntry] over the given listing. A nil
// listing pointer yields an empty best-effort listing carrying err.
func testListingDir(names []string, err error) listingDir[fakeEntry] {
	return listingDir[fakeEntry]{
		trio: collectionTrio{
			kind:     "tests",
			parentID: "p1",
			onFlush:  func(context.Context, []byte) syscall.Errno { return 0 },
		},
		listing: func(_ context.Context, fetchErr *error) infoListing[fakeEntry] {
			if err != nil && fetchErr != nil {
				*fetchErr = err
			}
			return fakeListing{names: names, err: err}
		},
		nameOf: func(e fakeEntry) string { return e.name },
	}
}

func streamNameSet(t *testing.T, d listingDir[fakeEntry]) map[string]bool {
	t.Helper()
	stream, errno := d.readdir(context.Background())
	if errno != 0 {
		t.Fatalf("readdir errno = %v, want 0", errno)
	}
	m := make(map[string]bool)
	for stream.HasNext() {
		e, errno := stream.Next()
		if errno != 0 {
			t.Fatalf("stream.Next errno = %v", errno)
		}
		m[e.Name] = true
	}
	return m
}

func TestListingDirReaddirAssembly(t *testing.T) {
	t.Parallel()

	// Empty: the trio surfaces only.
	empty := streamNameSet(t, testListingDir(nil, nil))
	for _, want := range []string{"_create", ".error", ".last"} {
		if !empty[want] {
			t.Errorf("empty readdir missing trio surface %q", want)
		}
	}
	if empty["a.rel"] {
		t.Error("empty readdir should carry no item files")
	}

	// Two items: trio + one DirEntry per listing entry, projected by nameOf.
	got := streamNameSet(t, testListingDir([]string{"a.rel", "b.rel"}, nil))
	for _, want := range []string{"_create", ".error", ".last", "a.rel", "b.rel"} {
		if !got[want] {
			t.Errorf("readdir missing %q", want)
		}
	}
}

func TestListingDirReaddirErrorPolicy(t *testing.T) {
	t.Parallel()
	boom := errors.New("fetch failed")

	// Best-effort (links/attachments): a fetch error lists what succeeded
	// rather than failing the directory. The fake carries no names on error,
	// so only the trio remains — but the call succeeds.
	best := testListingDir(nil, boom) // failReaddirOnError defaults false
	got := streamNameSet(t, best)
	if !got["_create"] {
		t.Error("best-effort readdir should still list the trio on fetch error")
	}

	// Fail-hard (relations): a fetch error fails the whole directory with EIO.
	hard := testListingDir(nil, boom)
	hard.failReaddirOnError = true
	if _, errno := hard.readdir(context.Background()); errno != syscall.EIO {
		t.Errorf("fail-hard readdir errno = %v, want EIO", errno)
	}
}

func TestListingDirResolve(t *testing.T) {
	t.Parallel()
	boom := errors.New("fetch failed")

	cases := []struct {
		desc    string
		d       listingDir[fakeEntry]
		name    string
		want    infoLookupKind
		wantHit string
	}{
		{"hit", testListingDir([]string{"a.rel", "b.rel"}, nil), "a.rel", infoHit, "a.rel"},
		{"clean miss", testListingDir([]string{"a.rel"}, nil), "z.rel", infoNotFound, ""},
		{"fetch error", testListingDir(nil, boom), "a.rel", infoFetchErr, ""},
	}
	for _, tc := range cases {
		res := tc.d.resolve(context.Background(), tc.name)
		if res.kind != tc.want {
			t.Errorf("%s: resolve(%q) kind = %v, want %v", tc.desc, tc.name, res.kind, tc.want)
		}
		if tc.wantHit != "" && res.entry.name != tc.wantHit {
			t.Errorf("%s: resolve(%q) entry = %q, want %q", tc.desc, tc.name, res.entry.name, tc.wantHit)
		}
	}
}

func TestListingDirResolvePreFilter(t *testing.T) {
	t.Parallel()

	// preFilter rejects any name not ending ".rel" BEFORE the fetch — so even a
	// listing that would error on fetch returns a clean ENOENT (notFound), never
	// EIO, for a rejected name.
	d := testListingDir(nil, errors.New("must not be reached"))
	d.preFilter = func(name string) bool { return strings.HasSuffix(name, ".rel") }

	if res := d.resolve(context.Background(), ".swp"); res.kind != infoNotFound {
		t.Errorf("resolve(.swp) with preFilter = %v, want infoNotFound (no fetch)", res.kind)
	}

	// A name the preFilter accepts still reaches the fetch (and here errors).
	if res := d.resolve(context.Background(), "a.rel"); res.kind != infoFetchErr {
		t.Errorf("resolve(a.rel) with preFilter = %v, want infoFetchErr", res.kind)
	}
}
