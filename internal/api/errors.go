package api

import (
	"errors"
	"time"
)

// RateLimitedError indicates Linear rejected — or the client pre-emptively
// deferred — a request because the rate budget was exhausted. RetryAt, when
// non-zero, is the server-reported window reset (from X-RateLimit-Reset);
// callers should back off until then.
//
// It is the single typed classification of a rate-limit condition. The API layer
// is the one place that knows Linear's wire signals (an HTTP 429, a "RATELIMITED"
// GraphQL error), so it converts them to this type once; downstream layers
// classify with IsRateLimited instead of re-matching error strings.
type RateLimitedError struct {
	Msg     string
	RetryAt time.Time
}

func (e *RateLimitedError) Error() string { return e.Msg }

// transientError marks a non-rate-limit failure the client still considers worth
// retrying — currently the circuit breaker tripping during a connectivity outage.
// Unexported: callers classify via IsRetryable, never by the concrete type.
type transientError struct{ msg string }

func (e *transientError) Error() string { return e.msg }

// IsRateLimited reports whether err (or a wrapped cause) is a Linear rate-limit
// rejection.
func IsRateLimited(err error) bool {
	var rl *RateLimitedError
	return errors.As(err, &rl)
}

// IsRetryable reports whether err is transient and worth retrying — a rate limit
// or a tripped circuit breaker. Context cancellation/timeout is intentionally not
// included; callers that care classify it with errors.Is(context.Canceled/…).
func IsRetryable(err error) bool {
	if IsRateLimited(err) {
		return true
	}
	var te *transientError
	return errors.As(err, &te)
}
