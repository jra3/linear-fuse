# Rate-limit management revamp — the `rateBudget` module

**Status:** PR1 built (2026-07-06, working tree): `rateBudget` module + governance
wired into `Client.query`, plus the decision-2 cold-start probe in the sync worker.
**Scope:** `internal/api/client.go` rate limiting + `internal/sync/worker.go` cold-start pacing.

## Problem

The client throttles the **wrong meter**. Linear bills two axes per hour —
**requests** and **complexity points** — and the binding constraint at cold-start
is complexity. The current client:

1. Shapes only by **request count**, at a hardcoded `rate.NewLimiter(1500/3600, 16)`.
2. **Ignores complexity entirely** — the axis that actually gets blown.
3. Parses the reset time from the wrong header (`X-RateLimit-Reset`) as **seconds**,
   but Linear sends `X-RateLimit-{Requests,Complexity}-Reset` in **epoch milliseconds** —
   so adaptive backoff is dead and falls back to a fixed 15-min guess.
4. Keeps the limiter **in-memory**, so a restart resets it to a full tank and
   **cold-bursts** even though the server remembers the hour's spend.
5. Syncs **all issue details eagerly** (comments/docs/attachments per issue) — the
   single largest complexity cost — up front, before anyone views them.

**Failure mode observed (2026-07-06):** a fresh mount's full initial sync + the
live service together exhausted the 3,000,000-complexity/hr budget, wedging the
account into `RATELIMITED` (HTTP 400) until the window reset.

## Ground truth

From Linear's docs (<https://linear.app/developers/rate-limiting>) **and a live
probe** of this workspace's API key (`query { viewer { id } }`, cost 1 point):

```
x-complexity: 1                              # this query's cost
x-ratelimit-complexity-limit: 3000000
x-ratelimit-complexity-remaining: 30713      # real-time remaining
x-ratelimit-complexity-reset: 1783373283146  # epoch MILLISECONDS
x-ratelimit-requests-limit: 2500             # NOT 5000 (docs) or 1500 (code)
x-ratelimit-requests-remaining: 2499
x-ratelimit-requests-reset: 1783373283146
```

