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

The classifier (`classifyMutationErr`) is the single owner of that failure
model, shared by the create and delete tails **and every edit-mutation site**
(issue/comment/label/document/milestone flushes and renames, the project/
initiative scalar+reconcile paths — the flushes/renames used to bypass it with
a flat `EIO`, violating the README's documented contract). Rate-limit and
not-found detection are the api package's predicates (`api.IsRateLimited` —
structural `GraphQLError.Code == "RATELIMITED"` plus message fallbacks, and
deliberately excluding the client-side "circuit breaker" transient, which
stays a `retryableCreateErr` concern; `api.IsNotFound` — the "Entity not
found" rejection), the single owners the client's GraphQL-errors branch, the
sync worker's backoff, and the repo's orphan defense also delegate to.

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
`resolveProjectLabels` (see "Project-label selection") is the second multi-name
resolver — same `FieldError` contract, but a pure function over a catalog slice
rather than a method on the resolver seam.

### Project-label selection (`projectlabels.go`)
The workspace project-label surface (#130). Linear's `ProjectLabel` is
**workspace-scoped** — the schema has no team edge at all (contrast `IssueLabel`'s
nullable team edge, which is why issue `labels.md` lives per-team) — with a
lifecycle issue labels lack: **groups** (`isGroup` containers; only one child per
group may be applied) and **retirement** (kept on existing projects, not newly
assignable). The surface: a root read-only `project-labels.md` **renderFile**
(never-ENOENT, groups/retired flagged, the assignment rules as prose in-file — it
is the file an agent reads after a validation `.error`), a per-team
`project-labels.md` **symlinkNode** alias, a `project_labels` twin table synced by
a workspace **syncCollection** pass (complete unfiltered drain = the completeness
set licensing a full-table prune; retired labels are IN the drain, live-verified
2026-07-08), and a `labels:` names list in project.md that resolves and validates
in `internal/fs/projectlabels.go` — all **pure functions over a catalog slice**
(no mount, no interface) — then rides the existing single `UpdateProject` call
(`ProjectUpdateInput.LabelIds *[]string`, pointer-or-omit full-set write). The
front-half composition lives in `labelsEdit` (same file, sibling of `scalarEdit`
in the edit-front-half family): it composes the pure primitives and owns the
whole label decision — guard timing, the single `changed` computation, `applyTo`
pointer-or-omit, and the guarded `divergences` compare — so
`ProjectInfoNode.Flush` makes one call instead of smearing label knowledge
across three points.

Load-bearing invariant: **render unknown label IDs verbatim; the resolver accepts
exact-ID passthrough** (catalog IDs and current-member IDs) — a cold or stale
catalog can never strip labels on an untouched save, and IDs are the documented
duplicate-name disambiguation (bare-name ties: prefer-current, then the single
active candidate, else a loud ambiguity error listing candidate IDs — never a
silent sibling pick). Validation policy in one sentence: **we enforce what
Linear's docs say about label assignment, even where the API is lax** —
live-verified that the wire *accepts* retired assignment; the mount still rejects
newly-applied retired labels (carried-through ones pass, since a full-set write
re-sends them) and group/one-child-per-group violations, with errors that name
the assignable children. The stale-blob clobber guard (one interactive-promoted
`GetProject` refresh iff current `LabelIds` is empty and the write changes
labels) closes the pre-upgrade-blob wipe: a full-set write computed against an
empty stale set would silently erase the project's real labels.

**Unification with issue labels was evaluated and rejected** (recorded so no
future round re-derives the merge): a `kind` column on `labels` breaks on
`GetLabelByName`'s `(team_id = ? OR team_id IS NULL)` union (project-label names
would resolve as issue labels and feed ProjectLabel IDs to `issueUpdate.labelIds`)
and on `ListTeamLabels` (project labels would leak into every team's `labels.md`
and `labels/` CRUD dir, where `rm` fires `issueLabelDelete`); the scoping axis,
prune regime, and lifecycle all differ; and GraphQL fragments cannot span
`IssueLabel`/`ProjectLabel`, so the one expensive share is forbidden by the wire
anyway. Sharing happens only through the already-generic seams (renderFile,
symlinkNode, syncCollection, hydrate-then-overlay, FieldError, paginate, ino).

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
Project labels deliberately do **not** reconcile through this module: `labelIds`
is one atomic full-set input on the project update (no per-pair link mutation
exists), so the labels edit is scalar-edit-shaped: `labelsEdit` — see
"Project-label selection".

### Scalar edit (`scalarEdit`)
The **deep module** owning the *scalar* front half of a project/initiative edit —
the counterpart to Link reconciliation, which owns the *relational* (list) front
half of the same two mirror-image handlers (`labelsEdit` — see "Project-label
selection" — is the third sibling, owning project.md's full-set labels front
half). `project.md` and `initiative.md` each
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

### Entity render (`marshal.*ToMarkdown`)
Every entity's markdown render lives in `internal/marshal`, one seam for
markdown ↔ entity: Issue/Document/Milestone always did, and round 14 moved
Project and Initiative (plus their `.meta` renders) out of the fs node methods
(`ProjectToMarkdown`/`ProjectMetaToMarkdown`, `InitiativeToMarkdown`/
`InitiativeMetaToMarkdown`) — before that, two of five entities' render policy
was observable only through a mounted filesystem. The editable-only split
(server-managed fields live in `.meta`, so a successful write never rewrites
the writer's bytes) is now pinned by unit tests on the exact frontmatter key
sets. The fs nodes keep one-line wrappers that degrade a render failure to an
empty file. The parse side stays with [[scalar-edit]] (name/description) and
[[link-reconciliation]] (the member lists). The read-only catalog renders
(team.md, states.md, labels.md, project-labels.md, user.md, cycle.md, updates,
label files) also route their frontmatter through this seam
(`renderWithFrontmatter`, internal/fs/catalogrender.go) — they used to
fmt.Sprintf-concatenate YAML, so a name like `Q3: Bets` emitted invalid YAML
in exactly the files agents machine-parse after a `.error`; the bodies stay
hand-built prose/tables.

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

### Node refresh (`nodeRefresher`/`refreshExisting`)
The **deep module** that closes the captured-entity staleness hole (round 15;
confirmed live by experiment): go-fuse dedups inodes by StableAttr and keeps
the FIRST node ever mounted for an ino — `bridge.addNewChild` silently
discards the freshly-constructed node **after the Lookup handler returns**
(`NewInode`'s return value is always the fresh struct, so reuse cannot be
detected from it). Any node that bakes entity state at construction (an
editBuffer's content, an entity dir's snapshot, a render closure's capture)
therefore served first-Lookup data for as long as the kernel remembered the
inode — the sync worker deliberately never notifies the kernel, and the
timeout-driven re-Lookup that was supposed to bring freshness hit the old
node. The long-skipped `TestCacheExpiryRefreshesData` ("FUSE inode caching
prevents immediate refresh") was this bug, filed away.

The seam: construction helpers (`newDirInode`/`newFileInode`/`newRenderInode`/
`mountRenderFile` and the few raw `NewInode` sites) now take the child's
**name** and probe `parent.GetChild(name)` *inside* the handler — the inode
the bridge will keep if it dedups — and push the fresh twin's volatile state
into it via `refreshFrom(fresh)` (`internal/fs/refresh.go`). A nil child means
the kernel FORGOT it and the fresh node installs — already fresh. Per-type
rules: the three entity dir nodes swap their entity under `attrNode.stateMu`
(which also guards `nodeAttr`, re-stamped by the seam) and expose
`entity()/setEntity` snapshots; the seven editBuffer file nodes go through
`editBuffer.refresh` — **a dirty buffer always wins** (a user's in-flight
edit is never clobbered by background sync) — with Getattr snapshotting
size+times under one lock; renderFile swaps its closure under `renderMu`
(embedders with entity fields shadow `refreshFrom` and reuse that lock);
`EmbeddedFileNode` swaps its file metadata under its own mu. `TeamNode`/
`UserNode`/cycle dirs are constructed with auto-assigned inos (fresh node per
Lookup) and don't need the seam — a pre-existing inconsistency with the inode
namespace that happens to dodge the bug. Guarded end-to-end by
`TestRemoteUpdateVisibleAfterKernelRevalidation` (remote upsert → pinned
inode chain so the kernel cannot FORGET → 31s real entry-timeout expiry →
fresh content and mtime; the pin is what forces the reuse path — without it
the kernel may forget everything and the test passes vacuously).

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

### Render file (`renderFile`)
The **deep module** owning every read-only *generated* file — the render-through
file complement to `attrNode` (the directory mixin) and the read-side twin of
`editBuffer` (the editable-file buffer). `renderFile` (`internal/fs/renderfile.go`)
is `{BaseNode, render func(ctx) (content []byte, mtime, ctime time.Time)}` and
provides the three FUSE ops such a file needs — `Open`/`Read`/`Getattr`, promoted
into whatever embeds it. Its whole interface is the one render closure, which
receives the FUSE handler's ctx on every path (Read, Getattr, and the
Lookup-time render — `TestRenderFileThreadsContext` pins it): a closure whose
source is a synchronous API call promotes it via `api.WithInteractive` at the
call; SQLite-backed closures pass it through for cancellation. It
replaced **nine** hand-copied node types (`TeamInfoNode`, `StatesInfoNode`,
`LabelsInfoNode`, `UserInfoNode`, `CycleFileNode`, `ReadmeNode`, `MetaFileNode`,
`ErrorFileNode`, `SuccessFileNode`) and reduced two more (`RelationFileNode`,
`ExternalAttachmentNode`, which embed it and keep only their `Unlink`) — net
−490-odd lines. The byte-window offset-clamp that all of them hand-rolled (a dozen
verbatim copies) lives once in `readWindow`.

**It renders on every read (`FOPEN_DIRECT_IO`), uniformly.** go-fuse dedups
inodes by `StableAttr.Ino` and reuses the first node for a given ino, so baking
bytes *or times* at Lookup serves stale values for the life of the mount — the
reasoning the `.meta`/`.error`/`.last` nodes already used. Collapsing the old
`KEEP_CACHE` nodes onto DIRECT_IO also fixed a latent bug: `states.md`/`labels.md`
carried a 10-min TTL content cache that was **dead under `KEEP_CACHE`** (the
kernel served the first read forever); the TTL/`cachedContent` fields are gone —
each read now fetches from SQLite (cheap) and re-renders. These files are tiny and
read interactively, so the per-read FUSE round-trip is imperceptible. The attr
timeout stays a per-construction param (`inheritTimeout` = leave the mount default
60s/30s, preserving the nodes that set none; `.meta`/`.error`/`.last` keep 0;
relation/attachment keep 30s).

**The closure returns real times, never `now()`** — the drift this module kills
(`ls -lt` used to reshuffle those files every call). A zero time reports as an
unset attr (`nonZeroTime`), i.e. honest "unknown". Sources: `team.md` uses the
team's times; `cycle.md` uses `StartsAt` (a cycle has no `updatedAt`; its
former decorative `atime=EndsAt` is dropped); attachment `.link` uses the
attachment's times; `.error` uses `WriteError.Timestamp`; `.last` uses the newest
`WriteResult.Timestamp`; `.rel` uses the relation's own times (see below);
`states.md`/`labels.md` use the **team's** times as a stable proxy (a collection
has no single mtime); `user.md`/README report **zero** (`api.User` has no time
fields; README is a generated doc). Construction goes through
`newRenderInode`/`lookupRenderFile` (parent is a `*BaseNode`) or `mountRenderFile`
(parent handed in as an `fs.InodeEmbedder`, for the `.meta`/`.error`/`.last`
helpers), all filling the Lookup `EntryOut` from the same `renderAttr()` the
`Getattr` uses — so a Lookup answer and a later stat can't disagree, the `attrNode`
guarantee extended to dynamic-size files. Unit-tested directly on the struct with
a stub closure (window clamp, write-open→`EACCES`, size/time reporting, zero-time),
no FUSE mount.

**Precursor — real `.rel` times (`IssueRelation` created/updated).** `.rel` files
used to fabricate `now()`; making them report real times required carrying the
relation's `createdAt`/`updatedAt` end-to-end, which nothing did. The
`issue_relations` table already had the columns and `UpsertIssueRelation` already
took them — the gap was above the DB: `api.IssueRelation` gained the two fields,
the `CreateIssueRelation` mutation (and the issue fragment) now select
`createdAt`/`updatedAt`, the create-persist writes the server's times (not
`now()`), and `GetIssueRelations`/`GetIssueInverseRelations`/`GetIssueRelationByID`
map them back onto the struct. (The orthogonal gap noted here at the time —
relations populated **only** by the local create handler, so UI-made relations
never appeared as `.rel` files — was closed in round 14: relations are now the
fourth detail-sync collection, see [[sync-reconcile-tail]].) `EmbeddedFileNode` (the actual `*.png`/`*.pdf`
bytes) stays out of `renderFile`: it is a lazy CDN byte-streamer, not a
render-closure file, and `api.EmbeddedFile` has no times either.

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
index. Its un-indexed twin is [[named-listing]].

