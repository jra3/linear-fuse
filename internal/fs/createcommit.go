package fs

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
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
	AppendWriteFailure(key, msg string)
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
	// (-> ENOENT); any other error is classified by the tail (see
	// classifyMutationErr, the single owner of the failure model).
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
	// recheck re-asks Linear about the entity that OWNS this collection — the
	// issue a comment is filed on, the project a milestone belongs to — and the
	// tail calls it on exactly one verdict: Linear itself answering "entity not
	// found" (serverSaysGone). That rejection says the local row the caller
	// walked through is an orphan, and without the hint the mount keeps listing
	// it, keeps accepting a create into it, and keeps failing identically until
	// an unrelated read or a sync cycle rediscovers the truth (#477).
	//
	// It is the same seam editFlush's refresh hook uses, and for the same
	// reason: the failed write supplies a HINT, it does not mutate the cache.
	// The closure triggers the owner's existing SWR spec, so the prune stays
	// behind orphanOnNotFound in the repo layer, which re-asks Linear before
	// deleting anything — api.IsNotFound answers on message text, and a
	// misclassified rejection acted on directly would delete a live entity's
	// row.
	//
	// Optional: wired iff the owner has an SWR spec to trigger. A collection
	// owned by a TEAM (issues/, labels/, projects/) leaves it nil — a team is
	// the sync root and its specs carry no orphan handler — exactly as
	// editFlush's refresh is nil for labels/*.md and milestones/*.md.
	recheck func()
}

