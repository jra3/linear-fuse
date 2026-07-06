package fs

import (
	"fmt"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// indexedListing owns the index-derived filenames of a collection whose entries
// are named <NNNN>-<date>…md by creation order (comments, project and
// initiative updates). Fixing the sort and the name derivation in one place is
// what lets a collection's Readdir, Lookup, and Unlink agree on every name — a
// file you can `ls` you can also open and `rm`. Before this, each surface
// re-sorted and re-formatted independently, so a change to one (a timestamp
// format, an off-by-one) could silently strand a file. Sibling of
// collectionTrio, which owns the same collection's _create/.error/.last. See
// CONTEXT.md "Indexed listing".
type indexedListing[T any] struct {
	items   []T
	lessKey func(T) time.Time          // sort ascending; the index follows this order
	nameOf  func(i int, item T) string // 0-based position -> filename
}

// sorted returns the items in the canonical order the index numbers follow. The
// order is the one source of truth every surface shares.
func (l indexedListing[T]) sorted() []T {
	out := make([]T, len(l.items))
	copy(out, l.items)
	sort.SliceStable(out, func(i, j int) bool {
		return l.lessKey(out[i]).Before(l.lessKey(out[j]))
	})
	return out
}

// entries is the Readdir projection: one S_IFREG DirEntry per item, named by
// nameOf over the canonical order.
func (l indexedListing[T]) entries() []fuse.DirEntry {
	items := l.sorted()
	entries := make([]fuse.DirEntry, len(items))
	for i, it := range items {
		entries[i] = fuse.DirEntry{Name: l.nameOf(i, it), Mode: syscall.S_IFREG}
	}
	return entries
}

// find is the Lookup/Unlink projection: locate the item whose derived name
// matches, over the same canonical order entries() used.
func (l indexedListing[T]) find(name string) (T, bool) {
	for i, it := range l.sorted() {
		if l.nameOf(i, it) == name {
			return it, true
		}
	}
	var zero T
	return zero, false
}

// updateEntryName is the filename for a project or initiative status update:
// <NNNN>-<date>-<health>.md by creation order. Shared by both update
// collections (their convention is identical); comments own a different format
// (a per-minute timestamp, no health).
func updateEntryName(i int, createdAt time.Time, health string) string {
	return fmt.Sprintf("%04d-%s-%s.md", i+1, createdAt.Format("2006-01-02"), strings.ToLower(health))
}
