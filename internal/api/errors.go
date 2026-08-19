package api

import (
	"errors"
	"regexp"
	"strings"
)

// ErrDeferred marks an error as the client's OWN admission ladder deferring a
// request — the local rate budget said "not right now". It is deliberately
// distinct from a server rate limit (IsRateLimited): a defer clears on the
// ladder's minute-scale timescale (retry next cycle), whereas a server
// RATELIMITED warrants a long pause until the window resets. Conflating them
// cost an hour of detail-sync latency on deploy day when the worker paused for a
// full hour on a local defer (#257). The pagination-preflight ErrBudget is the
// same class; IsDeferred recognizes both.
var ErrDeferred = errors.New("request deferred: rate-limit budget low")

// IsDeferred reports whether err is a local budget deferral (ErrDeferred or the
// pagination-preflight ErrBudget) rather than a server rate limit. Callers that
// back off hard on a server rate limit must treat a defer as skip-this-cycle.
func IsDeferred(err error) bool {
	return errors.Is(err, ErrDeferred) || errors.Is(err, ErrBudget)
}

// ErrInFlight marks a request whose HTTP POST had already been sent when its
// context died — a cancelled or timed-out request, where the SERVER's view is
// unknown: it may have processed the mutation and lost the response, or never
// received it. Live example (#399): a mkdir interrupted mid-create logged
// `Post "https://api.linear.app/graphql": context canceled`.
//
// It exists so a caller can tell that apart from the failures that provably
// never reached Linear — a budget deferral, a cancelled pre-send rate-limit
// wait, a tripped circuit breaker. All of them are retryable, but only the
// pre-send ones can honestly say "the operation did not take effect", and a
// retry of an in-flight create can duplicate the entity.
var ErrInFlight = errors.New("request was in flight when its context ended; outcome unknown")

// IsOutcomeUnknown reports whether err left the request's outcome genuinely
// undetermined (see ErrInFlight). Callers phrasing a retry hint must not claim
// the operation had no effect when this is true.
func IsOutcomeUnknown(err error) bool { return errors.Is(err, ErrInFlight) }

// Error predicates: the package-level classification of Linear API failures.
//
// Every layer above the client (fs mutation handlers, the repo's orphan
// defense, the sync worker's backoff) needs to ask the same questions about an
// error — "was that a rate limit?", "does the entity no longer exist?" — and
// each used to answer with its own substring sniff, so the checks drifted
// (different substrings, different case handling). Each predicate here is the
// single owner of its question, and layers above the client delegate to it
// rather than sniffing substrings themselves. All of them prefer the structured
// *GraphQLError (errors.As, so wrapping is transparent) and keep the message
// fallbacks for errors that never carried the type: HTTP-level failures are
// plain fmt.Errorf strings carrying Linear's error envelope verbatim.

// IsRateLimited reports whether err is Linear telling us the account's
// request or complexity budget is exhausted. Structured check first: Linear
// tags budget exhaustion with extensions {code: "RATELIMITED"}. The message
// fallbacks cover HTTP 429/400 failures that surface as plain strings
// ("RATELIMITED" in Linear's error envelope, or a "rate limit ..." message,
// case-insensitive).
//
// Deliberately NOT absorbed: the client's "circuit breaker" connectivity
// error. That is a client-side transient, not the server rate limiting us —
// callers that retry on both (retryableCreateErr) check it separately.
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	// A local budget deferral is NOT a server rate limit — it clears on the
	// admission ladder's own timescale, so it must not trip a long server-rate-
	// limit backoff (#257). The typed check takes precedence over the message
	// fallback below.
	if IsDeferred(err) {
		return false
	}
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) && gqlErr.Code == "RATELIMITED" {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "RATELIMITED") ||
		strings.Contains(strings.ToLower(msg), "rate limit")
}

