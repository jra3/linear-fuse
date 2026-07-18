package fs

import (
	"log"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// kernelNotifyTimeout bounds one kernel-cache invalidation intent. A package var
// so a test can lower it (the sqliteRetryBackoff seam idiom). 5s is ~1000x a
// healthy notify's sub-millisecond latency, so a false trip is nearly impossible
// while a real wedge still returns control promptly.
var kernelNotifyTimeout = 5 * time.Second

// boundedNotify runs one kernel-cache invalidation intent under a deadline.
//
// server.InodeNotify / EntryNotify do not honor a context and can wedge — on an
// internal go-fuse lock, or a backed-up kernel-notify queue — with no way to
// interrupt them. A create's write-back once hung the whole FUSE handler that
// way until a manual `systemctl restart` (#277). Running the intent in a
// goroutine and selecting on kernelNotifyTimeout bounds it: on a wedge the stuck
// goroutine is LEAKED (unavoidable — the call is uninterruptible) and control
// returns to the caller, so the handler completes instead of hanging forever.
// The abandoned directory's kernel cache stays stale until its TTL;
// recordNotifyTimeout + a WARN log make that legible.
//
// A persistent wedge leaks one goroutine per mutation. That is accepted
// deliberately — no circuit breaker (grilled 2026-07-17): mutations are
// interactive-rate so the leak grows slowly, and the counter names the condition
// for an operator restart, which a truly wedged notify warrants anyway. Trading
// the leak for a breaker would only swap it for silent permanent staleness.
func boundedNotify(intent string, run func()) {
	// Buffered so the goroutine's send never blocks after a timeout moved on.
	done := make(chan struct{}, 1)
	go func() {
		run()
		done <- struct{}{}
	}()
	timer := time.NewTimer(kernelNotifyTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		recordNotifyTimeout(intent)
		log.Printf("Warning: kernel-notify %q exceeded %s and was abandoned; the guard goroutine is leaked and this directory's cache may be stale until its TTL — restart linearfs if this persists (#277)", intent, kernelNotifyTimeout)
	}
}

// kernelNotify owns the filesystem's one coupling to the FUSE server: the two
// raw kernel-cache primitives (InodeNotify / EntryNotify) and the intent
// wrappers built on the coherence policy below. It was a loose *fuse.Server
// field plus seven methods on the LinearFS god-object; gathering them localizes
// the server dependency. LinearFS embeds one, so lfs.InvalidateCreated /
// lfs.InvalidateUpdated / … keep working by promotion, and it satisfies
// kernelNotifier itself.
type kernelNotify struct {
	server *fuse.Server
}

// SetServer wires the FUSE server (known only after mount).
func (k *kernelNotify) SetServer(server *fuse.Server) { k.server = server }

// InvalidateKernelInode tells the kernel to drop cached data for an inode.
func (k *kernelNotify) InvalidateKernelInode(ino uint64) {
	if k.server != nil {
		k.server.InodeNotify(ino, 0, -1) // -1 = entire file
	}
}

// InvalidateKernelEntry tells the kernel to drop a cached directory entry.
func (k *kernelNotify) InvalidateKernelEntry(parent uint64, name string) {
	if k.server != nil {
		k.server.EntryNotify(parent, name)
	}
}

// InvalidateCreated / Deleted / Updated / Renamed name what happened; the
// coherence policy (below) picks the correct notifies. fileIno/name may be zero
// where the policy allows. Each runs its notify sequence through boundedNotify,
// so a wedged InodeNotify/EntryNotify can no longer hang the calling handler
// (#277). The nil-server short-circuit keeps the pre-mount / fixture no-op AND
// avoids spawning a guard goroutine when there is nothing to notify.
func (k *kernelNotify) InvalidateCreated(dirIno uint64, name string) {
	if k.server == nil {
		return
	}
	boundedNotify("created", func() { invalidateCreated(k, dirIno, name) })
}
func (k *kernelNotify) InvalidateDeleted(dirIno uint64, name string) {
	if k.server == nil {
		return
	}
	boundedNotify("deleted", func() { invalidateDeleted(k, dirIno, name) })
}
func (k *kernelNotify) InvalidateUpdated(fileIno uint64) {
	if k.server == nil {
		return
	}
	boundedNotify("updated", func() { invalidateUpdated(k, fileIno) })
}
func (k *kernelNotify) InvalidateRenamed(dirIno uint64, oldName, newName string, fileIno uint64) {
	if k.server == nil {
		return
	}
	boundedNotify("renamed", func() { invalidateRenamed(k, dirIno, oldName, newName, fileIno) })
}

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

// invalidateRenamed refreshes both names after a file is renamed within a
// directory: the old name no longer resolves and the new one now does. When
// fileIno is nonzero it also drops the renamed file's cached content — needed for
// an atomic save (temp file renamed over a real .md), where go-fuse moves the
// spent scratch inode into place and the real file must re-Lookup. Pass fileIno 0
// for a pure entry rename (e.g. a doc/label title change) where no inode's content
// changes.
func invalidateRenamed(n kernelNotifier, dirIno uint64, oldName, newName string, fileIno uint64) {
	n.InvalidateKernelEntry(dirIno, oldName)
	n.InvalidateKernelEntry(dirIno, newName)
	if fileIno != 0 {
		n.InvalidateKernelInode(fileIno)
	}
}
