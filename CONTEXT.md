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
path** (`unlink`/`rmdir`), which skip name→ID resolution and read-your-writes
verification but share the **mutation-commit tail** (below) for the persist →
invalidate → `.error` invariant.

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

### Mutation-commit tail (`commitMutation`)
The create/delete counterpart to the WriteBack tail: the **deep module** that owns the
invariant tail of every **create** and **delete**. After the API call it owns *both*
arms of the `.error` symmetry — on failure it sets `.error` and returns the errno
(default `EIO`, or a caller-supplied classification, e.g. issue creation maps a
rate-limited request to `EAGAIN`); on success it clears `.error`, runs the caller's
`persist`, then `invalidate`s the kernel caches (`persist` before `invalidate`, so the
refreshed readdir hits updated SQLite). Lives in `internal/fs/mutationcommit.go`. One
non-generic module covers both directions: the caller closes over the created/deleted
entity inside `persist` (upsert for a create, delete-from-SQLite for a delete), so the
tail never names the entity type. `persist` is the **single uniform home of each
mutation's SQLite effect** — its failure is non-fatal (a cache miss must not fail a
write Linear accepted) and it replaced the old scatter where some deletes wrote SQLite
in a handler, some in an `lfs` helper, and labels not at all. Like the WriteBack tail it
depends only on the **`ErrorSink`** seam plus the spec's closures, so it is unit-tested
with a fake sink and stubs — no FUSE mount, SQLite, or API. The per-mutation **front
half** (parse, pre-API validation → `EINVAL`/`ENOENT`, call the create/delete API,
build any returned inode) stays per-entity.

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
