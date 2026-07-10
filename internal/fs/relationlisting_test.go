package fs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/repo"
	"gopkg.in/yaml.v3"
)

// TestRelationListingRoundTrip guards the module's core invariant: every name
// entries() emits resolves back through find to the same relation with the
// same direction, so a file you can ls you can also open and rm.
func TestRelationListingRoundTrip(t *testing.T) {
	t.Parallel()
	l := relationListing{
		outgoing: []api.IssueRelation{
			{ID: "r1", Type: "blocks", RelatedIssue: &api.ParentIssue{Identifier: "ENG-1"}},
			{ID: "r2", Type: "related", RelatedIssue: &api.ParentIssue{Identifier: "ENG-2"}},
		},
		inverse: []api.IssueRelation{
			{ID: "r3", Type: "blocks", Issue: &api.ParentIssue{Identifier: "ENG-3"}},
			{ID: "r4", Type: "duplicate", Issue: &api.ParentIssue{Identifier: "ENG-4"}},
		},
	}

	entries := l.entries()
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
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
		if got.relation.ID != e.relation.ID || got.isInverse != e.isInverse {
			t.Errorf("find(%q) = (id=%s, isInverse=%v), want (id=%s, isInverse=%v)",
				e.name, got.relation.ID, got.isInverse, e.relation.ID, e.isInverse)
		}
	}

	if _, ok := l.find("blocks-ENG-999.rel"); ok {
		t.Error("find matched a name no entry has")
	}
}

// TestRelationListingDirections pins the direction split: an outgoing
// relation names itself from the raw type and the related issue, an inverse
// one from the inverted type and the source issue — and find reports which
// side derived the match (Unlink and the render depend on it).
func TestRelationListingDirections(t *testing.T) {
	t.Parallel()
	l := relationListing{
		outgoing: []api.IssueRelation{
			{ID: "out", Type: "blocks", RelatedIssue: &api.ParentIssue{Identifier: "ENG-1"}},
		},
		inverse: []api.IssueRelation{
			{ID: "in", Type: "blocks", Issue: &api.ParentIssue{Identifier: "ENG-2"}},
		},
	}

	got, ok := l.find("blocks-ENG-1.rel")
	if !ok || got.relation.ID != "out" || got.isInverse {
		t.Errorf("find(blocks-ENG-1.rel) = (%+v, %v), want the outgoing relation with isInverse=false", got, ok)
	}

	got, ok = l.find("blocked-by-ENG-2.rel")
	if !ok || got.relation.ID != "in" || !got.isInverse {
		t.Errorf("find(blocked-by-ENG-2.rel) = (%+v, %v), want the inverse relation with isInverse=true", got, ok)
	}

	// The inverse relation must NOT also answer to its raw-type name.
	if _, ok := l.find("blocks-ENG-2.rel"); ok {
		t.Error("find matched an inverse relation by its un-inverted type name")
	}
}

