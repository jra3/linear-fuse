package fs

import (
	"fmt"
	"strings"

	"github.com/jra3/linear-fuse/internal/api"
)

// attachmentListing owns the filenames of the attachments directory — the
// heterogeneous sibling of namedListing (labels/docs/milestones) and
// indexedListing (comments/updates). The directory mixes two item types:
// embedded files (CDN-backed *.png/*.pdf bytes, named by their filename) and
// external attachments (*.link files, named by their sanitized title). Both
// Readdir and Lookup derive names through this one module over one canonical
// order, so a file you can `ls` you can also open and `rm` — before this,
// each surface rebuilt the dedup map independently and external duplicates
// emitted duplicate dirents.
//
// Collisions are DEDUPLICATED (`foo (2).link`), unlike namedListing's
// first-match/shadow policy — the same freedom indexedListing has, and for
// the same recorded reason: attachment filenames are resolution keys nowhere
// else (labels/milestones pin their filenames to the raw entity name because
// ResolveMilestoneID/GetLabelByName match it; nothing name-resolves an
// attachment). One counter spans both families in listing order (embedded
// first, then external), so even an embedded file literally named "foo.link"
// and an external link titled "foo" disambiguate instead of shadowing.
// `rm` on a deduplicated name still deletes the right entity: find returns
// the matched item and the node holds it through Unlink.
//
// Ordering is the repo's job, never this module's: the list queries carry
// deterministic ORDER BY (embedded by filename,id; external by created_at,id)
// so a name keeps its dedup suffix across calls. The caller fetches and
// passes the slices; the module is pure and unit-tested on literals.
type attachmentListing struct {
	embedded []api.EmbeddedFile
	external []api.Attachment
}

// attachmentEntry is one derived directory entry: the final (deduplicated)
// name plus exactly one of the two item kinds.
type attachmentEntry struct {
	name     string
	embedded *api.EmbeddedFile
	external *api.Attachment
}

// linkName derives an external attachment's base filename (before dedup).
// The create surface reuses it for its .last path and kernel-entry name, so
// the derivation is written exactly once.
func linkName(att api.Attachment) string {
	return sanitizeFilename(att.Title) + ".link"
}

// entries derives every entry's final name through one shared dedup counter,
// embedded files first, then external links — the Readdir projection.
func (l attachmentListing) entries() []attachmentEntry {
	result := make([]attachmentEntry, 0, len(l.embedded)+len(l.external))
	nameCount := make(map[string]int)
	for i := range l.embedded {
		result = append(result, attachmentEntry{
			name:     deduplicateFilename(l.embedded[i].Filename, nameCount),
			embedded: &l.embedded[i],
		})
	}
	for i := range l.external {
		result = append(result, attachmentEntry{
			name:     deduplicateFilename(linkName(l.external[i]), nameCount),
			external: &l.external[i],
		})
	}
	return result
}

// find replays the same derivation and returns the entry whose final name
// matches — the Lookup projection. Every name entries() emits resolves here
// by construction.
func (l attachmentListing) find(name string) (attachmentEntry, bool) {
	for _, e := range l.entries() {
		if e.name == name {
			return e, true
		}
	}
	return attachmentEntry{}, false
}

// deduplicateFilename returns a unique filename by appending (2), (3), etc. for duplicates.
// The nameCount map tracks how many times each base name has been seen.
func deduplicateFilename(name string, nameCount map[string]int) string {
	nameCount[name]++
	count := nameCount[name]
	if count == 1 {
		return name
	}

	// Insert counter before extension: image.png -> image (2).png
	ext := ""
	base := name
	if dot := strings.LastIndex(name, "."); dot > 0 {
		ext = name[dot:]
		base = name[:dot]
	}
	return fmt.Sprintf("%s (%d)%s", base, count, ext)
}

// sanitizeFilename converts a string to a safe filename by replacing problematic characters
func sanitizeFilename(s string) string {
	// Replace path separators and null bytes
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, "\x00", "")
	// Trim spaces and dots from ends
	s = strings.Trim(s, " .")
	if s == "" {
		return "untitled"
	}
	return s
}
