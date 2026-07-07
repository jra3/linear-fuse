package fs

import (
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// TestAttachmentListingRoundTrip guards the module's core invariant: every
// name entries() emits resolves back through find to the same item, so a file
// you can ls you can also open and rm.
func TestAttachmentListingRoundTrip(t *testing.T) {
	t.Parallel()
	l := attachmentListing{
		embedded: []api.EmbeddedFile{
			{ID: "e1", Filename: "image.png"},
			{ID: "e2", Filename: "image.png"},
			{ID: "e3", Filename: "design.pdf"},
		},
		external: []api.Attachment{
			{ID: "a1", Title: "PR #12"},
			{ID: "a2", Title: "PR #12"},
			{ID: "a3", Title: "Spec doc"},
		},
	}

	entries := l.entries()
	if len(entries) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(entries))
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
		switch {
		case e.embedded != nil:
			if got.embedded == nil || got.embedded.ID != e.embedded.ID {
				t.Errorf("find(%q) resolved to a different item: %+v", e.name, got)
			}
		case e.external != nil:
			if got.external == nil || got.external.ID != e.external.ID {
				t.Errorf("find(%q) resolved to a different item: %+v", e.name, got)
			}
		}
	}

	if _, ok := l.find("nope.png"); ok {
		t.Error("find matched a name no entry has")
	}
}

// TestAttachmentListingDedupNames pins the derived names themselves: counter
// before the extension, external titles sanitized + .link, and one counter
// spanning both families so a cross-family collision disambiguates instead of
// shadowing.
func TestAttachmentListingDedupNames(t *testing.T) {
	t.Parallel()
	l := attachmentListing{
		embedded: []api.EmbeddedFile{
			{ID: "e1", Filename: "image.png"},
			{ID: "e2", Filename: "image.png"},
			{ID: "e3", Filename: "foo.link"}, // collides with the external "foo" below
		},
		external: []api.Attachment{
			{ID: "a1", Title: "foo"},
			{ID: "a2", Title: "foo"},
		},
	}

	want := []string{"image.png", "image (2).png", "foo.link", "foo (2).link", "foo (3).link"}
	entries := l.entries()
	if len(entries) != len(want) {
		t.Fatalf("expected %d entries, got %d", len(want), len(entries))
	}
	for i, e := range entries {
		if e.name != want[i] {
			t.Errorf("entry %d: got %q, want %q (order must be embedded-then-external, input order preserved)", i, e.name, want[i])
		}
	}
}

// TestDeduplicateFilenameEdges covers the extension-split subtleties.
func TestDeduplicateFilenameEdges(t *testing.T) {
	t.Parallel()
	nameCount := make(map[string]int)
	cases := []struct{ in, want string }{
		{"noext", "noext"},
		{"noext", "noext (2)"},
		{"noext", "noext (3)"},
		{".hidden", ".hidden"},
		{".hidden", ".hidden (2)"}, // leading dot is not an extension separator
		{"a.b.c.txt", "a.b.c.txt"},
		{"a.b.c.txt", "a.b.c (2).txt"}, // only the last extension splits
	}
	for _, c := range cases {
		if got := deduplicateFilename(c.in, nameCount); got != c.want {
			t.Errorf("deduplicateFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLinkName pins the external attachment name derivation the create
// surface shares with the listing.
func TestLinkName(t *testing.T) {
	t.Parallel()
	cases := []struct{ title, want string }{
		{"Spec doc", "Spec doc.link"},
		{"a/b\\c", "a-b-c.link"},
		{"  trimmed. ", "trimmed.link"},
		{"", "untitled.link"},
	}
	for _, c := range cases {
		if got := linkName(api.Attachment{Title: c.title}); got != c.want {
			t.Errorf("linkName(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}
