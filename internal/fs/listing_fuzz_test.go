package fs

import (
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// listingSeedCorpus is shared by the listing fuzz targets: realistic entity
// names plus the adversarial inputs that break a name->file->name round trip —
// path separators (`/`, `\`), NUL, all-dots (`.`, `..`, `...`), unicode, empty,
// duplicate names, a very long name, and leading/trailing spaces/dots. Each
// target splits these on `\n` into a set of item names, so a single seed doubles
// as both a realistic single name and a duplicate-heavy multi-item list.
var listingSeedCorpus = []string{
	"",
	"Bug",
	"Backend\nFrontend\nBug",
	"Bug\nBug\nBug", // duplicate names -> exercises emit-once / dedup
	"a/b/c",
	"a\\b\\c",
	"with\x00nul",
	".",
	"..",
	"...",
	"   ",
	" leading and trailing dots. ",
	"...leading.dots...",
	"café ☃ 日本語",
	"Q3: Bets",
	"has\nnewline\nembedded",
	strings.Repeat("x", 4096), // very long name
	"foo.png\nfoo.png\nfoo",   // attachment-style collision across extensions
	"blocks\nduplicate\nrelated\nsimilar",
}

// splitNames turns a `\n`-separated fuzz string into the item names a listing is
// built from. It intentionally keeps empty segments — an empty name is a real
// input the derivations must survive.
func splitNames(s string) []string {
	return strings.Split(s, "\n")
}

// assertListedImpliesOpenable is the core listing contract shared by every
// name-derived listing: every name entries() emits, find() must resolve. A
// stranded name (listed but unopenable) is a broken readdir/lookup pair.
func assertListedImpliesOpenable[E any](t *testing.T, label string, names []string, find func(string) (E, bool)) {
	t.Helper()
	for _, name := range names {
		if _, ok := find(name); !ok {
			t.Fatalf("%s: listed name %q not openable via find()", label, name)
		}
	}
}

// assertEmitOnce asserts entries() never emits the same name twice — the
// first-wins/emit-once contract of namedListing and relationListing.
func assertEmitOnce(t *testing.T, label string, names []string) {
	t.Helper()
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, dup := seen[name]; dup {
			t.Fatalf("%s: entries() emitted duplicate name %q", label, name)
		}
		seen[name] = struct{}{}
	}
}

// FuzzNamedListing drives the three real entity->filename derivations (labels,
// documents, milestones) through namedListing. For each derivation it asserts
// listed⇒openable and emit-once. Per ADR-0001, path-safety is NOT asserted on
// these derivations — only on sanitizeFilename (FuzzSanitizeFilename). The
// derivations only uphold listed⇒openable and emit-once, because entries() and
// find() re-derive through the same function, so a weird-but-deterministic name
// round-trips even without a full safety floor.
func FuzzNamedListing(f *testing.F) {
	for _, s := range listingSeedCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		names := splitNames(raw)

		labels := make([]api.Label, len(names))
		docs := make([]api.Document, len(names))
		milestones := make([]api.ProjectMilestone, len(names))
		for i, n := range names {
			labels[i] = api.Label{Name: n}
			// SlugID="" forces the title-sanitization path in documentFilename
			// (the slug shortcut would bypass the derivation under test).
			docs[i] = api.Document{Title: n, SlugID: ""}
			milestones[i] = api.ProjectMilestone{Name: n}
		}

		labelListing := namedListing[api.Label]{items: labels, nameOf: labelFilename}
		docListing := namedListing[api.Document]{items: docs, nameOf: documentFilename}
		msListing := namedListing[api.ProjectMilestone]{items: milestones, nameOf: milestoneFilename}

		// entries()/emit-once and listed⇒openable per derivation.
		{
			entries := labelListing.entries()
			emitted := make([]string, len(entries))
			for i, e := range entries {
				emitted[i] = e.Name
			}
			assertEmitOnce(t, "labels", emitted)
			assertListedImpliesOpenable(t, "labels", emitted, labelListing.find)
		}
		{
			entries := docListing.entries()
			emitted := make([]string, len(entries))
			for i, e := range entries {
				emitted[i] = e.Name
			}
			assertEmitOnce(t, "documents", emitted)
			assertListedImpliesOpenable(t, "documents", emitted, docListing.find)
		}
		{
			entries := msListing.entries()
			emitted := make([]string, len(entries))
			for i, e := range entries {
				emitted[i] = e.Name
			}
			assertEmitOnce(t, "milestones", emitted)
			assertListedImpliesOpenable(t, "milestones", emitted, msListing.find)
		}
	})
}