// IsUsageLimited reports whether err is Linear refusing the mutation because the
// WORKSPACE is over a plan/usage limit — a capacity wall, not a request budget.
// It is deliberately distinct from IsRateLimited: a rate limit clears when the
// window resets, so waiting is the fix, whereas no amount of waiting clears a
// plan limit. Only archiving/deleting entities or raising the plan does, which
// is why classifyMutationErr maps this to EDQUOT rather than to the retryable
// EAGAIN (#409).
//
// The check is message-shaped because Linear's extensions.code for this
// rejection has never been observed — the only recorded instance is the bare
// string "usage limit exceeded". If a code is ever captured, add a structured
// check in first position, matching IsRateLimited's structured-first layering.
// Capturing it no longer needs a reproduction: every rejection's decoded
// extensions are written down as they arrive — the client's per-rejection log
// line (rejectionLogLine, which renders GraphQLError.LogDetail under a prefix
// that carries the severity verdict) and, when the request debug log is on, the
// `error` object on the requests.jsonl line. Both carry userPresentableMessage
// as well as message, which matters because that is where the quota wording
// lands when Linear rejects with a generic message.
// Grep either for a usage-limit message and read its code/type (#448).
//
// For THIS predicate a false positive is strictly worse than a false negative,
// so the match is biased hard toward missing. A miss degrades to the pre-#409
// behavior — EIO, or EINVAL when Linear happened to tag the rejection
// userError — which is the bug being fixed and no worse than before it. A false
// hit actively lies: it tells a caller whose input is fixable that the
// workspace is over quota and that retrying will NOT help.
//
// That asymmetry is why the phrase must CONSTITUTE the server's message rather
// than appear anywhere within it. Linear echoes user-supplied entity names into
// UserPresentableMessage ("The label 'X' is a group and cannot be assigned to
// projects directly."), so a substring test hands a workspace that owns a label
// named "Usage limits" a bogus quota verdict on every validation rejection that
// names it. An echo is always embedded in a sentence while the recorded quota
// message is bare, so an anchored whole-message test separates the two and
// still tolerates a rewording like "workspace usage limit reached".
func IsUsageLimited(err error) bool {
	if err == nil {
		return false
	}
	// A server rate limit is NOT a plan wall. The typed check takes precedence so
	// a future widening of IsRateLimited's message fallback cannot land a request
	// budget in the arm that tells the caller waiting will not help.
	if IsRateLimited(err) {
		return false
	}
	// The same matcher in both positions: the recorded quota message is bare, so
	// there is no wrapper prefix for it to tolerate.
	return matchesServerMessage(err, isUsageLimitMessage, isUsageLimitMessage)
}

// matchesServerMessage runs the shape every message-keyed predicate here shares.
// The structured *GraphQLError comes first (errors.As, so wrapping is
// transparent) and both of its message fields are asked. Otherwise err is an
// HTTP-level failure carrying Linear's envelope verbatim, where the server's
// messages are quoted values INSIDE a JSON body — so the whole-message rule
// applies to each extracted value, never to the envelope text, which embeds an
// echoed name exactly as it embeds a real rejection.
//
// The two matchers differ by the SOURCE of the text they judge, which is why
// this takes both. isServerMessage sees only what Linear wrote, so it may demand
// the phrase constitute the whole message. isWrappedMessage sees the flattened
// error string, which OUR OWN layers prefix — (*GraphQLError).Error() prepends
// "GraphQL error: ", the client prepends "API error (status N): ", and every
// fmt.Errorf %w prepends more — so a predicate that must survive those passes a
// looser matcher in that position only. A predicate with nothing to tolerate
// passes the same matcher twice.
//
// err must be non-nil; every caller guards.
func matchesServerMessage(err error, isServerMessage, isWrappedMessage func(string) bool) bool {
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) {
		return isServerMessage(gqlErr.Message) ||
			isServerMessage(gqlErr.UserPresentableMessage)
	}
	msg := err.Error()
	for _, m := range envelopeMessageRe.FindAllStringSubmatch(msg, -1) {
		if isServerMessage(m[1]) {
			return true
		}
	}
	return isWrappedMessage(msg)
}

// normalizeServerMessage reduces a message to the form the anchored matchers
// judge — case and a trailing full stop are the only slack any of them allows.
func normalizeServerMessage(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSpace(strings.TrimRight(s, ".!"))
}

// usageLimitMessageRe matches a message that IS a usage-limit rejection rather
// than one that merely mentions the phrase: the bare wording, optionally scoped
// ("workspace usage limit") and optionally closed with a limit verb ("...
// exceeded" / "... reached"). Anchored on purpose — see IsUsageLimited.
var usageLimitMessageRe = regexp.MustCompile(
	`^(?:the |your )?(?:workspace |organization |account |plan |team )?usage limit(?: (?:exceeded|reached|hit))?$`)

