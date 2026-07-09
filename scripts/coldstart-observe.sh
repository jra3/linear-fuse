#!/usr/bin/env bash
# coldstart-observe.sh — unattended cold-start observation runbook for the
# LIVE linearfs service. Implements docs/plans/2026-07-09-coldstart-observation-plan.md:
#
#   Run A (pure):   budget-gate, snapshot, wipe cache.db, restart, record T0,
#                   wait until sync settles. Clean baseline for phase timing
#                   and complexity attribution.
#   Run B (probed): gate on the next budget window, wipe again, restart, and
#                   run scripts/coldstart-probe.sh alongside until settled —
#                   user-visible latency under sync pressure.
#
# Artifacts land in a timestamped dir under ~/linearfs-coldstart/ (metrics
# JSONL slices, requests.jsonl, journald slices from a cursor, the probe TSV,
# and timestamps.txt with T0/mount-ready/T_settled per run).
#
# DANGER: this stops the live service, moves the live cache.db aside, and
# edits the live config.yaml (a marked backup is taken; config comments are
# NOT preserved by the flip, which is why the restore is a byte-for-byte copy
# of the backup). It therefore refuses to run unless
#
#   LINEARFS_COLDSTART_CONFIRM=1
#
# is set. Abort-safe: on ERR/INT/TERM the config is restored, and if the
# cold sync had not settled the pre-run cache.db is moved back. On success
# the new (warm) db is kept and the backup left on disk as
# cache.db.pre-coldstart.
#
# Scheduling example (the intended off-hours use):
#   echo "LINEARFS_COLDSTART_CONFIRM=1 ~/jra3/linear-fuse/scripts/coldstart-observe.sh" | at 02:00
#
# Environment (all optional beyond the confirm gate):
#   LINEARFS_MOUNT                 mount point (default: from ~/.config/linearfs/env, else ~/am/linear)
#   LINEARFS_COLDSTART_DIR         artifact root      (default: ~/linearfs-coldstart)
#   LINEARFS_COLDSTART_BUDGET_MIN  complexity floor to start a run   (default: 2500000)
#   LINEARFS_COLDSTART_ABORT_FLOOR remaining at which a run aborts   (default: 50000 — write-tier territory)
#   LINEARFS_COLDSTART_MAX_WAIT    max seconds to wait on a budget gate (default: 10800)
#   LINEARFS_COLDSTART_SETTLE_TIMEOUT  max seconds for one run to settle (default: 3600)
#   LINEARFS_PROBE_TEAM / LINEARFS_PROBE_ISSUE  passed through to the probe
#
# Requires: jq, systemctl --user, journalctl, and python3 with PyYAML (or
# mikefarah yq) for the config flip. Never run by CI or any test.

set -euo pipefail

log() { printf '[coldstart] %s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() {
    log "FATAL: $*"
    exit 1
}

# ---------------------------------------------------------------- footgun gate
[[ "${LINEARFS_COLDSTART_CONFIRM:-}" == "1" ]] ||
    die "refusing to run: this manipulates the live service and cache.db. Set LINEARFS_COLDSTART_CONFIRM=1 to proceed."

# ---------------------------------------------------------------------- paths
SERVICE=linearfs.service
CFG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/linearfs"
CONFIG="$CFG_DIR/config.yaml"
CONFIG_BACKUP="$CONFIG.coldstart-backup"
DB="$CFG_DIR/cache.db"
DB_BACKUP="$DB.pre-coldstart"
METRICS="$CFG_DIR/metrics.jsonl"
REQUESTS="$CFG_DIR/requests.jsonl"

ENV_FILE="$CFG_DIR/env"
if [[ -z "${LINEARFS_MOUNT:-}" && -f "$ENV_FILE" ]]; then
    # shellcheck disable=SC1090
    source "$ENV_FILE"
fi
MOUNT="${LINEARFS_MOUNT:-$HOME/am/linear}"

