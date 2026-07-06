package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// fakeClock drives rateBudget's injected now() in tests.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }
func newFakeClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)} }
func testBudget(c *fakeClock) *rateBudget    { return newRateBudget(c.now) }
func hdr(kv map[string]string) http.Header {
	h := http.Header{}
	for k, v := range kv {
		h.Set(k, v)
	}
	return h
}

// fullHeaders builds a complete synthetic Linear rate-limit header set.
func fullHeaders(cost, cLimit, cRemaining, rLimit, rRemaining float64, reset time.Time) http.Header {
	return hdr(map[string]string{
		"X-Complexity":                     strconv.FormatFloat(cost, 'f', -1, 64),
		"X-RateLimit-Complexity-Limit":     strconv.FormatFloat(cLimit, 'f', -1, 64),
		"X-RateLimit-Complexity-Remaining": strconv.FormatFloat(cRemaining, 'f', -1, 64),
		"X-RateLimit-Complexity-Reset":     strconv.FormatInt(reset.UnixMilli(), 10),
		"X-RateLimit-Requests-Limit":       strconv.FormatFloat(rLimit, 'f', -1, 64),
		"X-RateLimit-Requests-Remaining":   strconv.FormatFloat(rRemaining, 'f', -1, 64),
		"X-RateLimit-Requests-Reset":       strconv.FormatInt(reset.UnixMilli(), 10),
	})
}

// seedWindows installs known window state directly (white-box), the way an
// earlier observe would have.
func seedWindows(b *rateBudget, complexity, requests window) {
	b.mu.Lock()
	defer b.mu.Unlock()
	complexity.name = "complexity"
	requests.name = "requests"
	b.complexity = complexity
	b.requests = requests
}

// TestRateBudget_ReserveLadder: under a drained complexity budget
// (120k of 1M remaining), low tiers defer while skeleton/interactive/write
// still pass; an unmeasured op costs defaultPredictedCost (10k).
func TestRateBudget_ReserveLadder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		p         priority
		wantAllow bool
	}{
		{pDetail, false},     // reserve 400k > 120k remaining
		{pList, false},       // reserve 150k > 120k remaining
		{pSkeleton, true},    // reserve 50k: 120k-50k=70k >= 10k
		{pInteractive, true}, // reserve 20k
		{pWrite, true},       // reserve 0
	}

	for _, tt := range tests {
		t.Run(tt.p.String(), func(t *testing.T) {
			clock := newFakeClock()
			b := testBudget(clock)
			seedWindows(b,
				window{limit: 1000000, remaining: 120000, resetAt: clock.t.Add(time.Hour), seen: true},
				window{limit: 2500, remaining: 2400, resetAt: clock.t.Add(time.Hour), seen: true},
			)
			adm, dec := b.admit("SomeOp", tt.p)
			if got := adm != nil; got != tt.wantAllow {
				t.Fatalf("admit at %s: allow=%v (reason %q), want %v", tt.p, got, dec.reason, tt.wantAllow)
			}
			if !tt.wantAllow && dec.retryAfter != time.Hour {
				t.Errorf("retryAfter = %v, want %v (time to reset)", dec.retryAfter, time.Hour)
			}
			if adm != nil {
				adm.release()
			}
		})
	}
}

// TestRateBudget_ReserveLadderRequestsAxis: the ladder gates the requests
// axis independently — a drained request count defers details even with a
// full complexity tank.
func TestRateBudget_ReserveLadderRequestsAxis(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	b := testBudget(clock)
	seedWindows(b,
		window{limit: 3000000, remaining: 3000000, resetAt: clock.t.Add(time.Hour), seen: true},
		window{limit: 2500, remaining: 300, resetAt: clock.t.Add(time.Hour), seen: true},
	)

	if adm, dec := b.admit("Op", pDetail); adm != nil {
		t.Errorf("detail admit should defer on drained requests axis, got allow (reason %q)", dec.reason)
	}
	adm, dec := b.admit("Op", pWrite)
	if adm == nil {
		t.Fatalf("write admit should pass with 300 requests remaining: %q", dec.reason)
	}
	adm.release()
}

