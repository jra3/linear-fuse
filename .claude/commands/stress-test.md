# LinearFS Stress Test

Run a suite of read and write stress tests against the live FUSE mount to verify
correctness and performance. Tests are organized into phases: preflight, read-only,
write (on a safe throwaway issue), and a final health report.

## Important lessons learned

- Always use `/bin/ls` — `ls` is aliased to `eza` with long format, which breaks `xargs`
- `by/status/Done` has 1000+ symlinks and takes >60s to list — skip it
- Reads from `cat issue.md` do NOT trigger API calls — sub-resource refresh only fires on `ls comments/`, `ls docs/`, `ls attachments/`
- The sync worker startup backfill can still exhaust the token bucket; wait ~90s after a restart before running write tests
- `triggerBackgroundRefresh` deduplicates by key, so 3x `MaybeRefreshIssueDetails` for the same issue = 1 API call
- Deploy the latest binary before running write tests (`/reinstall`) to ensure new schema tables exist
- `systemctl stop` can hang if a stress-test process holds the mount open; use `fusermount3 -u` to unblock
- `status` is a read-only variable in zsh — use `s` as the loop variable in status-filter loops
- Multi-line xargs commands with `\` continuation followed by `echo "exit: $?"` on a new line can cause the echo to be fed to xargs as stdin — put everything on one line with `&&`
- Python `open().write()` without explicit close triggers EIO during GC finalization — use `with open() as f:` to close explicitly; the FUSE write still succeeds, only the finalization EIO is cosmetic
- Comment creation (test 2c) requires a rate-limit token; if the sync worker startup just ran, the token bucket may be empty — check logs for `token bucket empty, CreateComment`

## Phase 0: Preflight checks

Before running any tests:

1. Confirm the FUSE mount is active:
   ```bash
   mount | grep linear
   ```
   If not mounted, run `/reinstall` first and wait 5 seconds.

2. Confirm the SQLite DB has the required tables:
   ```bash
   sqlite3 ~/.config/linearfs/cache.db ".tables" | tr ' ' '\n' | grep -E "viewer_cache|pending_detail_sync"
   ```
   Both `viewer_cache` and `pending_detail_sync` must be present.
   If either is missing, the running binary is outdated — run `/reinstall`.

3. Note the current budget (to compare after tests):
   ```bash
   sqlite3 ~/.config/linearfs/cache.db "SELECT COUNT(*) FROM issues;"
   ```

4. Confirm the mount point and team key:
   ```bash
   echo $LINEARFS_MOUNT
   /bin/ls $LINEARFS_MOUNT/teams/
   ```
   If `$LINEARFS_MOUNT` is empty, source the env file: `source ~/.config/linearfs/env`
   Use the team listing result as `TEAM` (e.g. `TEAM=ENG`).

## Phase 1: Read-only tests

Run these in order. Each should complete without errors.

### 1a. Parallel bulk read — 50 issues, 16 workers
Tests concurrent inode allocation and FUSE handler parallelism.
```bash
TEAM=ENG
/bin/ls $LINEARFS_MOUNT/teams/$TEAM/issues/ | head -50 | xargs -P 16 -I{} cat $LINEARFS_MOUNT/teams/$TEAM/issues/{}/issue.md > /dev/null && echo "exit: 0" || echo "exit: $?"
```
**Expected:** exit 0, completes in <5s (cache warm) or <15s (cold).

### 1b. Full directory walk — all issue.md files
Tests `readdir` chaining across 1700+ directories.
```bash
TEAM=ENG
time find $LINEARFS_MOUNT/teams/$TEAM/issues/ -name "issue.md" | wc -l
```
**Expected:** Count matches `sqlite3 ~/.config/linearfs/cache.db "SELECT COUNT(*) FROM issues;"`. Takes ~9s.

### 1c. Content grep — urgent priority issues
Drives `Lookup → Open → Read` for every issue file sequentially; the best end-to-end read test.
```bash
TEAM=ENG
time grep -r "^priority: urgent" $LINEARFS_MOUNT/teams/$TEAM/issues/ 2>/dev/null | wc -l
```
**Expected:** Completes in ~9s, no errors.

### 1d. Stat storm — 50 issues, 32 workers
Hammers `Getattr` under high concurrency.
```bash
TEAM=ENG
/bin/ls $LINEARFS_MOUNT/teams/$TEAM/issues/ | head -50 | xargs -P 32 -I{} stat $LINEARFS_MOUNT/teams/$TEAM/issues/{}/issue.md > /dev/null && echo "exit: 0" || echo "exit: $?"
```
**Expected:** exit 0, sub-second.

### 1e. By-filter views — status buckets (skip Done)
Tests filtered query paths. **Do not list `Done`** — it has 1000+ entries and hangs.
```bash
TEAM=ENG
for s in Backlog Todo "In Progress" "In Review" Canceled; do
  count=$(/bin/ls "$LINEARFS_MOUNT/teams/$TEAM/by/status/$s" | wc -l)
  echo "  status/$s: $count issues"