// FuzzIndexedListing drives indexedListing with position-derived names
// (updateEntryName). Names are index-derived so they must be pairwise distinct;
// the target also assigns synthetic (and deliberately colliding) timestamps to
// exercise equal-lessKey ties, and asserts entries()/find() agree under those
// ties.
func FuzzIndexedListing(f *testing.F) {
	for _, s := range listingSeedCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		names := splitNames(raw)

		// synthUpdate carries a name (used as the "health" segment), plus a
		// timestamp that intentionally collides across items (i/2) to force
		// equal-lessKey ties.
		type synthUpdate struct {
			health string
			at     time.Time
		}
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		items := make([]synthUpdate, len(names))
		for i, n := range names {
			items[i] = synthUpdate{
				health: n,
				at:     base.Add(time.Duration(i/2) * time.Hour), // ties every two items
			}
		}

		l := indexedListing[synthUpdate]{
			items:   items,
			lessKey: func(u synthUpdate) time.Time { return u.at },
			nameOf: func(i int, u synthUpdate) string {
				return updateEntryName(i, u.at, u.health)
			},
		}

		entries := l.entries()
		emitted := make([]string, len(entries))
		for i, e := range entries {
			emitted[i] = e.Name
		}

		// Names are position-derived (the %04d index prefix), so they must be
		// pairwise distinct regardless of duplicate healths or tied timestamps.
		seen := make(map[string]struct{}, len(emitted))
		for _, name := range emitted {
			if _, dup := seen[name]; dup {
				t.Fatalf("indexed: entries() emitted duplicate name %q (names must be position-distinct)", name)
			}
			seen[name] = struct{}{}
		}

		// listed⇒openable, and entries()/find() agree even under equal-lessKey
		// ties: sorted() is stable, so both surfaces share one order.
		assertListedImpliesOpenable(t, "indexed", emitted, l.find)
	})
}

// FuzzAttachmentListing drives attachmentListing with both item families —
// embedded files (named by Filename) and external attachments (named by
// linkName(Title)). It asserts listed⇒openable and the dedup contract: every
// name entries() emits is pairwise unique across BOTH families (one counter
// spans embedded then external).
func FuzzAttachmentListing(f *testing.F) {
	for _, s := range listingSeedCorpus {
		f.Add(s, "")
	}
	// A couple of seeds that cross the two families deliberately.
	f.Add("foo.link", "foo")
	f.Add("image.png\nimage.png", "image.png")
	f.Fuzz(func(t *testing.T, embeddedRaw, externalRaw string) {
		embNames := splitNames(embeddedRaw)
		extNames := splitNames(externalRaw)

		embedded := make([]api.EmbeddedFile, len(embNames))
		for i, n := range embNames {
			embedded[i] = api.EmbeddedFile{Filename: n}
		}
		external := make([]api.Attachment, len(extNames))
		for i, n := range extNames {
			external[i] = api.Attachment{Title: n}
		}

		l := attachmentListing{embedded: embedded, external: external}
		entries := l.entries()
		emitted := make([]string, len(entries))
		for i, e := range entries {
			emitted[i] = e.name
		}

		// Dedup contract: all emitted names pairwise unique.
		seen := make(map[string]struct{}, len(emitted))
		for _, name := range emitted {
			if _, dup := seen[name]; dup {
				t.Fatalf("attachments: entries() emitted duplicate name %q (dedup contract violated)", name)
			}
			seen[name] = struct{}{}
		}

		// listed⇒openable: every emitted name replays through find().
		for _, name := range emitted {
			if _, ok := l.find(name); !ok {
				t.Fatalf("attachments: listed name %q not openable via find()", name)
			}
		}
	})
}

