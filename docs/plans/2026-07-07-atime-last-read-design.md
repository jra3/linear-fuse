# atime as persisted last-read tracking

**Status: spec — not built.** Deferred project recorded 2026-07-06 (memory:
atime-last-read-tracking). Today atime is decorative: `nodeAttr.fill` reports
`atime = mtime = UpdatedAt` uniformly (nodeattr.go:34), and
`nodeattr_test.go:50` pins that. The goal: an agent asks "which issues changed
since I last read them" and the filesystem can answer, across restarts.

## The one query this exists to serve

`mtime > atime` ⇔ "modified since last read" — the classic mail-file idiom
(`ls -lu`, `find -newerat`). That works only if atime is a *real, persisted*
last-read time and mtime stays `UpdatedAt` (already true).

## Design

### What counts as a read

A **content read of an entity file** — `Read` on `issue.md` (editBuffer),
`Read` on a renderFile that represents an entity (`issue.meta` no,
`history.md` arguably, comments/docs yes), `Read` on an embedded file.
Explicitly NOT a read: Lookup/Getattr (content is eagerly materialized at
Lookup for size — editbuffer.go:22 — so construction must not count), readdir,
and reads by the create/scratch machinery.

Hook points (all exist, all small): `editBuffer.Read`, `renderFile.Read`,
`EmbeddedFileNode.Read`. Each calls a new `lfs.readTracker.touch(kind, id)`.
Offset-0 reads only (a sequential read of a big file touches once).

### `readTracker` — the deep module

`internal/fs/readtracker.go`: `{mu, dirty map[key]time.Time}` +
`touch(kind, id)` + a flush loop. **Write-behind, debounced**: touch records
in-memory; a background flusher batches rows to SQLite every ~5s (and on
unmount/Close). A `grep -r` storm costs one map write per file per window,
not one SQLite write per Read — the write-amplification hazard the survey
flagged. Injectable clock + store seam; unit-tested with fakes.

Persistence: new table
```sql
CREATE TABLE IF NOT EXISTS read_log (
  kind TEXT NOT NULL, id TEXT NOT NULL, read_at DATETIME NOT NULL,
  PRIMARY KEY (kind, id));
```
Deliberately NOT a column on entity tables: entity rows are owned by the sync
worker's upsert/prune cycle (a prune would eat the read state; an upsert
conflict-clause would have to preserve it). A side table is join-cheap and
survives entity churn. Prune rows older than ~90 days opportunistically.

### Reporting

`nodeAttr`/`fileAttr` construction takes an optional `lastRead` (zero = fall
back to today's `UpdatedAt`, so files never read report atime == mtime — reads
as "not modified since last read", the conservative default; see decision 1).
The entity file nodes and dir nodes pass it from a batched
`GetLastReads(kind, ids)` repo call during listing/lookup. The kernel attr
timeouts (60s default / 30s entity tier) bound staleness — acceptable for the
target query, which is agent-paced, not interactive-precise.

### Constraints honored

- **Cycle symlinks keep atime = EndsAt** (symlink_test.go:72 — semantic
  encoding, untouched; symlinks aren't entity content files).
- `nodeattr_test.go:50` (atime == updatedAt) is rewritten to assert the
  fallback + the override path.
- The mount must not set `noatime`-style suppression (it doesn't).

## Consumers (same PR or fast-follow)

- `ls -lu` / `find -newerat` just work.
- Optional: `my/unread/` view (issues where `updated_at > read_at or read_at
  IS NULL`) — one `namedListing`-style symlink dir; makes the feature legible
  to agents without stat math. Generated README documents the contract.

## Tests

- Unit: readTracker debounce/flush/Close-flush with fake clock; offset-0
  gating; attr fallback-vs-override.
- Integration: read issue.md, remount (restart server), stat shows persisted
  atime < a subsequent issue update's mtime.

## Effort

Medium. New table + sqlc, one new module, three small hooks, attr plumbing,
README + tests.

## Decisions to grill

1. **Never-read files**: report atime = UpdatedAt (conservative: "nothing
   new") or atime = 0/epoch ("never read" is visible but ugly in ls)?
   Recommendation: UpdatedAt fallback; the `my/unread/` view carries the
   never-read distinction instead.
2. Which renderFiles count as entity reads (history.md? .meta? team.md?).
   Recommendation: only files whose mtime is an entity's UpdatedAt —
   issue.md, comments, docs, project.md, initiative.md, embedded files —
   keep sidecars out.
3. Multi-reader semantics: last-read is per-mount (single-user by design).
   If a second consumer ever matters, the table gains a reader column — not
   now.
4. Does touching atime require bumping `AttrTimeout` down for entity files?
   Recommendation: no — 30–60s staleness is fine for the target query.