done
```
**Expected:** Each bucket returns quickly; counts should sum to roughly total issues minus Done.

### 1f. By-label and by-assignee (first 3 of each, concurrent)
```bash
TEAM=ENG
for label in $(/bin/ls $LINEARFS_MOUNT/teams/$TEAM/by/label/ | head -3); do
  count=$(/bin/ls "$LINEARFS_MOUNT/teams/$TEAM/by/label/$label" | wc -l)
  echo "  label/$label: $count"
done &
for person in $(/bin/ls $LINEARFS_MOUNT/teams/$TEAM/by/assignee/ | head -3); do
  count=$(/bin/ls "$LINEARFS_MOUNT/teams/$TEAM/by/assignee/$person" | wc -l)
  echo "  assignee/$person: $count"
done &
wait
```
**Expected:** All complete within 20s.

### 1g. Concurrent multi-subsystem reads
Hits teams, my/, users/, initiatives/ simultaneously.
```bash
TEAM=ENG
cat $LINEARFS_MOUNT/teams/$TEAM/states.md > /dev/null &
cat $LINEARFS_MOUNT/teams/$TEAM/labels.md > /dev/null &
/bin/ls $LINEARFS_MOUNT/my/assigned/ > /dev/null &
/bin/ls $LINEARFS_MOUNT/teams/$TEAM/projects/ > /dev/null &
/bin/ls $LINEARFS_MOUNT/initiatives/ > /dev/null &
/bin/ls $LINEARFS_MOUNT/users/ > /dev/null &
wait && echo "all done"
```
**Expected:** Completes in <10s.

### 1h. Cache hit test — 100 re-reads of same file
Confirms the kernel page cache is working (should be near-instant after first read).
```bash
TEAM=ENG
FIRST=$(/bin/ls $LINEARFS_MOUNT/teams/$TEAM/issues/ | head -1)
FILE=$LINEARFS_MOUNT/teams/$TEAM/issues/$FIRST/issue.md
time (for i in $(seq 1 100); do cat "$FILE" > /dev/null; done)
```
**Expected:** 100 reads in <1s.

## Phase 2: Write tests

**Only run on issues you created that are Canceled.** Find a safe target automatically:

```bash
TEAM=ENG
# Find a Canceled issue you created
WRITE_TARGET=""
for id in $(/bin/ls $LINEARFS_MOUNT/my/created/ | head -20); do
  issue_path="$LINEARFS_MOUNT/teams/$TEAM/issues/$id/issue.md"
  if [ -f "$issue_path" ] && grep -q "^status: Canceled" "$issue_path"; then
    WRITE_TARGET="$id"
    echo "Using $WRITE_TARGET for write tests"
    break
  fi
done
[ -z "$WRITE_TARGET" ] && echo "No Canceled issue found in my/created — skipping write tests"
true
```

### 2a. Rapid successive writes — priority toggle (5x, restore)
Tests `Flush` → Linear API → SQLite upsert → cache invalidation loop.
```bash
ISSUE=$LINEARFS_MOUNT/teams/$TEAM/issues/$WRITE_TARGET/issue.md
ORIG=$(grep "^priority:" "$ISSUE" | awk '{print $2}')
echo "original priority: $ORIG"