// commitCreate runs a create: the spec's mutate closure inside the create
// timeout, then the invariant tail. It returns the created entity (nil on
// failure) and the errno the handler should return.
//
// Contract:
//   - mutate returns *FieldError    -> .error gets Detail(), EINVAL.
//   - mutate returns *notFoundError -> .error gets Detail(), ENOENT.
//   - mutate fails transiently      -> .error gets a retry hint, EAGAIN.
//   - mutate fails otherwise        -> .error gets the cause, classified errno.
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
		// Also append a compact failure entry to .last so a scripted batch of
		// failing _create writes leaves N countable outcomes instead of .error
		// collapsing to only the last one (#370). Note this is the *clean*
		// failure branch — the mutation never took effect. The persist-failure
		// branch below deliberately does NOT append: that create SUCCEEDED on
		// Linear and is only unconfirmed locally, so logging it as a .last
		// failure would misreport a live entity as failed (#276).
		sink.AppendWriteFailure(spec.key, msg)
		// Linear said the owner is gone: hand the repo layer the hint (#477).
		// After the .error and .last records, because the recheck is a
		// background trigger and the caller's feedback must not wait on it.
		if spec.recheck != nil && serverSaysGone(err) {
			spec.recheck()
		}
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
// EAGAIN, the workspace over its plan limit -> EDQUOT, backend failure -> EIO —
// either way the reason lands in .error, and the errno itself hints where a
// specific one exists. Rate-limit/not-found, too-long and usage-limit detection
// delegate to the api package's predicates (api.IsRateLimited via
// retryableCreateErr, api.IsNotFound both here and — for the opposite verdict,
// idempotent success — via the delete tail's remoteAlreadyGone step,
// api.IsFieldTooLong, api.IsUsageLimited).
//
// Arm ORDER carries meaning twice over. The arms that classify on a condition
// Linear does not reliably tag (not-found, usage limit, length cap) sit above
// the userError gate, so their errno does not depend on a server-set bit (#409).
// And the three of them, which answer on message TEXT, sit below the arms that
// answer on error STRUCTURE (*notFoundError, *FieldError, the HTTP-status arms,
// retryableCreateErr) — text can be the caller's own echoed input, so a
// structural answer outranks a textual one. See #409, #445, #447.
//
// The HTTP-status arms (api.IsAuthFailure, api.IsServerTransient) are the
// newest members of that structural tier and sit at its end, just above
// retryableCreateErr. A status code is the least ambiguous thing in a failed
// response — it is not the caller's echoed input and not a server-set label —
// so nothing that answers on text should outrank it. A 429 is deliberately
// absent: api.IsRateLimited recognises it by status now, so it lands on
// retryableCreateErr with that arm's stronger "did not take effect" promise.
func classifyMutationErr(op string, err error) (string, syscall.Errno) {
	var nferr *notFoundError
	if errors.As(err, &nferr) {
		return nferr.Detail(), syscall.ENOENT
	}
	var ferr *FieldError
	if errors.As(err, &ferr) {
		return ferr.Detail(), syscall.EINVAL
	}
	// The two arms an HTTP STATUS answers, both above retryableCreateErr because
	// a status is structure and its message fallbacks are text — the same
	// structure-outranks-text rule the arms below follow (#447).
	//
	// A 429 is NOT handled here: api.IsRateLimited now recognises it by status,
	// so it reaches the retryableCreateErr arm below and gets that arm's
	// "did not take effect" wording, which is correct — a 429 is refused at
	// admission and never reaches a mutation handler.
	if api.IsAuthFailure(err) {
		status, _ := api.HTTPStatus(err)
		return "Operation: " + op + "\nError: Linear rejected this request's CREDENTIALS (HTTP " + strconv.Itoa(status) +
			"), so the operation did NOT take effect. Retrying will NOT help: the API key is missing, revoked, mistyped, or lacks the scope for this operation. " +
			"Fix the key — LINEAR_API_KEY in the environment, or api_key in ~/.config/linearfs/config.yaml — and retry. " +
			"The response body is deliberately not echoed here: a rejection at this layer is often an HTML page from a proxy rather than anything Linear wrote.", syscall.EACCES
	}
	// A 5xx is Linear's own side failing, not a rejection of what we sent, so
	// EAGAIN — EIO told the caller to stop when waiting is the fix.
	//
	// The wording deliberately does NOT promise the operation had no effect,
	// which is where this departs from #447's sketch. That issue grouped 5xx with
	// 429 as "never reached a mutation handler"; that holds for a 503 turned away
	// at an edge proxy, but a 500 is the application itself failing and may have
	// applied the mutation before losing the response. Nothing in the status
	// separates the two. #399 settled which way to guess when the outcome is
	// unknowable: a false "did not take effect" costs a duplicated entity on
	// retry, a false "unknown" costs one existence check, so the conservative
	// claim is the correct default.
	if api.IsServerTransient(err) {
		status, _ := api.HTTPStatus(err)
		return "Operation: " + op + "\nError: Linear returned HTTP " + strconv.Itoa(status) +
			" — a fault on Linear's side, not a rejection of what you sent. Wait a few seconds and retry. " +
			"Whether it took effect is UNKNOWN: the request did reach Linear, so it may have been applied and the response lost. " +
			"CHECK whether the entity exists (read the directory listing, or .last) before retrying a create, or a blind retry can create a duplicate.", syscall.EAGAIN
	}
	if retryableCreateErr(err) {
		// Both are EAGAIN — retry is the right move either way — but they cannot
		// make the same promise. A request that never left (budget deferral,
		// cancelled pre-send wait, tripped breaker) provably had no effect. One
		// that died mid-flight may have been processed with the response lost, so
		// claiming "did not take effect" there is a guess the caller acts on: a
		// retry of an in-flight create can duplicate the entity (#399).
		if api.IsOutcomeUnknown(err) {
			return "Operation: " + op + "\nError: the request was interrupted after it was sent, so whether it took effect is UNKNOWN — it may have been applied and the response lost. Wait a few seconds, then CHECK whether the entity exists (read the directory listing, or .last) before retrying: a blind retry can create a duplicate.", syscall.EAGAIN
		}
		return "Operation: " + op + "\nError: the request was rate-limited or deferred before it was sent, so the operation did not take effect. Wait a few seconds and retry.", syscall.EAGAIN
	}
	// The same verdict as the *notFoundError arm above, when LINEAR is the one
	// saying the entity is gone rather than our own pre-send resolution: the
	// reference does not exist, so ENOENT — the errno the mount's contract
	// already gives "reference to something that doesn't exist" (#445). Without
	// this arm it fell to the EIO fallthrough, which the generated README and
	// docs/ARCHITECTURE.md both teach as a retryable backend fault; it is not,
	// and every retry earns the same rejection. Reachable whenever the local
	// catalog is ahead of the workspace (an entity archived or deleted between a
	// read and a write).
	//
	// Placement is the whole contract here. Condition, not tag — so, like the
	// arms below, it sits ABOVE the userError gate (#409). But it sits BELOW the
	// typed *FieldError and retryableCreateErr arms, because api.IsNotFound
	// answers on TEXT while those two answer on structure, and text can be the
	// caller's own: *FieldError renders the caller's frontmatter value verbatim,
	// so a status of "Entity not found" would otherwise pick its own errno, and
	// a throttled request whose envelope also names a missing entity would be
	// reported permanently unfixable when waiting is exactly what fixes it.
	// api.IsNotFound is anchored against the same class at the source; the order
	// here is the belt to that predicate's braces.
	//
	// The delete tail's MUTATE step never reaches this arm: remoteAlreadyGone
	// claims that rejection first, where already-gone is idempotent success. A
	// delete whose FIND fails this way is not behind that gate and does classify
	// here.
	if api.IsNotFound(err) {
		return "Operation: " + op + "\nError: " + serverClause(err) +
			". The referenced entity no longer exists on Linear, so retrying will NOT help. The local listing may still show it until the next sync cycle reconciles the cache.", syscall.ENOENT
	}
	// A workspace over its plan/usage limit is neither the caller's bad input nor
	// a backend fault — it is a capacity wall. EDQUOT makes the errno itself the
	// hint (cf. EMSGSIZE below), and this arm sits ABOVE the userError gate
	// deliberately: whether Linear tags this rejection userError is unobserved
	// (#409), so classifying on the CONDITION rather than on the tag is what makes
	// the errno deterministic either way.
	if api.IsUsageLimited(err) {
		return "Operation: " + op + "\nError: this workspace is over a plan/usage limit, so the operation did NOT take effect. Retrying will NOT help until the workspace has room — archive or delete entities, or raise the workspace's plan limit, then retry. Linear said: " + serverDetail(err), syscall.EDQUOT
	}
	// A length-cap rejection is a size error, not merely malformed input:
	// EMSGSIZE makes the errno itself a hint (the reason still lands in .error).
	// Also above the userError gate, and for the same reason: the two cases
	// api.IsFieldTooLong documents — a cap phrasing carried only in
	// UserPresentableMessage, and one arriving as a plain HTTP-400 envelope —
	// reach us with the tag unset, and used to fall through to EIO.
	if api.IsFieldTooLong(err) {
		return "Operation: " + op + "\nError: " + serverDetail(err), syscall.EMSGSIZE
	}
	// A structured Linear input rejection (userError: true) is the caller's
	// bad input, not a backend failure: EINVAL, preferring the server's
	// user-presentable message over its terse internal one (live example:
	// "The label 'X' is a group and cannot be assigned to projects directly."
	// vs internal "labelIds contain parent labels").
	var gqlErr *api.GraphQLError
	if errors.As(err, &gqlErr) && gqlErr.UserError {
		return "Operation: " + op + "\nError: " + serverDetail(err), syscall.EINVAL
	}
	// Everything else is a backend fault: EIO. The DETAIL still goes through
	// serverDetail, because "did Linear tag this userError?" is a choice about
	// how the server labelled the rejection, not a fact about which text is
	// useful. An untagged rejection carries the same UserPresentableMessage the
	// tagged one does, and err.Error() throws it away in favour of the terse
	// internal message (live: "GraphQL error: Argument Validation Error", where
	// the user-presentable text was "name must be at most 80 characters").
	// Non-GraphQL errors are unaffected — serverDetail falls through to
	// err.Error() for them. This changes the text only; the errno is unchanged,
	// and reclassifying untagged validation phrasings as EINVAL is deliberately
	// NOT done here (#446 part 2, which needs its own judgement).
	return "Operation: " + op + "\nError: " + serverDetail(err), syscall.EIO
}

