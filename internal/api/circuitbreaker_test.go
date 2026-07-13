package api

import (
	"testing"
	"time"
)

// circuitBreaker's whole state machine (trip at threshold, refuse while
// cooling, one HALF-OPEN probe at expiry, close on success / re-trip on a
// failed probe) is driven here with the package's fakeClock and no HTTP — the
// isolation the two-atomics-in-query() shape never had. fakeClock is defined in
// ratebudget_test.go (same package).

const (
	testThreshold = 3
	testCooldown  = 30 * time.Second
)

func newTestBreaker() (*circuitBreaker, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	return newCircuitBreaker(testThreshold, testCooldown, clk.now), clk
}

func TestCircuitBreakerClosedUntilThreshold(t *testing.T) {
	t.Parallel()
	cb, _ := newTestBreaker()

	// Below threshold: never trips, always allows.
	for i := 1; i < testThreshold; i++ {
		tripped, count := cb.recordFailure()
		if tripped {
			t.Fatalf("failure %d tripped the breaker before threshold %d", i, testThreshold)
		}
		if count != i {
			t.Errorf("failure %d reported count %d", i, count)
		}
		if !cb.allow() {
			t.Errorf("breaker refused a request below threshold (after %d failures)", i)
		}
	}

	// The threshold-th failure trips it.
	tripped, count := cb.recordFailure()
	if !tripped || count != testThreshold {
		t.Fatalf("threshold failure: tripped=%v count=%d, want true/%d", tripped, count, testThreshold)
	}
	if cb.allow() {
		t.Error("breaker allowed a request immediately after tripping")
	}
}

func TestCircuitBreakerSuccessResetsCount(t *testing.T) {
	t.Parallel()
	cb, _ := newTestBreaker()

	cb.recordFailure()
	cb.recordFailure() // one below threshold
	cb.recordSuccess() // resets the count

	// After the reset it takes a full `threshold` run to trip again.
	for i := 1; i < testThreshold; i++ {
		if tripped, _ := cb.recordFailure(); tripped {
			t.Fatalf("tripped after only %d post-reset failures", i)
		}
	}
	if tripped, _ := cb.recordFailure(); !tripped {
		t.Error("did not trip after a full post-reset failure run")
	}
}

func TestCircuitBreakerCooldownAndProbe(t *testing.T) {
	t.Parallel()
	cb, clk := newTestBreaker()

	// Trip it.
	for i := 0; i < testThreshold; i++ {
		cb.recordFailure()
	}
	if cb.allow() {
		t.Fatal("open breaker allowed a request")
	}

	// Still cooling one instant before expiry.
	clk.advance(testCooldown - time.Nanosecond)
	if cb.allow() {
		t.Error("breaker allowed a request before the cooldown expired")
	}

	// At expiry: exactly ONE probe is allowed (the deadline clears)...
	clk.advance(time.Nanosecond)
	if !cb.allow() {
		t.Error("breaker refused the half-open probe after cooldown expiry")
	}
	// ...and because openUntil was cleared, subsequent allows stay open too
	// (the count is still at/above threshold, but the breaker is now closed
	// pending the probe's outcome).
	if !cb.allow() {
		t.Error("breaker did not stay closed after clearing the cooldown")
	}
}

func TestCircuitBreakerFailedProbeReTrips(t *testing.T) {
	t.Parallel()
	cb, clk := newTestBreaker()

	for i := 0; i < testThreshold; i++ {
		cb.recordFailure()
	}
	clk.advance(testCooldown) // cooldown expires
	if !cb.allow() {
		t.Fatal("probe not allowed after cooldown")
	}

	// The probe FAILS: since allow() kept the count (>= threshold), a single
	// failure re-trips immediately — the half-open contract.
	tripped, _ := cb.recordFailure()
	if !tripped {
		t.Error("a failed half-open probe did not re-trip the breaker")
	}
	if cb.allow() {
		t.Error("breaker allowed a request after a failed probe re-tripped it")
	}
}

func TestCircuitBreakerSuccessfulProbeCloses(t *testing.T) {
	t.Parallel()
	cb, clk := newTestBreaker()

	for i := 0; i < testThreshold; i++ {
		cb.recordFailure()
	}
	clk.advance(testCooldown)
	if !cb.allow() {
		t.Fatal("probe not allowed after cooldown")
	}

	// The probe SUCCEEDS: the count resets, so it now takes a full threshold
	// run to trip again.
	cb.recordSuccess()
	for i := 1; i < testThreshold; i++ {
		if tripped, _ := cb.recordFailure(); tripped {
			t.Fatalf("re-tripped after only %d failures following a successful probe", i)
		}
	}
	if tripped, _ := cb.recordFailure(); !tripped {
		t.Error("did not trip after a full failure run following a successful probe")
	}
}
