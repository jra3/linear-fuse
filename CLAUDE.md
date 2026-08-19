# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
make build          # Build binary to bin/linearfs
make install        # Build and copy to ~/.local/bin
make test           # Run all tests
make test-cover     # Run tests with coverage summary
make coverage       # Generate full coverage report (unit + integration)
make coverage-html  # Open coverage report in browser
make run            # Build and mount to /tmp/linear
make fmt            # Format code
make lint           # Run golangci-lint

# Systemd service management (Linux)
make install-service   # Install binary + systemd service + env file
make uninstall-service # Remove systemd service (keeps config)
make enable-service    # Enable service to start on login
make disable-service   # Disable autostart
make start             # Start the service
make stop              # Stop the service
make restart           # Restart the service
make status            # Check service status
```

To reinstall while running:
```bash
# Linux (systemd)
make stop && make install && make start

# macOS (launchd)
launchctl stop com.linearfs.mount
make install
launchctl start com.linearfs.mount
```

To test manually:
```bash
./bin/linearfs mount -f -d /tmp/linear  # Foreground with debug
fusermount3 -u /tmp/linear              # Unmount
```

Integration tests:
```bash
# Default: Runs with SQLite fixtures (no API key needed, fast)
go test -v ./internal/integration/...

# Live API mode: Runs against real Linear API (the -timeout is a budget the
# setup gate shares — see below; go test's 10m default is too tight)
LINEARFS_LIVE_API=1 LINEAR_API_KEY=xxx go test -v -timeout 15m ./internal/integration/...

