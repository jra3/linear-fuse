# Sync Architecture Optimization Plan

## Context

Linear API budget: **1,500 requests/hour** (0.417/sec sustained).
Sync worker interval: **2 minutes** (30 syncs/hour).
Cache layer: SQLite — all FS reads should go through it; API calls only to refresh.

Root cause of rate limit exhaustion: three compounding bugs:
1. Rate limiter was set to 7,200/hr (4.8× over budget)
2. `syncIssueDetailsBatch` stored fresh data but never touched `synced_at`, causing the FS
   layer to immediately re-trigger on-demand fetches for data we just wrote
3. FS metadata reads (states/labels/cycles) bypass SQLite and call the API directly

---

## Phase 1 — Critical Fixes ✅ DONE

### C-1: Fix rate limiter `internal/api/client.go:44`

**Before:** `rate.NewLimiter(rate.Limit(2), 50)` = 7,200 req/hr (4.8× over budget)
**After:** `rate.NewLimiter(rate.Limit(1500.0/3600.0), 10)` = 1,500 req/hr sustained, burst 10

### C-2: Touch synced_at after batch detail sync `internal/sync/worker.go`

`syncIssueDetailsBatch` was storing fresh comments/docs/attachments but never bumping
`synced_at`. On next FS access `maybeRefreshIssueDetails` would see stale timestamps and
immediately fire another API call — a positive feedback loop burning through the budget.

**Fix:** After storing all results, call `TouchIssueComments`, `TouchIssueDocuments`,
`TouchIssueAttachments` for every issue in the batch. History is intentionally excluded
(changed issues should have history refreshed on next access).

---

## Phase 2 — High Priority Fixes ✅ DONE

### H-3: Reduce batch size `internal/sync/worker.go:49`

**Before:** `detailsBatchSize = 20` — 80–90% of Linear's 10k GraphQL complexity limit
**After:** `detailsBatchSize = 15` — leaves safe headroom

### H-4: Non-blocking history on cold cache `internal/repo/sqlite.go`

`GetIssueHistory` was making a **synchronous** API call on first access, blocking the FUSE
dispatch goroutine for the full HTTP round-trip.

**Fix:** Replace synchronous first-fetch with `triggerBackgroundRefresh`. Return `nil, nil`
immediately; history populates on next read once the background fetch completes.

### M-1: Align staleness threshold `internal/repo/sqlite.go:19`

**Before:** `defaultStalenessThreshold = 30 * time.Minute`
**After:** `defaultStalenessThreshold = 5 * time.Minute` (2.5× the 2-min sync interval)

The old threshold was so generous that on-demand refreshes almost never fired for legitimate
misses, while still firing constantly due to the C-2 bug.

---

## Phase 3 — High Priority ✅ DONE

### H-1: Persist current user in SQLite ✅

**Implemented:** Added `viewer_cache` singleton table to SQLite. `EnableSQLiteCache()` now
loads the viewer from SQLite immediately on startup (no API wait), then refreshes from API
in the background and persists the result. `/my/` is populated instantly on cold restarts.

**Files changed:** `internal/db/schema.sql`, `internal/db/queries.sql`,
`internal/db/queries.sql.go`, `internal/fs/linearfs.go`

### H-2: Route FS metadata reads through SQLite ✅ (was already done)

States, labels, and cycles already route through `lfs.repo` → `SQLiteRepository` →
`store.Queries().ListTeamStates/Labels/Cycles()`. No additional changes needed.

### H-5: Persist pending-details queue across rate-limit backoff ✅

**Implemented:** Added `pending_detail_sync` table. When `syncIssueDetailsBatch` is
skipped (rate limited) or hits a RATELIMITED error, all affected issues are written to
the table. On each sync cycle, `drainPendingDetailSync()` is called first — if not rate
limited, it re-processes the queue in batches and removes successfully synced entries.

**Files changed:** `internal/db/schema.sql`, `internal/db/queries.sql`,
`internal/db/queries.sql.go`, `internal/sync/worker.go`

---

## Phase 4 — Medium Priority ✅ DONE

### M-3: Parse X-RateLimit-Reset for adaptive backoff ✅

**Implemented:** `checkRateLimitHeaders()` now parses `X-RateLimit-Reset` (Unix timestamp)
and stores it on the `Client` struct (thread-safe). `RateLimitResetAt() time.Time` is
exposed via the `APIClient` interface. `setRateLimited()` in the sync worker uses the
server-provided reset time when available, with a 5-second buffer; falls back to 15 min.

**Files changed:** `internal/api/client.go`, `internal/sync/worker.go`,
`internal/sync/worker_test.go`

### M-4: Deduplicate maybeRefreshIssueDetails at directory level

**Problem:** `maybeRefreshIssueDetails(issueID)` is called 3× per issue open (once each
from `GetIssueComments`, `GetIssueDocuments`, `GetIssueAttachments`). Each call is
individually deduplicated by key, but all three checks still hit SQLite.

**Status:** Partially mitigated — existing key-based deduplication in
`triggerBackgroundRefresh` ensures at most one background API call fires. The three SQLite
staleness checks remain. Full fix (move check to Readdir level) is low priority now.

**Plan (if desired later):**
1. Add a `maybeRefreshIssueDetailsOnce(issueID)` method that checks all three resources
   and queues a single refresh if any are stale
2. Call it once at the `Readdir` level when listing an issue directory, rather than
   per-getter

---

## Request Budget Estimates

| Scenario | Before | After |
|---|---|---|
| Rate limiter ceiling | 7,200/hr | 1,500/hr |
| 30 syncs/hr × 1 metadata call | 30 | 30 |
| Detail batch (15 issues, 1 call) | — | ~2–4/batch |
| FS metadata reads (states/labels) | unbounded | 0 (SQLite, H-2) |
| History cold open | 1 blocking | 1 background |
| Touch feedback loop | ∞ | 0 (C-2 fixed) |

---

## Implementation Order

```
Phase 1 (Critical)   ✅  C-1 rate limiter, C-2 Touch after batch
Phase 2 (High)       ✅  H-3 batch size, H-4 non-blocking history, M-1 staleness threshold
Phase 3 (High)       ✅  H-1 persist viewer, H-2 SQLite metadata reads, H-5 queue persistence
Phase 4 (Medium)     ✅  M-3 reset header parsing  ⬜  M-4 dedup refresh (low priority)
```