- Every response carries `X-Complexity` (this query's cost) and
  `X-RateLimit-Complexity-Remaining` (authoritative remaining) — so we can both
  **measure** per-op cost and **reconcile** the budget on every round-trip.
- The request limit is **2,500**, matching neither the docs nor the code:
  **read every limit from the header; never hardcode.**
- Complexity formula (for intuition only, not implemented): property 0.1,
  object 1, connection ×pagination arg (default 50), rounded up. Single-query
  max 10,000 (already respected by `paginate`'s page-size budgeting).
- Exceeding → HTTP 400, `extensions.code = "RATELIMITED"`.

## Design decisions (grilled)

1. **Control model — hybrid, server-anchored.** Keep a local predictive budget
   for pre-send gating; **reconcile to the server's `X-RateLimit-*-Remaining` +
   actual `X-Complexity` on every response.** The server value is ground truth —
   snap to it each round-trip, so estimate drift is erased and a restart
   self-heals on the first response.

2. **Cold-start probe.** On startup, before the sync worker's first cycle, fire
   one cheap `query { viewer { id } }` (~2 pts, dual-purpose — the viewer is
   already needed for `/my`) and seed the budget (both axes' limit + remaining +
   reset) from its headers. The probe *is* the first `observe`; it subsumes the
   "seed on restart" fix. If the probe itself returns `RATELIMITED`, honor its
   reset and delay the whole sync start.

   **BUILT (PR1):** `Worker.probeBudget` (`internal/sync/worker.go`) — `run()`
   calls it synchronously before the initial `syncAllTeams`, via the existing
   `Client.GetViewer` (added to the worker's `APIClient` interface), so the
   probe response is observed (budget seeded) strictly before any expensive
   query is admitted. On a `RATELIMITED` probe it calls `setRateLimited()`
   (backoff = the budget's server-reported reset, which the probe's own
   response headers just seeded, +5s; 15-min fallback) and sleeps until the
   expiry — interruptible by ctx/Stop, in which case `run` exits without
   firing a post-stop cycle. Any other probe failure logs and proceeds (the
   same failure repeats in `syncAllTeams` and is handled there). Shape delta
   from the sketch: the probe lives in the worker (not a separate startup
   hook) and reuses the full `GetViewer`, so the FS's background viewer
   refresh in `EnableSQLiteCache` is untouched (one extra ~2-pt query).

3. **Cold-start gentleness — emergent, not a mode.** The worker fetches in
   **priority tiers** — (1) skeleton (viewer, team metadata, states),
   (2) issue lists, (3) issue **details** (lowest) — and each request is
   budget-gated (below). Cold-start isn't special-cased: from empty, a
   constrained budget delivers the skeleton first and defers details to the
   existing `pending_detail_sync` queue, trickling them in later cycles / the
   next window. This also protects a **warm restart on a busy account**, not
   just a literally-empty DB.

4. **The gate — priority-reserve ladder, two-axis.** Per request, `admit` allows
   it only if on **both** axes `predictedCost ≤ remaining − reserve(priority)`.
   Higher priority → lower reserve (spends deeper); details → large reserve (stop
   first). This ladder is what makes tier-deferral emergent:

   ```
   mutations / interactive reads   reserve ≈ 0        (flow unless ~empty)
   skeleton reads                  reserve small
   list reads                      reserve medium
   detail reads                    reserve LARGE      (only with ample headroom)
   ```

   Blocked **read → defer** (return the deferral error; worker queue retries).
   Blocked **mutation → wait** until reset (user-facing, never silently dropped),
   unless the wait is absurd. Keep a **right-sized `rate.Limiter`** (limit read
   from the header) as a thin micro-burst smoother — the budget prevents hourly
   overshoot, the limiter prevents instantaneous spikes.

5. **Cost prediction — learned per operation.** Predict a query's cost as the
   last-seen `X-Complexity` for its `opName` (rolling). First-ever call for an
   unmeasured op uses a conservative default (the 10,000 single-query max, so
   unknowns are treated as expensive until measured). Self-calibrating: no
   formula code, auto-tracks page-size/fragment changes (e.g. PR #179).

6. **The seam — a `rateBudget` deep module** (`internal/api/ratebudget.go`)
   owning both windowed budgets, the per-op predictor, the reserve ladder, and
   header reconciliation. Small interface:
   - `admit(opName string, p priority) decision` — allow / defer / wait-until.
   - `observe(opName string, h http.Header)` — record actual `X-Complexity` for
     the op; snap remaining/limit/reset (both axes) to the headers; release the
     in-flight reservation.
   - injected **clock** (`now func() time.Time`) so window resets are testable.

   `Client.query` calls `admit` before / `observe` after. The scattered logic
   (`checkRateLimitHeaders`, the inline write-reserve gate, `LowBudget`,
   `RateLimitResetAt`, and the worker's `setRateLimited`/`isRateLimited`/
   `rateLimitExpiry`) collapses into these two calls or is deleted.

   **Priority = static base-tier ⊕ caller `interactive` flag.** Base tier from a
   small `opName → tier` map inside the module (tiers classify *intent*, stable
   unlike cost). The same op differs by urgency — a background detail-sync vs. a
   user who just `cat`'d an issue's comments — so **on-demand FS reads and
   mutations pass `interactive: true`**, promoting them above their base tier;
   background sync passes false.

7. **Reset mechanics.**
   - Parse `X-RateLimit-Complexity-Reset` / `-Requests-Reset` **per-axis, as
     `time.UnixMilli`** (fixes the dead backoff).
   - **Optimistic refill:** when `now() ≥ resetAt` for an axis, treat it as
     refilled to its full limit until the next response reports fresh remaining —
     so the budget believes the clock, not a stale exhausted remaining, and
     cold-start doesn't wait an extra hour.
   - **Defensive `RATELIMITED`:** on a 400/`RATELIMITED` response, snap that
     axis's remaining to 0 and honor the error's reset; `admit` then defers all
     non-interactive work until reset.

8. **Concurrency — reserve-on-admit / release-on-observe semaphore.** `admit`
   optimistically `inFlight += predictedCost` when it allows, so concurrent
   admits see `remaining − inFlight − reserve` and start deferring — no
   thundering herd. `observe` (and any dropped/errored send) releases the
   reservation, then snaps remaining to the server value. Mutex-guarded. Net: a
   semaphore over complexity points.

9. **Rollout.**
   - **Pre-build live header check — DONE** (probe above; header names/units/limit
     confirmed).
   - **PR1 — the `rateBudget` module + correct governance:** admit/observe,
     predictor, reserve ladder, in-flight semaphore, per-axis ms reset, probe
     seed; wired into `Client.query`; priority threaded (interactive flag on
     on-demand reads); delete the scattered old logic. Delivers the correctness
     fix *and* emergent detail-deferral.
   - **PR2 — cold-start sequencing polish**, only if PR1's emergent gentleness
     isn't enough in practice (explicit skeleton-first ordering).
   - **Testing is unit-heavy** — the point of the seam: `rateBudget` with a fake
     clock + synthetic headers (reserve ladder defers the right tiers, observe
     reconciles to server truth, in-flight semaphore under concurrent admits,
     reset rollover, `RATELIMITED` snap-to-zero). The live probe is the only
     live dependency.

## Interface sketch (illustrative, not final)

```go
type priority int // pWrite > pInteractive > pSkeleton > pList > pDetail

type decision struct {
    allow bool
    retryAfter time.Duration // when !allow: defer this long (0 = next cycle)
}

type rateBudget struct {
    mu       sync.Mutex
    now      func() time.Time
    axes     [2]window            // complexity, requests: {limit, remaining, resetAt}
    inFlight float64              // reserved complexity for in-flight requests
    cost     map[string]float64   // opName -> last-seen X-Complexity
}

func (b *rateBudget) admit(op string, p priority) decision
func (b *rateBudget) observe(op string, h http.Header) // or the RATELIMITED error
```

## Follow-ups / notes

- On build, add a **CONTEXT.md** concept entry ("Rate budget (`rateBudget`)").
- Request limit is **per-key and not what any doc says** — the "read from header"
  rule is load-bearing; the `linearHourlyLimit` constant should go.
- The existing `deferDetailIssues` + `pending_detail_sync` queue is the deferral
  substrate PR1 leans on — details tripping their reserve route there.
- Live validation of a full mount is **budget-hungry** (it exhausted the account
  today); prefer the cheap direct-probe for any live check, or validate when the
  account is idle.