BUDGET_MIN="${LINEARFS_COLDSTART_BUDGET_MIN:-2500000}"
ABORT_FLOOR="${LINEARFS_COLDSTART_ABORT_FLOOR:-50000}"
MAX_GATE_WAIT="${LINEARFS_COLDSTART_MAX_WAIT:-10800}"
SETTLE_TIMEOUT="${LINEARFS_COLDSTART_SETTLE_TIMEOUT:-3600}"
SETTLE_POLL=30    # seconds between settled-checks
DETAIL_QUIET=150  # detail_outcomes must be flat this long (>= two sync cycles)

ART_ROOT="${LINEARFS_COLDSTART_DIR:-$HOME/linearfs-coldstart}"
ART_DIR="$ART_ROOT/$(date +%Y%m%d-%H%M%S)"
TS_FILE="$ART_DIR/timestamps.txt"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# State the cleanup trap consults.
SYNC_SETTLED=1 # 1 = current cache.db is a valid warm cache (nothing to restore)
CLEANED=0

mark() { # key [value…]  → timestamps.txt
    printf '%s=%s\n' "$1" "${2:-$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)}" >>"$TS_FILE"
}

# ------------------------------------------------------------- jq extractors
# Latest-line readers over the metrics JSONL (see docs/telemetry.md).
metric_axis() { # name axis → value of the latest datapoint with that axis
    [[ -f "$METRICS" ]] || return 0
    tail -1 "$METRICS" | jq -r --arg n "$1" --arg ax "$2" '
        [.ScopeMetrics[]?.Metrics[]? | select(.Name==$n) | .Data.DataPoints[]?
         | select(any(.Attributes[]?; .Key=="axis" and .Value.Value==$ax)) | .Value]
        | first // empty' 2>/dev/null || true
}
metric_first() { # name → first datapoint value
    [[ -f "$METRICS" ]] || return 0
    tail -1 "$METRICS" | jq -r --arg n "$1" '
        [.ScopeMetrics[]?.Metrics[]? | select(.Name==$n) | .Data.DataPoints[]?.Value]
        | first // empty' 2>/dev/null || true
}
metric_sum() { # name → sum over datapoints (cumulative counter total)
    [[ -f "$METRICS" ]] || return 0
    tail -1 "$METRICS" | jq -r --arg n "$1" '
        [.ScopeMetrics[]?.Metrics[]? | select(.Name==$n) | .Data.DataPoints[]?.Value]
        | add // 0' 2>/dev/null || true
}

journal_cursor() {
    journalctl --user -u "$SERVICE" -n 0 --show-cursor 2>/dev/null | sed -n 's/^-- cursor: //p'
}

catchup_active() { # cursor → success(0) if the last catch-up line since cursor says enabled
    local last
    last=$(journalctl --user -u "$SERVICE" --after-cursor="$1" --no-pager 2>/dev/null |
        grep -o 'catch-up mode \(enabled\|disabled\)' | tail -1 || true)
    [[ "$last" == "catch-up mode enabled" ]]
}

# ---------------------------------------------------------------- config flip
flip_config() {
    if python3 -c 'import yaml' 2>/dev/null; then
        python3 - "$CONFIG" <<'PY'
import sys, yaml
p = sys.argv[1]
with open(p) as f:
    cfg = yaml.safe_load(f) or {}
t = cfg.setdefault("telemetry", None) or {}
cfg["telemetry"] = t
fl = t.setdefault("file", None) or {}
t["file"] = fl
fl["enabled"] = True
fl["interval"] = "10s"
rq = t.setdefault("requests", None) or {}
t["requests"] = rq
rq["enabled"] = True
with open(p, "w") as f:
    yaml.safe_dump(cfg, f, default_flow_style=False, sort_keys=False)
PY
    elif command -v yq >/dev/null 2>&1; then
        yq eval -i '.telemetry.file.enabled = true
            | .telemetry.file.interval = "10s"
            | .telemetry.requests.enabled = true' "$CONFIG"
    else
        return 1
    fi
}