// FuzzRelationListing drives relationListing with both directions. Outgoing
// relations derive from Type + RelatedIssue.Identifier; inverse from
// inverseRelationType(Type) + Issue.Identifier. It asserts listed⇒openable and
// emit-once, and that nil-issue / empty-identifier entries are correctly
// skipped (never emitted, never resolvable).
func FuzzRelationListing(f *testing.F) {
	for _, s := range listingSeedCorpus {
		f.Add(s, "")
	}
	f.Add("blocks\nduplicate\nrelated\nsimilar", "blocks\nrelated")
	relTypes := []string{"blocks", "duplicate", "related", "similar", "custom"}
	f.Fuzz(func(t *testing.T, outgoingRaw, inverseRaw string) {
		outNames := splitNames(outgoingRaw)
		invNames := splitNames(inverseRaw)

		// Build outgoing relations. Every 4th item gets a nil RelatedIssue or an
		// empty identifier to exercise the skip path.
		outgoing := make([]api.IssueRelation, 0, len(outNames))
		for i, ident := range outNames {
			rel := api.IssueRelation{Type: relTypes[i%len(relTypes)]}
			switch i % 4 {
			case 0:
				rel.RelatedIssue = nil // skip: nil issue
			case 1:
				rel.RelatedIssue = &api.ParentIssue{Identifier: ""} // skip: empty identifier
			default:
				rel.RelatedIssue = &api.ParentIssue{Identifier: ident}
			}
			outgoing = append(outgoing, rel)
		}

		inverse := make([]api.IssueRelation, 0, len(invNames))
		for i, ident := range invNames {
			rel := api.IssueRelation{Type: relTypes[i%len(relTypes)]}
			switch i % 4 {
			case 0:
				rel.Issue = nil // skip: nil issue
			case 1:
				rel.Issue = &api.ParentIssue{Identifier: ""} // skip: empty identifier
			default:
				rel.Issue = &api.ParentIssue{Identifier: ident}
			}
			inverse = append(inverse, rel)
		}

		l := relationListing{outgoing: outgoing, inverse: inverse}
		entries := l.entries()
		emitted := make([]string, len(entries))
		for i, e := range entries {
			emitted[i] = e.name
		}

		assertEmitOnce(t, "relations", emitted)
		for _, name := range emitted {
			if _, ok := l.find(name); !ok {
				t.Fatalf("relations: listed name %q not openable via find()", name)
			}
		}

		// Skipped entries (nil issue / empty identifier) never appear. A name
		// derived from an empty identifier would end in "-.rel"; assert none of
		// the *skipped* shapes leaked. We can't reconstruct exact skipped names
		// cheaply, but we can assert the count: entries() must not exceed the
		// number of non-skipped inputs.
		nonSkipped := 0
		for i := range outgoing {
			if outgoing[i].RelatedIssue != nil && outgoing[i].RelatedIssue.Identifier != "" {
				nonSkipped++
			}
		}
		for i := range inverse {
			if inverse[i].Issue != nil && inverse[i].Issue.Identifier != "" {
				nonSkipped++
			}
		}
		if len(entries) > nonSkipped {
			t.Fatalf("relations: entries()=%d exceeds non-skipped inputs=%d (skip path leaked)", len(entries), nonSkipped)
		}
	})
}

// FuzzSanitizeFilename holds sanitizeFilename — and only sanitizeFilename, per
// ADR-0001 — to the path-safety bar: the output never contains `/`, `\`, or NUL,
// and is never "", ".", or "..". These are the properties a name used as a
// virtual filename must have; the per-entity derivations deliberately do not
// carry this floor (see ADR-0001 and FuzzNamedListing).
func FuzzSanitizeFilename(f *testing.F) {
	for _, s := range listingSeedCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := sanitizeFilename(s)
		if strings.Contains(out, "/") {
			t.Errorf("sanitizeFilename(%q) = %q contains '/'", s, out)
		}
		if strings.Contains(out, "\\") {
			t.Errorf("sanitizeFilename(%q) = %q contains '\\'", s, out)
		}
		if strings.Contains(out, "\x00") {
			t.Errorf("sanitizeFilename(%q) = %q contains NUL", s, out)
		}
		if out == "" || out == "." || out == ".." {
			t.Errorf("sanitizeFilename(%q) = %q is not a usable filename", s, out)
		}
	})
}