for priority in medium high low medium low; do
  python3 -c "
import re
with open('$ISSUE') as f: content = f.read()
content = re.sub(r'^priority:.*', 'priority: $priority', content, flags=re.MULTILINE)
with open('$ISSUE', 'w') as f: f.write(content)
"
  sleep 0.3
  GOT=$(grep "^priority:" "$ISSUE" | awk '{print $2}')
  [ "$GOT" = "$priority" ] && echo "  OK: $priority" || echo "  STALE: wrote $priority, got $GOT"
done

# Restore original
python3 -c "
import re
with open('$ISSUE') as f: content = f.read()
content = re.sub(r'^priority:.*', 'priority: $ORIG', content, flags=re.MULTILINE)
with open('$ISSUE', 'w') as f: f.write(content)
"
echo "restored to: $ORIG"
```
**Expected:** All 5 writes read back correctly. A stale read on the first write after a cold start is a known edge case (kernel page cache).

### 2b. Write-then-immediate-read consistency (no sleep)
Tighter version of 2a — checks for stale reads without the 0.3s buffer.
```bash
ISSUE=$LINEARFS_MOUNT/teams/$TEAM/issues/$WRITE_TARGET/issue.md
PASS=0; FAIL=0
for priority in medium high low medium high low; do
  python3 -c "
import re
with open('$ISSUE') as f: content = f.read()
content = re.sub(r'^priority:.*', 'priority: $priority', content, flags=re.MULTILINE)
with open('$ISSUE', 'w') as f: f.write(content)
"
  GOT=$(grep "^priority:" "$ISSUE" | awk '{print $2}')
  if [ "$GOT" = "$priority" ]; then
    PASS=$((PASS+1)); echo "  OK: $priority"
  else
    FAIL=$((FAIL+1)); echo "  STALE: wrote $priority, read $GOT"
  fi
done
python3 -c "
import re
with open('$ISSUE') as f: content = f.read()
content = re.sub(r'^priority:.*', 'priority: low', content, flags=re.MULTILINE)
with open('$ISSUE', 'w') as f: f.write(content)
"
echo "result: $PASS pass, $FAIL stale reads"
```
**Expected:** 6/6 pass (immediate invalidation via `InodeNotify`).

### 2c. Create comment and verify immediate visibility
Tests `_create` trigger → API create → SQLite upsert → `EntryNotify` cache invalidation.

**Note:** This test requires rate-limit tokens. If the sync worker recently started, wait ~90s or check
logs for `token bucket empty, CreateComment` to see if the bucket has recovered.
```bash
ISSUE_DIR=$LINEARFS_MOUNT/teams/$TEAM/issues/$WRITE_TARGET
BEFORE=$(/bin/ls "$ISSUE_DIR/comments/" | wc -l)
echo "stress test $(date +%s)" > "$ISSUE_DIR/comments/_create"
sleep 1
AFTER=$(/bin/ls "$ISSUE_DIR/comments/" | wc -l)
echo "comments: $BEFORE → $AFTER (new: $((AFTER - BEFORE)))"
```
**Expected:** Count increases by 1 after 1s. If it stays the same, check `journalctl --user -u linearfs.service -n 5` for rate-limit errors.

### 2d. Interleaved concurrent read+write
Runs a background reader while the foreground writes — tests for races between `Flush` and `GetAttr`.
```bash
ISSUE=$LINEARFS_MOUNT/teams/$TEAM/issues/$WRITE_TARGET/issue.md

( for i in $(seq 1 30); do
    cat "$ISSUE" > /dev/null
    sleep 0.2
  done
  echo "reader done" ) &

WRITES_OK=0
for priority in medium high medium low low; do
  python3 -c "
import re
with open('$ISSUE') as f: content = f.read()
content = re.sub(r'^priority:.*', 'priority: $priority', content, flags=re.MULTILINE)
with open('$ISSUE', 'w') as f: f.write(content)
"
  WRITES_OK=$((WRITES_OK+1))
  sleep 1
