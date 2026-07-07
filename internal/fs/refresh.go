package fs

import (
	"github.com/hanwen/go-fuse/v2/fs"
)

// nodeRefresher is the seam that closes the captured-entity staleness hole
// (round 15): go-fuse deduplicates inodes by StableAttr — bridge.addNewChild
// keeps the FIRST node ever mounted for an ino and silently discards the
// freshly-constructed one — so a node that bakes entity state at construction
// (an editBuffer's content, a directory's entity, a render closure's capture)
// would serve first-Lookup data for as long as the kernel remembers the
// inode. The sync worker deliberately never notifies the kernel; freshness
// arrives via attr/entry timeout expiry forcing a re-Lookup — and that
// re-Lookup is exactly where this seam acts: the parent has just fetched the
// entity fresh and built a fresh node; if the bridge still knows an old node
// under this name, push the fresh state into it.
//
// The dedup itself happens in the bridge AFTER a Lookup handler returns
// (addNewChild — NewInode's return value is always the fresh struct), so the
// seam cannot detect reuse from NewInode's result. Instead the construction
// helpers probe parent.GetChild(name): the inode the bridge will keep if it
// dedups. A nil child (kernel FORGOT it) means the fresh node will be
// installed — already fresh, nothing to do.
//
// Implementations swap their volatile state under their own lock, and an
// editBuffer node MUST skip the refresh while dirty — a user's in-flight
// edit always wins over a background sync.
//
// TestRemoteUpdateVisibleAfterKernelRevalidation (internal/integration) is
// the end-to-end guard: remote upsert → kernel revalidation → fresh reads.
type nodeRefresher interface {
	refreshFrom(fresh fs.InodeEmbedder)
}

// refreshExisting runs inside a construction seam, before NewInode: if the
// parent still links a child by this name, that node is what the bridge will
// keep — push the fresh twin's volatile state into it. Nodes that don't
// implement the seam (id-only collection dirs, symlinks) are unaffected.
func refreshExisting(parent fs.InodeEmbedder, name string, fresh fs.InodeEmbedder) {
	inode := parent.EmbeddedInode().GetChild(name)
	if inode == nil {
		return
	}
	ops := inode.Operations()
	if ops == fresh {
		return
	}
	if r, ok := ops.(nodeRefresher); ok {
		r.refreshFrom(fresh)
	}
}
