package fs

import (
	"testing"
)

// nameBySelf is a nameOf that treats each string item as its own filename.
func nameBySelf(s string) string { return s }

// TestNamedListingRoundTrip is the core invariant: every name entries() emits
// resolves back through find(). Same contract TestIndexedListingRoundTrip guards
// for the indexed sibling.
func TestNamedListingRoundTrip(t *testing.T) {
	l := namedListing[string]{
		items:  []string{"alpha.md", "beta.md", "gamma.md"},
		nameOf: nameBySelf,
	}
	for _, e := range l.entries() {
		if _, ok := l.find(e.Name); !ok {
			t.Errorf("entries() emitted %q but find() could not resolve it", e.Name)
		}
	}
}

// TestNamedListingCollisionFirstWins guards the collision contract settled on
// evidence: two items deriving the same name emit that name exactly once, and
// find returns the FIRST item in items order. The second item is shadowed — a
// tested contract, not an accident, mirroring how Linear itself shadows
// same-named cross-scope entities.
func TestNamedListingCollisionFirstWins(t *testing.T) {
	type label struct{ id, name string }
	l := namedListing[label]{
		items: []label{
			{id: "workspace", name: "Bug.md"},
			{id: "team", name: "Bug.md"},
			{id: "other", name: "Chore.md"},
		},
		nameOf: func(x label) string { return x.name },
	}

	// entries() emits each name once.
	got := l.entries()
	if len(got) != 2 {
		t.Fatalf("entries() = %d entries, want 2 (collision collapsed): %+v", len(got), got)
	}
	seen := map[string]int{}
	for _, e := range got {
		seen[e.Name]++
	}
	if seen["Bug.md"] != 1 {
		t.Errorf("entries() emitted Bug.md %d times, want 1 (no duplicate dirent)", seen["Bug.md"])
	}

	// find() returns the first item in items order.
	if hit, ok := l.find("Bug.md"); !ok || hit.id != "workspace" {
		t.Errorf("find(Bug.md) = (%+v, %v), want the first item (id=workspace)", hit, ok)
	}
}

// TestNamedListingPreservesOrder: distinct names are emitted in items order, not
// sorted — determinism is the repo's ORDER BY, never this module's.
func TestNamedListingPreservesOrder(t *testing.T) {
	items := []string{"gamma.md", "alpha.md", "beta.md"} // deliberately unsorted
	l := namedListing[string]{items: items, nameOf: nameBySelf}

	got := l.entries()
	if len(got) != len(items) {
		t.Fatalf("entries() = %d, want %d", len(got), len(items))
	}
	for i, e := range got {
		if e.Name != items[i] {
			t.Errorf("entries()[%d] = %q, want %q (items order preserved)", i, e.Name, items[i])
		}
	}
}

// TestNamedListingTotality: find of an absent name is a clean miss, and an empty
// listing yields no entries.
func TestNamedListingTotality(t *testing.T) {
	l := namedListing[string]{items: []string{"only.md"}, nameOf: nameBySelf}
	if _, ok := l.find("missing.md"); ok {
		t.Error("find(missing.md) = true, want false")
	}

	empty := namedListing[string]{items: nil, nameOf: nameBySelf}
	if got := empty.entries(); len(got) != 0 {
		t.Errorf("empty.entries() = %d entries, want 0", len(got))
	}
	if _, ok := empty.find("anything"); ok {
		t.Error("empty.find() = true, want false")
	}
}
