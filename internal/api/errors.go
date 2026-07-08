package api

import (
	"errors"
	"strings"
)

// Error predicates: the package-level classification of Linear API failures.
//
// Every layer above the client (fs mutation handlers, the repo's orphan
// defense, the sync worker's backoff) needs to answer the same two questions
// about an error — "was that a rate limit?" and "does the entity no longer
// exist?" — and each used to answer with its own substring sniff, so the
// checks drifted (different substrings, different case handling). These two
// predicates are the single owners. Both prefer the structured *GraphQLError
// (errors.As, so wrapping is transparent) and keep the message fallbacks for
// errors that never carried the type: HTTP-level failures are plain
// fmt.Errorf strings carrying Linear's error envelope verbatim.

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
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) && gqlErr.Code == "RATELIMITED" {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "RATELIMITED") ||
		strings.Contains(strings.ToLower(msg), "rate limit")
}

// IsNotFound reports whether err is Linear's "Entity not found" rejection —
// the entity the request referenced no longer exists upstream. Structured
// check first ("Entity not found: <Type> - ..." is Linear's standard message
// on the GraphQL error); the fallback covers not-found rejections that arrive
// as plain strings (e.g. an HTTP 400 whose body carries the error envelope).
//
// For a delete this is idempotent success (the entity is already gone); for a
// refresh it marks the local row an orphan to be cleaned up.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) && strings.Contains(gqlErr.Message, "Entity not found") {
		return true
	}
	return strings.Contains(err.Error(), "Entity not found")
}