### Named listing (`namedListing`)
The **deep module** owning the *entity-derived* filenames of a collection — the
un-indexed sibling of [[indexed-listing]]. Where that module derives a name from
an item's **position** in a sorted order, this one derives it from the item's
**identity**, so there is no `lessKey` and no sort. `namedListing[T]{items,
nameOf}` (`internal/fs/namedlisting.go`) exposes `entries()` (Readdir) and
`find(name)` (Lookup/Unlink/Rename/Create-overwrite), and each of the three
collections declares its listing via a `listing(items)` method (mirroring
`trio()`) that reuses the existing `documentFilename`/`labelFilename`/
`milestoneFilename` by value. It absorbed **13 hand-copied name-matching sites**
across `documents.go`/`labels.go`/`milestones.go` (5+5+3): every surface that
mapped a name to an entity re-derived and re-matched independently, so a
`sanitizeFilename` tweak in one could strand a file (listed but un-openable). The
caller fetches and passes the slice; the module is pure of the repo, so it is
unit-tested on literal slices, no mount.

**Ordering is the repo's job, never this module's.** The SQLite list queries
carry the `ORDER BY` (labels by name, documents by title, milestones by
`sort_order` — a *meaningful manual order*), so `namedListing` preserves the
`items` slice as given. A filename sort here would clobber milestones'
`sort_order` into alphabetical — a regression — so the module stays neutral to
order and owns only name derivation and matching.

