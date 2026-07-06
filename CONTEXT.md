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
so a skipped forget resurrects the deleted item), and apply the kernel-cache
coherence policy (`InvalidateDeleted` is module-guaranteed). An unknown name
notes itself in `.error` before returning `ENOENT`. Generic over `T`, behind the
`deleteSink` seam; unit-tested with fakes, no FUSE mount.

Durability of the forget (a stress test caught a delete whose forget lost a
`SQLITE_BUSY` race to the sync worker, leaving a phantom file that resurrected
forever): the connection-level `busy_timeout` DSN pragma makes the race rare,
the tail retries a failed forget before giving up, a delete of an entity Linear
already lacks ("Entity not found") is **idempotent success** — the row is still
forgotten, so re-`rm`ing a phantom heals it — and the details sync **prunes**
rows a (provably complete, sub-page-cap) fetch no longer returns, scoped by
issue and a pre-fetch `synced_at` cutoff so rows created mid-fetch survive.

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

### Scalar edit (`scalarEdit`)
The **deep module** owning the *scalar* front half of a project/initiative edit —
the counterpart to Link reconciliation, which owns the *relational* (list) front
half of the same two mirror-image handlers. `project.md` and `initiative.md` each
expose exactly two editable scalars: a **name** (frontmatter) and a
**description** (body). `scalarEdit` (`internal/fs/scalaredit.go`) is the diff of
those two — `{name, desc *string, origName, origDesc string}` — and owns the
change decision (trim both sides of the body so a render/parse trailing-newline
delta doesn't read as an edit; coerce the name via `marshal.ScalarToString` so a
numeric/bare-scalar name updates instead of being silently dropped by a direct
type assertion), the `changed()` predicate, and the read-your-writes
`divergences(freshName, freshDesc)` classification (one canonical field order,
`writeBackDivergence` per changed field). It stays **neutral to the entity type**:
the caller maps `name`/`desc` onto its own typed `api.ProjectUpdateInput` /
`api.InitiativeUpdateInput` and pulls the fresh values back out — nothing
Project- or Initiative-shaped crosses the interface, so no generics. This
collapsed the byte-identical `fieldChanged`-flag diff and the byte-identical
`commitWriteBack` compare closure that both handlers hand-rolled. The scalar
mutation failure now routes through the shared `classifyMutationErr` (like the
reconcile path 20 lines above it), so a rate-limited scalar edit returns
`EAGAIN` — not the old flat `EIO` — and the server's reason reaches `.error`.
Pure of the FUSE mount, SQLite, and API: unit-tested directly on a parsed
`marshal.Document` plus current values. (`marshal.ScalarToString` and
`marshal.StringSliceFromYAML` — the list coercion the handlers now share for the
relational front half — were exported from marshal for this.)

### Create trigger (`createFileNode`)
The **deep module** owning the write-only `_create` file (and the named-file
`Create` paths that share its mechanics): buffer written bytes, and on close hand
the complete content to a per-surface **`onFlush`** closure — the module's whole
interface — which parses and calls the Create tail. The buffer lives on the
**per-open FileHandle**, so its lifetime is one open-write-close cycle: a dup'd
descriptor's second flush sees a consumed buffer and no-ops, while a genuinely
new open through the same kernel-cached inode gets a fresh buffer and really
creates. This replaced nine hand-copied `New*Node` types, two of which (the old
per-node buffers') `created` latch silently swallowed the second create — and
issues/_create's zero-lookup-timeout hack existed only to dodge that bug. Lives
in `internal/fs/createfile.go`; buffer edge cases unit-tested once with a
recording closure, no FUSE mount.

### Writable-collection trio (`collectionTrio`)
The **deep module** owning which virtual files a writable collection directory
serves: `_create`, `.error`, `.last`. A directory declares a spec (`kind`,
`parentID`, `onFlush`) and delegates its Readdir header to `entries()` and the
three special names in Lookup to `lookupCollectionTrio` — so the trio is
module-guaranteed the way `InvalidateCreated` is, and a new collection cannot
drift (the updates directories shipped without `.error`/`.last` for months
because each dir restated the trio by hand). mkdir-created collections
(projects) set `onFlush` nil and serve just the two sidecars. Lives in
`internal/fs/collection.go`.

### Symlink views (`symlinkNode`)
The **deep module** owning every symlink the filesystem serves: the issue
symlinks under `by/`, `cycles/`, `recent/`, `projects/`, `users/`, `my/`, and
`children/`, the project symlinks under `initiatives/`, and the
`cycles/current` alias. Its
whole interface is construction: a view's Lookup computes the relative target
where it already holds the entity, and hands `newSymlinkInode` the target plus
the entity's real created/updated times (cycle views pass a distinct atime —
the cycle end date — through the same construction). The helper fills the
Lookup answer's attributes from the same code path that answers a later
`stat`, so a Lookup answer and a Getattr can never disagree — the drift that had the `current`
alias reporting `now()` while its Lookup reported cycle times, and the
initiative project symlink fabricating size/timestamps while re-scanning every
team's projects on each `readlink`, and the `children/` symlink shipping a
dangling one-level target with root ownership. Eight hand-copied node types
collapsed into this one; an unresolvable target (a project whose team
association hasn't synced yet, an issue whose team hasn't) is a reference to
something that doesn't exist -> `ENOENT` at Lookup, never a dangling
placeholder. Lives in `internal/fs/symlink.go`;
unit-tested directly, no FUSE mount.

