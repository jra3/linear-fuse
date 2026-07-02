package fs

import (
	"context"
	"log"
	"syscall"
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
	// truth for listings, so skipping it resurrects the deleted item until the
	// next sync. Failure is non-fatal (sync will reconcile).
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
//   - success             -> clear .error, forget SQLite (non-fatal on failure),
//     InvalidateDeleted(dir, name), run extras, errno 0.
func commitDelete[T any](ctx context.Context, sink deleteSink, spec deleteSpec[T]) syscall.Errno {
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	target, err := spec.find(ctx)
	if err != nil {
		msg, errno := classifyMutationErr(spec.op, err)
		log.Printf("Failed to %s: %v", spec.op, err)
		sink.SetWriteError(spec.key, msg)
		return errno
	}
	if target == nil {
		sink.SetWriteError(spec.key, "Operation: "+spec.op+"\nError: no such entry. It may already be deleted; list the directory for current names.")
		return syscall.ENOENT
	}

	if err := spec.mutate(ctx, target); err != nil {
		msg, errno := classifyMutationErr(spec.op, err)
		log.Printf("Failed to %s: %v", spec.op, err)
		sink.SetWriteError(spec.key, msg)
		return errno
	}

	sink.ClearWriteError(spec.key)

	if err := spec.forget(ctx, target); err != nil {
		log.Printf("Warning: failed to delete entity from SQLite (%s): %v", spec.key, err)
	}

	sink.InvalidateDeleted(spec.dir, spec.name)
	if spec.invalidateExtra != nil {
		spec.invalidateExtra(target)
	}
	return 0
}