# Include write tests (creates/modifies issues in Linear)
LINEARFS_LIVE_API=1 LINEAR_API_KEY=xxx LINEARFS_WRITE_TESTS=1 go test -v -timeout 25m ./internal/integration/...
```

The make targets wrap the two live modes (the default offline suite is plain
`make test` and needs no key):

```bash
make integration-tests-ro    # live API, READS ONLY
make integration-tests-rw    # live API + writes: CREATES AND MODIFIES REAL LINEAR DATA
make integration-tests       # -ro then -rw (rw is a superset; the value is sequencing)
```

**Which workspace a live run touches is a choice, not an accident.** The live
targets read `.env` (gitignored; see `.env.example`) and OVERRIDE the ambient
environment, because a `LINEAR_API_KEY` exported in a shell is normally the key
for the workspace you actually work in — and a live run reads that entire
workspace, then in write mode creates issues and projects in it. `.env` carries
two lines: the test workspace's key, and `LINEARFS_TEST_TEAM`.

`LINEARFS_TEST_TEAM` is the interlock. Set it and `pickTestTeam` either finds
that team or fails setup naming the teams it did find; unset, it falls back to
"prefer TST, else the first team listed", which is how a run with the wrong key
in the environment quietly proceeded against a real work workspace that happened
to have a TST team. The CI write job sets it explicitly for the same reason (it
cannot read `.env`). Every live run also logs the resolved organization — and,
in write mode, says so loudly — before it mounts anything.

Live mode gates setup on the sync worker's persisted full-cycle stamp
(`sync.ScheduleKeyFullCycle`) before any test touches the mount: its SQLite cache
is a per-run temp db, so a cold start would otherwise race the background sync
and read empty listings. The gate spends up to a third of the binary's `-timeout`
waiting, which is why the live targets budget 15m/25m rather than the offline
suite's default. The stamp only means the cycle reached its end — `syncCycle`
log-and-continues past a failed fetch — so once it lands the gate also asserts
the test team is visible with a non-zero issue count, and fails setup rather than
letting an empty store become 300 unexplained test failures. A test team that
genuinely has no issues fails here by design.

Which mode a test runs in is declared in exactly one place —
`internal/integration/modes_test.go` — and there are exactly three spellings:

- `skipIfNoWriteTests(t)` — needs `liveAPIMode` **and** `LINEARFS_WRITE_TESTS=1`.
  It guards the tests that mutate a real workspace.
- `skipIfLiveAPI(t, why)` — skips when `liveAPIMode` is true, printing `why` in
  place of the test. It is the inverse interlock, and covers three kinds of
  fixture dependence: the write-contract tests that write *through the mount* to
  assert a structural invariant (#131, #137, #140, #142) — inert offline, but a
  real mutation live, and `TestMkdirIssueFailureIsLegible` leaks an issue it
  cannot clean up (`fixtureWriteContract`) — the read tests that assert
  against seeded rows like `TST-1` or `test-project` (`fixtureSeededData`) — and
  the tests whose assertion needs the mock mutator to model a specific BACKEND
  behavior the real one need not have (`WithBodyReformat`,
  `WithEmptyContentIgnored`), which take a `why` naming that dependency. #411 is
  what the last kind costs when it is guarded the other way:
  `TestClearProjectBodyIsRejectedLegibly` asserted the verdict for a server that
  declines an empty body, so live it failed the moment Linear applied one —
  after creating a real project to find that out.
  Never convert one guard to the other: `skipIfNoWriteTests` on a fixture-mode
  guard deletes it from the default offline suite, the only place it runs.
- **no guard** — the test runs in every mode, and therefore may not name a
  seeded row, nor REACH one through a package-local helper: a shared seeder like
  `seedIssueProbe` spells `TST-` on its callers' behalf, and
  `TestFixtureLiteralsCarryTheGuard` follows the call chain, so a caller carries
  the guard in its own body exactly as if it had spelled the literal inline (the
  failure names the chain). Take the identifier from the workspace with
  `someIssueID(t)` / `someProjectSlug(t)` (or their `…Dir` forms), which return
  the fixture's `TST-1`/`test-project` offline and the first listed
  issue/project live.

So `grep skipIf` over a test file answers "does this run live?" per test. #395
is what the alternative costs: four files asserted seeded fixture data with no
guard at all, and the first live dispatch of the write suite failed ~48 tests on
`no such file or directory` — every one of them a hardcoded path, none a bug.
A hardcode inside an `if err == nil` is the quieter form of the same defect: it
does not fail live, it passes while asserting nothing.

## Claude Code Integration

To allow Claude Code to read from the mounted filesystem, add these permissions to `~/.claude/settings.json`:

```json
{
  "allow": [
    "Read(~/linear/**)",
    "Bash(ls ~/linear/:*)",
    "Bash(cat ~/linear/:*)"
  ]
}
```

Also add to your global `~/.claude/CLAUDE.md`:
```markdown
# Linear.app issues via FUSE mount
- Linear data is available at ~/linear
- Read ~/linear/README.md for usage instructions
```

## Architecture

LinearFS exposes Linear as a FUSE filesystem with SQLite as the persistent data store:

```
Linear API → api.Client → Sync Worker → SQLite → Repository → LinearFS → FUSE
                ↓
           (mutations only)
