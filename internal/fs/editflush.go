package fs

import (
	"context"
	"syscall"
	"time"
)

// The edit-flush shell.
//
// Every editable file node (issue.md, project.md, initiative.md, and a
// comment/doc/label/milestone .md) drives its FUSE Flush through the same
// invariant shell around its per-entity front half: take the buffer lock, skip
// a clean/empty buffer, bound the API work with a timeout, run the front half
// (parse → resolve → mutate), and on success run the commit tail
// ([[edit-commit]], commitWriteBack), adopt the fresh value, invalidate the
// node's kernel-cache set, and clear the dirty flag. That shell was copy-pasted
// across all seven handlers, and it had drifted: issues invalidated *before*
// persisting the fresh value (a stale-repopulation window), and the
// invalidation set — which inodes an edit dirties — lived as loose
// InvalidateUpdated calls each handler had to remember.
//
// editFlush is the one deep module that owns the shell. Each handler keeps its
// per-entity front half as the mutate closure and its commit tail as a
// writeBackSpec, then declares its invalidation set as data (coherence []uint64)
// and hands editFlush a small spec. The module depends only on the
// editFlushSink seam plus the spec's closures, so it is unit-tested with a
// recording fake — no FUSE mount, SQLite, or API.
//
// Invalidate-after-persist is uniform here by construction: the shell
// invalidates only after commitWriteBack has upserted the fresh value, so a
// racing read can never repopulate the kernel cache from a not-yet-written row.

// editFlushSink is the minimal surface the shell needs: the errorSink the commit
// tail already requires, plus the kernel-cache invalidation the shell owns.
// *LinearFS satisfies it directly (SetWriteError/ClearWriteError via
// writeFeedback, InvalidateUpdated via kernelNotify), so production wiring needs
// no adapter while tests inject a recording fake.
type editFlushSink interface {
	errorSink
	InvalidateUpdated(fileIno uint64)
}

// editFlushSpec describes the per-entity parts of an edit's flush. T is the
// entity type (api.Issue, api.Label, …). The front half and the commit tail
// stay T-specific; the shell is fully generic.
type editFlushSpec[T any] struct {
	// mutate runs the per-entity front half — parse, resolve, and call the API —
	// and reports one of three outcomes:
	//   - errno != 0            → the front half failed (parse/resolve/mutation);
	//     the shell returns errno and LEAVES the buffer dirty so a corrected
	//     re-save retries. mutate owns its own .error message.
	//   - errno == 0, !proceed  → nothing changed; the shell clears dirty and
	//     returns 0 without committing.
	//   - errno == 0, proceed   → the API accepted a write; the shell runs the
	//     commit tail, adopts, invalidates, and clears dirty.
	mutate func(ctx context.Context) (proceed bool, errno syscall.Errno)
	// writeBack is the commit tail spec (see commitWriteBack).
	writeBack writeBackSpec[T]
	// adopt installs the fresh value onto the node (n.entity = *fresh). Runs
	// after commitWriteBack's compare has read the pre-write originals.
	adopt func(fresh *T)
	// coherence lists the kernel inodes this edit dirties (the entity file plus
	// its .meta sidecar, and any dependent listing). Declared as data so a
	// forgotten sidecar is a visible one-line omission, not a missing call
	// buried in a handler. Invalidated only after the commit tail persists.
	coherence []uint64
}

// editFlush runs the invariant shell of a file node's Flush. eb is the node's
// embedded editBuffer (the shell owns the lock, the clean/empty guard, and the
// dirty flag); sink carries the error + invalidation surfaces; spec supplies the
// per-entity front half, commit tail, adopt, and invalidation set.
//
// Returns the errno the Flush should surface: the front half's errno on
// failure, 0 on a no-op, or the commit tail's errno on a completed write. The
// buffer lock is held across the whole shell, exactly as the hand-written
// handlers held n.mu.
func editFlush[T any](ctx context.Context, sink editFlushSink, eb *editBuffer, spec editFlushSpec[T]) syscall.Errno {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if !eb.dirty || eb.content == nil {
		return 0
	}

	// Bound the API work — the front half and the commit tail both call Linear.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	proceed, errno := spec.mutate(ctx)
	if errno != 0 {
		// Front half failed: leave the buffer dirty for a corrected re-save.
		return errno
	}
	if !proceed {
		// Nothing changed.
		eb.dirty = false
		return 0
	}

	// The API accepted a write. Run the commit tail, then — and only then —
	// adopt and invalidate: invalidating before the tail persists would let a
	// racing read repopulate the kernel cache from the stale row.
	fresh, errno := commitWriteBack(ctx, sink, spec.writeBack)
	if fresh != nil {
		spec.adopt(fresh)
	}
	for _, ino := range spec.coherence {
		sink.InvalidateUpdated(ino)
	}
	eb.dirty = false
	return errno
}