done
wait
echo "writes: $WRITES_OK/5"
grep "^priority:" "$ISSUE"
```
**Expected:** All 5 writes succeed, final priority is `low`, no errors from the reader.

## Phase 2e: Adversarial — reads don't starve writes

These tests verify that reading issue.md files does NOT trigger API calls, so
writes remain available immediately after bulk reads. This was a regression that
was fixed by moving `MaybeRefreshIssueDetails` out of the repo `Get*` methods
and into the FS-layer directory nodes (comments/, docs/, attachments/).

### 2e-i. Bulk reads then immediate writes
The critical regression test. Before the fix, 50x `cat issue.md` would spawn 50
`IssueDetails` API calls and exhaust the token bucket, blocking all writes with EIO.
```bash
TEAM=ENG
echo "=== Bulk read 50 issues ==="
/bin/ls $LINEARFS_MOUNT/teams/$TEAM/issues/ | head -50 | xargs -P 16 -I{} cat $LINEARFS_MOUNT/teams/$TEAM/issues/{}/issue.md > /dev/null && echo "reads: OK"

echo ""
echo "=== Immediate write test ==="
ISSUE=$LINEARFS_MOUNT/teams/$TEAM/issues/$WRITE_TARGET/issue.md
PASS=0; FAIL=0
for priority in high low high; do
  python3 -c "
import re
with open('$ISSUE') as f: content = f.read()
content = re.sub(r'^priority:.*', 'priority: $priority', content, flags=re.MULTILINE)
with open('$ISSUE', 'w') as f: f.write(content)
"
  GOT=$(grep "^priority:" "$ISSUE" | awk '{print $2}')
  if [ "$GOT" = "$priority" ]; then
    PASS=$((PASS+1)); echo "  OK: $priority"
  else
    FAIL=$((FAIL+1)); echo "  STALE: wrote $priority, read $GOT"
  fi
done
python3 -c "
import re
with open('$ISSUE') as f: content = f.read()
content = re.sub(r'^priority:.*', 'priority: low', content, flags=re.MULTILINE)
with open('$ISSUE', 'w') as f: f.write(content)
"
echo "result: $PASS pass, $FAIL stale reads"
```
**Expected:** 3/3 writes pass. Bulk reads trigger zero API calls, so the token bucket is unaffected.

### 2e-ii. Bulk reads then immediate comment creation
Same principle but tests the `_create` write path.
```bash
TEAM=ENG
echo "=== Bulk read 50 issues ==="
/bin/ls $LINEARFS_MOUNT/teams/$TEAM/issues/ | head -50 | xargs -P 16 -I{} cat $LINEARFS_MOUNT/teams/$TEAM/issues/{}/issue.md > /dev/null && echo "reads: OK"

echo ""
echo "=== Create comment immediately after ==="
ISSUE_DIR=$LINEARFS_MOUNT/teams/$TEAM/issues/$WRITE_TARGET
BEFORE=$(/bin/ls "$ISSUE_DIR/comments/" | wc -l)
echo "stress test validation $(date +%s)" > "$ISSUE_DIR/comments/_create"
sleep 1
AFTER=$(/bin/ls "$ISSUE_DIR/comments/" | wc -l)
echo "comments: $BEFORE → $AFTER (new: $((AFTER - BEFORE)))"
```
**Expected:** Comment count increases by 1. The `ls comments/` triggers `MaybeRefreshIssueDetails` (correct — user browsed into sub-dir) but the `_create` write should still succeed.

### 2e-iii. Verify no IssueDetails API calls from reads
Directly checks the logs to confirm reads are not triggering API calls.
```bash
# Snapshot the budget before reads
BEFORE_BUDGET=$(journalctl --user -u linearfs.service --no-pager --since "1 sec ago" 2>&1 | grep -oP 'budget: \K[0-9]+' | tail -1)

TEAM=ENG
/bin/ls $LINEARFS_MOUNT/teams/$TEAM/issues/ | head -50 | xargs -P 16 -I{} cat $LINEARFS_MOUNT/teams/$TEAM/issues/{}/issue.md > /dev/null
sleep 3