```

**Data Flow:**
- **Sync Worker**: Background process fetches data from Linear API and stores in SQLite
- **Repository**: Abstraction layer for all data access (reads from SQLite)
- **LinearFS**: FUSE implementation that serves data via Repository
- **API Client**: Used directly only for mutations (create, update, delete)

### Directory Structure

```
~/linear/
├── teams/<KEY>/
│   ├── team.md, states.md, labels.md    # Team metadata (read-only)
│   ├── parent                            # Symlink to the parent team (sub-teams only)
│   ├── subteams/<KEY>                    # Sub-team symlinks; teams/ itself stays flat
│   ├── issues/
│   │   └── <ID>/
│   │       ├── issue.md                  # Issue content (read/write)
│   │       ├── .error                    # Last validation error (read-only)
│   │       ├── comments/*.md             # Comments (read/write/delete)
│   │       ├── docs/*.md                 # Documents (read/write/delete)
│   │       └── children/                 # Sub-issue symlinks
│   ├── by/                               # Filtered views
│   │   ├── status/<state>/               # Issues by workflow state
│   │   ├── label/<name>/                 # Issues by label
│   │   └── assignee/<name>/              # Issues by assignee (includes "unassigned")
│   ├── labels/*.md                       # Label CRUD via _create
│   ├── projects/<slug>/
│   │   ├── project.md                    # Project metadata (read/write)
│   │   ├── docs/*.md                     # Project documents
│   │   ├── updates/*.md                  # Status updates via _create
│   │   └── TEAM-*/                       # Issue symlinks
│   └── cycles/
│       ├── current                       # Symlink to active cycle
│       └── <name>/                       # Cycle directories with issue symlinks
├── initiatives/<slug>/
│   ├── initiative.md                     # Initiative metadata
│   ├── projects/                         # Linked project symlinks
│   └── updates/*.md                      # Status updates via _create
├── users/<name>/                         # Per-user issue symlinks
└── my/
    ├── assigned/, created/, active/      # Personal issue views
```

### Key Packages

- **internal/api**: GraphQL client for Linear. Types in `types.go` mirror Linear's schema. Queries in `queries.go`.
- **internal/fs**: FUSE implementation using go-fuse/v2. Key node types:
  - `LinearFS` - Main struct with caches, server reference, and the mutation seam
  - `IssueFileNode` - Read/write issue.md files (editable fields only)
  - `MetaFileNode` - Read-only `<entity>.meta` sidecar (server fields); render-through, see below
  - `ErrorFileNode` - Read-only `.error` file for the last failed write
  - `SuccessFileNode` - Read-only `.last` sidecar: YAML list of recent creates
  - `CommentsNode`/`CommentNode` - Comment listing and CRUD
  - `DocsNode`/`DocumentFileNode` - Document CRUD
  - `LabelsNode`/`LabelFileNode` - Label CRUD
  - `ProjectsNode`/`ProjectInfoNode` - Project management
  - `NewIssueCreateNode` - Write-only `issues/_create` full-object create trigger
  - `RecentNode` - `teams/{KEY}/recent/` newest-first issue view
  - `ByNode`/`FilteredIssuesNode` - Server-side filtered queries
  - `ReadmeNode` - Serves the generated `<mount>/README.md` (see "Generated README")
  - `MutationClient` (`mutationclient.go`) - Interface over the API's mutation
    methods; `LinearFS.mutator` defaults to the real client and is swappable in
    tests via `InjectTestMutationClient` (see `internal/testutil/mockmutation`)
- **internal/marshal**: Markdown ↔ Linear issue conversion with YAML frontmatter
- **internal/db**: SQLite database layer with sqlc-generated queries
  - `schema.sql` - Table definitions (well-commented, see inline docs)
  - `queries.sql` - sqlc query definitions
  - `convert.go` - API ↔ DB type conversion functions
- **internal/repo**: Repository pattern for data access
  - `repo.go` - Repository interface (~50 methods)
  - `sqlite.go` - SQLite-backed implementation
  - `mock.go` - In-memory mock for testing
- **internal/sync**: Background sync worker for Linear → SQLite
- **internal/cache**: Generic TTL cache (legacy, no longer imported - kept for reference)

### Generated README (agent-facing docs)

The `README.md` at the mount root (e.g. `~/linear/README.md`) is **generated at
runtime**, not a checked-in file. `ReadmeNode` (`internal/fs/root.go`) serves it,
and `generateReadme(mountPoint, userFeedback)` builds the content — the
directory-structure map, `<operations>`, frontmatter templates, `<permissions>`,
`<_create_behavior>`, `<validation_errors>`, and `<claude_code_instructions>`,
plus the opt-in `<agent_feedback_protocol>` (see Configuration). It is the
primary usage doc an LLM/agent reads to learn the filesystem, so it is part of
the product, not a comment.

**This means the generated README can silently lie about behavior.** When you
change a filesystem surface or contract — add/rename a virtual file (`.error`,
`.last`, `issue.meta`, `_create`), change permissions or read/write semantics,
add a view like `recent/`, or change the failure model — you MUST update
`generateReadme` in the same change so the doc matches the code. (A real example:
the doc claimed `_create` reads "return empty" while every `_create` node returns
`EACCES`.)

`TestGeneratedReadmeMatchesBehavior` (`internal/integration/readme_test.go`)
guards this: it reads the mounted `README.md` and asserts it doesn't carry the
stale claim, mentions the current surfaces (`.last`, `issue.meta`, `recent/`),
and that documented write-only files really are unreadable. Extend it when you
add a surface the README should describe.

### Architecture doc (orientation map)

`docs/ARCHITECTURE.md` is the verified prose+diagram orientation map for the
whole system (mermaid data-flow diagram, the two governing rules, the per-package
seam/contract descriptions). It was reconstructed and agent-verified on 2026-07-11
(262 claims checked, 34 corrections applied) — but, exactly like the generated
README, it can silently drift back into lying about the code.

**When a change adds, removes, or reshapes a package-level seam, contract, data
flow, or invariant — a new sub-module on `LinearFS`, a change to the read/write
data path, a new network caller, a change to the sync cycle or rate-budget tiers,
a moved responsibility between packages — you MUST update `docs/ARCHITECTURE.md`
in the same change** so the map matches the code. (A real example from this
backlog: making `teams/{KEY}/docs/` a synced surface removed the "reads never
touch the API" exception the doc had called out — both the rule text and the
persistence-layer description had to change with it.)

Prefer contracts over counts: method counts, file counts, and tuning constants
drift fastest, so state the invariant ("every mutation projects through the
entity's fragment") rather than the tally where you can. There is no automated
test guarding this doc — the discipline is the guard, so treat the architecture
doc as part of the diff, not a follow-up.

### Threat model (security reference)

`docs/THREAT-MODEL.md` is the security companion to the architecture doc: the
personas LinearFS defends against, the trust boundaries where untrusted data
(remote Linear strings, CDN bytes, the API key, the build path) crosses into the
process — plus the one where LinearFS tells an agent to send workspace-derived
content *out* of it (feedback mode, TB5) — and the explicit out-of-scope and
non-goal sections. It answers "is this change security-relevant?" and is the
reference the audit's review passes key off.

**When a change adds, removes, or reshapes a trust boundary — a new consumer of
remote data, a new on-disk artifact, a new network caller, a change to how a
remote string becomes a filename/symlink target/path, a new file mode, or a
change to the fetch/build/release path — you MUST update `docs/THREAT-MODEL.md`
in the same change**, exactly like the architecture doc. There is no automated
test guarding it; the discipline is the guard. The operator-facing `SECURITY.md`
(reporting, key/cache handling) points at it and moves far less often.

### GraphQL Query Design

Queries in `internal/api/queries.go` use GraphQL fragments to avoid field duplication:

```graphql
fragment IssueFields on Issue {
  id
  identifier
  title
  ...
}

query TeamIssues($teamId: String!) {
  team(id: $teamId) {
    issues { nodes { ...IssueFields } }
  }
}
```

Fragments are defined as Go constants and appended via string concatenation:

```go
const issueFieldsFragment = `fragment IssueFields on Issue { ... }`

var queryTeamIssues = `
query TeamIssues($teamId: String!) {
  team(id: $teamId) {
    issues { nodes { ...IssueFields } }
  }
}
` + issueFieldsFragment
```

Available fragments:
- `IssueFields` / `IssueFieldsLite` - Issue fields; the Lite variant (no relations) also backs the create mutation
- `CommentFields` - Comment fields (query, create, update)
- `DocumentFields` - Document fields (issue docs, project docs, create)
- `LabelFields` - Label fields (query, create, update)
- `AttachmentFields` - Attachment fields (detail query, create, link)
- `ProjectMilestoneFields` - Milestone fields (nested in project queries, milestone query, create, update)
- `ProjectUpdateFields` / `InitiativeUpdateFields` - Status-update fields (query + create)
- `UserFields` - User fields wherever whole users are listed (team members + drain page, workspace users + drain page, viewer); assignees/owners keep narrower inline sets
- `CycleFields` - Cycle fields (combined team metadata query + drain page)
- `TemplateFields` - Template identity for a team's default templates (`defaultTemplateForMembers` / `defaultTemplateForNonMembers` / `defaultProjectTemplate` on the teams query); `templateData` is deliberately not selected — a prefilled entity would be a content surface, not team metadata
- `InitiativeFields` - Initiative scalar fields (workspace query + drain page, single-initiative query, lean-cycle initiatives probe); the nested projects connection stays inline per query (page sizes differ; the probe deliberately selects none)

A combined query and its drain-page twin MUST project through the same
fragment — a field added to one but not the other means nodes past page one
silently carry zero values.

**Every mutation that returns an entity must project it through the entity's
fragment, not an inlined field list** — an inlined copy silently drifts when the
fragment gains a field (a real instance: the attachment create/link mutations
had drifted, omitting `metadata` and `creator`). A fragment canonicalizes to one
field set, so a created entity carries the same fields a fetched one does.

When adding new fields to an entity, update the corresponding fragment.

### Write Flow

1. User edits file → `Write()` buffers changes
2. On save → `Flush()` parses content, calls Linear API
3. **Flush handler upserts to SQLite** for immediate visibility
4. Internal caches invalidated (`InvalidateTeamIssues`, `InvalidateFilteredIssues`, etc.)
5. Kernel cache invalidated via `server.InodeNotify()` / `server.EntryNotify()`
6. Subsequent reads see fresh data immediately

**Architecture principle**: API and DB layers are intentionally decoupled. The `api.Client` methods only call the Linear API; they do not touch SQLite. Write handlers (`Flush`, `Mkdir`, etc.) are responsible for upserting to SQLite after successful API calls. This keeps concerns separated and makes the data flow explicit.

### Validation Errors

Invalid frontmatter values return `EINVAL` and store a descriptive error message readable via `.error`:

```go
// In Flush() - set error and return EINVAL
lfs.SetIssueError(issueID, fmt.Sprintf("Field: %s\nValue: %q\nError: %s", field, value, errMsg))
return 0, syscall.EINVAL

// On successful write - clear the error
lfs.ClearIssueError(issueID)
```

The `.error` file is implemented by `ErrorFileNode` - a read-only virtual file that reads from `LinearFS.issueErrors` map. This makes validation failures visible to LLMs and scripts that can't easily parse FUSE error codes.

### Cache Invalidation

After writes, both internal and kernel caches must be invalidated:
- Internal: `lfs.issueCache.Delete()`, `lfs.InvalidateFilteredIssues()`, etc.
- Kernel: `lfs.InvalidateKernelInode(ino)`, `lfs.InvalidateKernelEntry(parent, name)`

Each writable node type has stable inode generation via fnv hash (e.g., `issueIno()`, `commentIno()`).

**Critical for create/delete operations**: Directory listings are cached on the directory inode. You must call `InvalidateKernelInode(dirIno)` to refresh `readdir` results - `InvalidateKernelEntry` alone only clears individual name lookups.

```go
// After creating a document - invalidate directory inode AND entries
lfs.InvalidateKernelInode(docsDirIno(issueID))  // Clears directory listing
lfs.InvalidateKernelEntry(docsDirIno(issueID), "_create")
lfs.InvalidateKernelEntry(docsDirIno(issueID), newFilename)

// After deleting - also delete from SQLite
lfs.store.Queries().DeleteDocument(ctx, docID)  // Immediate visibility
lfs.InvalidateKernelInode(docsDirIno(issueID))
lfs.InvalidateKernelEntry(docsDirIno(issueID), filename)
```

**Delete operations**: Must delete from both API and SQLite. The `api.Client` methods only call the Linear API; delete handlers must also call `store.Queries().DeleteX()` for immediate visibility.

## Configuration

API key via `LINEAR_API_KEY` env var or `~/.config/linearfs/config.yaml`:
```yaml
api_key: "lin_api_xxxxx"
cache:
  ttl: 60s
user_feedback: false   # or USER_FEEDBACK=1
```

`USER_FEEDBACK` (env, or `user_feedback` in the file; default off) appends the
agent self-reporting protocol to the generated README — the agent reading the
mount is told to file friction with these contracts as a `dx-friction` issue on
this repo. Off means off: the generated README is byte-for-byte unchanged, so the
protocol text lives in one static const (`agentFeedbackProtocol` in
`internal/fs/root.go`) appended after `generateReadme`'s template.

That const is an outbound data path from a private workspace to a public repo
(`docs/THREAT-MODEL.md`, TB5), so its REDACTION block — report the shape, not the
payload — is load-bearing: keep it, and keep the REPORT BODY bullets consistent
with it. `internal/fs/readme_test.go` pins the rule.

## Linear API Reference

The full Linear GraphQL schema is available locally at `docs/linear-schema.graphql` (gitignored).

To refresh the schema:
```bash
curl -s "https://raw.githubusercontent.com/linear/linear/master/packages/sdk/src/schema.graphql" > docs/linear-schema.graphql
```

Key input types for mutations:
- `IssueUpdateInput` - Use `labelIds` to set labels, `removedLabelIds` to clear (not empty array)
- `IssueCreateInput` - Fields for creating new issues
- `CommentCreateInput` / `CommentUpdateInput` - Comment mutations

## Database Design

SQLite serves as the persistent cache layer. See `internal/db/schema.sql` for table definitions.

### Key Principles

1. **Hybrid Column + JSON Storage**: Extract queryable fields as columns, store full API response in `data JSON`
2. **Denormalization**: Store both IDs and names to avoid joins (e.g., `state_id` + `state_name`)
3. **Sync Metadata**: Every table has `synced_at` for staleness detection

### Time Handling

**Important**: SQLite and Linear's GraphQL API use different time formats.

| Source | Format | Example |
|--------|--------|---------|
| Linear API | RFC3339 | `2025-12-23T21:35:36.017Z` |
| SQLite | Space-separated | `2025-12-23 21:35:36.017+00:00` |

The SQLite driver is configured with `_time_format=sqlite` which returns space-separated timestamps instead of RFC3339's `T` separator. This causes `time.Parse(time.RFC3339, s)` to fail silently.

**Solution**: Use the canonical helpers in `internal/db/timeparse.go` —
`db.ParseSQLiteTime(string)` and `db.ParseSQLiteTimeAny(interface{})` (the
latter for `MAX()`/`MIN()` aggregates, which return `interface{}`). They try
every layout a timestamp can arrive in (RFC3339 variants plus the
space-separated SQLite forms). `internal/repo/sqlite.go`'s `parseTime` is a
thin wrapper over `ParseSQLiteTimeAny`. Never call
`time.Parse(time.RFC3339, s)` directly on a value read from SQLite.

### Adding New Tables

```sql
CREATE TABLE IF NOT EXISTS new_entity (
    id TEXT PRIMARY KEY,
    -- Extract columns for querying
    parent_id TEXT NOT NULL,
    name TEXT NOT NULL,
    -- Timestamps
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    -- Full API response
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_new_entity_parent ON new_entity(parent_id);
```

After schema changes:
1. Update `internal/db/queries.sql` with CRUD queries
2. Run `sqlc generate`
3. Add conversion functions to `internal/db/convert.go`
4. Add repository methods to `internal/repo/repo.go` and implementations

## Development Notes

- Breaking changes are acceptable - this is a prototype
- Integration tests use TST team by preference (falls back to first team)
- Test cache TTL is 100ms for fast tests; waits removed after filesystem writes

## Agent skills

### Issue tracker

Issues live as GitHub issues on `jra3/linear-fuse`, driven with the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical triage roles, each label string equal to its name. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one root `CONTEXT.md` plus `docs/adr/`. See `docs/agents/domain.md`.
