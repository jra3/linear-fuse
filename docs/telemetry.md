# Telemetry reference

The complete OTEL metrics surface of linearfs. Everything here is generated
from the code as of #206's completion (PRs #207/#209/#210); source pointers
accompany each section. Design rationale lives in
`docs/plans/2026-07-08-otel-metrics-design.md`; the architectural entry is
CONTEXT.md "Telemetry (meter)".

**Policy (locked): metrics only — no tracer is ever configured.** Traces were
considered and rejected (YAGNI); revisit only if something concrete demands
them.

## Architecture: one source, two renderings

One SDK `MeterProvider` (built by `telemetry.Init`, registered globally via
`otel.SetMeterProvider`) feeds two `PeriodicReader`s:

| Rendering | Cadence | Always on? | Audience |
|---|---|---|---|
| **journald summary** — one compact log line | 5 min (`summaryInterval`, fixed) | **yes** | humans running `journalctl --user -u linearfs` |
| **JSONL file** — one OTLP-style JSON object per line | configurable (default 60s) | **no** (config-gated) | machines/agents running `jq` |

Instrument sites never import the SDK — they call `otel.Meter("linearfs/<layer>")`
against the global provider. With no provider registered (unit tests, tools),
the global no-op provider makes every record free; no nil checks exist at
call sites. Telemetry can never block mounting: `Init` failure is
log-and-continue in cmd, and a file-exporter setup failure degrades to
summary-only inside `Init`.

Source: `internal/telemetry/telemetry.go` (`Init`, wiring),
`internal/telemetry/instruments.go` (shared `MustInt64Counter` /
`MustFloat64Histogram` helpers).

## Configuration

```yaml
# ~/.config/linearfs/config.yaml
telemetry:
  file:
    enabled: true            # default false — the ONLY opt-in
    path: ~/metrics.jsonl    # default: <UserConfigDir>/linearfs/metrics.jsonl (next to cache.db)
    interval: 60s            # default 60s (export period)
    max_size_mb: 50          # default 50 (rotation cap)
```

- The meter and the journald summary are **not configurable** — always on.
- `~/` in `path` is expanded; parent directories are created.
- Zero/omitted `interval` / `max_size_mb` fall back to defaults.
- **Rotation**: size-capped with a single rollover slot — when a write would
  push the file past the cap, `path` is renamed to `path.1` (replacing any
  previous `.1`) and a fresh file starts. Disk usage is bounded at ~2× the
  cap. Source: `internal/telemetry/rotate.go`.

Source: `internal/config/config.go` (`TelemetryConfig`,
`TelemetryFileConfig`, `DefaultTelemetryPath`).

`telemetry.requests.*` (the per-request debug log — not an OTEL signal) is
documented in its own section below.

## Instruments

Naming: `linearfs.<layer>.<what>`; meter scopes are `linearfs/process`,
`linearfs/api`, `linearfs/budget`, `linearfs/sync`, `linearfs/swr`.
Histograms use the SDK default buckets; durations are in **seconds**.

### Process heartbeats — `internal/telemetry/heartbeat.go`

| Instrument | Kind | Attributes | Semantics |
|---|---|---|---|
| `linearfs.process.uptime_seconds` | observable gauge (float64, s) | — | seconds since process start |
| `linearfs.build.info` | gauge (int64, always 1) | `version`, `commit` | build metadata carried as attributes |

### API layer — `internal/api/metrics.go` (`apiMetrics`, bound at `Client` construction)

| Instrument | Kind | Attributes | Recorded |
|---|---|---|---|
| `linearfs.api.requests` | counter | `op`, `outcome` = `ok` \| `error` \| `ratelimited` | at `Client.query` completion — **only requests actually sent** (budget deferrals never reach here; they land in `linearfs.budget.decisions`) |
| `linearfs.api.duration` | histogram (s) | `op` | same site, wall time of the request |
| `linearfs.api.complexity` | histogram | `op` | in `rateBudget.reconcileLocked` — the ONE place `X-Complexity` is parsed (headers are never parsed twice); it is the response's *actual* server-scored cost |

`op` is the GraphQL operation name (`extractOpName`, ~30 values — e.g.
`TeamIssuesByUpdatedAt`, `IssueDetailsBatch`, `GetViewer`).

