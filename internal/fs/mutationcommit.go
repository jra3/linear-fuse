package fs

import (
	"context"
	"log"
	"syscall"
)

// The mutation-commit tail.
//
// Every create (_create / mkdir) and delete (unlink / rmdir) ends with the same
// invariant sequence once the API has accepted the mutation: clear the .error file,
// persist the change to SQLite (an upsert for a create, a delete for a delete), and
// invalidate the kernel caches so the next readdir/lookup sees it. On failure the
// mirror image runs: set .error and return an errno. That tail was copy-pasted across
// ~18 handlers and had drifted — most deletes never cleared .error on success, the
// SQLite effect lived in three different places (and not at all for labels), and a
// failed issue archive surfaced nothing.
//
// commitMutation is the one deep module that owns the tail, the create/delete
// counterpart to commitWriteBack. Each handler keeps its per-entity front half
// (parse, pre-API validation, call the create/delete API, build any returned inode)
// and hands the tail a small spec. The module depends only on the errorSink seam plus
// the spec's closures, so it is unit-tested with a fake sink and stub closures — no
// FUSE mount, SQLite, or API.

// mutationSpec describes the per-mutation parts of a create or delete tail.
type mutationSpec struct {
	// errKey identifies the .error file to set or clear.
	errKey string
	// op names the operation for the default failure message
	// ("Operation: <op>\nError: <err>"). Ignored when onError is set.
	op string
	// persist applies the mutation to SQLite for immediate visibility: an upsert of
	// the created entity, or a delete of the removed one. The caller closes over the
	// entity, so the tail never names its type. nil when there is nothing to persist.
	// A persist failure is non-fatal (logged) — a cache miss must not fail a write
	// Linear already accepted; the next sync reconciles.
	persist func(ctx context.Context) error
	// invalidate refreshes the kernel caches after a successful mutation
	// (InvalidateCreated/Deleted plus any per-entity extras). Runs after persist so
	// the kernel's refreshed readdir hits updated SQLite.
	invalidate func()
	// onError maps an API failure to the .error message and errno. nil falls back to
	// the default: the op-based message and syscall.EIO. Used by issue creation to
	// map a rate-limited request to EAGAIN with a retry hint.
	onError func(err error) (message string, errno syscall.Errno)
}

// commitMutation runs the invariant tail after the create/delete API call. err is
// that call's result. It returns the errno the handler should return: the failure
// errno when err != nil, 0 on success.
func commitMutation(ctx context.Context, sink errorSink, spec mutationSpec, err error) syscall.Errno {
	if err != nil {
		log.Printf("%s failed (%s): %v", spec.op, spec.errKey, err)
		message := "Operation: " + spec.op + "\nError: " + err.Error()
		errno := syscall.EIO
		if spec.onError != nil {
			message, errno = spec.onError(err)
		}
		sink.SetWriteError(spec.errKey, message)
		return errno
	}

	sink.ClearWriteError(spec.errKey)
	if spec.persist != nil {
		if err := spec.persist(ctx); err != nil {
			log.Printf("Warning: failed to persist mutation to SQLite (%s): %v", spec.errKey, err)
		}
	}
	if spec.invalidate != nil {
		spec.invalidate()
	}
	return 0
}