// serverSaysGone reports whether LINEAR answered "entity not found" — the one
// verdict that says a local row is an orphan, and the trigger for the mutation
// tails' recheck hint (#477).
//
// It is deliberately not "errno == ENOENT". Two other things wear that errno
// and neither means the cache is stale:
//
//   - a *notFoundError is a LOCAL resolution failure — the caller named a
//     relation target or a parent the catalog does not have. Nothing upstream
//     was asked, so there is nothing to prune and nothing to re-ask about.
//   - a delete or rename whose find step returns no entry answers ENOENT
//     without an error at all, and commitDelete already self-heals that row.
//
// Everything else here mirrors classifyMutationErr's arm ORDER rather than
// repeating each arm's reasoning: api.IsNotFound answers on message TEXT, so
// EVERY arm the classifier places above it wins over a not-found reading of the
// same error, and this predicate must reach the same conclusion or the hint
// fires on a verdict the classifier does not call not-found. That is four arms,
// each excluded for its own reason and none of them optional:
//
//   - *FieldError renders the caller's own frontmatter value verbatim, so its
//     text is not Linear speaking about an entity at all.
//   - an auth failure (401/403) is a statement about our CREDENTIALS; whatever
//     body rode along — often a proxy's page, not Linear's envelope — says
//     nothing about whether the entity exists.
//   - a 5xx is Linear's own side failing, and the classifier calls it EAGAIN;
//     a recheck there fetches during the backoff, and the background refresh's
//     own orphanOnNotFound would then weigh the same 5xx text.
//   - a throttled or deferred request is EAGAIN because waiting is what fixes
//     it — a recheck adds a fetch during the exact window Linear is asking us
//     to back off.
//
// TestServerSaysGoneAgreesWithTheClassifier pins the agreement in both
// directions, so the two cannot drift apart.
func serverSaysGone(err error) bool {
	var local *notFoundError
	if errors.As(err, &local) {
		return false
	}
	var field *FieldError
	if errors.As(err, &field) {
		return false
	}
	if api.IsAuthFailure(err) || api.IsServerTransient(err) {
		return false
	}
	if retryableCreateErr(err) {
		return false
	}
	return api.IsNotFound(err)
}

// serverDetail renders the most caller-useful text Linear supplied: its
// user-presentable message where it set one, else the terse internal message,
// else the raw error. It exists for the arms that classify on the CONDITION
// rather than on the userError tag — those cannot assume the tag is set, so they
// cannot reach for the message the way the EINVAL arm historically did.
func serverDetail(err error) string {
	var gqlErr *api.GraphQLError
	if errors.As(err, &gqlErr) {
		if gqlErr.UserPresentableMessage != "" {
			return gqlErr.UserPresentableMessage
		}
		if gqlErr.Message != "" {
			return gqlErr.Message
		}
	}
	return err.Error()
}

// serverClause renders serverDetail(err) as a clause an arm can append its own
// sentence to. Linear's canonical messages already end in a full stop ("Entity
// not found: Issue - Could not find referenced Issue."), so joining onto the raw
// detail doubles it — a typo in exactly the .error sentence an agent is meant to
// read and act on. Pure string transform: trailing sentence punctuation off, the
// wording of neither half touched.
func serverClause(err error) string {
	return strings.TrimRight(serverDetail(err), " .!?")
}
