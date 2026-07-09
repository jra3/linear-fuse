# detailOutcome + swrRefresh — round-17 design (grilling-locked)

Candidates 1 and 2 from the 2026-07-08 architecture review (report:
`~/tmp/architecture-review-20260708-130636.html`). Both designed together
because they share the detail-sync/SWR territory; every decision below was
grilling-locked with John on 2026-07-08.

## The hazards being closed

1. **Touch/dequeue over the requested set** (worker.go:1040–1051): the sync
   loop iterates the *returned* `detailsMap`, the touch/dequeue loop iterates
   the *requested* `issues`. An issue silently dropped by the client is
   stamped `synced_at = now` (masking staleness from the SWR path) and
   deleted from `pending_detail_sync` (losing its retry). The comment says
   "successfully synced"; the code removes all.
2. **Silent drop in `GetIssueDetailsBatch`** (client.go:1008, 1014): missing
   alias or decode failure → `continue`. Worse: `toDetails()` never returns
   nil, so a `null` alias yields an **empty** `IssueDetails` whose five empty
   collections are each "complete" (0 < page size) → **full prune of a live
   issue's details**. The `details == nil` guard at worker.go:939 is dead code.
3. **Lost retry on whole-batch failure** (worker.go:914–915): non-rate-limit
   fetch failure logs and returns without deferring — team-sync-sourced
   issues lose their worker-side retry entirely.
4. **Per-issue masked staleness**: an issue whose comment *upsert* failed
   (e.g. transient SQLITE_BUSY) still gets touched — prune is suppressed
   (clean guard) but the stale row is stamped fresh, hiding it from SWR.
5. **SWR fragmentation** (repo/sqlite.go): two staleness policies in three
   implementations (`staleSince` TTL — the only one `SetCatchUpMode` reaches;
   the event-driven `issue.updatedAt > synced_at` hand-copied at :838–840 and
   :1341–1345); the history fetch closure pasted verbatim at :1316 and :1346;
   `isEntityNotFound → deleteOrphan*` restated at 7 sites; bare-string dedup
   keys at 6 sites.
6. **Duplicated persist**: `refreshIssueDetails` (repo) is a hand-rolled
   near-copy of the worker's five syncCollection specs, diverging: no prunes,
   no clean guard, **no embedded-file extraction** (a comment fetched via SWR
   never gets its `*.png` row until the next worker detail sync), and silent
   `continue` on convert errors.

## Locked decisions

### Candidate 1 — syncDetails (worker) + all-or-nothing client

- **Client contract: all-or-nothing.** `GetIssueDetailsBatch` returns an
  error if any alias is missing, `null`, or fails to decode. A nil-error
  return guarantees an entry for every requested ID — the same contract
  language as `drainFrom`, and prune callers rely on it. (Per-issue outcome
  maps and null-means-gone were considered and rejected: decode anomalies
  indicate systemic schema drift, not per-issue state.)
- **Defer on any failure.** Non-rate-limit batch failure defers the issues to
  `pending_detail_sync` like the other gates (fixes hazard 3). No attempt
  cap (YAGNI — bounded cost, loudly logged, SWR backstops).
- **Clean gate.** `syncCollection` returns `clean bool` (existing metadata
  callers may ignore it — zero churn). An issue is clean iff all five of its
  collections are clean; only clean issues get touched + dequeued; unclean
  issues are (re-)enqueued to pending.
- **Shape: one Worker method.** `syncDetails(ctx, issues []issueRef)
  detailOutcome` merges `syncOrDeferDetailBatch` + `syncIssueDetailsBatch`,
  owning all gates (budget, rate-limit before/mid, fetch failure).
  `detailOutcome = {synced, deferred []issueRef, gated bool}`.
  `drainPendingDetailSync` becomes a thin loop that breaks on `gated`.
  The anonymous `struct{ID, Identifier}` becomes a named `issueRef`.
  No separate detailSyncer struct — Worker already has the narrow APIClient
  seam + fixture store for tests.

### Candidate 2 — internal/reconcile + swrRefresh coordinator

- **Scope: coordinator + shared persist** (John chose the bigger scope).
- **New package `internal/reconcile`** (repo and sync import it; neither
  imports the other — verified acyclic):
  - `syncCollection` moves here (returns `clean bool`).
  - `PersistIssueDetails(ctx, deps, issueID, details, cutoff) (clean bool)` —
    the five collection specs + `pruneWhenComplete` written once, called by
    both the worker batch and the repo SWR refresh.
  - Embedded-file extraction module: pure parse (already split) + HEAD I/O
    tail, deps = {queries, `AuthHeader()`, injectable httpClient — the
    embeddedFileCache precedent}.
  - Rejected placements: repo-imports-sync (read path depending on the
    worker package misstates the relationship), internal/db (reconcile
    POLICY doesn't belong in the schema layer).
- **Repo SWR behavior changes riding along (all improvements, recorded):**
  prunes-when-complete, clean guard, embedded-file extraction on the SWR
  path; convert errors now log-and-mark-unclean instead of silent continue.
- **swrRefresh coordinator** (repo): one spec-shaped entry point with typed
  refresh keys (kind + id factory), two staleness flavors — TTL via
  threshold, event-driven via a `changedAt` closure (nil ⇒ TTL, the
  nil-prune idiom) — dedup/trigger, and orphan-on-not-found classification
  (`spec.orphan` closure). The six call sites become one-liners; the pasted
  history closure exists once.
- **Catch-up mode stays TTL-only — explicit policy, not accident.** Grilled
  with full tradeoffs: extending suppression to event-driven surfaces would
  save duplicate fetches the rateBudget ladder already governs, at the cost
  of silently-empty `comments/` listings during big syncs — the worst
  failure mode for an agent-facing filesystem. Flipping later is a one-line
  policy change.

## Build sequence (4 stacked PRs, background agents build, main loop reviews/merges)

1. **Client hardening** (independent): all-or-nothing `GetIssueDetailsBatch`
   + synthetic-response tests (missing / null / undecodable alias → error).
2. **internal/reconcile** (independent of 1): move syncCollection (+clean
   return), add PersistIssueDetails + Extractor; worker switches over;
   behavior-preserving (clean value ignored until step 3).
3. **syncDetails ledger** (needs 2): merge the three methods, clean-gated
   touch/dequeue, defer-on-failure, thin drain.
4. **swrRefresh** (needs 2): coordinator + route the repo's six surfaces;
   `refreshIssueDetails` → `reconcile.PersistIssueDetails`; delete the five
   hand-rolled tails.

CONTEXT.md updates ride each step: new entries (detail outcome, SWR refresh
coordinator, detail persist), amendments (sync-reconcile-tail moves package +
returns clean; the relations paragraph's SWR sentence; catch-up asymmetry as
recorded policy).
