package fs

import (
	"fmt"
	"testing"
	"time"
)

// TestMetaSidecarNameMapping pins the pure "X.md" ⇄ "X.meta" derivation both
// Lookup and Readdir route through.
func TestMetaSidecarNameMapping(t *testing.T) {
	t.Parallel()
	if got := metaSidecarName("Bug.md"); got != "Bug.meta" {
		t.Errorf("metaSidecarName(Bug.md) = %q, want Bug.meta", got)
	}
	if got := metaSidecarName("0001-2026-01-02T15-04.md"); got != "0001-2026-01-02T15-04.meta" {
		t.Errorf("metaSidecarName(comment name) = %q", got)
	}

	if md, ok := metaSidecarSource("Bug.meta"); !ok || md != "Bug.md" {
		t.Errorf("metaSidecarSource(Bug.meta) = (%q, %v), want (Bug.md, true)", md, ok)
	}
	// Non-.meta names miss, so item lookups fall through untouched.
	for _, miss := range []string{"Bug.md", "_create", ".error", ".last", "Bug.metadata"} {
		if _, ok := metaSidecarSource(miss); ok {
			t.Errorf("metaSidecarSource(%q) matched, want miss", miss)
		}
	}
}

// TestMetaSidecarRoundTrip extends the listed⇔openable guarantee the listings
// give the .md files to their sidecars: every name metaSidecarEntries emits
// maps back (via metaSidecarSource) to an .md name the same listing resolves —
// for both the named (labels/docs/milestones) and indexed (comments) listings.
func TestMetaSidecarRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("namedListing", func(t *testing.T) {
		l := namedListing[string]{
			items:  []string{"alpha.md", "beta.md", "gamma.md"},
			nameOf: nameBySelf,
		}
		items := l.entries()
		metas := metaSidecarEntries(items)
		if len(metas) != len(items) {
			t.Fatalf("sidecar entries = %d, want one per item (%d)", len(metas), len(items))
		}
		for _, e := range metas {
			mdName, ok := metaSidecarSource(e.Name)
			if !ok {
				t.Errorf("emitted sidecar %q does not map back to an .md", e.Name)
				continue
			}
			if _, found := l.find(mdName); !found {
				t.Errorf("sidecar %q maps to %q, which the listing cannot resolve", e.Name, mdName)
			}
		}
	})

	t.Run("indexedListing", func(t *testing.T) {
		type item struct {
			id string
			at time.Time
		}
		base := time.Unix(1_700_000_000, 0)
		l := indexedListing[item]{
			items:   []item{{"a", base}, {"b", base.Add(time.Hour)}},
			lessKey: func(it item) time.Time { return it.at },
			nameOf: func(i int, it item) string {
				return fmt.Sprintf("%04d-%s.md", i+1, it.at.Format("2006-01-02T15-04"))
			},
		}
		items := l.entries()
		for _, e := range metaSidecarEntries(items) {
			mdName, ok := metaSidecarSource(e.Name)
			if !ok {
				t.Errorf("emitted sidecar %q does not map back to an .md", e.Name)
				continue
			}
			if _, found := l.find(mdName); !found {
				t.Errorf("sidecar %q maps to %q, which the listing cannot resolve", e.Name, mdName)
			}
		}
	})
}