**Collisions are first-match, emit-once — deliberately NOT dedup (the
load-bearing decision, settled on live evidence; a future review must not
re-suggest disambiguation).** `find` returns the first match and `entries()`
emits each derived name once (first wins), yielding a well-formed readdir
consistent with `find` by construction — and fixing the pre-existing sloppiness
where the hand-rolled loops emitted a *duplicate dirent* and leaned on the kernel
to collapse it. Why not disambiguate the second (`Bug (2).md`)? Because the mount
is a name-addressed projection of a source that *permits* duplicate names, and
the whole name→entity stack is already assume-first:

- **Documents** can't collide: `slug_id` is `UNIQUE NOT NULL` and
  `documentFilename` uses the slug first — the slug is their index.
- **Milestones** can't collide on name: Linear *enforces* per-project milestone-
  name uniqueness (verified in the product UI). The only residual is
  `sanitizeFilename` mangling an exotic name — narrow.
- **Labels** *can* collide: a workspace label (`team_id IS NULL`) and a team
  label share a directory (`WHERE team_id = ? OR team_id IS NULL`) and can share
  a name — but they **shadow each other in Linear's own product too**, so
  first-match faithfully mirrors the source (verified: two `testy-one` labels →
  one file in the mount).

A disambiguated `Bug (2).md` would be strictly worse than a shadow: it resolves
**nowhere**, because `ResolveMilestoneID` and `GetLabelByName` match the raw
entity `Name`, not the filename — an addressable file you can't assign to (a
decoy), not completeness. `indexedListing` escapes this only because
comments/updates are name-resolved *nowhere else*, so it can disambiguate freely;
milestones/labels are resolution keys, pinning the filename to the resolution
name. True per-file addressability would mean reworking name resolution end-to-
end — a separate change, not a listing collapse. Attachments were originally
excluded (two heterogeneous item types in one dir + stateful
`deduplicateFilename`) and later got their own heterogeneous sibling,
[[attachment-listing]].
`TestNamedListing*` guards the round-trip, the collision first-wins contract
(the shadow as a *tested* invariant), order preservation, and totality.

