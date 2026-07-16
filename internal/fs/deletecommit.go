package fs

import (
	"context"
	"log"
	"syscall"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// The delete-commit tail.
//
// Every delete surface (unlink of a collection file, rmdir of an issue/project)
// ends the same way: locate the target, call the delete/archive mutation,
// forget the row from SQLite, and re-coher the kernel's view of the directory.
// That tail was copy-pasted across nine handlers and drifted where it was
// hand-rolled: labels, issues, and projects never forgot SQLite — the store is
// the source of truth for listings, so the "deleted" item resurrected on the
// next readdir until the sync worker reconciled — and no delete classified
// rate limits as EAGAIN.
//
// commitDelete is the one deep module that owns the tail, the delete-path
// sibling of commitCreate (createcommit.go) and commitWriteBack (editcommit.go).
// Each handler keeps a per-entity find closure (or hands over the entity it
// already holds) and a delete mutation; the module owns the failure model,
// the .error reporting, the SQLite forget, and the coherence policy. It depends
// only on the deleteSink seam plus the spec's closures, so it is unit-tested
// with a fake sink and stub closures — no FUSE mount, SQLite, or API.

// deleteSink is the minimal surface the delete tail needs: .error reporting and
// the kernel-cache coherence policy for the collection directory. *LinearFS
// satisfies it directly through its existing methods.
type deleteSink interface {
	errorSink
	InvalidateDeleted(dirIno uint64, name string)
}

// deleteSpec describes the per-entity parts of a delete. T is the entity type.
type deleteSpec[T any] struct {
	// op names the operation in .error messages, e.g. `delete label "bug.md"`.
	op string
	// key identifies the .error sidecar (shared namespace with .last).
	key string
	// find locates the target the caller named. Return (nil, nil) when it does
	// not exist -> .error note + ENOENT. A handler that already holds the
	// entity (file-node deletes) returns it directly.
	find func(ctx context.Context) (*T, error)
	// mutate performs the API delete/archive. Failures are classified like
	// creates: transient -> EAGAIN, else -> EIO, reason in .error.
	mutate func(ctx context.Context, target *T) error
	// forget removes the row from SQLite. Required: the store is the source of
	// truth for listings, so a skipped forget resurrects the deleted item — and
	// the details sync is not guaranteed to prune it. The tail retries a failed
	// forget (SQLITE_BUSY races the sync worker) before giving up; on exhaustion
	// (a wedge) it fails loud with EIO and a .error naming the self-heal (re-run
	// rm hits the already-gone path and forgets the row).
	forget func(ctx context.Context, target *T) error
	// dir + name drive the kernel-cache coherence policy: the module always
	// runs InvalidateDeleted(dir, name).
	dir  uint64
	name string
	// invalidateExtra covers per-entity internal caches and dependent views.
	invalidateExtra func(target *T)
}

// commitDelete runs a delete: find, mutate, then the invariant tail. It returns
// the errno the handler should return.
//
// Contract:
//   - find fails          -> .error gets the cause, classified errno.
//   - find returns nil    -> .error notes the unknown name, ENOENT.
//   - mutate fails        -> .error gets the cause, EAGAIN if transient else EIO.
//   - forget fails (retried) -> .error names the self-heal (re-run rm), EIO; the
//     coherence policy is skipped since the phantom row is still present.
//   - success             -> clear .error, forget SQLite, InvalidateDeleted(dir,
//     name), run extras, errno 0.
func commitDelete[T any](ctx context.Context, sink deleteSink, spec deleteSpec[T]) (errno syscall.Errno) {
	start := time.Now()
	defer func() { recordFuseOp(ctx, "delete", start, errno) }()

	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	target, err := spec.find(ctx)
	if err != nil {
		var msg string
		msg, errno = classifyMutationErr(spec.op, err)
		log.Printf("Failed to %s: %v", spec.op, err)
		sink.SetWriteError(spec.key, msg)
		return errno
	}
	if target == nil {
		sink.SetWriteError(spec.key, "Operation: "+spec.op+"\nError: no such entry. It may already be deleted; list the directory for current names.")
		return syscall.ENOENT
	}

	if err := spec.mutate(ctx, target); err != nil {
		if !remoteAlreadyGone(err) {
			var msg string
			msg, errno = classifyMutationErr(spec.op, err)
			log.Printf("Failed to %s: %v", spec.op, err)
			sink.SetWriteError(spec.key, msg)
			return errno
		}
		// The entity no longer exists on Linear, so the delete's outcome is
		// already true — proceed to the success tail so the local row is
		// forgotten. This is also the self-heal path for a phantom row left
		// by an earlier delete whose forget failed: rm the file again and
		// the listing comes back consistent.
		log.Printf("%s: entity already deleted on Linear; forgetting the local row", spec.op)
	}

	sink.ClearWriteError(spec.key)

	// Forget gates the delete's local completion: the store is the listing source
	// of truth, so a dropped forget leaves a phantom row the details sync cannot
	// always prune. Retry the transient (SQLITE_BUSY racing the sync worker); on
	// exhaustion — a wedge — fail loud rather than reporting a clean rm over a
	// listing that still shows the item. Skip the coherence policy: the row is
	// still present, so invalidating would only repopulate the phantom. The
	// message names the self-heal (re-run rm) and clarifies it's a local-cache
	// failure, not a server one (#278).
	if err := retrySQLite(ctx, spec.forget, target); err != nil {
		log.Printf("ERROR: failed to forget deleted entity from SQLite after retries (%s): %v — re-run rm to clear the lingering listing entry", spec.key, err)
		sink.SetWriteError(spec.key, unconfirmedDeleteMsg(spec.op, spec.name, err.Error()))
		return syscall.EIO
	}

	sink.InvalidateDeleted(spec.dir, spec.name)
	if spec.invalidateExtra != nil {
		spec.invalidateExtra(target)
	}
	return 0
}

// remoteAlreadyGone reports whether a delete mutation failed because Linear no
// longer has the entity — the shared predicate the repo layer's orphan defense
// uses too. For a delete that is success, not failure.
func remoteAlreadyGone(err error) bool {
	return api.IsNotFound(err)
}