### Attr construction (`nodeAttr`/`attrNode`)
The **deep module** owning how a directory or file node's attributes are
constructed — the non-symlink complement to Symlink views (`symlinkNode`), and
the same guarantee: construction fixes the reporting data, so a Lookup answer
and a later `Getattr` render it identically and can never disagree. `nodeAttr`
(`{mode, size, created, updated}`, `internal/fs/nodeattr.go`) has one
`fill(*fuse.Attr)` that owns mode/uid/gid/size/times; the `attrNode` mixin
(`BaseNode` + a stored `nodeAttr`) **provides the default `Getattr`**, so a
directory node cannot hand-write a divergent one. The `newDirInode`/`newFileInode`
`BaseNode` constructors stash the `nodeAttr` on the child, fill the Lookup
`EntryOut` from that same value, take the entry timeout as an explicit param
(the deliberate 30s/5s/0/1s classes are preserved verbatim, never rationalized
here), and return `StableAttr{Mode, Ino}`.

This replaced 54 hand-fabricated attribute blocks whose per-site copies had
already drifted: `DocsNode`/`AttachmentsNode.Getattr` reported `time.Now()`
(non-deterministic — every `ls -lt` reshuffled, violating the `mtime=updatedAt`
convention), and `CommentsNode.Getattr` reported a wrong ctime, all disagreeing
with the times their parent's Lookup answered. The collapsed contract is the
issue's times uniformly (`atime/mtime = UpdatedAt`, `ctime = CreatedAt`), which
forced the directory nodes to become self-describing — carry the times they
report rather than re-derive them per call.

**Directories vs files.** A directory's attributes are wholly static, so it
gets the mixin and the inherited `Getattr` (true no-drift). A file's `Size` is
a *legitimately* dynamic edit-buffer value (it grows after a write and is meant
to diverge from what Lookup first reported), so a file keeps its own `Getattr`
and reuses only the immutable half of `nodeAttr.fill` (mode/uid/gid/times) — its
dynamic size stays owned by the node. Unit-tested directly (the `fill` fields
plus an anti-drift equality test: the Lookup `EntryOut.Attr` and the node's
`Getattr` `AttrOut.Attr` must be equal for each directory kind), with a mounted
attr-conformance/stat-determinism test in `internal/integration` guarding the
real kernel `Getattr` path.