### Attachment listing (`attachmentListing`)
The **deep module** owning the filenames of the attachments directory — the
*heterogeneous* sibling of [[named-listing]] and [[indexed-listing]], covering
the collection those two excluded. The directory mixes two item types:
embedded files (CDN-backed bytes, named by filename) and external attachments
(`.link` files, named by sanitized title). `attachmentListing{embedded,
external}` (`internal/fs/attachmentlisting.go`) exposes `entries()` (Readdir)
and `find(name)` (Lookup) returning a tagged entry, and owns
`deduplicateFilename`, `sanitizeFilename`, and `linkName` (the `.link`
derivation the create surface's `.last` path and kernel-entry name reuse —
formerly restated at four sites). Before it, Readdir and Lookup each rebuilt
the dedup map independently, duplicate-titled externals emitted *duplicate
dirents* (kernel-collapsed shadowing), and the dedup algorithm had zero tests.

**Collisions are deduplicated (`foo (2).link`) — deliberately the opposite of
[[named-listing]]'s first-match/shadow policy, licensed by that policy's own
recorded rationale:** disambiguation is forbidden only where the filename is a
resolution key (labels/milestones); attachment names are resolution keys
nowhere, the same freedom `indexedListing` uses for comments. One counter
spans both families in listing order (embedded first, then external), so even
an embedded file literally named `foo.link` disambiguates against an external
titled `foo` instead of shadowing. `rm` on a deduplicated name deletes the
right entity — `find` returns the matched item and the node holds it through
Unlink. Dedup-suffix stability across calls comes from the repo (ordering is
the repo's job): the two list queries carry `id` tiebreakers
(`filename, id` / `created_at, id`), since equal sort keys are exactly the
dedup case. The caller fetches and passes the slices; Readdir stays
best-effort (a failed fetch lists that family empty) while Lookup
distinguishes not-found (`ENOENT`) from couldn't-look (`EIO`) via the
`listing(ctx, &fetchErr)` seam. Pure of the repo; unit-tested on literal
slices (`TestAttachmentListing*`: round-trip, cross-family dedup, extension
edges, linkName), no mount.

### Entity-directory manifest (`dirManifest`)
The **deep module** owning the *static* children of an entity directory — the
`issue.md`/`issue.meta`/`.error`/`.last`/`history.md` files and the
`comments`/`docs`/`children`/`attachments`/`relations` subdirs of an issue, and
the equivalents for a project/initiative. Where [[named-listing]] and
[[indexed-listing]] own a collection's *dynamic* (entity-derived) children, this
owns the *fixed framework* children — the static twin. Before it, each of the
three entity directories (`IssueDirectoryNode`, `ProjectNode`, `InitiativeNode`)
declared its children **twice**: once as a hardcoded `Readdir` `[]DirEntry`, once
as a `Lookup` `switch`/`if` chain — two hand-kept lists that could drift into a
file you can `ls` but not `open`. `dirManifest` (`internal/fs/manifest.go`) is
the single source: `entries()` (Readdir) and `find(name)` (Lookup) are both pure
projections of one `children []staticChild`, so they cannot disagree — the
listed⇔openable guarantee the listing modules already give dynamic children,
lifted one tier up to the skeleton.

**Self-describing, like the directory nodes `attrNode` forced.** The manifest is
a builder carrying the facts *every* child shares — `parent *BaseNode`, the
entity `id` (scopes `.error`/`.last`/`.meta` keys), the entity `created`/`updated`
times, and the child `timeout` (uniform within a directory: issue children 30s,
project/initiative children 0) — so each child declares only its difference. Five
typed constructors cover all 22 arms across the three directories: `subdir(name,
ino, node)` → `newDirInode(dirAttr(created,updated), ino, timeout)`; `file(name,
ino, build)` where `build` returns `(node, content, errno)` → `fileAttr`;
`metaFile(name, render)` → `lookupMetaFile`; `errorFile(name)`/`lastFile(name)` →
`lookupErrorFile`/`lookupSuccessFile`; `renderFile(name, ino, render)` →
`lookupRenderFile`. The two oddballs fold in with no special case: `issue.meta`'s
read-through closure is the `render` arg to `metaFile`, and `history.md`
(fetch-during-lookup) is a `renderFile()` — a read-only generated file rendered
fresh on each read (see [[render-file]]), whose closure fetches the issue history
and, on a transient failure, renders an empty file rather than failing the
lookup.

**find/build split — pure match, effectful build.** `find(name)` returns the
matched `staticChild` (pure — the anti-drift surface, unit-testable with no
mount because build closures are captured but not invoked); the caller then runs
`child.build(ctx, out)`, which touches a live inode. A matched-but-failed build
(`issue.md`'s `EIO` on a marshal failure) is **terminal** — the caller returns
that errno and does **not** fall through to the dynamic tail. This is why the
manifest is find/build and not a fused `(inode, ok)` like `lookupCollectionTrio`,
whose builds never fail. (`history.md`, being a `renderFile`, never fails the
lookup at all — it renders empty on a fetch error.)

**The dynamic tail stays outside.** Only `ProjectNode` has one (issue symlinks);
its `Readdir` appends symlink dirents after `entries()`, its `Lookup` runs the
symlink loop only on a `find` miss. Issue and initiative directories have no
dynamic tail.

**Folds the three hand-rolled dir `Getattr`s onto [[attr-construction]].** The
three entity dir nodes bypassed `newDirInode` — hand-building the Lookup
`EntryOut` at their six construction sites *and* hand-rolling a separate
`Getattr`, two attr copies per directory that had to agree. Embedding `attrNode`
and routing all six sites through `newDirInode(dirAttr(...), <dirIno>, 30s)`
deletes both and makes Lookup==Getattr by construction. It also normalized three
latent inconsistencies: the initiative dir was constructed with `Ino: 0`
(auto-assigned — now a stable `initiativeDirIno`), the issue-dir sites disagreed
on setting `Uid`/`Gid`, and the initiative dir set no entry timeout (mount
default) while its sibling entity dirs used 30s — **standardized to a uniform 30s
entity-dir tier** (a deliberate, recorded behavior change, not preservation:
initiative's unset read as an oversight, not a considered 0). The three dir-ino
wrappers use symmetric `issuedir`/`projectdir`/`initiativedir` prefixes,
registered in `TestInodeNamespaceDistinct`.

`TestDirManifestRoundTrip` (`internal/fs/manifest_test.go`) is the primary guard:
built in-memory from each dir node's `manifest()`, it asserts every `entries()`
name resolves via `find`, no duplicates, modes agree, and the exact child-name
set per directory (issue 10 / project 6 / initiative 6) as a change-detector. The
`nodeattr` anti-drift equality test gains the three entity-dir kinds; the
effectful `build` path stays covered by existing integration tests.

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
prune-if-clean`. It now lives in the shared package **`internal/reconcile`**
as `reconcile.Collection(ctx, reconcile.CollectionSpec[T])` — extracted from
`internal/sync` so the repo's SWR refresh path can join as a second caller (a
later step; neither `sync` nor `repo` imports the other, so the shared policy
lives between them). The package also hosts `PersistIssueDetails` (the
per-issue five-collection persist described below, deps = `{*db.Queries,
Extract hook}`) and the embedded-file `Extractor` (pure CDN-URL parse + the
HEAD/upsert I/O tail; nil `HTTPClient` = `http.DefaultClient`, injectable for
tests). Seven sites reconcile through it: `states` (upsert-only),
`labels`, `cycles`, `projects` (entity + `project_teams` junction + nested
milestones), `members` (entity + `team_members` junction),
`initiativeProjects` (junction-only), and `projectLabels` (the workspace
catalog — a complete unfiltered drain licenses its full-table prune). Each restated the same `clean`-flag idiom
by hand — declare `xClean := true`, loop upserting, `if xClean { Prune }` — so a
new metadata kind copy-pasted it and a forgotten flag would let a *partial* fetch
read as removals (silent data loss). `initiativeProjects` even hand-rolled a
fail-fast variant whose error the caller only logged.

Its interface is closures, no `*db.Store`: `reconcile.CollectionSpec[T]{Label,
Items, Upsert, Prune}`. `Upsert(ctx, T)` does whatever the kind needs — convert, entity
upsert, any junction upsert, nested sub-writes — and `Prune(ctx)` runs **once,
iff every `Upsert` returned nil**; a `nil` prune closure means no prune (the
`states` case). Semantics are uniform **log-and-continue**: a failed `Upsert` is
logged, marks the collection unclean, and does not abort the loop (so
`initiativeProjects` trades its fail-fast for continue — the observable outcome,
prune-skipped-on-any-failure, is unchanged and strictly more rows refresh). The
module never returns an error — sync is best-effort — but it does return
`clean bool` (true iff every upsert succeeded; a prune failure doesn't affect
it) so a caller can gate freshness stamps on it; the worker's metadata sites
ignore the return today. Pure over closures, so it is unit-tested with
recording closures asserting *prune runs iff clean* — no real store or API.

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

The **per-issue detail sync** (an issue's comments, documents, attachments,
relations, and inverse relations) reconciles through the same tail, five calls
per issue — written once as `reconcile.PersistIssueDetails`, which
`syncIssueDetailsBatch` calls per issue (its `clean` return is ignored until
the detail-outcome step consumes it). Here completeness is *page*-shaped
rather than *drain*-shaped: a full page (`len == IssueDetailsPageSize`, or
`IssueRelationsPageSize` for the relation connections) may be truncated, so
`PersistIssueDetails` composes a `pruneWhenComplete(complete, fn)` policy that
passes the real prune only on a short (provably complete) page and `nil`
otherwise — the module then adds its clean guard, so a detail prune fires
**iff clean AND complete**. This closed a silent-prune bug the hand-rolled version carried: it
gated the prune on page completeness alone, so a failed
comment/doc/attachment upsert (its `synced_at` left un-stamped) was deleted as
stale on the next complete page. Embedded-file extraction from a comment body
is the nested best-effort here (its own never-pruned `embedded_files` table),
analogous to milestones — it runs inside the comment `upsert` closure
regardless of the upsert result and cannot affect cleanliness.

**Relations (round 14) closed the last one-way surface**: previously only the
FUSE create handler wrote `issue_relations`, so a relation made in Linear's
own UI never appeared as a `.rel` file and one deleted there lingered as a
phantom. The details selection (one `IssueDetailsSelection` shared by the
single and batch queries, so they can't drift) now carries `relations` and
`inverseRelations`. A row is always stored from its **owner's** perspective
(`db.IssueRelationUpsertParams(rel, ownerID, relatedID)` — an inverse fetch
passes the ids swapped), and only the owner's fetch is a completeness set for
its rows: the outgoing collection prunes via `PruneIssueRelations` (scoped
`issue_id` + cutoff), the inverse collection is **upsert-only** (its rows are
owned by the other issue; pruning them here would delete against someone
else's partial view). `refreshIssueDetails` (the repo's SWR path) persists
both families best-effort like its siblings.

### Reverse conversion contract (hydrate-then-overlay)
Every DB→API reverse conversion in `internal/db/convert.go` **starts from the
`data` blob and overlays its queryable columns** (canonical statement at
`DBMilestoneToAPIProjectMilestone`). The columns are the authoritative source;
the blob carries any api field without a column, so a field added to an api
struct flows through with zero converter edits. Reading columns *only* — the
pre-contract shape of the State/Label/User/Cycle converters — silently dropped
JSON-only fields; for Cycle this was a **live bug**: the history arrays that
`cycle.md` renders its progress from were fetched, stored, and then zeroed on
every read (progress permanently 0/0). Overlay converters are best-effort on a
corrupt/legacy blob (fall back to columns — one bad row must not poison a
listing); pure-unmarshal converters (Issue, Project, …, whose blob is the whole
row) trivially satisfy the contract; `EmbeddedFile` is the excluded case (its
table has no blob). Label's `Team` overlays from the `team_id` column — the
authoritative source per the workspace-label churn fix — never from the blob's
copy. Each overlay converter is pinned by a `Test*RoundTrip` in
`convert_test.go` (forward → reverse == identity, plus corrupt-blob fallback).

### Rate budget (`rateBudget`)
The **deep module** governing Linear's hourly rate limits
(`internal/api/ratebudget.go`). Linear meters every key on TWO axes —
requests AND complexity points — and reports both on every response
(`X-RateLimit-{Requests,Complexity}-{Limit,Remaining,Reset}` plus
`X-Complexity`, this query's actual cost). The old client governed only
request count, at a hardcoded 1500/hr that matched neither the docs (5000)
nor the live limit (2500), and parsed the reset from a header Linear doesn't
send, as seconds — Linear sends per-axis epoch **milliseconds** — so the
complexity axis (the one that actually gets exhausted; it wedged the account
into `RATELIMITED` on 2026-07-06) was never governed and adaptive backoff was
dead. `Client.query` makes exactly two calls: `admit(op, priority)` before
sending, and on the returned admission `observe(headers)` /
`rateLimited(headers)` / `release()` after (idempotent; a deferred `release`
is the catch-all for early returns).

Inside: two windowed budgets `{limit, remaining, resetAt}` — **all read from
response headers, never hardcoded** — reconciled to server truth on every
round-trip (a restart self-heals on the first response); a per-op cost
predictor (last-seen `X-Complexity`, conservative 10k default for unmeasured
ops); a **priority-reserve ladder** (write > interactive > skeleton > list >
detail, each with a reserve floor as a fraction of the limit) so detail
fetches stop first and cold-start gentleness is emergent, not a mode —
blocked reads defer to the existing retry queues, blocked mutations wait
briefly for the window; a reserve-on-admit/release-on-settle **in-flight
semaphore** on both axes (concurrent admits see `remaining − inFlight −
reserve`); **optimistic refill** past `resetAt`; and a defensive
`RATELIMITED` snap-to-zero honoring the error's reset (bounded fallback when
headerless). Base tier comes from a static `opName → tier` intent map in the
module; `WithInteractive(ctx)` is the promotion mechanism for on-demand FS
reads — threaded at the **only two** synchronous user-blocking API calls
(`GetTeamDocuments`; the attachment-create re-check); every other FS read is
SQLite-first with background refresh, which must stay at base tier. The rule
at `WithInteractive`: promote at the moment of the call, never store a
promoted ctx or hand it to a goroutine. It collapsed
`checkRateLimitHeaders`, the inline `Tokens() < 2` write-reserve gate, the
`linearHourlyLimit` constant, and the token-count `LowBudget`;
`Client.LowBudget`/`RateLimitResetAt` now delegate to it (paginate's
`ErrBudget` gate and the worker's backoff consult real budget state), and the
micro-burst `rate.Limiter` survives only as a spike smoother re-sized from
the observed request limit. The injected clock (`now func() time.Time`) is
the test seam: `ratebudget_test.go` drives the ladder, reconcile, semaphore,
rollover, and RATELIMITED paths with a fake clock and synthetic headers — no
HTTP, no live API.

An unseen axis doesn't gate, so a fresh process would burst un-gated before
the first response lands. The **cold-start probe** closes that hole:
`Worker.probeBudget` (`internal/sync/worker.go`) fires one cheap
`GetViewer` (now on the worker's `APIClient` interface) synchronously at the
top of `run()`, so the probe's headers seed the budget strictly before the
first `syncAllTeams` issues expensive work. A `RATELIMITED` probe (account
already exhausted) marks the worker rate-limited — the backoff honors the
budget's reset, seeded by that very response — and sleeps until expiry
before starting sync (interruptible by ctx/Stop); any other probe failure
logs and proceeds. Probe sequencing and the delay path are unit-tested in
`worker_test.go` (`TestProbe*`); the client-level seed-then-defer wiring in
`client_test.go` (`TestViewerProbeSeedsBudget`).

### Repository read path (deliberately concrete — no interface)
The read path is the concrete `*repo.SQLiteRepository`; there is **no
Repository interface in front of it, on purpose** (round 14 decision — a
future review must not re-suggest one "for testability"). A 59-method
interface plus an in-memory mock existed for the project's whole life without
ever gaining a consumer: `LinearFS.repo` was always the concrete type, the
sync worker has its own narrow `APIClient` seam, and the mock's sole caller
was its own fixture's test — one adapter means a hypothetical seam, so both
were deleted (~900 lines). Two reasons a mock repo can't buy fs testability
here: fs write handlers hit `lfs.store.Queries()` directly (24 sites), so
write tests need real SQLite regardless; and node `Lookup`/`Readdir` need a
live inode tree (round-7 finding), which is why this codebase's testing
strategy is **pure-projection extraction** (`dirManifest.find`, the listing
modules) rather than mocking under node methods. If a real second adapter
ever appears (read-through cache, alternate store), re-extract the interface
from `SQLiteRepository` mechanically. The SQLite fixture helpers
(`fixtures.PopulateTestData` et al.) are the surviving, genuinely-used part
of the old scaffolding.

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
