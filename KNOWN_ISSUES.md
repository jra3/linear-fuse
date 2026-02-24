# Known Issues

Discovered during stress testing on 2026-02-24.

---

## 1. Intermittent EIO on reads immediately after a write

**Symptom:** After writing to an `issue.md`, the very next `read` on that file sometimes returns `EIO` (errno 5 / Input/output error). The data is still correct — `grep` finds the right value before hitting EIO mid-read — so the error appears to fire partway through a read, not before it.

**Observed in:** Tests 2a and 2b (priority toggle loop). Every other write cycle triggered an EIO on the subsequent grep, but grep still extracted the correct value.

**Likely cause:** The `InodeNotify` call after `Flush` invalidates the kernel page cache. If a read is initiated in the same kernel scheduling window, the kernel may return EIO while the cache is in the process of being invalidated. The read that follows (without a concurrent write) succeeds normally.

**Impact:** Low — data integrity is not affected. Shell scripts using `grep | awk` still extract correct values. Python `with open()` reads will raise an exception, which is more visible.

**Workaround:** A brief `sleep 0.1` between write and read avoids the race. Not needed in normal interactive use.

---

## 2. Comment/doc creation fails silently when rate-limited

**Symptom:** `echo "text" > comments/_create` returns exit code 0 but the comment is never created. No error is surfaced to the caller.

**Root cause:** The `_create` write handler calls `rate.Wait(ctx)` before making the API call. If the FUSE request context has a deadline shorter than the wait time (which happens when the token bucket is exhausted), `Wait` returns `context deadline exceeded` and `CreateComment` logs `Failed to create comment: rate limit wait cancelled`. The FUSE `Write` syscall already returned success at that point.

**Observed in:** Test 2c, run immediately after bulk read tests that drained the token bucket (418/1500 tokens used, IssueDetails waiting 13+ minutes per token).

**Workaround:** Check `journalctl --user -u linearfs.service -n 5 | grep "token bucket"` after a failed create. Wait for the bucket to replenish (~2.4 req/min for mutations) before retrying.

**Fix direction:** Return `EBUSY` or `EAGAIN` from the write handler when rate-limited so the error propagates to the caller instead of silently succeeding.

---

## 3. Bulk reads exhaust the rate-limit token budget for mutations

**Symptom:** Reading all 1700+ `issue.md` files (e.g. `grep -r` or `find ... | wc -l`) queues a background `IssueDetails` fetch for every issue not yet in cache. Each fetch consumes a rate-limit token. After a full sweep, `CreateComment`, `CreateDocument`, and other mutation calls block for 10+ minutes waiting for tokens to replenish (0.417 req/s).

**Observed in:** After tests 1b and 1c, `IssueDetails waited 13m+` appeared in logs for every subsequent background sync tick.

**Impact:** Medium — mutations degrade silently (see issue #2) until the bucket recovers.

**Workaround:** Run write tests before bulk read tests, or wait ~30 minutes after a full read sweep before running mutation tests.

**Fix direction:** Separate rate-limit buckets for reads (IssueDetails) and writes (mutations), or deprioritize background IssueDetails fetches so mutation tokens are always available.

---

## 4. Python anonymous file handles trigger EIO on GC finalization

**Symptom:** Python code using `open('path').read()` or `open('path', 'w').write(content)` without explicit close prints:
```
Exception ignored while finalizing file <_io.TextIOWrapper ...>:
OSError: [Errno 5] Input/output error
```

**Root cause:** When a Python file object is garbage-collected without being explicitly closed, Python calls `close()` in the `__del__` finalizer. If the FUSE `Release` call returns an error at that point (after the write has already been flushed successfully), Python logs the error but cannot propagate it.

**Impact:** Cosmetic — the write to Linear succeeds. The EIO only fires during cleanup.

**Fix:** Use `with open(...) as f:` — explicit close before the Python object goes out of scope surfaces the error immediately and cleanly.
