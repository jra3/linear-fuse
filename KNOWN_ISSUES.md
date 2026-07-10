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

---

Discovered during an interactive project-creation session on 2026-07-10 (Claude Code). See the session transcript at the bottom of this file.

---

## 5. Project/initiative markdown body maps to the short `description` (≤255) field instead of `content`

> **RESOLVED (2026-07-10):** The `project.md`/`initiative.md` body now maps to Linear's uncapped `content` field; the short `description` is read-only in `<entity>.meta`. `Content` was added to `Project`/`Initiative` + both `UpdateInput`s and to the shared query fragments (it rides in `data JSON`, so no schema change). Render/parse (`internal/marshal`), Flush (`internal/fs/{projects,initiatives}.go`), the mock, and the generated README were moved in lockstep, preserving the body round-trip invariant. Guarded by new marshal/fs tests (>255-char body round-trips; `.meta` carries `description`).

**Symptom:** Writing a normal multi-paragraph write-up into `project.md` (or `initiative.md`) fails. Via an editor / the Claude Code Edit/Write tools it surfaces as a bare `EINVAL` on the atomic-save rename; via a direct in-place write (`cat > project.md`) it surfaces as `cat: write error: Invalid argument`. In both cases `.error` reads:
```
Operation: update project
Error: description must be shorter than or equal to 255 characters.
```
A body trimmed to ≤255 chars saves fine — which is the tell that the whole markdown body is being routed into Linear's short description field.

**Root cause:** For projects and initiatives the FUSE maps the markdown **body** onto Linear's `description` field, which the API caps at 255 characters. Linear's data model splits project/initiative prose into two fields: a short `description: String` (≤255) and a long `content: String` ("The project content as markdown" / "The initiative's content in markdown format" — no length cap). The body should map to `content`.

- `internal/fs/projects.go:503-504` — `newScalarEdit(parsed.Name, parsed.Body, …)` then `api.ProjectUpdateInput{Name: …, Description: edit.desc}` — the body becomes `Description`.
- `internal/fs/initiatives.go:337-338` — identical pattern for `InitiativeUpdateInput`.
- `internal/api/types.go:190` — `ProjectUpdateInput` exposes only `Description *string`; no `Content` field is wired even though the GraphQL input (`docs/linear-schema.graphql`, `input ProjectUpdateInput`) and `InitiativeUpdateInput` both define `content: String`.

**Cross-consumer note:** **Issues are not affected** and correctly map body → `description`, because Linear's `Issue.description` *is* the long markdown field (issues have no separate `content`). Projects and initiatives are the anomaly — the issue mapping appears to have been copied to them without accounting for the description/content split. The README (`~/am/linear/README.md:156` and `:171`) documents the current behavior — "the body maps to the description" — so the docs match today's code but describe the wrong target field.

**Impact:** High for these two entity types — you cannot store a real project/initiative description through the filesystem. Anything longer than a tweet is rejected.

**Workaround:** Keep `project.md` / `initiative.md` bodies ≤255 characters. Put longer prose in a `docs/` entry under the entity instead.

**Fix direction:** Add `Content *string json:"content,omitempty"` to `ProjectUpdateInput` and `InitiativeUpdateInput`; map `parsed.Body` → `Content`. Then either (a) expose the short `description` as an editable frontmatter field, or (b) drop it from the editable set and render it read-only in `.meta`. Update the README's "body maps to the description" lines to "body maps to the content (markdown)". Add a cross-consumer test that a >255-char project/initiative body round-trips.

---

## 6. API validation errors surface as an opaque errno; the reason is only in `.error`

> **RESOLVED (2026-07-10, partial — the errno-hint mitigation):** A length-cap rejection now returns `EMSGSIZE` instead of a bare `EINVAL`, so the errno itself hints at the cause (new `api.IsFieldTooLong` predicate wired into `classifyMutationErr`; the reason still lands in `.error`). The generated README's failure model documents `EMSGSIZE` and reiterates "always read `.error` after any failed write, including an atomic-save rename." The broader "a string can't ride through errno" limitation is inherent to FUSE; this closes the specific too-long case that most often misled callers (it was what masked #5).