### Inode namespace (`ino`)
Every virtual inode number the filesystem hands the kernel is
`fnv64a("kind:"+id)` through the single `ino(kind, id)` function
(`internal/fs/ino.go`). The kind prefix is a hard invariant — there are **no
bare hashes** — so two entities that happen to share an id (an issue and its
`comments/` directory) never collide. The ~28 named one-line wrappers
(`commentsDirIno`, `issueIno`, …) gathered in that one file **are** the
namespace: they keep call sites typo-proof (the `"comment:"`/`"comments:"`
one-character gap is written exactly once) and make the whole set auditable at
a glance. Adding a virtual file means adding a wrapper here, never hashing
inline. `issueIno` used to hash the bare id — the lone exception the registry
removed. `TestInodeNamespaceDistinct` calls every wrapper with one shared id and
asserts distinct results, guarding against a duplicated or mistyped prefix and
serving as the checklist a new kind must join. `scratchIno` (`atomicwrite.go`)
is deliberately **not** a wrapper: its key mixes the parent directory inode with
the name (so concurrent temp files in different dirs don't collide), a different
shape than `kind:id`.

### Edit buffer (`editBuffer`)
The **deep module** owning the read/write byte buffer of every editable file
node — the edit-side twin of `createFileNode`'s buffer. `editBuffer`
(`internal/fs/editbuffer.go`) is `{mu, content, dirty}` and provides the FUSE
buffer operations (`Open`/`Read`/`Write`/`Setattr`/`Fsync`), **promoted into the
node** the way `attrNode` promotes `Getattr`. Each of the seven editable file
nodes (`IssueFileNode`, `ProjectInfoNode`, `InitiativeInfoNode`, `CommentNode`,
`LabelFileNode`, `MilestoneFileNode`, `DocumentFileNode`) embeds it and keeps
only its **`Getattr`** (a one-liner: `fileAttr(n.size(), created, updated).fill`
— the file-side of the Attr-construction module) and its **`Flush`** (the
per-entity parse → API → write-back tail). This replaced ~5 byte-identical
buffer methods hand-copied across all seven.

**Content is eagerly seeded at construction, never lazily generated — and that
is forced, not a shortcut.** Lookup must report an accurate size (the kernel
skips READ entirely when size is 0), and the size is `len(markdown)`, so every
Lookup already materialises the content for the size; a lazy path could only
duplicate that work, never avoid it. An audit at the time confirmed the pre-
existing lazy machinery was vestigial: `IssueFileNode.ensureContent` never fired
(its two construction sites both seeded), and `project.md`/`initiative.md`
Lookup computed the content for the size and then *discarded* it, forcing a
regenerate on first Read — a live double-compute this fix removed by seeding.
`labelfile`/`milestonefile` remain the timestamp-less exception (their API types
carry no `CreatedAt`/`UpdatedAt`, so `Getattr` reports `now()` — see
[[attr-construction]]). Unit-tested directly (write-expands, in-place,
truncate-grow/shrink, read-clamps-at-EOF), no FUSE mount.

### Indexed listing (`indexedListing`)
The **deep module** owning the index-derived filenames of a collection whose
entries are named `<NNNN>-<date>…md` by creation order — comments and the
project/initiative status updates. The sibling of `collectionTrio` (which owns
the same collection's `_create`/`.error`/`.last`): the trio owns the *virtual*
files, this owns the *item* files. `indexedListing[T]{items, lessKey, nameOf}`
(`internal/fs/indexedlisting.go`) **owns the sort** and the name derivation, and
exposes `entries()` (the Readdir projection) and `find(name)` (the Lookup/Unlink
projection). Because all three surfaces derive names through the one module over
one canonical order, they cannot disagree — a file you can `ls` you can also
open and `rm`. Before this, each surface re-sorted and re-`Sprintf`'d
independently (seven copies across three files), so a timestamp-format tweak or
an off-by-one in one surface would silently strand a file: listed but
un-openable. Each collection declares its listing via a `listing(items)` method
(mirroring `trio()`); the two update collections share the `updateEntryName`
formatter (their `<NNNN>-<date>-<health>.md` convention is identical), while
comments own a per-minute timestamp format with no health. `TestIndexedListing-
RoundTrip` guards the invariant: every name `entries()` emits resolves back
through `find`, and same-second items still get distinct names via the 1-based
index.

### Connection drain (`paginate`)
The **deep module** owning cursor pagination of Linear GraphQL connections —
the read-side counterpart to `execMutation`. Linear silently caps a
connection without a `first:` argument at 50 nodes (and any page at 250), so
"fetch the projects" without draining is a lie past one page: the team
metadata sync shipped that lie for months (a 50-project cap that dangled a
third of the live initiative symlinks), and the reconcile ID fetches carried
a worse one (a truncated "authoritative" set feeding a diff-and-delete).
`drainFrom` (`internal/api/paginate.go`) owns the invariant tail — cursor
threading, termination, a stalled-cursor guard, an `ErrBudget` abort between
pages, and an **all-or-nothing result** (a nil-error return is the complete
set; diff-and-delete callers rely on this) — over a `pageFetch` closure, its
test seam. The `fetchAll`/`drain` spellings walk the response by path the way
`execMutation` does; `drain` resumes a connection whose first page arrived
embedded in a combined query (`queryTeamMetadata`, `queryWorkspace`) and
costs zero API calls when nothing overflowed. The envelope type carries
`*PageInfo` so a query that forgets to select `pageInfo` is a loud error,
never a silent single-page truncation. Page sizes are complexity-budgeted:
Linear scores a query's cost, and `team.projects`' nested selections price it
out of the combined metadata query entirely (~187 points/node; 50/page max —
measured live, documented at the query).

Completeness is what licenses the **metadata prunes**: after a drained
fetch, the rows the response no longer contains are deleted — `project_teams`
and `initiative_projects` junctions (per team / per initiative), and the
team's own `labels`, `cycles`, and `team_members` (departed members leave the
junction; the workspace-wide `users` table is never pruned). Each uses the
pre-fetch `synced_at` cutoff so mid-sync writes survive, is gated on a
per-entity clean flag (skipped when that entity's fetch or any upsert failed),
and runs only against a drained fetch — a truncated list reads as removals.
`states` are workflow-bounded and fetched single-page, so they stay
upsert-only. A label's `team_id` follows its own `team` (fetched via the
`LabelFields` fragment) — `nil` means a workspace-level label, stored
`team_id=NULL` — so a workspace label no longer churns to whichever team's
sync last touched it (`team.labels` returns workspace labels mixed in, which
is why stamping the syncing team was wrong). The team prune targets
`team_id = <team>`, so `NULL` workspace labels are outside every team's prune
scope.

### Sync reconcile tail (`syncCollection`)
The **deep module** owning the invariant tail of every metadata sync — the
sync-side sibling of the write-path tails (`commitCreate`/`commitWriteBack`/
`commitDelete`) and the module that actually **performs the metadata prunes**
`paginate`'s completeness licenses. Its shape is `convert → upsert-all →
prune-if-clean`. Six sites reconcile through it: `states` (upsert-only),
`labels`, `cycles`, `projects` (entity + `project_teams` junction + nested
milestones), `members` (entity + `team_members` junction), and
`initiativeProjects` (junction-only). Each restated the same `clean`-flag idiom
by hand — declare `xClean := true`, loop upserting, `if xClean { Prune }` — so a
new metadata kind copy-pasted it and a forgotten flag would let a *partial* fetch
read as removals (silent data loss). `initiativeProjects` even hand-rolled a
fail-fast variant whose error the caller only logged.

Its interface is closures, no `*db.Store`: `syncCollectionSpec[T]{label, items,
upsert, prune}`. `upsert(ctx, T)` does whatever the kind needs — convert, entity
upsert, any junction upsert, nested sub-writes — and `prune(ctx)` runs **once,
iff every `upsert` returned nil**; a `nil` prune closure means no prune (the
`states` case). Semantics are uniform **log-and-continue**: a failed `upsert` is
logged, marks the collection unclean, and does not abort the loop (so
`initiativeProjects` trades its fail-fast for continue — the observable outcome,
prune-skipped-on-any-failure, is unchanged and strictly more rows refresh). The
module returns nothing; sync is best-effort. Pure over closures, so it is
unit-tested with recording closures asserting *prune runs iff clean* — no real
store or API.

**"Clean" is completeness-set membership, not "no error anywhere."** An item is
unclean **iff a write the prune depends on failed** — a write in the prune's
completeness set. This is grounded in Linear's ontology and the drain contract:
`Project.teams` is a **peer many-to-many association** (the prunable
`project_teams` junction, safe to prune because the *projects* connection is
drained), whereas `ProjectMilestone.project` is **composition** — a milestone is
wholly owned by one project, its nested `projectMilestones` connection is
**capped at 50, never drained**, and it has **no prune anywhere**. So a milestone
upsert failure is a nested best-effort write outside the `project_teams` prune's
completeness set: the `projects` closure **logs-and-swallows** it (returns nil),
while a project-entity or `project_teams`-junction failure returns an error and
correctly suppresses the prune (a stale junction row must never be wrongly
deleted). The closure author honors the contract by choosing what to return
versus swallow.

The **per-issue detail sync** (`syncIssueDetailsBatch` — an issue's comments,
documents, attachments) reconciles through the same tail, three calls per issue.
Here completeness is *page*-shaped rather than *drain*-shaped: a full page
(`len == IssueDetailsPageSize`) may be truncated, so the caller composes a
`pruneWhenComplete(complete, fn)` policy that passes the real prune only on a
short (provably complete) page and `nil` otherwise — the module then adds its
clean guard, so a detail prune fires **iff clean AND complete**. This closed a
silent-prune bug the hand-rolled version carried: it gated the prune on page
completeness alone, so a failed comment/doc/attachment upsert (its `synced_at`
left un-stamped) was deleted as stale on the next complete page. Embedded-file
extraction from a comment body is the nested best-effort here (its own
never-pruned `embedded_files` table), analogous to milestones — it runs inside
the comment `upsert` closure regardless of the upsert result and cannot affect
cleanliness.

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