restore_config() {
    if [[ -f "$CONFIG_BACKUP" ]]; then
        cp -f "$CONFIG_BACKUP" "$CONFIG"
        rm -f "$CONFIG_BACKUP"
        log "config restored from backup"
    fi
}

# --------------------------------------------------------------- db handling
# mv the live db (plus WAL/SHM sidecars, kept consistent with it) aside, and
# verify the backup exists before considering the wipe done.
backup_db() {
    [[ -e "$DB_BACKUP" ]] && die "leftover $DB_BACKUP from a previous run — resolve it manually first"
    if [[ ! -f "$DB" ]]; then
        log "no cache.db at $DB (already cold); nothing to back up"
        return 0
    fi
    mv "$DB" "$DB_BACKUP"
    for side in wal shm; do
        [[ -f "$DB-$side" ]] && mv "$DB-$side" "$DB_BACKUP-$side"
    done
    [[ -f "$DB_BACKUP" && ! -e "$DB" ]] || die "db backup verification failed ($DB_BACKUP missing or $DB still present) — aborting before any further step"
    log "cache.db moved to $DB_BACKUP"
}

restore_db() {
    [[ -f "$DB_BACKUP" ]] || return 0
    rm -f "$DB" "$DB-wal" "$DB-shm"
    mv "$DB_BACKUP" "$DB"
    for side in wal shm; do
        [[ -f "$DB_BACKUP-$side" ]] && mv "$DB_BACKUP-$side" "$DB-$side"
    done
    log "cache.db restored from $DB_BACKUP"
}

# -------------------------------------------------------------------- cleanup
cleanup() {
    local rc=$?
    [[ "$CLEANED" == 1 ]] && exit "$rc"
    CLEANED=1
    trap - ERR EXIT INT TERM
    log "cleanup (exit=$rc, settled=$SYNC_SETTLED)"
    systemctl --user stop "$SERVICE" 2>/dev/null || true
    if [[ "$SYNC_SETTLED" != 1 ]]; then
        restore_db
        mark "aborted_unsettled" "$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"
    fi
    restore_config
    systemctl --user start "$SERVICE" 2>/dev/null || true
    log "service restarted; artifacts (if any) in $ART_DIR"
    exit "$rc"
}
trap cleanup EXIT INT TERM
trap 'log "ERROR at line $LINENO"' ERR

# ------------------------------------------------------------------ preflight
command -v jq >/dev/null || die "jq is required"
command -v journalctl >/dev/null || die "journalctl is required"
[[ -f "$CONFIG" ]] || die "no config at $CONFIG"
[[ -f "$CONFIG_BACKUP" ]] && die "leftover $CONFIG_BACKUP from a previous run — restore or remove it manually first"
python3 -c 'import yaml' 2>/dev/null || command -v yq >/dev/null 2>&1 ||
    die "config flip needs python3+PyYAML or yq"
systemctl --user is-active --quiet "$SERVICE" || die "$SERVICE is not active — start it healthy first"
[[ -f "$METRICS" ]] || die "no $METRICS — enable telemetry.file in config so the budget gate can read it"
if (($(date +%s) - $(stat -c %Y "$METRICS") > 900)); then
    die "$METRICS is stale (>15min old) — is the file export enabled on the live service?"
fi

mkdir -p "$ART_DIR"/{pre,runA,runB}
mark "script_start"
log "artifacts: $ART_DIR"

