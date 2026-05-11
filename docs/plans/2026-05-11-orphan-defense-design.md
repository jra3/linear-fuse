# Design: Orphan-Refresh-Loop Defense

**Date:** 2026-05-11
**Status:** Approved

## Problem

When an issue, project, or initiative is deleted or archived in Linear, the
local SQLite row persists indefinitely. Linear's `*ByUpdatedAt` queries — the
basis of the sync worker — never return deleted entities, so the orphan rows
have no path to expire. Any FUSE traversal that touches an orphan's path
triggers a background refresh of its sub-resources (`refreshIssueDetails`,
`maybeRefreshHistory`, project/initiative document and update refreshes). Each
refresh fails with `GraphQL error: Entity not found: <Type>`. Until commit
`f2417ba` (2026-05-11) the failure path did not update any timestamp or row,
so the in-memory dedup map cleared and the very next access retried the
identical call.

Observed in production for ~3 days: 60% error rate on `IssueDetails` and
`IssueHistory`, sustained CPU ~5 cores, hourly budget at 155% of limit,
user-facing writes failing with `rate limit wait cancelled`. 28 phantom
issues drove the loop, triggered by a routine `bfs` walk of `~/am/linear`
from a Claude Code session.

`f2417ba` fixed the issue path reactively. This design generalizes the fix
to projects and initiatives, and adds an adaptive proactive layer so an
extended outage or a missed reactive deletion cannot regrow the problem.

## Goals

1. **Never reach the bad state**: any orphan that exists in SQLite gets
   discovered and removed within a bounded time, regardless of whether
   the FS layer happens to access it.
2. **Auto-heal**: if an orphan slips through, the very first time anyone
   touches its path the reactive layer cleans it up without operator
   action, and the cleanup itself triggers a proactive sweep to catch
   siblings.

## Non-Goals

- **Other entity types**: only issues, projects, and initiatives have
  refresh closures that loop. Teams, labels, cycles, states, users, and
  workspaces are fetched in bulk during sync and don't exhibit the
  loop pattern. Excluded.
- **Orphan sub-resources of live entities**: a comment deleted from a still-
  existing issue is not in scope. It doesn't loop — it just produces
  stale data on next view. Out of scope.
- **General health monitoring**: error-rate alerts, stuck-sync detection,
  FUSE-handler watchdogs. The user explicitly chose narrow scope.

## Design

The defense is three layers, each addressing a different gap.

### Layer 1 — Reactive cleanup (extend the existing pattern)

Today (`internal/repo/sqlite.go`) has `isEntityNotFound(err)` and
`deleteOrphanIssue(ctx, id)`. We add two siblings and wire them into the
four remaining refresh closures.

**New helpers** (same shape as `deleteOrphanIssue`):

- `deleteOrphanProject(ctx, id)` — deletes from `projects`,
  `project_teams`, project-scoped `documents`, `project_updates`,
  `project_milestones`, and initiative-project links. Does **not** touch
  the `issues.project_id` column of issues that referenced this project;
  those rows stay (the issue still exists), and the column reflects the
  stale link until the issue itself is next synced.
- `deleteOrphanInitiative(ctx, id)` — deletes from `initiatives`,
  initiative-scoped `documents`, `initiative_updates`, initiative-project
  links.

Where a `Delete*` query doesn't already exist in `internal/db/queries.sql`,
add one and re-run `sqlc generate`.

**Refresh closures to wire** (matching the issue pattern from `f2417ba`):

| File / function | Catches | Calls |
|---|---|---|
| `maybeRefreshProjectDocuments`, project-doc cold fetch | `Entity not found: Project` | `deleteOrphanProject` |
| `maybeRefreshInitiativeDocuments`, init-doc cold fetch | `Entity not found: Initiative` | `deleteOrphanInitiative` |
| `maybeRefreshProjectUpdates`, project-update cold fetch | `Entity not found: Project` | `deleteOrphanProject` |
| `maybeRefreshInitiativeUpdates`, init-update cold fetch | `Entity not found: Initiative` | `deleteOrphanInitiative` |

Detection stays generic: `strings.Contains(err.Error(), "Entity not found")`.
The closure already knows which entity type it's refreshing, so it picks
the right `deleteOrphan*` helper directly.

### Layer 2 — Adaptive reconciliation trigger

Reconciliation should only run when there's evidence of drift, with a
cooldown to prevent thrashing. The trigger fires from any successful
reactive deletion.

