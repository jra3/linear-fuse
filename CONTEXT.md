# Domain & architecture vocabulary — linear-fuse

This file names the project's load-bearing concepts so reviews and designs use one
language. Architecture terms follow the deep-module vocabulary (module, interface,
implementation, depth, seam, adapter, leverage, locality).

## Concepts

### Edit path
The rich write flow for editing an existing entity's file (`issue.md`, `project.md`,
a comment/doc/label/milestone `.md`): parse markdown → resolve names→IDs → call the
Linear API → **read-your-writes verify** → upsert SQLite → invalidate caches → set or
clear `.error`. Distinct from the **create path** (`_create`/`mkdir`), whose invariant
tail is the Create tail below, and the **delete path** (`unlink`/`rmdir`). Only edits
carry read-your-writes verification; creates trust the mutation's echoed entity.

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

The **front half** of each edit (parse, resolve, call API) stays per-entity. For
issues the resolve step is itself a deep module — see Name→ID resolution below.

### Create tail (`commitCreate`)
The **deep module** that owns the invariant tail of every create (`_create` writes and
`mkdir`), the create path's counterpart to the WriteBack tail: run the caller's
`mutate` closure (parse → build input → call the mutation seam) → classify failure
(**`FieldError`** → `EINVAL`, unknown reference → `ENOENT`, retryable/rate-limited →
`EAGAIN`, other API failure → `EIO`; the reason renders to `.error`) → on success
clear `.error`, record the new identity in `.last`, persist to SQLite (non-fatal),
and apply the kernel-cache coherence policy. `InvalidateCreated` on the collection
dir is guaranteed by the module — a spec cannot forget it; per-entity internal-cache
extras are a spec closure. Generic over the entity type `T`. Every create surface
supplies a `.last` projection (including attachments/relations and project/initiative
status updates — updates were the last holdout, hand-rolling the tail with no
`.error`/`.last` until they joined), the `mutate` closure calls `lfs.mutator()`
directly, and `persist` is always explicit — no mutation wrapper hides an upsert.
Unit-tested through the ErrorSink/notifier fakes, no FUSE mount.

For status updates the front half is the shared `parseUpdateContent` (one parser for
both project and initiative updates): an explicitly-written unknown `health:` is a
`FieldError` (→ `EINVAL`), never silently coerced to `onTrack`, and frontmatter with
an empty body is likewise rejected; only plain whitespace content (no frontmatter)
is treated as flush noise and no-ops before the tail.

### Delete tail (`commitDelete`)
The **deep module** owning the invariant tail of every delete (`rm`/`rmdir`,
including archive-flavored deletes of issues/projects), sibling of the Create
tail: run the caller's `find` closure (locate the target by name, or hand over an
already-held entity) → run the delete/archive mutation → classify failure through
the shared classifier (`classifyMutationErr`) → on success clear `.error`,
**forget the SQLite row** (required — the store is the listing source of truth,
so a skipped forget resurrects the deleted item until sync), and apply the
kernel-cache coherence policy (`InvalidateDeleted` is module-guaranteed). An
unknown name notes itself in `.error` before returning `ENOENT`. Generic over
`T`, behind the `deleteSink` seam; unit-tested with fakes, no FUSE mount.

### Name→ID resolution (`resolveIssueUpdate`)
marshal returns an issue update whose relational fields hold *names* (a state name,
assignee email, label names, parent identifier, project/milestone/cycle names);
Linear's API needs IDs. `resolveIssueUpdate` (`internal/fs/resolve.go`) turns each
name into its ID in place and owns the **field ordering** (project resolves before
milestone, since a milestone resolves against the — possibly changing — project), the
**label-clearing special case** (Linear rejects an empty `labelIds`, so clearing uses
`removedLabelIds`), and the per-field error messages. A bad value returns a
**`FieldError`** (`Field`/`Value`/`Message`, rendered to `.error` via `Detail()`) and
the handler maps it to `EINVAL`. This collapsed the issue-`Flush` front half from
~125 lines to one call. It depends on the **`issueResolver`** seam (the seven
`Resolve*` lookups, satisfied by `*LinearFS`), so the whole path is unit-tested with a
fake resolver — no repo, SQLite, or API. The individual `Resolve*` methods remain as
shared primitives (also used singly by initiatives and projects).

### Link reconciliation (`reconcileLinks`)
The **deep module** owning the *relational* front half of an edit — the counterpart
to Name→ID resolution for many-to-many links. Editing project.md's `initiatives:`
list and initiative.md's `projects:` list are mirror images of one algorithm: diff
the desired member names against the current ones, resolve each delta to an ID, and
link/unlink it. That algorithm was hand-copied in both `ProjectInfoNode.Flush` and
`InitiativeInfoNode.Flush`, differing only in which name resolves, the argument order
to the shared `Add/RemoveProjectToInitiative` mutation, and the `.error` field label.
`reconcileLinks` (`internal/fs/reconcile.go`) owns the diff and the resolve-error
classification; each caller passes a `linkReconcileSpec` whose `link`/`unlink`
closures own the per-side effect (the API mutation plus an immediate best-effort
junction-row write via `persistInitiativeProjectLink`). Like Name→ID resolution it is
pure of the **ErrorSink** and of any entity type — it works only on ID strings and
name lists — so it returns a **`FieldError`** (bad name → `EINVAL` via
`classifyMutationErr`) or the wrapped mutation error (→ `EIO`/`EAGAIN`), and is
unit-tested with recording closures (no FUSE mount, SQLite, or API). Persisting each
junction row inline (rather than the old deferred batch a mid-loop failure skipped)
keeps SQLite consistent with whatever the API actually accepted on a partial failure.

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
says what happened — `InvalidateCreated` / `InvalidateDeleted` / `InvalidateUpdated` /
`InvalidateRenamed` — and the correct notifies follow. `InvalidateRenamed` covers both
an atomic save (temp → real `.md`, so it also drops the file inode) and a pure entry
rename (a doc/label title change, `fileIno` 0). Built on a `kernelNotifier` seam (the
two primitives, satisfied by `*LinearFS`), so the policy is unit-tested with a recording
fake — no FUSE server. The raw `InvalidateKernelInode`/`Entry` primitives are now
**internal-only**: every call site in the package goes through an intent method.
