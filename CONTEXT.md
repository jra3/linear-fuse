# Domain & architecture vocabulary — linear-fuse

This file names the project's load-bearing concepts so reviews and designs use one
language. Architecture terms follow the deep-module vocabulary (module, interface,
implementation, depth, seam, adapter, leverage, locality).

## Concepts

### Edit path
The rich write flow for editing an existing entity's file (`issue.md`, `project.md`,
a comment/doc/label/milestone `.md`): parse markdown → resolve names→IDs → call the
Linear API → **read-your-writes verify** → upsert SQLite → invalidate caches → set or
clear `.error`. Distinct from the **create path** (`_create`/`mkdir`) and **delete
path** (`unlink`/`rmdir`), which skip resolution and verification.

### Read-your-writes verification
After the API accepts a write, re-derive what *persisted* and compare it against what
was *sent*, classifying any difference: a silent revert or truncation is fatal
(surface as `EIO` + `.error`); a benign server-side markdown reformat is a non-fatal
note. Implemented by the pure helpers in `internal/fs/writeback.go`
(`writeBackDivergence`, `writeBackError`, `normalizeMarkdown`).

### WriteBack tail (`commitWriteBack`)
The **deep module** that owns the invariant tail of every edit:
fetch fresh → run the caller's `Compare` → persist → classify → set/clear `.error` →
return `EIO` on fatal divergence. Generic over the entity type `T`. Lives in
`internal/fs/editcommit.go`. Its **interface is its test surface**: it depends only on
a small `ErrorSink` seam plus `Fetch`/`Persist`/`Compare` closures, so it is unit-
tested with a fake sink and stub closures — no FUSE mount, SQLite, or API.

The **front half** of each edit (parse, resolve, call API) stays per-entity; it is
genuinely irreducible (issue uses an untyped `map[string]any` with 7 resolutions,
others use typed input structs). Only the tail is shared, and it was the drift-prone
part — `label` and `milestone` previously skipped verification entirely.

### ErrorSink
The minimal seam the WriteBack tail uses to record validation/divergence messages for
`.error` files: `SetWriteError(key, msg)` / `ClearWriteError(key)`. `*LinearFS`
satisfies it directly (no adapter), so production wiring is zero-cost while tests
inject a 2-method fake.

### Kernel-cache coherence policy (`invalidateCreated`/`Deleted`/`Updated`)
After a mutation the kernel still caches the old directory listing and name lookups.
Two primitives fix it — `InvalidateKernelInode(dir)` refreshes a readdir,
`InvalidateKernelEntry(dir, name)` drops a cached lookup — but *which* combination a
mutation needs is a **policy** that used to live in each handler, so handlers drifted:
relation `unlink` notified nothing (deleted item lingered), and label/project/issue
creates skipped the dir inode (new item invisible).

The **deep module** is the intent-named policy in `internal/fs/invalidate.go`: a handler
says what happened — `InvalidateCreated` / `InvalidateDeleted` / `InvalidateUpdated` —
and the correct notifies follow. Built on a `kernelNotifier` seam (the two primitives,
satisfied by `*LinearFS`), so the policy is unit-tested with a recording fake — no FUSE
server. The raw primitives remain only for the **rename** path (atomic save: old+new
entry + file inode), a distinct intent, and the `.error` virtual file.
