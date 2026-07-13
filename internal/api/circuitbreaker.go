package api

import (
	"sync"
	"time"
)

// circuitBreaker stops the client from burning rate-limiter tokens on requests
// that will fail during a connectivity loss (DNS outage, network partition).
// After `threshold` consecutive transport failures it trips OPEN for `cooldown`;
// while open, allow() refuses every request. When the cooldown expires it goes
// HALF-OPEN: allow() lets exactly one probe through (it clears the open deadline
// but keeps the failure count), so a failed probe re-trips immediately at the
// next recordFailure and only a successful one (recordSuccess) closes it.
//
// It is the isolated, clock-injected sibling of rateBudget: the whole state
// machine lives behind allow()/recordFailure()/recordSuccess() and is driven in
// tests with a fake clock and no HTTP (see circuitbreaker_test.go). Before this,
// the same logic was two atomic fields and three inline branches smeared across
// Client.query(), with no test and a benign race at the cooldown edge (N
// racing goroutines all got a probe instead of one).
//
// The clock is injected (now func() time.Time), matching rateBudget; the caller
// owns logging (recordFailure returns the trip facts) so the module never
// imports log and stays a pure, assertable state machine.
type circuitBreaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu                sync.Mutex
	consecutiveErrors int
	openUntil         time.Time // zero = closed
}

// newCircuitBreaker builds a breaker tripping after threshold consecutive
// failures and cooling down for cooldown, reading time from now.
func newCircuitBreaker(threshold int, cooldown time.Duration, now func() time.Time) *circuitBreaker {
	return &circuitBreaker{threshold: threshold, cooldown: cooldown, now: now}
}

// allow reports whether a request may proceed. It returns true when closed;
// false while open and still cooling; and true — after clearing the open
// deadline, letting exactly one HALF-OPEN probe through — once the cooldown has
// expired. It never touches the failure count: a failed probe re-trips via
// recordFailure, a successful one closes via recordSuccess.
func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.openUntil.IsZero() {
		return true
	}
	if cb.now().Before(cb.openUntil) {
		return false
	}
	cb.openUntil = time.Time{} // cooldown expired — allow one probe
	return true
}

// recordFailure counts a transport failure and trips the breaker when the count
// reaches threshold, arming the cooldown from now. It returns whether this
// failure tripped it and the current consecutive-error count, so the caller can
// log the trip edge (the module itself never logs).
func (cb *circuitBreaker) recordFailure() (tripped bool, count int) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveErrors++
	if cb.consecutiveErrors >= cb.threshold {
		cb.openUntil = cb.now().Add(cb.cooldown)
		return true, cb.consecutiveErrors
	}
	return false, cb.consecutiveErrors
}

// recordSuccess resets the failure count after a request that reached the
// network, closing a half-open breaker.
func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	cb.consecutiveErrors = 0
	cb.mu.Unlock()
}