# wait_budget: block until complexity remaining >= BUDGET_MIN (reading the
# live metrics file), bounded by MAX_GATE_WAIT.
wait_budget() {
    local waited=0 remaining reset
    while :; do
        remaining=$(metric_axis linearfs.budget.remaining complexity)
        if [[ -n "$remaining" ]] && awk -v r="$remaining" -v m="$BUDGET_MIN" 'BEGIN{exit !(r>=m)}'; then
            log "budget gate open: $remaining complexity remaining (>= $BUDGET_MIN)"
            return 0
        fi
        ((waited < MAX_GATE_WAIT)) || die "budget gate timed out after ${waited}s (remaining=${remaining:-unknown})"
        reset=$(metric_axis linearfs.budget.reset_seconds complexity)
        local nap=120
        if [[ -n "$reset" ]]; then
            nap=$(awk -v r="$reset" 'BEGIN{n=int(r)+60; if(n<60)n=60; if(n>900)n=900; print n}')
        fi
        log "budget gate: remaining=${remaining:-unknown} < $BUDGET_MIN; sleeping ${nap}s (reset in ${reset:-unknown}s)"
        sleep "$nap"
        waited=$((waited + nap))
    done
}

# wait_mount: poll until the mount root lists; records nothing itself.
wait_mount() {
    local tries=0
    until /bin/ls "$MOUNT" >/dev/null 2>&1; do
        ((tries++ < 600)) || die "mount at $MOUNT not ready after 120s"
        sleep 0.2
    done
}

# wait_settled <cursor> <run-label>: poll the settled predicate —
# pending_depth == 0 AND detail_outcomes flat for DETAIL_QUIET seconds AND
# catch-up mode off. Aborts the run when complexity remaining falls to the
# ABORT_FLOOR (that datapoint is itself a finding — recorded, then die).
wait_settled() {
    local cursor="$1" label="$2"
    local start now last_total="" last_change pending details remaining
    start=$(date +%s)
    last_change=$start
    while :; do
        sleep "$SETTLE_POLL"
        now=$(date +%s)
        ((now - start < SETTLE_TIMEOUT)) || die "$label: not settled after ${SETTLE_TIMEOUT}s"

        remaining=$(metric_axis linearfs.budget.remaining complexity)
        if [[ -n "$remaining" ]] && awk -v r="$remaining" -v f="$ABORT_FLOOR" 'BEGIN{exit !(r<=f)}'; then
            mark "${label}_abort_budget_remaining" "$remaining"
            mark "${label}_abort_pending_depth" "$(metric_first linearfs.sync.pending_depth)"
            mark "${label}_abort_detail_total" "$(metric_sum linearfs.sync.detail_outcomes)"
            die "$label: complexity remaining $remaining hit the abort floor $ABORT_FLOOR before settling (recorded)"
        fi

        pending=$(metric_first linearfs.sync.pending_depth)
        details=$(metric_sum linearfs.sync.detail_outcomes)
        if [[ -z "$last_total" || "$details" != "$last_total" ]]; then
            last_total="$details"
            last_change=$now
        fi
        log "$label: pending_depth=${pending:-?} detail_total=${details:-?} quiet=$((now - last_change))s remaining=${remaining:-?}"

        [[ "$pending" == "0" ]] || continue
        ((now - last_change >= DETAIL_QUIET)) || continue
        catchup_active "$cursor" && continue
        mark "${label}_settled"
        log "$label: settled"
        return 0
    done
}

collect_run() { # <run-dir> <cursor>
    local dir="$1" cursor="$2"
    cp -f "$METRICS" "$dir/metrics.jsonl" 2>/dev/null || true
    cp -f "$REQUESTS" "$dir/requests.jsonl" 2>/dev/null || true
    journalctl --user -u "$SERVICE" --after-cursor="$cursor" --no-pager \
        -o short-iso-precise >"$dir/journal.log" 2>/dev/null || true
}

# ================================================================== RUN A
log "preflight: waiting on the budget gate for Run A"
wait_budget
mark "runA_budget_remaining" "$(metric_axis linearfs.budget.remaining complexity)"

