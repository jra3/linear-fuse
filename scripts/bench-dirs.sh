#!/usr/bin/env bash
set -euo pipefail

# Directory Latency Benchmark for LinearFS
# Measures cold (cache miss) and warm (cache hit) ls latency for each directory type
# Unmounts/remounts between each test case for true cold cache isolation

MOUNT_POINT="/tmp/linear"
BINARY="./bin/linearfs"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

FUSE_PID=""

# Mount the filesystem
do_mount() {
    if mountpoint -q "$MOUNT_POINT" 2>/dev/null; then
        return 0
    fi

    $BINARY mount "$MOUNT_POINT" &
    FUSE_PID=$!

    # Wait for mount to be ready
    local timeout=30
    while ! mountpoint -q "$MOUNT_POINT" 2>/dev/null; do
        sleep 0.2
        timeout=$((timeout - 1))
        if [[ $timeout -le 0 ]]; then
            echo -e "${RED}Error: Timed out waiting for mount${NC}"
            exit 1
        fi
    done
}

# Unmount the filesystem
do_unmount() {
    if mountpoint -q "$MOUNT_POINT" 2>/dev/null; then
        fusermount3 -u "$MOUNT_POINT" 2>/dev/null || true
    fi
    if [[ -n "${FUSE_PID:-}" ]]; then
        wait "$FUSE_PID" 2>/dev/null || true
        FUSE_PID=""
    fi
    # Brief pause to ensure clean unmount
    sleep 0.3
}

# Cleanup function
cleanup() {
    echo ""
    echo -e "${YELLOW}Cleaning up...${NC}"
    do_unmount
    echo -e "${GREEN}Done.${NC}"
}

trap cleanup EXIT

# Time a command in milliseconds
time_ms() {
    local start end
    start=$(date +%s%N)
    "$@" >/dev/null 2>&1 || true
    end=$(date +%s%N)
    echo $(( (end - start) / 1000000 ))
}

# Check prerequisites
if [[ -z "${LINEAR_API_KEY:-}" ]]; then
    echo -e "${RED}Error: LINEAR_API_KEY environment variable not set${NC}"
    exit 1
fi

cd "$PROJECT_DIR"

# Build if needed
if [[ ! -x "$BINARY" ]]; then
    echo -e "${YELLOW}Building linearfs...${NC}"
    make build
fi

# Create mount point
mkdir -p "$MOUNT_POINT"

# Unmount if already mounted
do_unmount

# ============================================================================
# Phase 1: Discovery (single mount to discover all paths)
# ============================================================================
echo -e "${YELLOW}Phase 1: Discovering paths...${NC}"
do_mount

TEAM=$(ls "$MOUNT_POINT/teams/" 2>/dev/null | head -1 || echo "")
if [[ -z "$TEAM" ]]; then
    echo -e "${RED}Error: No teams found${NC}"
    exit 1
fi
echo "  Team: $TEAM"

ISSUE=$(ls "$MOUNT_POINT/teams/$TEAM/issues/" 2>/dev/null | head -1 || echo "")
echo "  Issue: ${ISSUE:-<none>}"

PROJECT=$(ls "$MOUNT_POINT/teams/$TEAM/projects/" 2>/dev/null | head -1 || echo "")
echo "  Project: ${PROJECT:-<none>}"

CYCLE=$(ls "$MOUNT_POINT/teams/$TEAM/cycles/" 2>/dev/null | grep -v current | head -1 || echo "")
echo "  Cycle: ${CYCLE:-<none>}"

USER_DIR=$(ls "$MOUNT_POINT/users/" 2>/dev/null | head -1 || echo "")
echo "  User: ${USER_DIR:-<none>}"

STATUS=$(ls "$MOUNT_POINT/teams/$TEAM/by/status/" 2>/dev/null | head -1 || echo "")
echo "  Status: ${STATUS:-<none>}"

LABEL=$(ls "$MOUNT_POINT/teams/$TEAM/by/label/" 2>/dev/null | head -1 || echo "")
echo "  Label: ${LABEL:-<none>}"

ASSIGNEE=$(ls "$MOUNT_POINT/teams/$TEAM/by/assignee/" 2>/dev/null | head -1 || echo "")
echo "  Assignee: ${ASSIGNEE:-<none>}"