**Symptom:** A failed edit returns a bare `EINVAL` ("invalid argument") on the `write`/`rename` syscall with no reason attached. For atomic-save-via-rename (editors, the Claude Code Edit/Write tools) the message is literally `EINVAL: invalid argument, rename 'project.md.tmp.<pid>.<rand>' -> 'project.md'`, which *reads like the save-via-rename path (#145) is unsupported* — even though the rename mechanism worked perfectly and the real failure was field validation (issue #5). The actual reason is written to the sibling `.error`, which the failing tool never reads.

**Root cause:** `ProjectInfoNode.Flush` (and the initiative/issue equivalents) validate and call the Linear API inside the FUSE write path. On a validation/mutation error they do the right thing for a shell user — `SetWriteError(id, msg)` then return an errno (`internal/fs/projects.go:436-437, 455-457, 507-511` via `classifyMutationErr`). But a POSIX `write`/`rename` can only return a numeric errno; the human-readable string can't ride along. Through the atomic-save tail (`internal/fs/renamesave.go:101` → `spec.flush`) that errno propagates straight up as the `rename(2)` result, so the caller sees `EINVAL` on the rename and nothing else.

**Impact:** Medium — no data loss, but it's a debugging trap. It cost real time here: the opaque rename-EINVAL made it look like the FUSE rejects atomic-save temp-file renames (a regression of #145), when the true cause was the #5 description-length limit. Any tool that writes-then-reports without consulting `.error` will misattribute the failure.

**Workaround:** After any `EINVAL`/"invalid argument" on an editable `.md` (whether from an in-place write or an atomic-save rename), **read the sibling `.error`** for the real reason before concluding anything about the write path.

**Fix direction:** Inherent to FUSE that a string can't return through `errno`, so the mitigations are: (a) prefer specific errnos where they exist — e.g. return `EMSGSIZE` for a too-long field, `EINVAL` only for genuinely malformed frontmatter — so the errno itself carries a hint; (b) document prominently (README + tool guidance) that `.error` holds the reason for every failed edit; (c) consider validating known length/enum limits *before* the API round-trip so the failure is deterministic and the `.error` is written even on the rename path (it already is here — the gap is purely that callers don't look).

---

## 7. Batch `_create` writes give no per-item feedback and share one `.error`/`.last`

**Symptom:** Creating several issues in a loop (`printf … > issues/_create` × N) where every write shares a latent frontmatter bug fails silently as a batch: `.last` is empty, and `.error` shows only the *last* failure. There's no signal that N/N failed rather than 1. The specific trigger here was a YAML gotcha — a title starting with `[`:
```
---
title: [1] Verify auto context-file generation
---
```
`[1] …` parses as a YAML flow sequence, so every create failed with:
```
Field: frontmatter
Error: failed to parse frontmatter: yaml: line 1: did not find expected key
```
Quoting the title (`title: "[1] …"`) fixed all of them.

**Root cause:** Two independent things. (1) The frontmatter is parsed as YAML, so any scalar beginning with a YAML indicator (`[ { * & ! | > % @ \``, leading `-`, etc.) must be quoted; the docs' examples never quote and never warn. (2) `_create` is single-shot with a single sibling `.error`/`.last`, so a scripted batch overwrites feedback on each iteration — a run of failures collapses to "the last one".

**Impact:** Low, and partly user error — but the failure mode is invisible enough to send you re-running a whole batch blind.

**Fix direction:** (a) In the frontmatter parse-error message, detect a value starting with a YAML indicator and hint "values starting with `[` `{` `*` … must be quoted"; add a quoted-title example to the README/`_create` docs. (b) Optionally accumulate batch outcomes in `.last` (append, not overwrite) so a scripted loop can read back how many of N succeeded.

---

## Appendix — 2026-07-10 session transcript (verbatim)

Context: creating a Linear project + 8 issues via the mounted filesystem from Claude Code. Ordered sequence of failures that surfaced issues #5, #6, #7.

1. **Edit/Write tool on `project.md`** with a ~700-char body (issue #5, via #6):
   ```
   EINVAL: invalid argument, rename
     '…/verification-first-onboarding/project.md.tmp.2578106.0d87e9a888bf'
     -> '…/verification-first-onboarding/project.md'
   ```
   (Repeated identically with the Edit tool, temp name `…8102a47492d0`.)

2. **`cat > project.md`** (in-place, same long body) — issue #5 reason finally visible:
   ```
   cat: write error: Invalid argument
   .error: Operation: update project
           Error: description must be shorter than or equal to 255 characters.
   ```

3. **`cat > project.md`** with a 241-char body — **succeeded**, confirming the ≤255 cap.

4. **Batch `printf … > issues/_create` × 8** with unquoted `title: [1] …` (issue #7):
   ```
   .error: Field: frontmatter
           Error: failed to parse frontmatter: yaml: line 1: did not find expected key
   .last:  (empty)   ← all 8 failed, no per-item signal
   ```

5. **Same batch with quoted titles** (`title: "[1] …"`) — all 8 created (ENG-5433 … ENG-5440).

Note: a secondary observation, not a bug — the Claude Code Edit/Write tools' atomic-save (temp-file + `rename`) is correctly supported by #145; the confusion in step 1 was entirely issue #6 masking issue #5.