log "Run A snapshot: stopping $SERVICE"
systemctl --user stop "$SERVICE"
CURSOR_A=$(journal_cursor)
mark "runA_journal_cursor" "$CURSOR_A"
# Pre-run telemetry moves aside so the run's files carry ONLY run lines.
[[ -f "$METRICS" ]] && mv "$METRICS" "$ART_DIR/pre/metrics.jsonl"
[[ -f "$REQUESTS" ]] && mv "$REQUESTS" "$ART_DIR/pre/requests.jsonl"
backup_db
SYNC_SETTLED=0

log "flipping config (backup at $CONFIG_BACKUP): telemetry interval 10s + request log on"
cp -f "$CONFIG" "$CONFIG_BACKUP"
flip_config || die "config flip failed"

log "starting $SERVICE (Run A cold start)"
systemctl --user start "$SERVICE"
mark "runA_T0"
wait_mount
mark "runA_mount_ready"
log "Run A: mount ready; waiting for sync to settle"

wait_settled "$CURSOR_A" runA
SYNC_SETTLED=1
collect_run "$ART_DIR/runA" "$CURSOR_A"
log "Run A artifacts collected"

# ================================================================== RUN B
log "gating Run B on the next budget window"
RESET_NOW=$(metric_axis linearfs.budget.reset_seconds complexity)
if [[ -n "$RESET_NOW" ]]; then
    NAP=$(awk -v r="$RESET_NOW" 'BEGIN{n=int(r)+120; if(n<60)n=60; print n}')
    log "sleeping ${NAP}s for the window reset"
    sleep "$NAP"
fi
wait_budget
mark "runB_budget_remaining" "$(metric_axis linearfs.budget.remaining complexity)"

log "Run B snapshot: stopping $SERVICE"
systemctl --user stop "$SERVICE"
# Final (complete) Run A telemetry replaces the settle-time copies.
[[ -f "$METRICS" ]] && mv "$METRICS" "$ART_DIR/runA/metrics.jsonl"
[[ -f "$REQUESTS" ]] && mv "$REQUESTS" "$ART_DIR/runA/requests.jsonl"
# Run A's warm db is an artifact; the pre-coldstart backup stays the restore point.
[[ -f "$DB" ]] && mv "$DB" "$ART_DIR/runA/cache.db"
rm -f "$DB-wal" "$DB-shm"
CURSOR_B=$(journal_cursor)
mark "runB_journal_cursor" "$CURSOR_B"
SYNC_SETTLED=0

log "starting $SERVICE (Run B cold start, probed)"
systemctl --user start "$SERVICE"
mark "runB_T0"
wait_mount
mark "runB_mount_ready"

PROBE_LOG="$ART_DIR/runB/probe.tsv"
STOP_FILE="$PROBE_LOG.stop"
log "Run B: launching probe loop ($PROBE_LOG)"
PROBE_LOG="$PROBE_LOG" STOP_FILE="$STOP_FILE" LINEARFS_MOUNT="$MOUNT" \
    "$SCRIPT_DIR/coldstart-probe.sh" &
PROBE_PID=$!

wait_settled "$CURSOR_B" runB
SYNC_SETTLED=1

touch "$STOP_FILE"
wait "$PROBE_PID" 2>/dev/null || true
rm -f "$STOP_FILE"
collect_run "$ART_DIR/runB" "$CURSOR_B"
log "Run B artifacts collected"

# ================================================================ CLEANUP
log "restoring config and restarting with the warm cache"
systemctl --user stop "$SERVICE"
# Final Run B telemetry into the artifact dir; the restored config re-creates
# metrics.jsonl at the normal cadence.
[[ -f "$METRICS" ]] && mv "$METRICS" "$ART_DIR/runB/metrics.jsonl"
[[ -f "$REQUESTS" ]] && mv "$REQUESTS" "$ART_DIR/runB/requests.jsonl"
restore_config
systemctl --user start "$SERVICE"
mark "script_end"
log "done. warm db kept; pre-run db left at $DB_BACKUP; artifacts in $ART_DIR"