// TestRateBudget_ObserveReconciles: observe snaps limit/remaining/resetAt
// for BOTH axes from the headers (reset parsed as epoch milliseconds) and
// records the op's actual X-Complexity. Header keys are matched
// case-insensitively (canonicalized).
func TestRateBudget_ObserveReconciles(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	b := testBudget(clock)
	reset := clock.t.Add(37 * time.Minute).Truncate(time.Millisecond)

	adm, _ := b.admit("TeamMetadata", pSkeleton) // unseen axes: admits freely
	if adm == nil {
		t.Fatal("fresh budget must admit before any observation")
	}

	// Deliberately weird casing: http.Header canonicalizes on Set/Get.
	h := hdr(map[string]string{
		"x-complexity":                     "1234",
		"x-ratelimit-complexity-limit":     "3000000",
		"x-ratelimit-complexity-remaining": "2716000",
		"x-ratelimit-complexity-reset":     strconv.FormatInt(reset.UnixMilli(), 10),
		"x-ratelimit-requests-limit":       "2500",
		"x-ratelimit-requests-remaining":   "2371",
		"x-ratelimit-requests-reset":       strconv.FormatInt(reset.UnixMilli(), 10),
	})
	adm.observe(h)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.complexity.limit != 3000000 || b.complexity.remaining != 2716000 || !b.complexity.seen {
		t.Errorf("complexity window = %+v, want limit 3000000 remaining 2716000 seen", b.complexity)
	}
	if !b.complexity.resetAt.Equal(reset) {
		t.Errorf("complexity resetAt = %v, want %v (UnixMilli parse)", b.complexity.resetAt, reset)
	}
	if b.requests.limit != 2500 || b.requests.remaining != 2371 || !b.requests.resetAt.Equal(reset) {
		t.Errorf("requests window = %+v, want limit 2500 remaining 2371 reset %v", b.requests, reset)
	}
	if got := b.cost["TeamMetadata"]; got != 1234 {
		t.Errorf("recorded cost = %v, want 1234 (X-Complexity)", got)
	}
	if b.inFlightCost != 0 || b.inFlightReqs != 0 {
		t.Errorf("in-flight not released: cost=%v reqs=%v", b.inFlightCost, b.inFlightReqs)
	}
}

// TestRateBudget_CostPredictor: an unmeasured op is priced at the
// conservative default (10k, the single-query max); once observed, the
// last-seen X-Complexity is used instead.
func TestRateBudget_CostPredictor(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	b := testBudget(clock)

	// Teach the budget: MeasuredOp costs 200; the same response reports
	// only 9999 complexity remaining.
	adm, _ := b.admit("MeasuredOp", pWrite)
	adm.observe(fullHeaders(200, 3000000, 9999, 2500, 2400, clock.t.Add(time.Hour)))

	// Unmeasured op: predicted 10000 > 9999 remaining, even at write tier.
	if adm, dec := b.admit("NeverSeenOp", pWrite); adm != nil {
		t.Errorf("unmeasured op should be priced at %d and defer, got allow (reason %q)", defaultPredictedCost, dec.reason)
	}
	// Measured op: predicted 200 <= 9999.
	adm, dec := b.admit("MeasuredOp", pWrite)
	if adm == nil {
		t.Fatalf("measured op (cost 200) should pass: %q", dec.reason)
	}
	adm.release()
}

// TestRateBudget_InFlightSemaphore: concurrent admissions reserve their
// predicted cost before any response lands, so admits start deferring once
// inFlight+reserve exceeds remaining; observe/release return the
// reservation.
func TestRateBudget_InFlightSemaphore(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	b := testBudget(clock)
	seedWindows(b,
		window{limit: 3000000, remaining: 35000, resetAt: clock.t.Add(time.Hour), seen: true},
		window{limit: 2500, remaining: 2400, resetAt: clock.t.Add(time.Hour), seen: true},
	)

	// Three unmeasured writes (10k each) fit in 35k; the fourth does not.
	var adms []*admission
	for i := 0; i < 3; i++ {
		adm, dec := b.admit(fmt.Sprintf("Op%d", i), pWrite)
		if adm == nil {
			t.Fatalf("admit %d should pass: %q", i, dec.reason)
		}
		adms = append(adms, adm)
	}
	if adm, dec := b.admit("Op3", pWrite); adm != nil {
		t.Fatal("4th concurrent admit should defer: inFlight 30k + cost 10k > 35k remaining")
	} else if dec.reason == "" {
		t.Error("deny decision should carry a reason")
	}

	// Observing one in-flight response releases its reservation.
	adms[0].observe(fullHeaders(10000, 3000000, 35000, 2500, 2400, clock.t.Add(time.Hour)))
	adm, dec := b.admit("Op3", pWrite)
	if adm == nil {
		t.Fatalf("admit after release should pass: %q", dec.reason)
	}

	// Settling is idempotent: double-release must not free extra budget.
	adms[1].release()
	adms[1].release()
	adms[1].observe(fullHeaders(1, 3000000, 35000, 2500, 2400, clock.t.Add(time.Hour)))
	b.mu.Lock()
	if b.inFlightCost != 20000 || b.inFlightReqs != 2 {
		t.Errorf("in-flight after settles = cost %v reqs %v, want 20000/2", b.inFlightCost, b.inFlightReqs)
	}
	b.mu.Unlock()
}