**State on `*SQLiteRepository`:**

```go
reconcileMu      sync.Mutex
lastReconcileAt  time.Time    // zero until first run
reconcilePending atomic.Bool
```

**Trigger** (called at the end of every `deleteOrphan*` helper after a
successful issue/project/initiative row delete):

```go
func (r *SQLiteRepository) maybeScheduleReconcile() {
    if r.client == nil { return }
    if r.reconcilePending.Load() { return }

    r.reconcileMu.Lock()
    elapsed := time.Since(r.lastReconcileAt)
    r.reconcileMu.Unlock()

    if elapsed < reconcileCooldown { return }
    if !r.reconcilePending.CompareAndSwap(false, true) { return }

    go r.runReconcile()
}
```

**Why this shape:**

- **Cold start handles itself.** `lastReconcileAt == 0` means
  `elapsed = forever`, so the first reactive delete after a restart fires
  a sweep. This covers the "service restarted with N orphans already in
  SQLite" case without a separate startup hook.
- **Naturally rate-limited.** At most one pass per `reconcileCooldown`
  (default 6h).
- **`reconcilePending` collapses bursts.** If 50 orphans are deleted in
  one `bfs` walk, only one pass is scheduled.
- **No timers, no goroutine bookkeeping.** Synchronous check at the call
  site; the work runs in a one-shot goroutine that clears
  `reconcilePending` when done.

**Constants** (in `sqlite.go`, not config):

```go
const reconcileCooldown = 6 * time.Hour
```

**Observability** (always-on logs):

```
[reconcile] adaptive trigger after orphan delete; pass scheduled
[reconcile] pass complete: issues=N projects=M initiatives=K duration=...
```

### Layer 3 — The reconciliation pass

For each entity type, fetch the authoritative ID set from Linear, diff
against SQLite, and call the cleanup helper for each missing local row.

**New cheap API queries** (`internal/api/queries.go` + methods on `*Client`):

- `GetTeamIssueIDs(ctx, teamID) ([]string, error)` — paginated
  `team(id).issues { nodes { id } }`. ID-only payload (~100 IDs per page).
- `GetWorkspaceProjectIDs(ctx) ([]string, error)` —
  `workspace.projects { nodes { id } }`.
- `GetWorkspaceInitiativeIDs(ctx) ([]string, error)` —
  `initiatives { nodes { id } }`.

ID-only is meaningfully cheaper than `IssueFields` (~30 fields × 100 issues
per page vs. 100 IDs).

**Pass structure** (on `*SQLiteRepository`):

```go
func (r *SQLiteRepository) runReconcile() {
    defer r.reconcilePending.Store(false)
    ctx, cancel := context.WithTimeout(r.refreshContext, 10*time.Minute)
    defer cancel()

    start := time.Now()
    issues := r.reconcileIssues(ctx)
    projects := r.reconcileProjects(ctx)
    initiatives := r.reconcileInitiatives(ctx)

    r.reconcileMu.Lock()
    r.lastReconcileAt = time.Now()
    r.reconcileMu.Unlock()

    log.Printf("[reconcile] pass complete: issues=%d projects=%d initiatives=%d duration=%s",
        issues, projects, initiatives, time.Since(start).Round(time.Millisecond))
}
```

**Per-entity (issues shown; projects and initiatives are the same shape
without the team loop):**

```go
func (r *SQLiteRepository) reconcileIssues(ctx context.Context) int {
    teams, err := r.store.Queries().ListTeams(ctx)
    if err != nil {
        log.Printf("[reconcile] list teams: %v", err)
        return 0
    }
    deleted := 0
    for _, team := range teams {
        if r.client.LowBudget() {                     // see "Budget protection"
            log.Printf("[reconcile] budget low, deferring remaining teams")
            return deleted
        }
        apiIDs, err := r.client.GetTeamIssueIDs(ctx, team.ID)
        if err != nil {
            log.Printf("[reconcile] issues team %s: %v (skipping)", team.Key, err)
            continue
        }
        localIDs, _ := r.store.Queries().ListTeamIssueIDs(ctx, team.ID)
        for _, id := range setDiff(localIDs, apiIDs) {
            r.deleteOrphanIssue(ctx, id)
            deleted++
        }
    }
    return deleted
}
```

