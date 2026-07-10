package fs

import (
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// TestLinkListingRoundTrip guards the module's core invariant: every name
// entries() emits resolves back through find to the same link, so a file you
// can ls you can also open and rm.
func TestLinkListingRoundTrip(t *testing.T) {
	t.Parallel()
	l := linkListing{
		links: []api.EntityExternalLink{
			{ID: "l1", Label: "Onboarding Sync"},
			{ID: "l2", Label: "Onboarding Sync"}, // duplicate label
			{ID: "l3", Label: "Design"},
		},
	}

	entries := l.entries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	seen := make(map[string]bool)
	for _, e := range entries {
		if seen[e.name] {
			t.Errorf("duplicate name emitted: %q", e.name)
		}
		seen[e.name] = true

		got, ok := l.find(e.name)
		if !ok {
			t.Errorf("entries() emitted %q but find missed it", e.name)
			continue
		}
		if got.link == nil || got.link.ID != e.link.ID {
			t.Errorf("find(%q) resolved to a different link: %+v", e.name, got)
		}
	}

	if _, ok := l.find("nope.link"); ok {
		t.Error("find matched a name no entry has")
	}
}

// TestLinkListingDedupNames pins the derived names: labels sanitized + .link,
// with a counter before the extension for collisions.
func TestLinkListingDedupNames(t *testing.T) {
	t.Parallel()
	l := linkListing{
		links: []api.EntityExternalLink{
			{ID: "l1", Label: "foo"},
			{ID: "l2", Label: "foo"},
			{ID: "l3", Label: "a/b"},
		},
	}

	want := []string{"foo.link", "foo (2).link", "a-b.link"}
	entries := l.entries()
	if len(entries) != len(want) {
		t.Fatalf("expected %d entries, got %d", len(want), len(entries))
	}
	for i, e := range entries {
		if e.name != want[i] {
			t.Errorf("entry %d: got %q, want %q", i, e.name, want[i])
		}
	}
}

// TestExternalLinkName pins the link name derivation the create surface shares
// with the listing.
func TestExternalLinkName(t *testing.T) {
	t.Parallel()
	cases := []struct{ label, want string }{
		{"Spec doc", "Spec doc.link"},
		{"a/b\\c", "a-b-c.link"},
		{"  trimmed. ", "trimmed.link"},
		{"", "untitled.link"},
	}
	for _, c := range cases {
		if got := externalLinkName(api.EntityExternalLink{Label: c.label}); got != c.want {
			t.Errorf("externalLinkName(%q) = %q, want %q", c.label, got, c.want)
		}
	}
}
