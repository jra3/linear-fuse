package fs

import (
	"syscall"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// namedListing owns the entity-derived filenames of a collection whose entries
// are named directly from an entity field — documents (slug/title), labels
// (name), and project milestones (name). It is the un-indexed sibling of
// indexedListing: where that module derives a name from an item's *position* in
// a sorted order, this one derives it from the item's *identity*, so there is no
// sort and no index. Deriving the name in one place is what lets a collection's
// Readdir, Lookup, Unlink, Rename, and Create-overwrite agree on every name — a
// file you can `ls` you can also open, `rm`, and `mv`. Before this, each of
// those surfaces re-derived and re-matched independently (13 sites across three
// nodes), so a change to one could silently strand a file.
//
// Ordering is the repo's job, not this module's: the SQLite list queries carry
// the ORDER BY (labels by name, documents by title, milestones by sort_order — a
// meaningful manual order that a filename sort would clobber), so namedListing
// preserves the items slice as given and never sorts.
//
// Collisions are first-match, emit-once. Two entities can derive the same
// filename (two cross-scope labels named "Bug"; a sanitized-name clash), and the
// mount is a name-addressed projection of a source that permits such duplicates —
// Linear itself shadows them in its own product. So find returns the first match
// and entries emits each derived name once (first wins), a well-formed readdir
// consistent with find by construction. This is deliberately not a dedup
// *policy*: a disambiguated name like "Bug (2).md" would resolve nowhere, since
// ResolveMilestoneID and GetLabelByName match the raw entity name — the whole
// name->entity stack is already assume-first. See CONTEXT.md "Named listing".
type namedListing[T any] struct {
	items  []T
	nameOf func(T) string
}

// entries is the Readdir projection: one S_IFREG DirEntry per distinct derived
// name, in items order. A name colliding with an earlier item's is dropped
// (first wins), so the listing never emits a duplicate dirent.
func (l namedListing[T]) entries() []fuse.DirEntry {
	entries := make([]fuse.DirEntry, 0, len(l.items))
	seen := make(map[string]struct{}, len(l.items))
	for _, it := range l.items {
		name := l.nameOf(it)
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, fuse.DirEntry{Name: name, Mode: syscall.S_IFREG})
	}
	return entries
}

// find is the Lookup/Unlink/Rename/Create-overwrite projection: locate the first
// item whose derived name matches, over the same items order entries() used.
func (l namedListing[T]) find(name string) (T, bool) {
	for _, it := range l.items {
		if l.nameOf(it) == name {
			return it, true
		}
	}
	var zero T
	return zero, false
}