// TestRateBudget_InFlightSemaphoreRequestsAxis: the request-count axis is a
// semaphore too — one request each.
func TestRateBudget_InFlightSemaphoreRequestsAxis(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	b := testBudget(clock)
	seedWindows(b,
		window{limit: 3000000, remaining: 3000000, resetAt: clock.t.Add(time.Hour), seen: true},
		window{limit: 2500, remaining: 2, resetAt: clock.t.Add(time.Hour), seen: true},
	)

	a1, _ := b.admit("Op", pWrite)
	a2, _ := b.admit("Op", pWrite)
	if a1 == nil || a2 == nil {
		t.Fatal("two writes should fit in 2 remaining requests")
	}
	if adm, _ := b.admit("Op", pWrite); adm != nil {
		t.Fatal("3rd admit should defer: 2 in-flight requests exhaust remaining 2")
	}
	a1.release()
	if adm, _ := b.admit("Op", pWrite); adm == nil {
		t.Fatal("admit should pass after a release")
	}
}

// TestRateBudget_ResetRollover: past an axis's resetAt the window is
// optimistically treated as refilled to its full limit until the next
// observe — the clock is believed over a stale exhausted remaining.
func TestRateBudget_ResetRollover(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	b := testBudget(clock)
	resetAt := clock.t.Add(time.Hour)
	seedWindows(b,
		window{limit: 3000000, remaining: 0, resetAt: resetAt, seen: true},
		window{limit: 2500, remaining: 0, resetAt: resetAt, seen: true},
	)

	adm, dec := b.admit("IssueDetailsBatch", pDetail)
	if adm != nil {
		t.Fatal("exhausted window must defer before reset")
	}
	if dec.retryAfter != time.Hour {
		t.Errorf("retryAfter = %v, want %v", dec.retryAfter, time.Hour)
	}

	clock.advance(time.Hour + time.Second)
	adm, dec = b.admit("IssueDetailsBatch", pDetail)
	if adm == nil {
		t.Fatalf("past resetAt the axis should read as full: %q", dec.reason)
	}
	adm.release()
}

// TestRateBudget_RateLimited: a RATELIMITED response snaps the exhausted
// axis's remaining to 0 and honors the header reset; everything defers
// until the window rolls over.
func TestRateBudget_RateLimited(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	b := testBudget(clock)
	reset := clock.t.Add(30 * time.Minute)
	seedWindows(b,
		window{limit: 3000000, remaining: 500000, resetAt: clock.t.Add(time.Hour), seen: true},
		window{limit: 2500, remaining: 2000, resetAt: clock.t.Add(time.Hour), seen: true},
	)

	adm, _ := b.admit("IssueDetailsBatch", pSkeleton)
	if adm == nil {
		t.Fatal("setup admit should pass")
	}
	// Server says: complexity effectively gone (12 <= 1% of limit),
	// requests fine, window resets in 30min.
	adm.rateLimited(fullHeaders(0, 3000000, 12, 2500, 1990, reset))

	b.mu.Lock()
	if b.complexity.remaining != 0 {
		t.Errorf("complexity remaining = %v, want 0 (snapped)", b.complexity.remaining)
	}
	if b.requests.remaining != 1990 {
		t.Errorf("requests remaining = %v, want 1990 (healthy axis untouched)", b.requests.remaining)
	}
	if !b.complexity.resetAt.Equal(reset.Truncate(time.Millisecond)) {
		t.Errorf("complexity resetAt = %v, want %v (the error's reset)", b.complexity.resetAt, reset)
	}
	b.mu.Unlock()

	// All read tiers — and even writes (cost > remaining 0) — defer until reset.
	for _, p := range []priority{pDetail, pList, pSkeleton, pInteractive, pWrite} {
		if adm, dec := b.admit("AnyOp", p); adm != nil {
			t.Errorf("%s admit should defer after RATELIMITED", p)
		} else if dec.retryAfter != 30*time.Minute {
			t.Errorf("%s retryAfter = %v, want 30m", p, dec.retryAfter)
		}
	}

	// Past the reset, optimistic refill recovers without a response.
	clock.advance(31 * time.Minute)
	adm, dec := b.admit("AnyOp", pList)
	if adm == nil {
		t.Fatalf("admit after reset should pass: %q", dec.reason)
	}
	adm.release()
}

