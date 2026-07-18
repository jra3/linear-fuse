# Offline integration suite: `-count` safety

## Problem

The offline integration suite (`internal/integration`, fixture mode) is not safe
under `go test -count=N` for `N > 1`. A single fresh-process run is reliably green
(verified: 5/5 separate `go test -count=1` invocations pass), which is why CI —
one run per fresh process — never sees it. But `-count=3` fails ~9 tests.

This matters because **separate-process repeats are the way you prove the absence
of flakes** (`for i in $(seq 20); do go test ./internal/integration/...; done`),
and `-count` is the cheap in-process equivalent. A suite that can't survive
`-count` can't be load-tested for real flakes, and the pollution it exposes is
latent order-dependent risk even at `count=1`.

## Root cause

`TestMain` mounts the filesystem **once** over a store seeded with shared
fixtures (`TST-1`, the `TST` team, `test-project`, …). Every test shares that one
mount and store. A test that **mutates shared fixture state without restoring it**
leaves debris that:

1. breaks **itself** on the next in-process iteration (it assumes a clean start), and
2. breaks later **reader** tests that observe the mutated fixture.

The isolation boundary the suite actually supports is the **process** (fresh
mount + freshly-seeded store), not the individual test. `-count` reuses the mount,
so the debris accumulates.

Two debris channels:

- **Store rows** written directly (`testStore.Queries().UpsertIssue/UpsertTeam/UpsertLabel`)
  or upserted with a mutation — never restored.
- **Mount node state**: a store restore alone is not enough, because a live node
  caches adopted content (and its 30s kernel entry timeout), so a later read
  within the window still serves the polluted value. Cache-coherent restore would
  need kernel invalidation the tests can't currently reach by inode.

## Taxonomy of the failing tests

| Test | Channel | Mechanism |
|---|---|---|
| `TestStaleCatalogWriteSelfHeals` | store label | Upserts label `TeammateFresh`, no cleanup. Rerun: label already present → resolve succeeds with **no refresh** → `refresh calls = []`. |
| `TestNonexistentNameFailsAfterOneRefresh` | store issues | Creates a probe issue per run (accumulates); rerun records a **double** state refresh. |
| `TestOffline_AtomicRenameEditPersists` | shared `TST-1` + scratch node | Restores via a mount write, but the consumed-scratch-node (`#280`) makes that restore unreliable across reruns → oscillates pass/fail. |
| `TestRemoteUpdateVisibleAfterKernelRevalidation` | shared `TST-1` row | Upserts `TST-1` as "Renamed By Remote Sync", never restores. Rerun: `before` no longer contains "Test Issue 1". Also pollutes the `TestFixture*` readers of `TST-1`. |
| `TestRemoteTeamUpdateVisibleAfterKernelRevalidation` | shared team row | Upserts the `TST` team renamed, never restores. |
| `TestFixtureIssueFileDescription`, `…CommentsDirectoryListing`, `…CommentFileContents`, `…AttachmentsDirectoryListing`, `TestFixtureIssueMetaRelations` | — (victims) | Read shared `TST-1`; pass in isolation, fail only after a polluter ran. Heal once polluters are fixed. |

## Fix strategy — self-contained tests, no new production seam

The clean fix is to stop mutating **shared** fixtures. Each mutating test operates
on a **unique-per-run throwaway entity** it creates and cleans up, so there is no
shared state to pollute and no cross-run collision:

- **`TestRemote*Revalidation`** — instead of renaming shared `TST-1` / the `TST`
  team, create a throwaway issue / team with a unique id per run
  (`iso-<name>-<UnixNano>`), upsert it (the mount serves it via store-backed
  Lookup), pin it, mutate *it*, wait out the timeout, assert revalidation on *it*.
  The go-fuse node-reuse path under test is identical on a throwaway node.
  `t.Cleanup` deletes the throwaway rows. Nothing shared is touched.
- **`TestOffline_AtomicRenameEditPersists`** — do the atomic-save-over-`issue.md`
  dance on a throwaway issue's `issue.md`, not `TST-1`. The consumed-scratch-node
  behavior is exercised the same way; the shared fixture is untouched, and there
  is no cross-run marker to linger.
- **`TestStaleCatalogWriteSelfHeals`** — the created label is throwaway already;
  add `t.Cleanup` that deletes it from the store (`DeleteLabel`) so a rerun starts
  with the local miss the test needs.
- **`TestNonexistentNameFailsAfterOneRefresh`** — clean up the probe issue and
  confirm the double-refresh is a consequence of accumulation; if it isn't,
  diagnose the second refresh directly.
- **Any remaining shared-fixture polluter** (a comment/attachment created on
  `TST-1` by `t6_conformance`/others without cleanup) — create it on a throwaway
  issue, or delete it in `t.Cleanup`.

Where a throwaway isn't practical, delete the injected store rows in `t.Cleanup`
(the pattern `TestRejectedSaveKeepsDirtyContentReadable` already uses:
`testStore.DB().Exec("DELETE FROM …")`).

## Verification

1. Each fixed test passes in isolation at `-count=5`.
2. Full suite passes `go test -count=20 ./internal/integration/...`.
3. Separate-process repeats stay green (the pre-existing guarantee):
   `for i in $(seq 10); do go test -count=1 ./internal/integration/...; done`.

## Non-goals / recorded decisions

- **No new production invalidation seam for tests.** The throwaway-entity approach
  avoids needing cache-coherent restore of shared fixtures, so we don't add a
  test-only `InvalidateIssueForTest` to `LinearFS`.
- The process remains the documented isolation boundary; `-count` safety is an
  additional guarantee this change buys, not a redefinition.
