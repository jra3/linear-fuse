package api

// The rate-budget core.
//
// Linear meters every API key on TWO hourly axes — requests and complexity
// points — and reports both on every response (X-RateLimit-{Requests,
// Complexity}-{Limit,Remaining,Reset}, plus X-Complexity: this query's
// cost). The old client shaped only request count, at a hardcoded rate that
// matched neither the docs nor the live limit, and parsed the reset from a
// header Linear doesn't send (as seconds; Linear sends per-axis epoch
// MILLISECONDS) — so the axis that actually gets exhausted (complexity) was
// never governed and adaptive backoff was dead. rateBudget owns everything
// the old code scattered: both windowed budgets, a per-operation cost
// predictor, the priority-reserve ladder, the in-flight reservation
// semaphore, and header reconciliation. Client.query makes exactly two
// calls: admit before sending, observe (or rateLimited/release) after.
//
// Control model: hybrid, server-anchored. admit gates on a local predictive
// budget; observe snaps remaining/limit/reset for both axes to the response
// headers, so estimate drift is erased every round-trip and a restart
// self-heals on the first response. Limits are NEVER hardcoded — the live
// request limit (2500/hr) matches neither Linear's docs (5000) nor the old
// constant (1500).
//
// The clock is injected (now func() time.Time) and every method is
// mutex-guarded; unit tests drive window resets and reservations with a
// fake clock and synthetic headers, no HTTP.

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// priority ranks a request's claim on the remaining budget. Higher
// priorities spend deeper into the window: each priority has a reserve
// floor (a fraction of the axis limit, reserveFrac) that admit refuses to
// dip into. Details stop first, then lists, then skeleton reads; writes
// flow until the tank is essentially empty. This ladder is what makes
// cold-start gentleness emergent — from a constrained budget the skeleton
// syncs first and detail fetches defer to pending_detail_sync — without a
// special cold-start mode.
type priority int

const (
	pDetail      priority = iota // issue details: comments/docs/attachments/updates
	pList                        // issue lists, reconcile ID sweeps
	pSkeleton                    // teams/states/labels/users/projects — the FS shape
	pInteractive                 // a read promoted because a live FS caller waits on it
	pWrite                       // mutations: user-facing, never silently dropped
)

func (p priority) String() string {
	switch p {
	case pDetail:
		return "detail"
	case pList:
		return "list"
	case pSkeleton:
		return "skeleton"
	case pInteractive:
		return "interactive"
	case pWrite:
		return "write"
	}
	return fmt.Sprintf("priority(%d)", int(p))
}

// reserveFrac is the fraction of an axis's limit held back from each
// priority. admit allows a request only if, on both axes,
// predictedCost <= remaining − inFlight − reserveFrac[p]·limit.
var reserveFrac = map[priority]float64{
	pWrite:       0,    // flow unless the window is essentially empty
	pInteractive: 0.02, // a user is waiting; spend nearly to the floor
	pSkeleton:    0.05,
	pList:        0.15,
	pDetail:      0.40, // the biggest spender runs only with ample headroom
}

// defaultPredictedCost prices an operation that has never been measured:
// the single-query complexity maximum, so unknowns are treated as expensive
// until the first response teaches us their real cost.
const defaultPredictedCost = 10000

// seedHourlyRequestLimit seeds the micro-burst rate.Limiter before the
// first response has reported the real request limit. It is a smoothing
// seed only — admit never gates on it; both budgets read their limits
// exclusively from response headers.
const seedHourlyRequestLimit = 2500

// rateLimitedFallbackBackoff bounds the lockout after a RATELIMITED
// response that carried no usable reset header — liveness insurance, not a
// guess we prefer (a header reset always wins).
const rateLimitedFallbackBackoff = 15 * time.Minute

// opBaseTier classifies each operation's INTENT — what breaks if it is
// deferred — which is stable in a way per-op cost is not. Ops absent from
// the map default to pList (mid-ladder); mutations are pWrite regardless
// (see tierFor).
var opBaseTier = map[string]priority{
	// Skeleton: identity and metadata the filesystem's shape depends on.
	"Viewer":                   pSkeleton,
	"Teams":                    pSkeleton,
	"TeamMetadata":             pSkeleton,
	"TeamLabelsPage":           pSkeleton,
	"TeamCyclesPage":           pSkeleton,
	"TeamMembersPage":          pSkeleton,
	"TeamProjects":             pSkeleton,
	"Workspace":                pSkeleton,
	"WorkspaceLabelsPage":      pSkeleton,
	"ProjectLabelsPage":        pSkeleton,
	"WorkspaceUsersPage":       pSkeleton,
	"WorkspaceInitiativesPage": pSkeleton,
	"InitiativesProbe":         pSkeleton,
	"InitiativeProjectsPage":   pSkeleton,
	"Initiative":               pSkeleton,
	"Project":                  pSkeleton,

	// Lists: issue pages and the reconcile ID sweeps.
	"TeamIssuesByUpdatedAt":  pList,
	"TeamIssueIDs":           pList,
	"WorkspaceProjectIDs":    pList,
	"WorkspaceInitiativeIDs": pList,
	"Issue":                  pList,

	// Details: the per-issue/project/initiative deep fetches — the largest
	// complexity spenders, and the first to defer.
	"IssueDetailsBatch":   pDetail,
	"IssueDetails":        pDetail,
	"IssueAttachments":    pDetail,
	"IssueHistory":        pDetail,
	"ProjectDocuments":    pDetail,
	"InitiativeDocuments": pDetail,
	"ProjectUpdates":      pDetail,
	"InitiativeUpdates":   pDetail,
}

