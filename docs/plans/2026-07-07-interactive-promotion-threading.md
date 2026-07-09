# Interactive promotion threading (rate-budget PR2)

**Status: BUILT 2026-07-07 — with a corrected inventory.** The build-time
verification found the spec's call-site table overstated the API surface:
the 7 render-closure sites (issue.meta, history.md, states.md, labels.md,
project/initiative meta) are **SQLite-first with background refresh** — they
never synchronously call the API, so promotion is moot there. Only TWO
genuinely synchronous user-blocking API calls exist and both now promote:
`LinearFS.GetTeamDocuments` (team docs listing — the one read that is
"currently via API as not synced") and the attachment-create authoritative
re-check (`attachments.go`). The renderFunc ctx-threading was built anyway —
it is correct hygiene (cancellation; honest ctx; any future synchronous
source in a render closure can promote) and `TestRenderFileThreadsContext`
pins the propagation. The never-store rule is documented at
`WithInteractive`. Original spec below for the record.

**Status (original): spec — not built.** Companion to `2026-07-06-rate-limit-budget-design.md`
(PR1, built): the `rateBudget` module and its priority ladder (write >
interactive > skeleton > list > detail) exist and are tested; `WithInteractive(ctx)`
is the promotion mechanism. **No production call site threads it**, so a human
waiting on `ls`/`cat` runs at base tier and can queue behind background sync —
observed live 2026-07-07 when the budget sat below the list-tier reserve and
everything deferred equally.

## Problem

A FUSE request handler sometimes must call the Linear API synchronously (the
user is blocked on the answer). Those calls should spend from the interactive
reserve (2% tier) so they outrank skeleton/list/detail work. Promotion is
ctx-borne (`tierFor` inspects the ctx), so the fix is plumbing, but the survey
found the plumbing is **broken upstream of the wrap**: the blocking call sites
mostly use `context.Background()`, so even a correct `WithInteractive` wrap
today would be discarded.

## Call-site inventory (verified 2026-07-07)

Synchronous, user-blocking, should promote — **9 sites**:

| # | Site | File | Current tier | ctx reaches query()? |
|---|------|------|--------------|----------------------|
| 1 | issue.meta render → `FetchIssueByIdentifier` | issues.go:332 | list | **no — context.Background()** |
| 2 | issue.meta render → `GetIssueAttachments` | issues.go:335 | detail | no |
| 3 | history.md render → `GetIssueHistory` | issues.go:347 | detail | no |
| 4 | states.md render → `GetTeamStates` | teams.go:117 | skeleton | no |
| 5 | labels.md render → `GetTeamLabels` | teams.go:127 | skeleton | no |
| 6 | project renders → `GetTeamProjects` | projects.go:240 | skeleton | no |
| 7 | initiative renders → `GetInitiatives` | initiatives.go:124 | skeleton | no |
| 8 | attachment create pre-check → `GetIssueAttachments` | attachments.go:233 | detail | yes |
| 9 | attachment create re-check → `GetIssueAttachments` | attachments.go:268 | detail | yes |

Confirmed correct as-is (do NOT touch):
- All background goroutines (`MaybeRefreshIssueDetails`, `refreshIssueDetails`,
  reconcile) deliberately run on `context.Background()` — they must never
  inherit a promoted ctx (see Hazard below).
- Mutations already classify as write tier via the query-string intent map.
- Cache-first repo reads return SQLite data without an API call.

## Design

Two pieces:

### 1. `renderFile.render` gains a ctx (the structural change)

`render func() ([]byte, time.Time, time.Time)` becomes
`render func(ctx context.Context) ([]byte, time.Time, time.Time)`.
`renderFile.Read`/`Open`/`Getattr` and `lookupRenderFile`/`newRenderInode`/
`mountRenderFile` pass the FUSE handler's ctx through. This is a mechanical
signature change across the ~12 render closures (most ignore the ctx — they
read SQLite); the 7 API-calling closures switch from `context.Background()`
to the threaded ctx.

**Getattr/Lookup nuance:** the render closure also runs for size during
Lookup/Getattr. That is still user-blocking I/O, so promotion is correct
there too — no special case.

### 2. Wrap the 9 sites

`ctx = api.WithInteractive(ctx)` at each site (or once inside a small helper
per closure). Sites 8–9 are one-liners today.

### Hazard: promoted-ctx leakage into background work

A promoted ctx must not be handed to anything that outlives the FUSE request
(a spawned goroutine would spend the interactive reserve on background work).
Rule, enforced by review + a test: `WithInteractive` is applied at the moment
of the synchronous call, never stored. The repo's background paths already
construct their own `context.Background()`-rooted ctx, so today's code is
safe; add a comment at `WithInteractive` stating the rule.

## Tests

- Unit: a fake-clock `rateBudget` test asserting a `WithInteractive`-wrapped
  op admits when only the interactive reserve remains while the same op
  un-wrapped defers (exists in spirit in ratebudget_test.go — add the
  end-to-end client-level variant via the seeded-headers test harness).
- Unit: render-closure ctx propagation — a stub render closure asserts it
  receives the ctx passed to `Read` (extend renderfile_test.go).
- Live validation: with the budget below the list reserve (reproducible by
  watching the hourly window), `cat issue.meta` succeeds while sync logs show
  deferrals.

## Effort

Small-medium: one mechanical signature change (~12 closures), 9 wraps, tests.
No schema, no API changes. Single PR.

## Decisions to grill

1. Should skeleton-tier files (states.md/labels.md — sites 4–7) promote at
   all? They're cheap and cached in SQLite; the API call only fires on a cold
   cache. Recommendation: yes — the tier is about *who waits*, not cost.
2. Wrap placement: per-site vs. inside `lookupRenderFile` for all render
   closures uniformly. Recommendation: per-closure (only the API-calling
   ones), so a future SQLite-only closure doesn't silently spend reserve.
