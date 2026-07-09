# Cold-start observation — test plan (grilling-locked)

Locked with John 2026-07-09. Goal: observe a fresh-process, NO-cache-db cold
start and answer four questions: (1) user-visible latency, (2) bottlenecks —
what do we wait on, (3) where API complexity is consumed, (4) wasted or
duplicate fetches.

## Locked decisions

- **Subject: the real service itself.** Stop `linearfs.service`, move
  `~/.config/linearfs/cache.db` aside (the abort/restore path), start — a true
  cold start under systemd with production config, telemetry, and mount path.
  On success no restore: the new db is a warm cache by the end. Scratch-mount
  variants rejected (same budget cost, less representative; running alongside
  the live service is the recorded round-10 mistake).
- **Timing: off-hours, budget-gated, unattended.** Confirmed live: complexity
  limit 3,000,000/hr; a historical full cold sync can consume a whole window.
  The runbook self-gates: start only when remaining ≥ ~2.5M (read from
  metrics.jsonl or response headers). Abort criterion: remaining hits the
  write-tier reserve floor before sync settles → stop service, restore backup
  db, record where it died (that datapoint is itself a bottleneck finding).
  Everything must come from recorded artifacts — nobody is watching.
- **Two runs, consecutive hourly windows** (~2.5h total, one night):
  - **Run A (pure)**: unattended cold start until sync settles. Clean baseline
    for phase timing, complexity attribution, time-to-warm.
  - **Run B (probed)**: wipe again after the window resets and budget
    recovers; cold start with a scripted probe loop measuring user-visible
    latency under sync pressure (SWR/promotion behavior). Kept separate so
    probe-induced fetches don't contaminate Run A's attribution.
- **Capture: request-timeline instrument built FIRST** (chosen over
  debug-log-only capture): a per-request JSONL log — `{ts, op, vars,
  duration_ms, status/outcome, complexity}` — config-gated
  (`telemetry.requests.{enabled, path}`, off by default), written by the api
  client at the one place responses settle. This is an application debug log,
  not an OTEL signal — the metrics-only/traces-never policy is untouched.
  Full vars (ids/cursors) are logged deliberately: duplicate-fetch detection
  needs to see WHICH entity was fetched twice. Complexity comes from the
  X-Complexity header the budget already reads.
  Plus for the run window: `telemetry.file.interval` dropped to 10s (restored
  to 60s after), debug logging on (journald carries `[sync]` phase lines).

## Runbook (scripts/coldstart-observe.sh, `at`-schedulable)

1. Preflight: assert service healthy; read remaining/reset from telemetry;
   sleep until reset + remaining ≥ 2.5M.
2. Snapshot: journal cursor, copy current metrics.jsonl aside, `mv cache.db
   cache.db.pre-coldstart`.
3. Flip config: telemetry interval 10s, request log on (config edits are
   scripted + reverted in cleanup).
4. `systemctl --user restart linearfs.service`; record T0; poll mount-ready
   (first successful `ls` of the root) and record.
5. Run A: wait until "settled" — pending_depth == 0 AND two consecutive sync
   cycles with no detail outcomes AND catch-up mode off (all visible in
   metrics.jsonl / journal); record T_settled. Collect artifacts.
6. Gate again on the next window (remaining recovered); repeat 2–4, then
   Run B: probe loop until settled — every 15s, wall-time each of:
   `ls teams/`, `ls teams/ENG/issues | head`, `cat` one fixed issue.md,
   `ls` that issue's `comments/`, `cat` a comment, `ls my/assigned/`,
   `cat teams/ENG/cycles/current/…` — timestamps + durations to a probe log.
   The probe set covers: skeleton reads, detail-triggering reads (SWR), and
   viewer-dependent views.
7. Cleanup: restore telemetry interval + request-log config, restart.
   Artifacts per run: metrics.jsonl slice, requests.jsonl, journald slice
   (from cursor), probe log, timestamps file.

## Analysis (next session, produces docs/plans/…-coldstart-findings.md)

- **Latency**: probe-log percentiles per path class over time-since-T0;
  mount-ready time; time-to-first-team, time-to-warm (T_settled − T0).
- **Bottlenecks**: journal phase lines + requests.jsonl waterfall (call
  ordering, gaps = what we waited on); budget.decisions{decision=defer|wait}
  and wait_duration during the run; pending_depth drain curve.
- **Complexity attribution**: api.complexity histogram deltas per op +
  requests.jsonl sum by op — ranked table of where the 3M goes.
- **Wasted fetches**: requests.jsonl grouped by (op, vars) — any group with
  count > 1 inside the run is a candidate duplicate (worker vs SWR races,
  re-fetches of unchanged data); swr.triggers during catch-up (should the
  worker's own churn be triggering SWR?); detail_outcomes deferred→retried
  churn; per-cycle re-fetch of workspace/team metadata that didn't change.

## Pre-work (one PR before the runs)

`feat/request-timeline-log`: the requests.jsonl instrument + the runbook
script + probe script. Off-by-default; zero cost when disabled.