**Budget protection.** Before each new team's pagination starts (and before
each workspace-level query for projects/initiatives), check the rate
limiter. If tokens are low, log a deferral and skip the remaining scope
for that sub-pass. `lastReconcileAt` is updated at the end of
`runReconcile` regardless of partial bail-out — orphans skipped due to
low budget will be picked up by the next adaptive trigger after the
cooldown elapses, or on the first reactive cleanup that hits them.
Introduce a small helper:

```go
// LowBudget reports whether the rate limiter has dropped below the
// reserved-write threshold. Reconciliation should defer when true.
func (c *Client) LowBudget() bool { return c.limiter.Tokens() < 5 }
```

**Concurrency.** Reconcile uses `r.client` directly — same rate limiter,
same circuit breaker, no interaction with the `triggerBackgroundRefresh`
semaphore. Concurrent user writes keep the last 2 burst tokens via
existing mutation priority.

## Safety properties

- **Whole-scope-or-nothing diffs.** Orphan deletion only runs after a
  successful full fetch of the relevant scope (one team's issues, or all
  workspace projects, or all workspace initiatives). If pagination fails
  partway, we abort that scope and delete nothing in it. This prevents
  the catastrophic false-positive where a network error halfway through
  pagination causes us to delete every row that would have been returned
  by subsequent pages.
- **Single source of truth for "not in API".** The reconciliation pass
  treats Linear's response as authoritative for the whole returned set —
  no partial trust. Reactive cleanup, by contrast, requires a per-entity
  explicit `Entity not found` error — never inferred from missing-in-list.
- **Re-add is cheap and correct.** If we false-positive-delete (e.g.
  Linear API transient bug), the next normal sync re-adds the row with
  empty sub-resources, which will repopulate on next access. No data
  integrity issue, only a small re-fetch cost.
- **No mutation of upstream Linear.** Cleanup is local-SQLite only.

## Cost estimate

Per pass, today's workspace shape:

| Source | Pages | Calls |
|---|---|---|
| ENG team issues (~2000) | 20 | 20 |
| GTM team issues (~600) | 6 | 6 |
| TST + small teams | ~5 | 5 |
| Workspace projects (~50) | 1 | 1 |
| Workspace initiatives (~20) | 1 | 1 |
| **Total per pass** | | **~33** |

At the adaptive cooldown of 6h, worst case 4 passes/day = ~132 calls.
Against Linear's 1500/hour (36,000/day) limit, this is 0.4% of budget.

## Testing strategy

New tests in `internal/repo/sqlite_test.go` and `internal/api/client_test.go`:

- `TestDeleteOrphanProject` — seed projects + sub-resources + a sibling
  keeper, assert orphan rows gone, keeper intact.
- `TestDeleteOrphanInitiative` — symmetric to above.
- `TestMaybeScheduleReconcile_CooldownGate` — invoke twice within
  cooldown window, assert only one goroutine fires.
- `TestMaybeScheduleReconcile_ColdStart` — `lastReconcileAt` zero, assert
  trigger fires.
- `TestReconcileIssues_DeletesOrphans` — seed SQLite with 3 issues, stub
  client returning 2, assert the missing one is deleted via the orphan
  helper.
- `TestReconcileIssues_AbortsTeamOnFetchError` — stub returns error on
  page 2 of a team, assert zero deletions for that team and the loop
  continues to the next team.
- `TestReconcileIssues_RespectsBudgetGate` — `LowBudget()` returns true,
  assert pass returns early without API calls.

Existing test `TestDeleteOrphanIssue` already covers the issue path. The
reactive-fix tests added by `f2417ba` (`TestIsEntityNotFound`,
`TestDeleteOrphanIssue`) stand.

## Known limitations

- **Detection is string-based.** `strings.Contains(err.Error(), "Entity not found")`
  depends on Linear's error message wording. If Linear changes the
  phrasing (e.g., to `"entity not found"` lower-case, or to a separate
  GraphQL error code), reactive cleanup silently regresses to the
  pre-`f2417ba` behavior. Mitigations to keep in mind for later: parse
  GraphQL error extensions if/when Linear surfaces structured codes; the
  proactive layer remains effective regardless.
- **Adaptive trigger has a startup grace period.** Until a reactive
  cleanup happens, no pass runs. If a brand-new install (no orphans
  cleaned reactively yet) somehow inherits orphans from a corrupted DB
  restore, the first sweep waits for the first FS access that hits an
  orphan. In practice this is seconds-to-minutes, not a concern.
- **Reconcile cost grows linearly with workspace size.** ~33 calls today;
  a 10× larger workspace would be ~330. Still well under budget; not
  worth optimizing pre-emptively.