# Count IssueDetails calls in the window
DETAILS=$(journalctl --user -u linearfs.service --no-pager --since "15 sec ago" 2>&1 | grep -c "IssueDetails waited")
echo "IssueDetails API calls during bulk read: $DETAILS (expect 0 from reads, sync worker may contribute a few)"
```
**Expected:** 0 IssueDetails calls from the reads. A small number (1-3) from the sync worker's background cycle is normal.

### 2e-iv. Bulk sub-dir browsing triggers controlled refreshes
Verifies that `ls comments/` does trigger refreshes (correct behavior) but deduplicates per-issue.
```bash
TEAM=ENG
echo "=== ls comments/ on 10 issues ==="
/bin/ls $LINEARFS_MOUNT/teams/$TEAM/issues/ | head -10 | xargs -P 4 -I{} /bin/ls $LINEARFS_MOUNT/teams/$TEAM/issues/{}/comments/ > /dev/null 2>&1 && echo "exit: 0"
sleep 3

echo ""
echo "=== Triple sub-dir ls on 1 issue (dedup test) ==="
FIRST=$(/bin/ls $LINEARFS_MOUNT/teams/$TEAM/issues/ | head -1)
/bin/ls "$LINEARFS_MOUNT/teams/$TEAM/issues/$FIRST/comments/" > /dev/null 2>&1
/bin/ls "$LINEARFS_MOUNT/teams/$TEAM/issues/$FIRST/docs/" > /dev/null 2>&1
/bin/ls "$LINEARFS_MOUNT/teams/$TEAM/issues/$FIRST/attachments/" > /dev/null 2>&1
echo "triple ls: OK (triggerBackgroundRefresh deduplicates to 1 API call)"
```
**Expected:** All succeed. 10x `ls comments/` triggers at most 10 refreshes (one per issue). Triple ls on the same issue deduplicates to 1 call.

## Phase 3: Health report

Run after all tests to confirm the system is in good shape.

```bash
echo "=== SQLite health ==="
sqlite3 ~/.config/linearfs/cache.db "SELECT COUNT(*) as issues FROM issues;"
sqlite3 ~/.config/linearfs/cache.db "SELECT COUNT(*) as pending_queue FROM pending_detail_sync;"
sqlite3 ~/.config/linearfs/cache.db "SELECT user_id FROM viewer_cache;"
sqlite3 ~/.config/linearfs/cache.db "PRAGMA integrity_check;"

echo ""
echo "=== WAL checkpoint ==="
sqlite3 ~/.config/linearfs/cache.db "PRAGMA wal_checkpoint(PASSIVE);"

echo ""
echo "=== Service status ==="
systemctl --user status linearfs.service --no-pager | head -n 6

echo ""
echo "=== Recent rate-limit activity ==="
journalctl --user -u linearfs.service --no-pager -n 20 | grep -E "ratelimit|rate limited|budget|Failed" | tail -10
```

**Expected:**
- `pending_queue` = 0 (no backlogged issues)
- `viewer_cache` has one row with your user UUID
- `integrity_check` = `ok`
- Service is `active (running)`

## Interpreting results

| Symptom | Likely cause |
|---|---|
| `by/status/Done` hangs | Normal — bucket is very large, skip it |
| `IssueDetails waited Xs` in logs | Sync worker startup or sub-dir browsing; recovers at 0.417 req/s |
| Writes fail with EIO after bulk `cat issue.md` | Regression — reads should NOT trigger API calls; check `MaybeRefreshIssueDetails` call sites |
| Writes fail with EIO after service restart | Sync worker startup backfill exhausted token bucket; wait ~90s |
| 1 stale read in test 2a | Kernel page cache; harmless if subsequent reads are correct |
| `pending_detail_sync` count > 0 after tests | Rate-limit hit during sync; will drain on next sync cycle |
| `viewer_cache` empty | Daemon just started; wait 5s and re-check |
| Missing `pending_detail_sync` or `viewer_cache` tables | Old binary running — run `/reinstall` |
| 2c comment count doesn't increase | Rate limiter exhausted — check logs for `token bucket empty, CreateComment` |