// interactiveCtxKey marks a context as carrying a live caller (a user
// blocked on a FUSE read), promoting the request above its base tier.
type interactiveCtxKey struct{}

// WithInteractive marks every API call made under ctx as user-facing: its
// base tier is promoted to pInteractive, so it spends nearly as deep as a
// mutation. Background sync must NOT use this.
//
// ADOPTION NOTE (PR1): this is the mechanism only. On-demand FS read call
// The FS layer threads it at its synchronous, user-blocking API call sites
// (GetTeamDocuments; the attachment-create authoritative re-check) — most
// read paths never need it because they are SQLite-first with *background*
// refresh, and background work must stay at base tier.
//
// RULE: apply WithInteractive at the moment of the synchronous call, never
// store a promoted ctx or hand it to a goroutine that outlives the FUSE
// request — a leaked promotion would let background work drain the
// interactive reserve the promotion exists to protect.
func WithInteractive(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveCtxKey{}, true)
}

func isInteractive(ctx context.Context) bool {
	v, _ := ctx.Value(interactiveCtxKey{}).(bool)
	return v
}

// tierFor resolves the effective priority for one request: mutations are
// always pWrite; reads take their base tier from opBaseTier (default
// pList), promoted to pInteractive when the context says a caller waits.
func tierFor(ctx context.Context, opName string, isMutation bool) priority {
	if isMutation {
		return pWrite
	}
	tier, ok := opBaseTier[opName]
	if !ok {
		tier = pList
	}
	if isInteractive(ctx) && tier < pInteractive {
		tier = pInteractive
	}
	return tier
}

// decision is admit's verdict. When allow is false, retryAfter is how long
// until the binding axis resets (0 = unknown; retry next cycle) and reason
// says which axis refused and why (for the deferral error message).
type decision struct {
	allow      bool
	retryAfter time.Duration
	reason     string
}

// window is one budget axis: {limit, remaining, resetAt}, all read from
// response headers, never hardcoded. seen is false until the first header
// reconcile — an unseen axis does not gate (the first response seeds it).
type window struct {
	name      string // "complexity" / "requests", for messages
	limit     float64
	remaining float64
	resetAt   time.Time
	seen      bool
}

// effectiveRemaining applies optimistic refill: past resetAt the axis is
// treated as full (the clock is believed over a stale exhausted remaining,
// so a new window never waits an extra hour) until the next observe reports
// fresh numbers. An unseen axis is unlimited.
func (w *window) effectiveRemaining(now time.Time) float64 {
	if !w.seen {
		return math.Inf(1)
	}
	if !w.resetAt.IsZero() && !now.Before(w.resetAt) {
		if w.limit > 0 {
			return w.limit
		}
		return math.Inf(1)
	}
	return w.remaining
}

// rateBudget owns the two windowed budgets, the per-op cost predictor, the
// reserve ladder, and the in-flight reservation semaphore. All fields are
// guarded by mu; the clock is injected for tests.
type rateBudget struct {
	mu           sync.Mutex
	now          func() time.Time
	complexity   window
	requests     window
	inFlightCost float64            // complexity points reserved by unsettled admissions
	inFlightReqs float64            // request count reserved by unsettled admissions
	cost         map[string]float64 // opName -> last-seen X-Complexity

	// metrics are the budget-owned OTEL instruments (metrics.go): the
	// decisions counter fires where admit resolves, the complexity
	// histogram where reconcile parses X-Complexity. No-op when no global
	// provider is registered.
	metrics budgetMetrics
}

func newRateBudget(now func() time.Time) *rateBudget {
	b := &rateBudget{
		now:        now,
		complexity: window{name: "complexity"},
		requests:   window{name: "requests"},
		cost:       make(map[string]float64),
		metrics:    newBudgetMetrics(),
	}
	registerBudgetGauges(b)
	return b
}

