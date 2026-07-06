package fs

import (
	"fmt"
	"testing"
	"time"
)

// TestIndexedListingRoundTrip is the anti-drift guarantee in executable form:
// for every name entries() produces, find(name) must return the matching item,
// over the same canonical order — so a collection's Readdir, Lookup, and Unlink
// can never disagree. It feeds unsorted items (including two sharing a second)
// to prove the sort is owned by the module and that same-second items still get
// distinct names via the 1-based index.
func TestIndexedListingRoundTrip(t *testing.T) {
	t.Parallel()

	type item struct {
		id string
		at time.Time
	}
	base := time.Unix(1_700_000_000, 0)
	items := []item{
		{"c", base.Add(2 * time.Hour)},
		{"a", base},
		{"b-same-second", base}, // duplicate timestamp — index must still disambiguate
		{"d", base.Add(3 * time.Hour)},
	}

	l := indexedListing[item]{
		items:   items,
		lessKey: func(it item) time.Time { return it.at },
		nameOf: func(i int, it item) string {
			return fmt.Sprintf("%04d-%s.md", i+1, it.at.Format("2006-01-02T15-04"))
		},
	}

	entries := l.entries()
	if len(entries) != len(items) {
		t.Fatalf("entries() len = %d, want %d", len(entries), len(items))
	}

	// Every listed name resolves back to exactly one item, and names are unique.
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Name] {
			t.Errorf("duplicate entry name %q — index failed to disambiguate", e.Name)
		}
		seen[e.Name] = true
		if _, ok := l.find(e.Name); !ok {
			t.Errorf("entries() produced %q but find() could not locate it", e.Name)
		}
	}

	// find is ordered by lessKey, not input order: the earliest two items (same
	// second) occupy slots 1 and 2, stably in input order.
	if got, _ := l.find("0001-" + base.Format("2006-01-02T15-04") + ".md"); got.id != "a" {
		t.Errorf("slot 1 = %q, want a (earliest, stable)", got.id)
	}
	if got, _ := l.find("0002-" + base.Format("2006-01-02T15-04") + ".md"); got.id != "b-same-second" {
		t.Errorf("slot 2 = %q, want b-same-second", got.id)
	}

	if _, ok := l.find("9999-nonexistent.md"); ok {
		t.Error("find() matched a name no entry produced")
	}
}

// TestUpdateEntryName pins the shared status-update filename format used by both
// the project and initiative update collections.
func TestUpdateEntryName(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	if got := updateEntryName(0, at, "onTrack"); got != "0001-2026-01-15-ontrack.md" {
		t.Errorf("updateEntryName = %q, want 0001-2026-01-15-ontrack.md", got)
	}
	// Health is lower-cased; index is 1-based and zero-padded to four digits.
	if got := updateEntryName(11, at, "AtRisk"); got != "0012-2026-01-15-atrisk.md" {
		t.Errorf("updateEntryName = %q, want 0012-2026-01-15-atrisk.md", got)
	}
}
