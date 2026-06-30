package fs

// Kernel-cache coherence policy.
//
// After a mutation the kernel still holds the old directory listing and cached
// name lookups. Two primitives fix that: InvalidateKernelInode(dir) refreshes a
// readdir, and InvalidateKernelEntry(dir, name) drops a cached lookup. But *which*
// combination a given mutation needs is a policy that used to live in each
// handler, so handlers drifted — a delete that notified nothing left the entry
// visible until the cache TTL (relations), and creates that skipped the dir inode
// left the new item invisible (labels, projects, issues).
//
// These intent functions own that policy in one place. A handler names what
// happened — created, deleted, updated — and the correct notifies follow. They
// take a kernelNotifier seam rather than *LinearFS, so the policy is unit-tested
// with a fake recorder, no FUSE server required.

// createTriggerName is the write-only trigger file present in every _create-based
// collection directory. A create resets its cached (always-empty) attributes.
const createTriggerName = "_create"

// kernelNotifier is the minimal surface the coherence policy needs. *LinearFS
// satisfies it through its InvalidateKernelInode / InvalidateKernelEntry methods.
type kernelNotifier interface {
	InvalidateKernelInode(ino uint64)
	InvalidateKernelEntry(parent uint64, name string)
}

// invalidateCreated refreshes a directory after a child was created: the readdir
// listing (the dir inode — the must-have, omitting it is why new items stayed
// invisible), the new entry's negative-lookup cache, and the _create trigger.
// name may be "" when the caller does not have the new filename to hand; the
// dir-inode refresh alone makes the item appear on the next readdir.
func invalidateCreated(n kernelNotifier, dirIno uint64, name string) {
	n.InvalidateKernelInode(dirIno)
	if name != "" {
		n.InvalidateKernelEntry(dirIno, name)
	}
	n.InvalidateKernelEntry(dirIno, createTriggerName)
}

// invalidateDeleted refreshes a directory after a child was removed: the readdir
// listing and the now-stale entry lookup. Omitting either leaves the deleted item
// visible until the kernel cache expires.
func invalidateDeleted(n kernelNotifier, dirIno uint64, name string) {
	n.InvalidateKernelInode(dirIno)
	n.InvalidateKernelEntry(dirIno, name)
}

// invalidateUpdated drops a file's cached content after its bytes changed.
func invalidateUpdated(n kernelNotifier, fileIno uint64) {
	n.InvalidateKernelInode(fileIno)
}