// admission is one allowed request's reservation. Exactly one of observe /
// rateLimited / release MUST eventually be called; all three are idempotent
// (the first settles, the rest no-op), so a deferred release is a safe
// catch-all under an explicit observe on the success path.
type admission struct {
	b       *rateBudget
	op      string
	tier    priority
	cost    float64
	settled bool // guarded by b.mu

	// The response's parsed X-Complexity, captured when observe/rateLimited
	// reconciles the headers (guarded by b.mu; see actualComplexity).
	actual     float64
	actualSeen bool
}

// admit gates one request at the given priority. On allow it reserves the
// predicted cost against both axes (the in-flight semaphore: concurrent
// admits see remaining − inFlight − reserve and start deferring before any
// response lands) and returns the reservation. On deny, admission is nil
// and the decision says how long to defer.
func (b *rateBudget) admit(op string, p priority) (*admission, decision) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	cost := b.predictLocked(op)
	if d, ok := admitAxis(&b.complexity, cost, b.inFlightCost, p, now); !ok {
		b.metrics.recordDecision(p, "defer")
		return nil, d
	}
	if d, ok := admitAxis(&b.requests, 1, b.inFlightReqs, p, now); !ok {
		b.metrics.recordDecision(p, "defer")
		return nil, d
	}
	b.inFlightCost += cost
	b.inFlightReqs++
	b.metrics.recordDecision(p, "admit")
	return &admission{b: b, op: op, tier: p, cost: cost}, decision{allow: true}
}

// admitAxis is the per-axis gate: cost <= remaining − inFlight − reserve.
func admitAxis(w *window, cost, inFlight float64, p priority, now time.Time) (decision, bool) {
	rem := w.effectiveRemaining(now)
	if math.IsInf(rem, 1) {
		return decision{allow: true}, true
	}
	reserve := reserveFrac[p] * w.limit
	if cost <= rem-inFlight-reserve {
		return decision{allow: true}, true
	}
	var retry time.Duration
	if w.resetAt.After(now) {
		retry = w.resetAt.Sub(now)
	}
	return decision{
		retryAfter: retry,
		reason: fmt.Sprintf("%s budget: need %.0f, remaining %.0f, in-flight %.0f, %s-tier reserve %.0f",
			w.name, cost, rem, inFlight, p, reserve),
	}, false
}

// predictLocked prices op: last-seen X-Complexity, or the conservative
// default for an op never measured.
func (b *rateBudget) predictLocked(op string) float64 {
	if c, ok := b.cost[op]; ok {
		return c
	}
	return defaultPredictedCost
}

// observe settles the admission from a normal response: releases the
// in-flight reservation, records the actual X-Complexity for the op, and
// snaps both axes' remaining/limit/reset to the headers (server truth).
func (a *admission) observe(h http.Header) {
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	if a.settled {
		return
	}
	a.settled = true
	a.b.releaseLocked(a.cost)
	a.actual, a.actualSeen = a.b.reconcileLocked(a.op, h)
}

// actualComplexity reports the response's X-Complexity as parsed by the
// budget's reconcile — the ONE place the header is parsed; the request debug
// log (requestlog.go) reads the value from here rather than parsing twice.
// ok is false until the admission has settled via observe/rateLimited, and
// stays false when the response carried no X-Complexity header (or the
// admission was released without a response).
func (a *admission) actualComplexity() (v float64, ok bool) {
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	return a.actual, a.actualSeen
}

// rateLimited settles the admission from a 400/RATELIMITED (or 429)
// response: reconciles whatever the headers still report, then defensively
// snaps the exhausted axis's remaining to 0 so admit defers everything
// until its reset. If the headers identify no exhausted axis, both are
// snapped; if no reset is known, a bounded fallback keeps the budget live.
func (a *admission) rateLimited(h http.Header) {
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	if a.settled {
		return
	}
	a.settled = true
	a.b.releaseLocked(a.cost)
	a.actual, a.actualSeen = a.b.reconcileLocked(a.op, h)
	a.b.snapExhaustedLocked()
	a.b.metrics.recordDecision(a.tier, "ratelimited")
}

// release settles the admission for a request that never produced a
// response (transport error, marshal failure, ctx cancellation): the
// reservation is returned, the windows are left alone.
func (a *admission) release() {
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	if a.settled {
		return
	}
	a.settled = true
	a.b.releaseLocked(a.cost)
}

func (b *rateBudget) releaseLocked(cost float64) {
	b.inFlightCost -= cost
	if b.inFlightCost < 0 {
		b.inFlightCost = 0
	}
	b.inFlightReqs--
	if b.inFlightReqs < 0 {
		b.inFlightReqs = 0
	}
}