// envelopeMessageRe pulls the server's quoted message values out of Linear's
// error envelope when it reaches a predicate as a plain string.
var envelopeMessageRe = regexp.MustCompile(
	`"(?:message|userPresentableMessage)"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// isUsageLimitMessage reports whether s, taken whole, is Linear's usage-limit
// wording.
func isUsageLimitMessage(s string) bool {
	return usageLimitMessageRe.MatchString(normalizeServerMessage(s))
}

// IsNotFound reports whether err is Linear's "Entity not found" rejection —
// the entity the request referenced no longer exists upstream. Structured
// check first ("Entity not found: <Type> - ..." is Linear's standard message
// on the GraphQL error, and the phrasing can ride in either message field, as
// IsFieldTooLong's can); the fallback covers not-found rejections that arrive
// as plain strings (e.g. an HTTP 400 whose body carries the error envelope).
//
// The verdicts this predicate feeds are opposite by caller: for a delete it is
// idempotent success (the entity is already gone), for a refresh it marks the
// local row an orphan to be cleaned up, and for a create/edit/rename it is
// ENOENT with "retrying will NOT help" (#445). None of those are recoverable
// from a wrong answer, so — exactly as for IsUsageLimited — the phrase must
// CONSTITUTE a message rather than appear anywhere within it. Linear echoes
// user-supplied entity names into UserPresentableMessage ("The label 'X' is a
// group and cannot be assigned to projects directly."), so an unanchored
// substring test hands a workspace that owns a label named "Entity not found"
// a bogus gone verdict on every validation rejection that names it — and hands
// the same verdict to any error whose text merely quotes the phrase, including
// our own *FieldError rendering of a caller's frontmatter value. An echo is
// always embedded mid-sentence while the real rejection OPENS its message, so
// an anchored test separates the two and still tolerates the type suffix.
//
// That invariant only holds of text Linear itself wrote, which is why the two
// matchers below split by source: server-supplied text (both *GraphQLError
// message fields, and every value extracted from the envelope) is judged by the
// strict opens-the-message rule, and only the flattened error string — where the
// prefixes are OUR OWN — gets any slack. See notFoundWrappedRe.
//
// No extensions.code for this rejection has ever been observed; if one is ever
// captured, add a structured check in first position, matching IsRateLimited's
// structured-first layering.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	// A rate limit is NOT proof the entity is gone. Without this precedence a
	// throttled DELETE — whose gate (remoteAlreadyGone) asks this predicate
	// directly, below the fs classifier's arm order — would report idempotent
	// success and forget the local row for an entity Linear never deleted.
	if IsRateLimited(err) {
		return false
	}
	return matchesServerMessage(err, isNotFoundServerMessage, isNotFoundWrappedMessage)
}

// notFoundTail is the phrase itself plus the optional type suffix, shared by
// both matchers below so that widening it (a new separator, a new phrasing)
// cannot land on one and miss the other. The two differ ONLY in the leading
// anchor they prepend, which is the whole point of the source split.
const notFoundTail = `entity not found(?:\s*[:\x{2013}\x{2014}-].*)?$`

// notFoundMessageRe matches a message that IS Linear's not-found rejection
// rather than one that merely mentions the phrase: the phrase OPENS the message,
// and anything after it is introduced by the type separator ("Entity not found:
// Issue - Could not find referenced Issue."). Strictly anchored, exactly like
// usageLimitMessageRe and for the same reason — see IsNotFound.
var notFoundMessageRe = regexp.MustCompile(`^` + notFoundTail)

// notFoundWrappedRe is notFoundMessageRe with one tolerance: the phrase may open
// a clause a wrapper prefixed with ": ". That slack exists ONLY for our own
// wrappers — (*GraphQLError).Error() prepends "GraphQL error: ", the client
// prepends "API error (status N): ", and each fmt.Errorf %w prepends more — so
// it is applied only to the flattened error string. Applying it to server text
// would read a trailing echo ("Cannot assign label: Entity not found") as a
// rejection, which is the hazard the anchoring exists to close.
var notFoundWrappedRe = regexp.MustCompile(`(?:^|: )` + notFoundTail)

// isNotFoundServerMessage reports whether s, as Linear wrote it, IS the
// not-found rejection.
func isNotFoundServerMessage(s string) bool {
	return notFoundMessageRe.MatchString(normalizeServerMessage(s))
}

// isNotFoundWrappedMessage is isNotFoundServerMessage plus tolerance for the
// prefixes our own layers add — see notFoundWrappedRe.
func isNotFoundWrappedMessage(s string) bool {
	return notFoundWrappedRe.MatchString(normalizeServerMessage(s))
}

// IsFieldTooLong reports whether err is Linear rejecting a field for exceeding
// its length cap — e.g. "description must be shorter than or equal to 255
// characters." This is a size limit, not merely malformed input, so callers
// (classifyMutationErr) can surface EMSGSIZE instead of the bare EINVAL or EIO
// the rejection would otherwise get — the tag Linear sets decides which, and
// both are cases this predicate exists to rescue (#409) — making the errno
// itself a hint. Structured check first (the phrasing rides in
// Message/UserPresentableMessage), with a plain-string fallback. The two
// substrings are the phrasings Linear uses for a max-length validation.
func IsFieldTooLong(err error) bool {
	if err == nil {
		return false
	}
	has := func(s string) bool {
		s = strings.ToLower(s)
		return strings.Contains(s, "shorter than or equal to") ||
			strings.Contains(s, "must be at most")
	}
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) && (has(gqlErr.Message) || has(gqlErr.UserPresentableMessage)) {
		return true
	}
	return has(err.Error())
}