INITIATIVE=$(ls "$MOUNT_POINT/initiatives/" 2>/dev/null | head -1 || echo "")
echo "  Initiative: ${INITIATIVE:-<none>}"

# Unmount after discovery
do_unmount
echo ""

# ============================================================================
# Phase 2: Build directory list
# ============================================================================
declare -a DIRS=(
    "/"
    "/teams/"
    "/teams/$TEAM/"
    "/teams/$TEAM/issues/"
    "/teams/$TEAM/cycles/"
    "/teams/$TEAM/projects/"
    "/teams/$TEAM/labels/"
    "/teams/$TEAM/by/"
    "/teams/$TEAM/by/status/"
    "/teams/$TEAM/by/label/"
    "/teams/$TEAM/by/assignee/"
    "/users/"
    "/my/"
    "/my/assigned/"
    "/my/created/"
    "/my/active/"
    "/initiatives/"
)

# Add paths that depend on discovered entities
[[ -n "$ISSUE" ]] && DIRS+=(
    "/teams/$TEAM/issues/$ISSUE/"
    "/teams/$TEAM/issues/$ISSUE/comments/"
    "/teams/$TEAM/issues/$ISSUE/docs/"
)
[[ -n "$STATUS" ]] && DIRS+=("/teams/$TEAM/by/status/$STATUS/")
[[ -n "$LABEL" ]] && DIRS+=("/teams/$TEAM/by/label/$LABEL/")
[[ -n "$ASSIGNEE" ]] && DIRS+=("/teams/$TEAM/by/assignee/$ASSIGNEE/")
[[ -n "$USER_DIR" ]] && DIRS+=("/users/$USER_DIR/")
[[ -n "$PROJECT" ]] && DIRS+=("/teams/$TEAM/projects/$PROJECT/")
[[ -n "$CYCLE" ]] && DIRS+=("/teams/$TEAM/cycles/$CYCLE/")
[[ -n "$INITIATIVE" ]] && DIRS+=(
    "/initiatives/$INITIATIVE/"
    "/initiatives/$INITIATIVE/projects/"
)

# ============================================================================
# Phase 3: Run benchmarks (remount for each directory)
# ============================================================================
echo -e "${GREEN}Phase 2: Running benchmarks (${#DIRS[@]} directories)...${NC}"
echo -e "${CYAN}Note: Remounting between each test for cold cache isolation${NC}"
echo ""

# Arrays to store results
declare -a RESULTS_DIR=()
declare -a RESULTS_COLD=()
declare -a RESULTS_WARM=()

count=0
total=${#DIRS[@]}

for dir in "${DIRS[@]}"; do
    count=$((count + 1))
    echo -ne "\r${CYAN}Progress: $count/$total - $dir${NC}$(printf '%*s' 20 '')\r" >&2

    # Mount fresh
    do_mount

    full_path="${MOUNT_POINT}${dir}"
    if [[ -d "$full_path" ]]; then
        # Cold read (fresh mount, no cache)
        cold=$(time_ms ls "$full_path")
        # Warm read (cache populated)
        warm=$(time_ms ls "$full_path")
        RESULTS_DIR+=("$dir")
        RESULTS_COLD+=("$cold")
        RESULTS_WARM+=("$warm")
    else
        RESULTS_DIR+=("$dir")
        RESULTS_COLD+=("SKIP")
        RESULTS_WARM+=("SKIP")
    fi

    # Unmount to clear cache for next test
    do_unmount
done

echo -ne "\r$(printf '%*s' 80 '')\r" >&2  # Clear progress line

# ============================================================================
# Phase 4: Print results table
# ============================================================================
echo ""
echo -e "${GREEN}Results:${NC}"
echo ""
printf "%-55s %10s %10s\n" "Directory" "Cold (ms)" "Warm (ms)"
printf "%-55s %10s %10s\n" "$(printf '%0.s-' {1..55})" "----------" "----------"

for i in "${!RESULTS_DIR[@]}"; do
    printf "%-55s %10s %10s\n" "${RESULTS_DIR[$i]}" "${RESULTS_COLD[$i]}" "${RESULTS_WARM[$i]}"
done

echo ""
echo -e "${GREEN}Benchmark complete.${NC}"