// TestInverseRelationType pins the type inversion for all four relation types
// plus the unknown-type fallback.
func TestInverseRelationType(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"blocks", "blocked-by"},
		{"duplicate", "duplicated-by"},
		{"related", "related-to"},
		{"similar", "similar-to"},
		{"mystery", "mystery-inverse"},
	}
	for _, c := range cases {
		if got := inverseRelationType(c.in); got != c.want {
			t.Errorf("inverseRelationType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRelationListingSkipsIncomplete: a relation with a nil issue or an empty
// identifier would derive a broken name, so it produces no entry and no find
// match — today's guard, kept.
func TestRelationListingSkipsIncomplete(t *testing.T) {
	t.Parallel()
	l := relationListing{
		outgoing: []api.IssueRelation{
			{ID: "r1", Type: "blocks", RelatedIssue: nil},
			{ID: "r2", Type: "blocks", RelatedIssue: &api.ParentIssue{Identifier: ""}},
		},
		inverse: []api.IssueRelation{
			{ID: "r3", Type: "blocks", Issue: nil},
			{ID: "r4", Type: "blocks", Issue: &api.ParentIssue{Identifier: ""}},
		},
	}
	if got := l.entries(); len(got) != 0 {
		t.Errorf("entries() = %d entries, want 0 (incomplete relations skipped): %+v", len(got), got)
	}
	if _, ok := l.find("blocks-.rel"); ok {
		t.Error("find matched a relation with an empty identifier")
	}
}

// TestRelationListingCollisionFirstWins guards the collision contract
// inherited from namedListing: duplicate-name inputs emit one dirent and find
// returns the first, so readdir and find agree by construction. The .rel name
// is a resolution key (rm deletes what find matched) — disambiguation would
// mint names that resolve nowhere.
func TestRelationListingCollisionFirstWins(t *testing.T) {
	t.Parallel()
	l := relationListing{
		outgoing: []api.IssueRelation{
			{ID: "first", Type: "blocks", RelatedIssue: &api.ParentIssue{Identifier: "ENG-1"}},
			{ID: "second", Type: "blocks", RelatedIssue: &api.ParentIssue{Identifier: "ENG-1"}},
		},
	}

	entries := l.entries()
	if len(entries) != 1 {
		t.Fatalf("entries() = %d entries, want 1 (collision collapsed): %+v", len(entries), entries)
	}
	if entries[0].name != "blocks-ENG-1.rel" {
		t.Errorf("entries()[0].name = %q, want %q", entries[0].name, "blocks-ENG-1.rel")
	}
	if hit, ok := l.find("blocks-ENG-1.rel"); !ok || hit.relation.ID != "first" {
		t.Errorf("find(blocks-ENG-1.rel) = (id=%s, %v), want the first item (id=first)", hit.relation.ID, ok)
	}
}

// TestRelationsNodeListingFetchErrSeam exercises the listing(ctx, &fetchErr)
// seam Lookup uses to distinguish couldn't-look from not-found: a healthy
// store with no matching relation leaves fetchErr nil (a genuine miss →
// ENOENT), while a failing store sets it (→ EIO). Before this seam, Lookup
// discarded fetch errors and misreported a store failure as ENOENT.
func TestRelationsNodeListingFetchErrSeam(t *testing.T) {
	t.Parallel()

	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	lfs := &LinearFS{}
	lfs.repo = repo.NewSQLiteRepository(store, nil)

	ctx := context.Background()
	n := &RelationsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, issueID: "issue-1"}

	// Healthy store, no relations: a miss with no fetch error (→ ENOENT).
	var fetchErr error
	if _, ok := n.listing(ctx, &fetchErr).find("blocks-ENG-1.rel"); ok {
		t.Error("find matched a relation in an empty store")
	}
	if fetchErr != nil {
		t.Errorf("fetchErr = %v on a healthy store, want nil", fetchErr)
	}

	// Failing store: the miss carries the fetch error (→ EIO).
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close failed: %v", err)
	}
	fetchErr = nil
	if _, ok := n.listing(ctx, &fetchErr).find("blocks-ENG-1.rel"); ok {
		t.Error("find matched a relation against a closed store")
	}
	if fetchErr == nil {
		t.Error("fetchErr = nil on a closed store, want the fetch error recorded")
	}
}

// TestRelationContent pins the rendered YAML body for both directions: field
// order (type, to/from, title), title omitted when empty, and — the recorded
// behavior change — a colon-bearing title renders VALID YAML (the old
// hand-Sprintf emitted it unquoted, the same bug class as the catalog fix).
func TestRelationContent(t *testing.T) {
	t.Parallel()

	outgoing := api.IssueRelation{
		Type:         "blocks",
		RelatedIssue: &api.ParentIssue{Identifier: "ENG-1", Title: "Fix the bug"},
	}
	if got, want := relationContent(outgoing, false), "type: blocks\nto: ENG-1\ntitle: Fix the bug\n"; got != want {
		t.Errorf("outgoing render = %q, want %q", got, want)
	}

	inverse := api.IssueRelation{
		Type:  "blocks",
		Issue: &api.ParentIssue{Identifier: "ENG-2", Title: "Upstream work"},
	}
	if got, want := relationContent(inverse, true), "type: blocks\nfrom: ENG-2\ntitle: Upstream work\n"; got != want {
		t.Errorf("inverse render = %q, want %q", got, want)
	}

	untitled := api.IssueRelation{
		Type:         "related",
		RelatedIssue: &api.ParentIssue{Identifier: "ENG-3"},
	}
	if got, want := relationContent(untitled, false), "type: related\nto: ENG-3\n"; got != want {
		t.Errorf("untitled render = %q, want %q (title omitted when empty)", got, want)
	}

	// A colon in the title demands quoting; the output must parse back as
	// YAML with the title intact.
	colon := api.IssueRelation{
		Type:         "related",
		RelatedIssue: &api.ParentIssue{Identifier: "ENG-4", Title: "Q3: Bets"},
	}
	rendered := relationContent(colon, false)
	var parsed struct {
		Type  string `yaml:"type"`
		To    string `yaml:"to"`
		Title string `yaml:"title"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatalf("colon-bearing title rendered invalid YAML: %v\n%s", err, rendered)
	}
	if parsed.Title != "Q3: Bets" || parsed.To != "ENG-4" || parsed.Type != "related" {
		t.Errorf("colon-title round-trip = %+v, want the original values intact", parsed)
	}
}

// TestParseRelationInput covers the _create command syntax: explicit type,
// bare identifier defaulting to "related", the empty-content FieldError, and
// the invalid-type FieldError.
func TestParseRelationInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		in             string
		wantType       string
		wantIdentifier string
		wantErrField   string // non-empty: expect a *FieldError on this field
	}{
		{"explicit type", "blocks ENG-123", "blocks", "ENG-123", ""},
		{"bare identifier defaults to related", "ENG-123", "related", "ENG-123", ""},
		{"surrounding whitespace", "  similar ENG-7 \n", "similar", "ENG-7", ""},
		{"empty", "", "", "", "content"},
		{"whitespace only", "  \n ", "", "", "content"},
		{"invalid type", "bogus ENG-1", "", "", "type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relType, identifier, err := parseRelationInput(tt.in)
			if tt.wantErrField != "" {
				var ferr *FieldError
				if !errors.As(err, &ferr) {
					t.Fatalf("parseRelationInput(%q) err = %v, want *FieldError on %q", tt.in, err, tt.wantErrField)
				}
				if ferr.Field != tt.wantErrField {
					t.Errorf("parseRelationInput(%q) FieldError.Field = %q, want %q", tt.in, ferr.Field, tt.wantErrField)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRelationInput(%q) unexpected err: %v", tt.in, err)
			}
			if relType != tt.wantType || identifier != tt.wantIdentifier {
				t.Errorf("parseRelationInput(%q) = (%q, %q), want (%q, %q)",
					tt.in, relType, identifier, tt.wantType, tt.wantIdentifier)
			}
		})
	}
}