// reconcileLocked snaps both axes to the response headers and records the
// op's actual cost. Missing headers leave the corresponding fields alone.
// The reset headers are epoch MILLISECONDS, per-axis (the old code read a
// header Linear doesn't send, as seconds — dead backoff). The parsed
// X-Complexity is returned (ok=false when the header is absent) so callers
// can thread it onward without parsing the header a second time.
func (b *rateBudget) reconcileLocked(op string, h http.Header) (complexity float64, ok bool) {
	if v, seen := headerFloat(h, "X-Complexity"); seen {
		complexity, ok = v, true
		b.cost[op] = v
		// The one place X-Complexity is parsed also records it.
		b.metrics.complexity.Record(context.Background(), v,
			metric.WithAttributes(attribute.String("op", op)))
	}
	reconcileAxis(&b.complexity, h, "X-RateLimit-Complexity")
	reconcileAxis(&b.requests, h, "X-RateLimit-Requests")

	// Preserve the old low-budget warning, now on real server numbers.
	for _, w := range []*window{&b.complexity, &b.requests} {
		if w.seen && w.limit > 0 && w.remaining/w.limit < 0.20 {
			log.Printf("[ratelimit] Linear API: %.0f/%.0f %s remaining this hour (after %s)",
				w.remaining, w.limit, w.name, op)
		}
	}
	return complexity, ok
}

func reconcileAxis(w *window, h http.Header, prefix string) {
	if v, ok := headerFloat(h, prefix+"-Limit"); ok {
		w.limit = v
		w.seen = true
	}
	if v, ok := headerFloat(h, prefix+"-Remaining"); ok {
		w.remaining = v
		w.seen = true
	}
	if ms, ok := headerInt(h, prefix+"-Reset"); ok {
		w.resetAt = time.UnixMilli(ms)
	}
}

// snapExhaustedLocked implements the defensive RATELIMITED snap: any axis
// the headers show as (near-)exhausted goes to remaining 0; if none
// qualifies (headers absent or contradicting the 400), both do — the 400 is
// believed over stale numbers. Every snapped axis gets a future resetAt
// (header reset if usable, else the bounded fallback) so optimistic refill
// cannot immediately undo the snap and the budget always self-recovers.
func (b *rateBudget) snapExhaustedLocked() {
	now := b.now()
	axes := []*window{&b.complexity, &b.requests}
	var snapped []*window
	for _, w := range axes {
		if w.seen && w.limit > 0 && w.remaining <= 0.01*w.limit {
			snapped = append(snapped, w)
		}
	}
	if len(snapped) == 0 {
		snapped = axes
	}
	for _, w := range snapped {
		w.seen = true
		w.remaining = 0
		if !w.resetAt.After(now) {
			w.resetAt = now.Add(rateLimitedFallbackBackoff)
		}
	}
}

// resetAt reports when the budget expects to be whole again: the later of
// the two axes' server-reported resets (zero if none seen yet). Client.
// RateLimitResetAt delegates here; the sync worker's backoff consults it.
func (b *rateBudget) resetAt() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.complexity.resetAt
	if b.requests.resetAt.After(r) {
		r = b.requests.resetAt
	}
	return r
}

// low reports whether a conservatively-priced (never-measured) request at
// priority p would currently be refused — the successor to the old
// token-count LowBudget, reusing the exact admit arithmetic.
func (b *rateBudget) low(p priority) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if _, ok := admitAxis(&b.complexity, defaultPredictedCost, b.inFlightCost, p, now); !ok {
		return true
	}
	if _, ok := admitAxis(&b.requests, 1, b.inFlightReqs, p, now); !ok {
		return true
	}
	return false
}

// axisSnapshot is one axis's state as read by the observable budget gauges
// (and Client.BudgetSnapshot): the raw window numbers plus the in-flight
// reservation, with seconds-to-reset computed on the injected clock. seen
// mirrors window.seen — false until the server has reported this axis.
type axisSnapshot struct {
	seen         bool
	limit        float64
	remaining    float64
	inFlight     float64
	resetSeconds float64
}

// snapshot reads both axes under the existing mutex — the read seam for the
// telemetry gauges; no new locks.
func (b *rateBudget) snapshot() (complexity, requests axisSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	return snapAxis(&b.complexity, b.inFlightCost, now),
		snapAxis(&b.requests, b.inFlightReqs, now)
}

func snapAxis(w *window, inFlight float64, now time.Time) axisSnapshot {
	s := axisSnapshot{
		seen:      w.seen,
		limit:     w.limit,
		remaining: w.remaining,
		inFlight:  inFlight,
	}
	if w.resetAt.After(now) {
		s.resetSeconds = w.resetAt.Sub(now).Seconds()
	}
	return s
}

// requestsLimit returns the server-reported hourly request limit, 0 until
// the first response has been observed. Client uses it to size the
// micro-burst limiter and the stats denominator.
func (b *rateBudget) requestsLimit() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.requests.seen {
		return 0
	}
	return b.requests.limit
}

func headerFloat(h http.Header, key string) (float64, bool) {
	s := h.Get(key)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func headerInt(h http.Header, key string) (int64, bool) {
	s := h.Get(key)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
