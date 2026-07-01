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

### Inode derivation (`ino`)
FUSE inode numbers were derived by 28 near-identical `fnv.New64a()` helpers spread
across 9 files, each hashing a hand-typed `"<prefix>:"+id` — nothing co-located the
prefixes, so nothing stopped two kinds accidentally sharing one (a real collision:
two entities → one inode → kernel confusion). Now one primitive `ino(namespace, id)`
(`internal/fs/inode.go`) owns the hashing; each per-kind helper is a one-liner
(`func commentIno(id string) uint64 { return ino("comment", id) }`), and `hash/fnv` is
imported in just two places — `inode.go` and `atomicwrite.go`'s `scratchIno` (which is
legitimately different: it keys off a parent inode + name, not a `prefix:id`). The
no-collision invariant is now *executable*, not merely auditable:
`TestInoNamespacesDistinct` calls every helper with one shared id and fails if any two
map to the same inode. Inodes are in-memory only (regenerated each mount, never
persisted), so re-namespacing `issueIno` — which alone used to hash the bare id — is
free. Lightest of the deepenings: it dedups the mechanism rather than reshaping an
interface.

### Rate-limit / retryable classification (`api.RateLimitedError` / `IsRetryable`)
Whether an error means "back off and retry" used to be re-derived by string-matching
in three layers: the API logged on `RATELIMITED`, the sync worker had `isRateLimitError`
(matched `RATELIMITED`/`rate limit`), and the fs create path matched `rate limit` +
`circuit breaker`. The knowledge of Linear's wire signals leaked upward as substring
checks. Now the **API seam classifies once**: `query()` returns a typed
`*RateLimitedError` (HTTP 429, a `RATELIMITED` GraphQL error, or a client-side deferral
to protect write capacity; it carries `RetryAt` from `X-RateLimit-Reset` when known) or
an unexported `transientError` (circuit breaker open). Downstream asks the type, not the
string: `api.IsRateLimited(err)` drives the sync worker's backoff; `api.IsRetryable(err)`
(rate-limit **or** circuit-breaker) drives the fs create path's `EAGAIN`. A limiter
`Wait` rejection (context cancelled, or a reservation that would exceed the deadline —
note the latter is *not* a context sentinel, so `errors.Is` misses it) is typed as a
`transientError` so it still classifies as retryable, preserving the create path's
`EAGAIN` (#131). `internal/api/errors.go`; unit-tested through the two `Is*` predicates.

### GraphQL client helpers (`paginate` / `mutateVoid` / `mutateEntity`)
`api.Client` had two shapes copy-pasted across ~40 methods. **`paginate[T]`**
(`internal/api/paginate.go`) owns the cursor loop every paginated read repeated —
loop, thread the cursor, accumulate nodes, stop on `!HasNextPage`; each query passes a
`fetchPage(cursor)` closure that keeps its own fully-typed decode struct, so the schema
binding is still compile-time checked. **`mutateVoid`** and **`mutateEntity[T]`**
(`internal/api/mutate.go`) own the mutation envelope: run the mutation, check the
`success` flag, and (for `mutateEntity`) decode the affected entity. The mutation's
payload key (`"issueLabelCreate"`) and entity key (`"issueLabel"`) — Linear schema names
that used to live in per-method struct tags — become string args; that trades one
compile-time-checked field name for ~680 fewer lines in client.go, and every one of
those names is exercised by an integration test, so a typo fails loudly there. These are
the query-layer siblings of the fs-layer generic tails ([[writable-content-buffer]]'s
cousins `commitWriteBack`/`commitMutation`): same move — a deep generic owns the
invariant control flow, the caller supplies only what varies.

### Writable content buffer (`contentBuffer`)
The in-memory byte content of every writable file node (`issue.md`, a comment/doc/
label/milestone `.md`, project/initiative `.md`, and the `_create` write-only nodes)
lives in one embedded **deep module**, `contentBuffer` (`internal/fs/contentbuffer.go`).
It owns the copy-pasted FUSE buffer mechanics — offset-expanding `writeAt`, grow/shrink
`truncate`, `bytes`/`size`, the `dirty` flag — **and** *when* content is materialized:
a `load func() ([]byte, error)` closure runs at most once on first access
(`ensureLoaded`), so "content is loaded before any length or byte is observed" is an
invariant of the module rather than a decision re-made in each node. That structurally
retires the old drift bug where edit-eager nodes' `Setattr` read a length before
loading while `IssueFileNode` alone guarded it. Three construction shapes: **lazy**
(loader set, `loaded` false — Issue/Project/Initiative, whose loader marshals/generates
from the node's entity), **eager pre-seed** (`buf` set, `loaded` true, no loader —
Comment/Label/Doc/Milestone, whose content is computed in `Lookup` for the entry size),
and **write-only** (nil loader → starts empty — the `_create` nodes).

It is deliberately **lock-free**: every method assumes the caller holds the *node's*
single `mu`. This is required, not incidental — the loader reads node fields (`n.issue`
etc.) that `Flush` also mutates, so buffer state and entity state must sit under one
lock; go-fuse dispatches Write/Setattr/Flush for one inode on concurrent goroutines
(no per-inode serialization), so that single lock is load-bearing. `dirty` lives in the
buffer; the write-only nodes' `created` idempotency guard stays on the node (a distinct
concern). After write-back a lazy node calls `invalidate()` (drop `loaded`, re-derive
from the fresh entity next access); an eager node calls `markClean()` (its old
`contentReady=false` was dead code — Read/Getattr never consulted it). The two
filehandle-based `_create` outliers (attachments, relations) keep their per-open handle
buffer and stay **out**; `scratchFileNode` (atomic-save temp) is a separate lifecycle.
Its interface is its test surface — a single-goroutine table test drives every method
with a stub loader, no mount/SQLite/API.

### SQLite time parsing (`db.ParseTime`)
The store is opened with `_time_format=sqlite`, so the driver hands back
space-separated timestamps (`2006-01-02 15:04:05...`) instead of RFC3339's `T`
separator — and `time.Parse(time.RFC3339, s)` fails *silently* on them. Reading any
stored timestamp therefore means trying an ordered list of layouts. `db.ParseTime(v
any) time.Time` (`internal/db/time.go`) is the **single home** of that invariant: it
accepts the shapes a timestamp arrives as (nil / already-parsed `time.Time` / string,
including what `MAX()`/`MIN()` aggregates return as `interface{}`) and yields the zero
time — read as "never" — for anything it can't interpret. It lives in `db` beside the
`_time_format=sqlite` config that *causes* the quirk and the sibling helpers
(`Now`, `ToNullTime`). It replaced two hand-kept copies of the format list plus parser
(one in `repo/sqlite.go`, one in `sync/worker.go`, the latter also hand-rolling its own
`time.Time`/`string` type-switch); the format list can no longer drift between callers.
Its interface is its test surface — one table test with no DB.