### Budget layer — `internal/api/metrics.go` (`budgetMetrics` + `registerBudgetGauges`, owned by `rateBudget`)

| Instrument | Kind | Attributes | Semantics |
|---|---|---|---|
| `linearfs.budget.remaining` | observable gauge | `axis` = `requests` \| `complexity` | server-reported budget remaining this window |
| `linearfs.budget.limit` | observable gauge | `axis` | server-reported hourly limit |
| `linearfs.budget.inflight` | observable gauge | `axis` | cost reserved by unsettled admissions |
| `linearfs.budget.reset_seconds` | observable gauge (s) | `axis` | seconds until the server-reported window reset |
| `linearfs.budget.decisions` | counter | `tier` = `write` \| `interactive` \| `skeleton` \| `list` \| `detail`; `decision` = `admit` \| `defer` \| `wait` \| `ratelimited` | ladder verdicts. **Counts EVENTS, not requests**: a mutation that is denied, waits for the window, and is re-admitted records `defer`, `wait`, then the re-admit's verdict |
| `linearfs.budget.wait_duration` | histogram (s) | — | time spent waiting on rate limiting (limiter smoothing >1ms + mutation-window waits); successor to APIStats' `rateLimitWaitNs` |

Gauge mechanics: one observable callback per collect reads
`rateBudget.snapshot()` — a single acquisition of the budget's existing mutex,
respecting its injected clock. **An axis the server has not yet reported is
skipped** ("no data point beats a fabricated zero"); `inflight` is always
observed — a stuck reservation is exactly the anomaly worth seeing.

### Sync layer — `internal/sync/metrics.go` + `internal/reconcile/collection.go`

