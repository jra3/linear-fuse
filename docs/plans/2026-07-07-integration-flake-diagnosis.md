# Integration-suite flake: diagnosis plan

**Status: BUILT — with corrected diagnosis.** H1 (lingering SWR goroutines) was
WRONG for fixture mode: `InjectTestStore` already wires a nil client and the SWR
paths guard on it; the fixture-mode 401s are the *mutation-path* tests exercising
failure branches (they do hit the real API — a follow-up candidate, not the flake).
The real mechanisms: (a) **the SQLite DB lived inside the mountpoint** — post-mount
file opens (WAL checkpoints, journals) routed through our own FUSE layer; (b) **an
intermittent leaked test fd made `server.Unmount` fail EBUSY** and the mount
silently orphaned at process exit — the next run then died against the dead
connection (the roaming-EIO signature). Fixes: DB moved to its own temp dir;
`WaitMount` readiness gate (both modes); TestMain preflight fails loud on a stale
`linearfs-test-*` mount; cleanup retries then falls back to `fusermount3 -uz`
(unprivileged `umount2` is EPERM on FUSE — the setuid helper is required) with a
leaked-fd diagnostic. Verified: 30 consecutive clean full-suite runs, zero orphans
(one leak occurred and self-healed via lazy detach). The one-at-a-time protocol is
retired. Original spec below. Confirmed 2026-07-07: full-suite runs of
`internal/integration` fail nondeterministically — a *different* test each run
(`TestTeamsListing`, `TestWriteInvalidInputIsLoud`,
`TestWriteContractAtomicRenameCreateNoEROFS`, `TestWriteContractCreateTrioUniform`,
cycles/ENOENT tests), always with mount I/O errors (`readdirent: input/output
error`); every failing test passes in isolation; failed runs often orphan the
mount. **Reproduces identically at base main@2987b7f** — pre-existing, not a
round-14 regression. CI passes (it runs a narrower read-only selection and
carries a `fusermount3 -u /tmp/linearfs-test-*` cleanup hammer —
.github/workflows/test.yml:60,65 — and runs *without* `-race`).

## Harness facts (surveyed)

- **One shared mount + one process-global LinearFS** for the whole binary
  (integration_test.go:20–57); cleanup runs once after `m.Run()` — a panic or
  `os.Exit` path skips unmount → the observed orphans.
- **No readiness gate** after `MountFS` — tests can hit the mount before the
  FUSE server serves (integration_test.go:150–157).
- **The SQLite database file lives inside/alongside the mount temp dir**
  (integration_test.go:112) — verify whether DB I/O ever crosses the FUSE
  boundary (it must not).
- Tests **don't** use `t.Parallel()`, but share process-global state: the
  injected mockmutation client, `.error`/`.last` sidecars ("the mount is
  shared, so other tests append too" — t4_create_test.go:115), and the repo's
  **background goroutines** (`refreshContext` rooted at `context.Background()`,
  sqlite.go:78–86; refresh/reconcile goroutines outlive the test that
  triggered them).
- In fixture mode there is **no API key, yet API calls fire and 401** (seen
  in every run's logs) — the SWR refresh paths run against the real endpoint
  and fail. `deleteOrphanIssue` triggers only on "Entity not found", not 401
  (sqlite.go:929) — verified safe, but the goroutines still contend for the DB.
- `busy_timeout(5000)` on the DSN — SQLITE_BUSY under contention surfaces as
  a FUSE handler error (EIO) after a 5s stall, which *is* the observed
  failure signature.

## Ranked hypotheses

**H1 (primary): lingering background goroutines contend with test I/O.**
A test triggers SWR refresh/reconcile; those goroutines (rooted in the
repo's own ctx, not the test's) keep running into later tests, take DB locks
or churn rows; a later test's FUSE handler hits SQLITE_BUSY/an inconsistent
row and returns EIO. Explains: different victim each run, isolation always
passes, timing sensitivity.

**H2: a FUSE handler panic kills the connection.** go-fuse turns an unrecovered
handler panic into a dead connection → *every* subsequent op returns EIO and
the mount orphans. One panicking handler anywhere explains total-mount death;
need panic evidence (nothing in captured logs yet — instrument first).

**H3: shared sidecar state races.** Tests polling `.error`/`.last` while
others write them — explains assertion-level flakes but not `readdirent: EIO`;
likely a secondary annoyance, not the mount-killer.

## Diagnosis plan (ordered, cheap → expensive)

1. **Instrument, then reproduce in a loop.** Run the suite 20× with:
   `-test.v`, go-fuse debug off but a `defer recover()` logging wrapper
   temporarily around handler entry (or run the server with `Debug` on to a
   file), SQLite busy/locked errors logged with stack, and `GOTRACEBACK=all`.
   The first failing run tells us: panic (H2), SQLITE_BUSY (H1), or neither.
2. **Kill H1 structurally and re-measure**: scope the repo's refresh ctx per
   test is invasive; instead add a `lfs.QuiesceBackground()` test hook (cancel
   + wait refresh/reconcile goroutines) called from a `t.Cleanup` helper in
   tests that trigger refreshes — or globally disable SWR refresh in fixture
   mode (`stalenessThreshold = ∞` when no API key): fixture tests never want
   a 401-destined refresh anyway. **This is likely the real fix regardless**:
   background 401 churn in fixture mode is pure noise.
3. **Readiness gate**: `WaitMount()` (go-fuse provides it) after MountFS.
   One line; removes a whole class of early-access races.
4. **Orphan-proof cleanup**: move unmount into a `defer` + signal handler in
   TestMain, and have the harness refuse to start if a stale
   `linearfs-test-*` mount exists (fail loud, print the cleanup command).
5. Only if flakes persist: per-test-file mount groups (heavier; not the
   first move).

## Success criteria

`for i in $(seq 20); do go test ./internal/integration/; done` — zero
failures, zero orphaned mounts, zero 401 log lines in fixture mode. Then
delete the memory-file protocol note ("run one at a time") and let CI run the
full suite.

## Effort

Diagnosis: half a day. Likely fix (fixture-mode SWR disable + WaitMount +
orphan-proof cleanup): small PR. H2, if real, is wherever the panic is.
