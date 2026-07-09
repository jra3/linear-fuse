package fs

import (
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// The collection .meta sidecars.
//
// The four small editable collection entities (comments, documents,
// milestones, labels) follow the same editable-only split as
// issue.md/project.md/initiative.md: the item's .md carries only the fields a
// writer may set, and a read-only "{base}.meta" sidecar carries the
// server-managed fields (id, timestamps, authorship). Before the split those
// fields leaked into the editable frontmatter and the parses silently ignored
// edits to them — a silent no-op with no .error, violating the documented
// failure model. The sidecar makes the mistake unrepresentable.
//
// Sidecar names derive from the item names the collection's listing
// (namedListing/indexedListing) already owns: "X.md" ⇄ "X.meta" through the
// two pure functions below. Readdir appends metaSidecarEntries(items) after
// the item entries, and Lookup routes a metaSidecarSource hit back through the
// same listing find() — so the listed⇔openable round-trip the listings
// guarantee for the .md files extends to their sidecars by construction. Each
// sidecar is a plain renderFile (0444, DIRECT_IO, timeout 0, like the entity
// .meta files), so it renders current state on every read and vanishes with
// its entity: rm of the .md deletes the entity, and the delete/rename paths
// invalidate the sidecar's kernel entry alongside the item's.

// metaSidecarName maps an item file's name to its read-only sidecar:
// "X.md" -> "X.meta".
func metaSidecarName(mdName string) string {
	return strings.TrimSuffix(mdName, ".md") + ".meta"
}

// metaSidecarSource maps a possible sidecar name back to the item file it
// shadows: "X.meta" -> ("X.md", true). Any other name is a miss, so callers
// fall through to their item lookup.
func metaSidecarSource(name string) (string, bool) {
	if !strings.HasSuffix(name, ".meta") {
		return "", false
	}
	return strings.TrimSuffix(name, ".meta") + ".md", true
}

// metaSidecarEntries is the Readdir half of the sidecar round-trip: one
// read-only dirent per item entry, derived from the same names the listing
// emitted — so every listed .md has a listed .meta and vice versa.
func metaSidecarEntries(items []fuse.DirEntry) []fuse.DirEntry {
	out := make([]fuse.DirEntry, len(items))
	for i, e := range items {
		out[i] = fuse.DirEntry{Name: metaSidecarName(e.Name), Mode: syscall.S_IFREG}
	}
	return out
}