| Instrument | Kind | Attributes | Recorded |
|---|---|---|---|
| `linearfs.sync.cycle_duration` | histogram (s) | `mode` = `lean` \| `full` | defer-recorded inside `syncCycle`, one sample per cycle for all three invokers (initial run, ticker, `SyncNow`). `mode` is the cycle's speed (#242): `full` = workspace + team-metadata drains + issues (cold start, `SyncNow`, and every `FullSyncInterval`, default 10m, off the persisted `sync_schedule` timestamp); `lean` = incremental issues + per-team projects probe, skipping the `GetWorkspace`/`GetTeamMetadata` fetches. The per-mode sample counts double as the cycle-mode counter — no separate instrument. Budget-skipped cycles record ~0s (attributed with the mode they would have run) — that near-zero spike IS the skip signature |
| `linearfs.sync.detail_outcomes` | counter | `outcome` = `synced` \| `deferred` | at `syncDetails`' two exits (the `deferAll` gate paths and the per-issue ledger). **Every issue leaving `syncDetails` is counted exactly once**, so summing both series gives issues processed |
| `linearfs.sync.probe_outcomes` | counter | `kind` = `team_projects`, `outcome` = `unchanged` \| `changed` \| `error` | one record per change-detection probe run (`probeTeamProjects`, lean cycles only, #243). `unchanged` = the newest-first page carried nothing past the persisted watermark (the ~1K steady-state check); `changed` = the resume walk upserted ≥1 project and advanced the watermark; `error` = fetch/upsert failure or cancellation, watermark untouched. A probe that never fires shows up as the series going flat while `cycle_duration{mode=lean}` keeps sampling. `kind` is future-proofing for the initiatives probe (later #238 slice) |
| `linearfs.sync.prunes` | counter | `collection` | inside `reconcile.Collection`, only when a prune **actually executes** (suppressed-by-unclean or nil prunes record nothing) |
| `linearfs.sync.reconcile_deletions` | counter | `kind` = `issue` | in `maybeReconcileIssueIDs`, the hourly scheduled issue-ID sweep (#245): local rows deleted because their ID was absent from a team's complete bare-ID drain. Zero-deletion sweeps record nothing. The reactive read-triggered orphan path and the repo's cooldown-gated reconcile pass are NOT counted here (log-only, as before) |
| `linearfs.sync.pending_depth` | observable gauge | — | `COUNT(*)` of `pending_detail_sync` (the detail-retry backlog), evaluated only at collect time; a count error skips the observation |

`collection` values are `CollectionSpec.Kind` — a closed set:
`state`, `label`, `cycle`, `project`, `member`, `initiative-project`,
`project-label`, `comment`, `document`, `attachment`, `relation`,
`inverse-relation`, `project-update`, `initiative-update`. (Kinds whose spec
carries a nil prune — e.g. `state`, `inverse-relation`, the repo's four
upsert-only tails — can never appear in `prunes`.) The spec's `Label` field
is NOT used as an attribute: it embeds entity IDs (unbounded cardinality) and
stays log-only.

### SWR layer — `internal/repo/metrics.go` (`swrMetrics`, bound at `SQLiteRepository` construction)

| Instrument | Kind | Attributes | Recorded |
|---|---|---|---|
| `linearfs.swr.triggers` | counter | `kind`, `decision` = `triggered` \| `fresh` \| `deduped` \| `sem_dropped` | `fresh` in `maybeRefreshSWR` when `swrStale` says no; the other three are `triggerBackgroundRefresh`'s exits (started / already in flight / refresh semaphore full) |
| `linearfs.swr.refresh_outcomes` | counter | `kind`, `outcome` = `ok` \| `error` \| `orphaned` | when a background refresh completes; `orphaned` mirrors the module's orphan classification (`api.IsNotFound` → local rows deleted) |

`kind` is the six `refreshKind` constants: `issue-details`, `history`,
`project-docs`, `initiative-docs`, `project-updates`, `initiative-updates`.
`triggerBackgroundRefresh(kind, id, fn)` mints its own dedup key from the
kind, so the attribute is bounded by construction. Fixture mode (nil client)
returns before any recording — zero-value `swrMetrics` records nothing.

**Reading the causal chain**: `swr.triggers{decision=triggered}` +
`sync.detail_outcomes` are the budget's consumers; `budget.remaining` is the
budget; `api.requests`/`api.complexity` are the spend. Round 18's leak
(`issue-details` triggering on every browse) would appear as a
`swr.triggers{kind=issue-details,decision=triggered}` slope with matching
`api.requests{op=GetIssueDetails}` growth and `budget.remaining{axis=complexity}`
decay — on one view.

## The journald summary line

Format: `metrics: name{k=v,...}=value ...` — counters/gauges as plain values
(integers printed exactly, never scientific notation), histograms as
`count:N,sum:X`.

To stay one readable line, attribute sets are **projected onto a keep-list**
(`summaryAttrKeys`): `outcome`, `decision`, `tier`, `axis`, `kind`,
`collection`, `version`, `commit`. Keys not in the list (notably the ~30-value
`op`) are dropped and the collided series **merged** (values and
count/sum summed). Full cardinality is only in the JSONL export — the summary
is deliberately the compact projection. Source:
`internal/telemetry/summary.go`.

## The JSONL file

One JSON object per line, one line per export interval: the SDK
`stdoutmetric` encoding of `ResourceMetrics` (PascalCase keys —
`ScopeMetrics`, `Metrics`, `Name`, `Data.DataPoints`, `Attributes`, …), with
the resource carrying `service.name=linearfs` and `service.version`.
Counters/histograms use the SDK default cumulative temporality — values are
totals since process start, so **rates are line-over-line deltas**.

### jq recipes

```bash
M=~/.config/linearfs/metrics.jsonl

# All instrument names in the latest export
tail -1 $M | jq -r '.ScopeMetrics[].Metrics[].Name' | sort

# Budget remaining per axis, latest
tail -1 $M | jq -r '.ScopeMetrics[] | .Metrics[] | select(.Name=="linearfs.budget.remaining")
  | .Data.DataPoints[] | "\(.Attributes[0].Value.Value): \(.Value)"'

# API spend by op (request counts), latest
tail -1 $M | jq -r '.ScopeMetrics[] | .Metrics[] | select(.Name=="linearfs.api.requests")
  | .Data.DataPoints[] | [(.Attributes[] | select(.Key=="op").Value.Value), .Value] | @tsv' | sort -k2 -rn

# SWR trigger decisions over the whole file (last value = cumulative total)
jq -r '.ScopeMetrics[] | .Metrics[] | select(.Name=="linearfs.swr.triggers")
  | .Data.DataPoints[] | [(.Attributes | map("\(.Key)=\(.Value.Value)") | join(",")), .Value] | @tsv' $M | tail -8

# Budget-remaining time series (one line per export) — spot a leak's slope
jq -r '[(.ScopeMetrics[].Metrics[] | select(.Name=="linearfs.budget.remaining")
  | .Data.DataPoints[] | select(.Attributes[0].Value.Value=="complexity") | .Value)] | first' $M
```

## Per-request debug log — `telemetry.requests.*`

**A debug log, not an OTEL signal.** The meter pipeline above is untouched by
this (metrics only — traces never, still). When enabled, the api client
appends one JSON line per completed GraphQL request to a separate JSONL file,
written at the same place in `Client.query` where the response settles (the
`apiMetrics` record site). Built for the cold-start observation runs
(`docs/plans/2026-07-09-coldstart-observation-plan.md`): duplicate-fetch
detection and complexity attribution need per-request granularity with full
variables, which no bounded-cardinality metric can carry.

```yaml
telemetry:
  requests:
    enabled: true                   # default false
    path: ~/custom-requests.jsonl   # default: <UserConfigDir>/linearfs/requests.jsonl
```

One line per request:

```json
{"ts":"2026-07-09T02:00:01.123456789Z","op":"TeamIssuesByUpdatedAt",
 "vars":{"teamId":"...","first":50},"duration_ms":312.4,"outcome":"ok","complexity":8231}
```

- `ts` — RFC3339Nano, UTC, at completion.
- `op` — the same `extractOpName` value as the `op` metric attribute.
- `vars` — the request's **full** variables map (ids, cursors), deliberately:
  grouping lines by `(op, vars)` is the duplicate-fetch detector. This is why
  the log is off by default — it can carry entity IDs.
- `duration_ms` — wall time of the request.
- `outcome` — `ok` | `error` | `ratelimited`, the exact classification of
  `linearfs.api.requests` (one shared classifier).
- `complexity` — the response's actual `X-Complexity`, threaded from the
  budget's reconcile (still the ONE place the header is parsed); **omitted**
  when the response carried none.

Semantics match `linearfs.api.requests`: only requests actually sent are
logged — budget deferrals never appear (they land in
`linearfs.budget.decisions`). Disabled costs nothing (nil writer, one
branch); the file reuses the metrics export's rotation writer with a fixed
100 MB cap (disk bounded at ~2×). Sources:
`internal/api/requestlog.go` (entry + log site),
`internal/telemetry/requestlog.go` (writer), wired at client construction in
`internal/fs/linearfs.go`.

### Cold-start observation scripts

`scripts/coldstart-observe.sh` is the unattended runbook that consumes this
log (budget-gated double cold start of the live service; see the plan doc).
It refuses to run without `LINEARFS_COLDSTART_CONFIRM=1` and is
`at`-schedulable:

```bash
echo "LINEARFS_COLDSTART_CONFIRM=1 ~/jra3/linear-fuse/scripts/coldstart-observe.sh" | at 02:00
```

`scripts/coldstart-probe.sh` is its Run B companion: a 15s loop wall-timing
fixed read paths on the mount into a TSV. Both are operator tools for the
live service — never run by CI or any test.

## Adding an instrument (the phase-2/3 pattern)

1. Define it in the layer's `metrics.go` on a small struct, created **once**
   at the owner's construction via `otel.Meter("linearfs/<layer>")` and the
   `telemetry.Must*` helpers — never per call.
2. Attributes must be closed enums or bounded-by-construction sets. Entity
   IDs and free strings never become attributes (log them instead).
3. If a new attribute key should appear in the journald summary, add it to
   `summaryAttrKeys` — otherwise its series merge in the summary (and remain
   distinct in the JSONL).
4. Observable gauges read state via a snapshot method under the owner's
   existing lock; skip observations you'd otherwise fabricate.
5. Test with `sdkmetric.NewManualReader` + hand-rolled `metricdata`
   assertions (stdlib-only; no `metricdatatest`/testify). Pin the global
   provider to no-op in `TestMain` if the package's tests create instruments.

## What was deleted

`APIStats` (`internal/api/stats.go`, ~300 lines: per-op count/latency/errors,
rolling hourly window, `rateLimitWaitNs`, 5-minute logger) — subsumed
entirely. Its journald role is the summary rendering; its rolling-window
budget estimate was replaced by `Client.BudgetSnapshot()` (server-truth
`limit − remaining` on the requests axis, which also counts other consumers
of the same API key). `config.Log.APIStats` was removed; old config files
carrying the key still parse (unknown YAML keys are ignored, pinned by test).
