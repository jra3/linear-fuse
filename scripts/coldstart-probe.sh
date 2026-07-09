#!/usr/bin/env bash
# coldstart-probe.sh — the Run B probe loop of the cold-start observation
# plan (docs/plans/2026-07-09-coldstart-observation-plan.md, runbook step 6).
#
# Every PROBE_INTERVAL (15s) it wall-times a fixed set of filesystem paths —
# skeleton reads, detail-triggering reads (SWR), and viewer-dependent views —
# and appends one TSV row per probe to PROBE_LOG:
#
#   epoch_ns <TAB> iso_ts <TAB> probe <TAB> duration_ms <TAB> exit_status
#
# Failures are data, not errors: a probe that fails (path not yet synced)
# records its non-zero exit status and the loop continues.
#
# Usage (normally invoked by coldstart-observe.sh for Run B):
#   PROBE_LOG=/tmp/probe.tsv scripts/coldstart-probe.sh
#
# Stops when STOP_FILE appears (the runbook's settled signal) or on SIGTERM.
#
# Environment:
#   LINEARFS_MOUNT       mount point       (default: from ~/.config/linearfs/env, else ~/am/linear)
#   PROBE_LOG            output TSV        (default: ./coldstart-probe.tsv)
#   STOP_FILE            stop sentinel     (default: $PROBE_LOG.stop)
#   PROBE_INTERVAL       seconds per round (default: 15)
#   LINEARFS_PROBE_TEAM  team key          (default: ENG)
#   LINEARFS_PROBE_ISSUE fixed issue ID    (default: auto-pick first issue seen)
#
# This script is an operator tool for a live mount. It is never run by CI or
# any test.

set -u

ENV_FILE="${XDG_CONFIG_HOME:-$HOME/.config}/linearfs/env"
if [[ -z "${LINEARFS_MOUNT:-}" && -f "$ENV_FILE" ]]; then
    # shellcheck disable=SC1090
    source "$ENV_FILE"
fi
MOUNT="${LINEARFS_MOUNT:-$HOME/am/linear}"
PROBE_LOG="${PROBE_LOG:-./coldstart-probe.tsv}"
STOP_FILE="${STOP_FILE:-$PROBE_LOG.stop}"
INTERVAL="${PROBE_INTERVAL:-15}"
TEAM="${LINEARFS_PROBE_TEAM:-ENG}"
ISSUE="${LINEARFS_PROBE_ISSUE:-}"

mkdir -p "$(dirname "$PROBE_LOG")"

log_row() { # probe duration_ms status
    printf '%s\t%s\t%s\t%s\t%s\n' \
        "$(date +%s%N)" "$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)" "$1" "$2" "$3" >>"$PROBE_LOG"
}

# probe NAME CMD... — wall-time one command, record TSV, never fail the loop.
probe() {
    local name="$1"
    shift
    local start end rc
    start=$(date +%s%N)
    "$@" >/dev/null 2>&1
    rc=$?
    end=$(date +%s%N)
    log_row "$name" "$(((end - start) / 1000000))" "$rc"
}

# cat of the first comment file under the fixed issue, if any exists yet.
cat_first_comment() {
    local dir="$MOUNT/teams/$TEAM/issues/$ISSUE/comments"
    local first
    first=$(/bin/ls "$dir" 2>/dev/null | grep '\.md$' | head -1) || true
    [[ -n "$first" ]] && cat "$dir/$first"
}

echo "# coldstart-probe start mount=$MOUNT team=$TEAM interval=${INTERVAL}s" >>"$PROBE_LOG"
trap 'echo "# coldstart-probe stop $(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$PROBE_LOG"; exit 0' TERM INT

while [[ ! -e "$STOP_FILE" ]]; do
    # Fixed-issue selection: auto-pick the first issue the mount shows and
    # then keep probing THAT issue every round (a stable detail-read target).
    if [[ -z "$ISSUE" ]]; then
        ISSUE=$(/bin/ls "$MOUNT/teams/$TEAM/issues" 2>/dev/null | grep -v '^_' | head -1) || true
        [[ -n "$ISSUE" ]] && echo "# probe issue selected: $ISSUE" >>"$PROBE_LOG"
    fi

    probe ls_teams        /bin/ls "$MOUNT/teams/"
    probe ls_team_issues  /bin/ls "$MOUNT/teams/$TEAM/issues"
    if [[ -n "$ISSUE" ]]; then
        probe cat_issue    cat "$MOUNT/teams/$TEAM/issues/$ISSUE/issue.md"
        probe ls_comments  /bin/ls "$MOUNT/teams/$TEAM/issues/$ISSUE/comments/"
        probe cat_comment  cat_first_comment
    fi
    probe ls_my_assigned  /bin/ls "$MOUNT/my/assigned/"
    probe ls_cycle_current /bin/ls "$MOUNT/teams/$TEAM/cycles/current/"

    sleep "$INTERVAL"
done
echo "# coldstart-probe stopped by $STOP_FILE" >>"$PROBE_LOG"
