# detailSyncedAt — round-18 top pick (grilling-locked)

Candidate 1 from the 2026-07-08 round-18 review (report
`~/tmp/architecture-review-20260708-173409.html`). Grilling-locked with John.

## The hazards being closed

1. **The empty-family refetch loop (nearly universal, live budget leak).**
   Issue-details staleness = min of three per-family `MAX(synced_at)`s
   (comments/documents/attachments). The touch pass is an `UPDATE` — it cannot
   stamp rows that don't exist — so any issue with an empty family (most have
   zero docs) yields `MAX = NULL` → zero time → `swrStale` event-driven reads
   "never synced" → **stale forever**. Every browse of `comments/`, `docs/`,
   or `attachments/` fires a background `GetIssueDetails`; the dedup key only
   covers the in-flight window and the refresh upserts nothing for the empty
   family, so the next browse re-triggers. Permanent per-browse complexity
   leak on the hottest agent path.
2. **The history-staleness mask.** The touch-on-unchanged block
   (`syncTeamIssues`, worker.go:371-380) re-stamps ALL four sub-resource
   families for unchanged issues, including `TouchIssueHistoryCache`. History
   is never worker-fetched (SWR-only), so an issue whose history was cached
   before its last update gets the stale cache stamped fresh every cycle —
   `history.md` serves pre-update history indefinitely.
3. **Dead code**: `repo.TouchIssueSubResources` has zero callers.

## Locked decisions

- **One freshness fact**: `issues.detail_synced_at` (nullable DATETIME).
  NULL = genuinely never synced (correct cold semantics). Stamped:
  - by `syncDetails` for CLEAN issues, exactly where the three touches ran
    (the stamp inherits the round-17 ledger's honesty — unclean issues stay
    unstamped and pending);
  - by the repo's `refreshIssueDetails` on a clean `PersistIssueDetails`
    (symmetric clean gate).
  Locally-created issues stay NULL → one harmless fetch on first browse.
- **Upsert safety**: the column is omitted from `UpsertIssue`'s INSERT list
  and ON CONFLICT SET clause — NULL on insert, preserved on every sync upsert.
- **Migration: bootstrap ALTER at store open** (first migration precedent —
  none existed). Check `pragma table_info(issues)`, `ALTER TABLE ADD COLUMN`
  if missing; idempotent, ~15 lines + test. Rejected: documented rm+resync
  (live service would error until manual deletion, and a full resync burns
  real complexity budget — the 2026-07-06 wedge lesson); a user_version
  framework (framework-building for one column; extract later when full
  columnization lands).
- **Full cleanup** (grilled; the alternative belt-and-suspenders option was
  rejected as two-freshness-mechanisms drift): delete the three `Touch*`
  queries, the three `GetIssue*SyncedAt` aggregates (their only consumers
  were staleness inference), the touch-on-unchanged block INCLUDING its
  history touch (under event-driven staleness an unchanged issue is fresh by
  definition — stamp > issue.updatedAt — and never-fetched SHOULD read
  stale; deleting the history touch FIXES hazard 2, a recorded behavior
  improvement), and the dead `TouchIssueSubResources`.
- **SWR spec**: the issue-details `syncedAt` closure becomes one query;
  builder may merge it with the `changedAt` lookup into a single two-column
  fetch (`updated_at, detail_synced_at`). NULL → zero → stale, unchanged
  `swrStale` semantics.
