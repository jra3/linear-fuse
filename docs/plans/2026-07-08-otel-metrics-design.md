# OTEL metrics instrumentation — design (grilling-locked)

Planned 2026-07-08 with John. Tracking: GitHub issue on jra3/linear-fuse (this
doc is the spec). Motivation: round 18's rate-budget leak was diagnosed via
journal archaeology + sqlite forensics; the budget/API layer should be
observable directly. The rateBudget module already holds exactly the state
worth exporting (two axes × limit/remaining/reset, in-flight reservations,
per-op cost predictions, ladder decisions) — today visible only as log lines.

## Locked decisions

- **Metrics only — traces never (YAGNI).** No tracer, no span design, no
  context-propagation work. Revisit only if something concrete demands traces.
- **Export: stdout/file, backend deferred.** No collector/LGTM stack now. The
  exporter stays config-pluggable so an OTLP endpoint can drop in later with
  zero instrumentation change. The value today: machine-readable JSONL an
  agent can jq during diagnosis.
- **Default state: meter + journald summary ON; file export OPT-IN.** The
  in-memory meter always runs (negligible at these rates) and a 5-minute
  human-readable summary line always goes to journald — preserving exactly
  today's default observability (APIStats' always-on log) with one data
  source. The JSONL file exporter is config-gated
  (`telemetry: {file: {enabled, path, interval, max_size_mb}}`; default path
  under the state dir, size-capped self-rotation). Rejected: on-by-default
  file (user wants zero disk footprint by default); fully-dark default (would
  regress the existing journald summary).
- **APIStats (internal/api/stats.go, ~290 lines) is DELETED.** It is a
  hand-rolled metrics SDK duplicating what OTEL instruments own (per-op
  count/latency/errors, rolling window, rate-limit-wait). The 5-minute human
  summary survives as a rendering of the same OTEL data (one source, two
  renderings — JSON for machines, one compact line for journalctl; no drift
  possible). Rejected: keeping both (two systems counting the same events).
- **Layers: the full rate-limit causal chain** — api + budget + sync + SWR.
  FS hot-path ops (per-Read/Lookup) are excluded: volume without diagnostic
  value for this story. The SWR triggers are literally round 18's leak — this
  set shows leak AND leaker.

## Instrument sketch (builder refines; naming `linearfs.<layer>.*`)

- `linearfs.api.requests` counter {op, outcome=ok|error|ratelimited}
- `linearfs.api.duration` histogram {op}; `linearfs.api.complexity` histogram {op} (X-Complexity)
- `linearfs.budget.remaining` / `.limit` / `.inflight` observable gauges {axis=requests|complexity}
- `linearfs.budget.reset_seconds` observable gauge {axis}
- `linearfs.budget.decisions` counter {tier, decision=admit|defer|wait|ratelimited}
- `linearfs.budget.wait_duration` histogram (rate-limit wait — replaces APIStats' rateLimitWaitNs)
- `linearfs.sync.cycle_duration` histogram; `linearfs.sync.detail_outcomes` counter {outcome=synced|deferred}
- `linearfs.sync.prunes` counter {collection}; `linearfs.sync.pending_depth` observable gauge
- `linearfs.swr.triggers` counter {kind, decision=triggered|fresh|deduped|sem_dropped}
- `linearfs.swr.refresh_outcomes` counter {kind, outcome=ok|error|orphaned}

Cardinality is tiny (ops ~30, kinds 6, tiers 5). Gauges read rateBudget /
pending-queue state via snapshot methods under existing locks — no new
mutexes. Dependencies: `go.opentelemetry.io/otel` + `sdk/metric` (+
stdoutmetric or a small custom JSONL exporter with a rotation writer).

## Phasing (small PRs, each independently green)

1. **Plumbing**: meter provider wiring in cmd/config (`telemetry` config
   section), the two renderings (journald summary reader @5min; file JSONL
   reader @interval, gated), rotation writer. No instruments yet beyond a
   heartbeat.
2. **api + budget instruments; APIStats deleted** (the summary rendering
   takes over its journald role in the same PR — no observability gap).
3. **sync + SWR instruments** (detail outcomes hook the syncDetails ledger;
   SWR decisions hook maybeRefreshSWR/triggerBackgroundRefresh).

CONTEXT.md gains a "Telemetry (meter)" entry when phase 1 lands.
