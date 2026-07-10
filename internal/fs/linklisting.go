package fs

import (
	"github.com/jra3/linear-fuse/internal/api"
)

// linkListing owns the filenames of a links/ directory — the project/initiative
// "Links / Resources" surface. Unlike attachmentListing it holds one item family
// (external links), so it is a thin wrapper: both Readdir and Lookup derive names
// through it over one canonical order, so a file you can `ls` you can also open
// and `rm`.
//
// Collisions are DEDUPLICATED (`foo (2).link`) via the shared deduplicateFilename
// counter, the same freedom attachmentListing has and for the same reason: link
// filenames are resolution keys nowhere else (nothing name-resolves an external
// link). Ordering is the repo's job: the list queries carry a deterministic
// ORDER BY (sort_order, id) so a name keeps its dedup suffix across calls. The
// caller fetches and passes the slice; the module is pure and unit-testable on
// literals.
type linkListing struct {
	links []api.EntityExternalLink
}

// linkEntry is one derived directory entry: the final (deduplicated) name plus
// the link it names.
type linkEntry struct {
	name string
	link *api.EntityExternalLink
}

// externalLinkName derives an external link's base filename (before dedup). The
// create surface reuses it for its .last path and kernel-entry name, so the
// derivation is written exactly once.
func externalLinkName(link api.EntityExternalLink) string {
	return sanitizeFilename(link.Label) + ".link"
}

// entries derives every entry's final name through one shared dedup counter —
// the Readdir projection.
func (l linkListing) entries() []linkEntry {
	result := make([]linkEntry, 0, len(l.links))
	nameCount := make(map[string]int)
	for i := range l.links {
		result = append(result, linkEntry{
			name: deduplicateFilename(externalLinkName(l.links[i]), nameCount),
			link: &l.links[i],
		})
	}
	return result
}

// find replays the same derivation and returns the entry whose final name
// matches — the Lookup projection. Every name entries() emits resolves here by
// construction.
func (l linkListing) find(name string) (linkEntry, bool) {
	for _, e := range l.entries() {
		if e.name == name {
			return e, true
		}
	}
	return linkEntry{}, false
}
