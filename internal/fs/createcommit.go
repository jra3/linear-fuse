package fs

import (
	"context"
	"errors"
	"log"
	"syscall"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// The create-commit tail.
//
// Every create surface (_create trigger writes and mkdir) ends the same way once
// the handler has content in hand: call the mutation, classify a failure into an
// errno plus a .error message, and on success persist to SQLite, clear .error,
// record the new identity in .last, and re-coher the kernel's view of the
// collection directory. Persist gates success: an entity Linear accepted but
// that cannot be reflected locally fails loud (EIO + a de-dupe .error) rather
// than reporting a silent no-op that invites a duplicate-creating retry (#276).
// That tail was copy-pasted across eight handlers and
// drifted where it was hand-rolled: attachments and relations never wrote .last,
// only projects and issues classified rate limits as EAGAIN, and creates never
// refreshed the recent/ view.
//
// commitCreate is the one deep module that owns the tail, the create-path
// counterpart to commitWriteBack (editcommit.go). Each handler keeps a per-entity
// mutate closure (parse -> build input -> call the mutation seam) and hands the
// tail a small spec. The module depends only on the createSink seam plus the
// spec's closures, so it is unit-tested with a fake sink and stub closures — no
// FUSE mount, SQLite, or API. Unlike edits, creates carry no read-your-writes
// verification: the mutation's echoed entity is trusted.

// createTimeout bounds every create so a rate-limited request fails legibly
// (EAGAIN + a retry hint in .error) instead of hanging indefinitely (#131).
const createTimeout = 30 * time.Second

// createSink is the minimal surface the create tail needs: .error reporting,
// .last recording, and the kernel-cache coherence policy for the collection
// directory. *LinearFS satisfies it directly through its existing methods, so
// production wiring needs no adapter while tests inject a fake.
type createSink interface {
	errorSink
	AppendWriteSuccess(key string, r WriteResult)
	InvalidateCreated(dirIno uint64, name string)
}

// notFoundError marks well-formed create input that references an entity that
// does not exist (e.g. a relation's target issue). Distinct from FieldError so
// the classifier maps it to ENOENT rather than EINVAL; the .error rendering is
// the same Field/Value/Error format.
type notFoundError struct{ FieldError }

// createSpec describes the per-entity parts of a create. T is the entity type
// (api.Issue, api.Label, api.Comment, …). Everything T-specific lives in these
// closures; the tail itself is fully generic.
type createSpec[T any] struct {
	// op names the operation in classifier-rendered .error messages, e.g.
	// `create label` or `create issue "Fix bug"`.
	op string
	// key identifies the .error and .last sidecars. The two stores intentionally
	// share one namespace (collectionSuccessKey returns the same string as
	// collectionErrorKey), so a single key drives both.
	key string
	// mutate is the per-entity front half: parse/validate the input, build the
	// API input, and call the mutation seam. Return a *FieldError for invalid
	// input (-> EINVAL) or a *notFoundError for a reference to a missing entity
	// (-> ENOENT); any other error is classified transient (-> EAGAIN) or hard
	// (-> EIO) by the tail.
	mutate func(ctx context.Context) (*T, error)
	// result projects the created entity into its .last entry. Required: every
	// create surface reports its resulting identity (#149/#151).
	result func(created *T) WriteResult
	// persist upserts the created entity to SQLite for immediate visibility.
	// Always explicit — no mutation wrapper hides an upsert. Failure is FATAL to
	// the create: an entity Linear accepted but that we cannot reflect locally
	// must fail loud (EIO + a de-dupe .error), not report a clean success — a
	// silent no-op is what invites a duplicate-creating retry (#276).
	persist func(ctx context.Context, created *T) error
	// dir is the collection directory's inode. The tail always applies the
	// kernel-cache coherence policy InvalidateCreated(dir, entryName(created)) —
	// a spec cannot forget it.
	dir uint64
	// entryName returns the created entity's on-disk name, or "" when it is not
	// knowable without re-listing (comments, relations). nil means "".
	entryName func(created *T) string
	// invalidateExtra covers per-entity internal caches and dependent views
	// (team/my/filtered issue caches, recent/). nil when the collection has none.
	invalidateExtra func(created *T)
}

// commitCreate runs a create: the spec's mutate closure inside the create
// timeout, then the invariant tail. It returns the created entity (nil on
// failure) and the errno the handler should return.
//
// Contract:
//   - mutate returns *FieldError    -> .error gets Detail(), EINVAL.
//   - mutate returns *notFoundError -> .error gets Detail(), ENOENT.
//   - mutate fails transiently      -> .error gets a retry hint, EAGAIN.
//   - mutate fails otherwise        -> .error gets the cause, EIO.
//   - mutate ok but persist fails   -> .error gets a de-dupe message naming the
//     created entity, EIO (the item is live on Linear but not cached locally;
//     .last is NOT appended and the caller must not recreate it — #276).
//   - success                       -> persist, clear .error, append .last,
//     InvalidateCreated(dir, name), run extras, errno 0.
func commitCreate[T any](ctx context.Context, sink createSink, spec createSpec[T]) (created *T, errno syscall.Errno) {
	start := time.Now()
	defer func() { recordFuseOp(ctx, "create", start, errno) }()

	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	created, err := spec.mutate(ctx)
	if err != nil {
		var msg string
		msg, errno = classifyMutationErr(spec.op, err)
		log.Printf("Failed to %s: %v", spec.op, err)
		sink.SetWriteError(spec.key, msg)
		return nil, errno
	}

	// The mutation succeeded — the entity is live on Linear. A create is only
	// truly done once that entity is reflected in the local cache, so persist is
	// part of the success contract, not a best-effort afterthought. If it fails
	// (a wedged or locked SQLite write, or the create timeout firing on a stuck
	// write), we must NOT report a clean success: a silent no-op is exactly what
	// let succeeded creates look like nothing happened, so callers retried and
	// duplicated them on the board (#276). Fail loud instead — record the cause
	// and the entity's identity in .error, and return EIO — so the caller sees
	// the create is in an unconfirmed state and does not blindly recreate it.
	// .last is appended only after confirmed reflection, so it never advertises a
	// create the local cache can't yet serve.
	if err := spec.persist(ctx, created); err != nil {
		log.Printf("Reflection failed after %s succeeded on Linear: %v", spec.op, err)
		sink.SetWriteError(spec.key, unconfirmedReflectionMsg(spec.op, spec.result(created), err))
		return nil, syscall.EIO
	}

	sink.ClearWriteError(spec.key)
	sink.AppendWriteSuccess(spec.key, spec.result(created))

	name := ""
	if spec.entryName != nil {
		name = spec.entryName(created)
	}
	sink.InvalidateCreated(spec.dir, name)
	if spec.invalidateExtra != nil {
		spec.invalidateExtra(created)
	}
	return created, 0
}

// unconfirmedReflectionMsg renders the .error for a create that Linear accepted
// but whose local reflection (the SQLite upsert) failed or timed out. It names
// the created entity so the caller can find the already-created item and,
// crucially, tells them NOT to recreate it — a blind retry on a create that only
// *looked* like a no-op is what turned one incident's creates into duplicates
// (#276). The identity comes from the create's own WriteResult (identifier where
// the entity has one, else its title).
func unconfirmedReflectionMsg(op string, r WriteResult, err error) string {
	who := r.Identifier
	if who == "" {
		who = r.Title
	}
	if who != "" {
		who = " (" + who + ")"
	}
	return "Operation: " + op +
		"\nError: this create SUCCEEDED on Linear" + who +
		" but its result could not be cached locally: " + err.Error() +
		". The entity already exists — do NOT recreate it (a blind retry duplicates it)." +
		" Restart the daemon (systemctl --user restart linearfs) or wait for the next sync to reflect it."
}

// classifyMutationErr maps a mutation failure to its .error message and errno.
// This is the single owner of the write failure model the generated README
// documents — shared by the create and delete tails and by every edit-mutation
// site (issue/comment/label/document/milestone flushes and renames, the
// project/initiative scalar+reconcile paths): bad input -> EINVAL, a field
// over its length cap -> EMSGSIZE, missing reference -> ENOENT, transient ->
// EAGAIN, backend failure -> EIO — either way the reason lands in .error, and
// the errno itself hints where a specific one exists. Rate-limit/not-found and
// too-long detection delegate to
// the api package's predicates (api.IsRateLimited via retryableCreateErr,
// api.IsNotFound via the delete tail's remoteAlreadyGone, api.IsFieldTooLong).
func classifyMutationErr(op string, err error) (string, syscall.Errno) {
	var nferr *notFoundError
	if errors.As(err, &nferr) {
		return nferr.Detail(), syscall.ENOENT
	}
	var ferr *FieldError
	if errors.As(err, &ferr) {
		return ferr.Detail(), syscall.EINVAL
	}
	if retryableCreateErr(err) {
		return "Operation: " + op + "\nError: the request was rate-limited or timed out before it completed, so the operation did not take effect. Wait a few seconds and retry.", syscall.EAGAIN
	}
	// A structured Linear input rejection (userError: true) is the caller's
	// bad input, not a backend failure: EINVAL, preferring the server's
	// user-presentable message over its terse internal one (live example:
	// "The label 'X' is a group and cannot be assigned to projects directly."
	// vs internal "labelIds contain parent labels").
	var gqlErr *api.GraphQLError
	if errors.As(err, &gqlErr) && gqlErr.UserError {
		msg := gqlErr.UserPresentableMessage
		if msg == "" {
			msg = gqlErr.Message
		}
		// A length-cap rejection is a size error, not merely malformed input:
		// EMSGSIZE makes the errno itself a hint (the reason still lands in
		// .error). See api.IsFieldTooLong.
		if api.IsFieldTooLong(err) {
			return "Operation: " + op + "\nError: " + msg, syscall.EMSGSIZE
		}
		return "Operation: " + op + "\nError: " + msg, syscall.EINVAL
	}
	return "Operation: " + op + "\nError: " + err.Error(), syscall.EIO
}