// TestRateBudget_RateLimitedNoHeaders: a RATELIMITED response with no
// usable headers snaps both axes and installs the bounded fallback reset,
// so the budget stays live instead of wedging open or shut.
func TestRateBudget_RateLimitedNoHeaders(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	b := testBudget(clock)

	adm, _ := b.admit("Teams", pSkeleton)
	if adm == nil {
		t.Fatal("fresh budget must admit")
	}
	adm.rateLimited(http.Header{})

	adm2, dec := b.admit("Teams", pSkeleton)
	if adm2 != nil {
		t.Fatal("admit should defer after headerless RATELIMITED")
	}
	if dec.retryAfter != rateLimitedFallbackBackoff {
		t.Errorf("retryAfter = %v, want fallback %v", dec.retryAfter, rateLimitedFallbackBackoff)
	}

	// After the fallback window the budget recovers (unseen-limit axes
	// stop gating rather than refilling to a limit of 0).
	clock.advance(rateLimitedFallbackBackoff + time.Second)
	adm3, dec := b.admit("Teams", pSkeleton)
	if adm3 == nil {
		t.Fatalf("admit should recover after fallback backoff: %q", dec.reason)
	}
	adm3.release()
}

// TestRateBudget_ResetAt: RateLimitResetAt's source — the later of the two
// axes' server-reported resets, zero before any observation.
func TestRateBudget_ResetAt(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	b := testBudget(clock)
	if !b.resetAt().IsZero() {
		t.Error("resetAt should be zero before any observation")
	}
	later := clock.t.Add(45 * time.Minute)
	seedWindows(b,
		window{limit: 1, remaining: 1, resetAt: clock.t.Add(10 * time.Minute), seen: true},
		window{limit: 1, remaining: 1, resetAt: later, seen: true},
	)
	if got := b.resetAt(); !got.Equal(later) {
		t.Errorf("resetAt = %v, want the later axis reset %v", got, later)
	}
}

// TestTierFor: base tiers classify intent; mutations are always writes; the
// interactive context promotes reads (and only reads) above their base tier.
func TestTierFor(t *testing.T) {
	t.Parallel()

	bg := context.Background()
	ia := WithInteractive(context.Background())

	tests := []struct {
		name       string
		ctx        context.Context
		op         string
		isMutation bool
		want       priority
	}{
		{"mutation is write", bg, "UpdateIssue", true, pWrite},
		{"metadata is skeleton", bg, "TeamMetadata", false, pSkeleton},
		{"workspace is skeleton", bg, "Workspace", false, pSkeleton},
		{"issue list is list", bg, "TeamIssuesByUpdatedAt", false, pList},
		{"details batch is detail", bg, "IssueDetailsBatch", false, pDetail},
		{"unknown op defaults to list", bg, "SomeNewOp", false, pList},
		{"interactive promotes detail", ia, "IssueDetailsBatch", false, pInteractive},
		{"interactive promotes list", ia, "TeamIssuesByUpdatedAt", false, pInteractive},
		{"interactive does not demote write", ia, "UpdateIssue", true, pWrite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tierFor(tt.ctx, tt.op, tt.isMutation); got != tt.want {
				t.Errorf("tierFor(%q, mutation=%v) = %s, want %s", tt.op, tt.isMutation, got, tt.want)
			}
		})
	}
}
