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

# Live API mode: Runs against real Linear API
LINEARFS_LIVE_API=1 LINEAR_API_KEY=xxx go test -v ./internal/integration/...

# Include write tests (creates/modifies issues in Linear)
LINEARFS_LIVE_API=1 LINEAR_API_KEY=xxx LINEARFS_WRITE_TESTS=1 go test -v ./internal/integration/...
```

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
  - `LinearFS` - Main struct with caches and server reference
  - `IssueFileNode` - Read/write issue.md files
  - `ErrorFileNode` - Read-only .error file for validation errors
  - `CommentsNode`/`CommentNode` - Comment listing and CRUD
  - `DocsNode`/`DocumentFileNode` - Document CRUD
  - `LabelsNode`/`LabelFileNode` - Label CRUD
  - `ProjectsNode`/`ProjectInfoNode` - Project management
  - `ByNode`/`FilteredIssuesNode` - Server-side filtered queries
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
- `IssueFields` - All issue fields (used by 11 queries)
- `CommentFields` - Comment fields (query, create, update)
- `DocumentFields` - Document fields (issue docs, project docs, create)
- `LabelFields` - Label fields (query, create, update)

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
```

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

**Solution**: Use the `parseTime()` helper in `internal/repo/sqlite.go` or `parseSQLiteTime()` in `internal/sync/worker.go`:

```go
var sqliteTimeFormats = []string{
    time.RFC3339,
    time.RFC3339Nano,
    "2006-01-02 15:04:05.999999999-07:00", // SQLite with timezone
    "2006-01-02 15:04:05.999999999Z07:00",
    "2006-01-02 15:04:05-07:00",
    "2006-01-02 15:04:05Z07:00",
    "2006-01-02 15:04:05",                 // SQLite without timezone
}
```

When parsing times from SQLite queries (especially `MAX()`, `MIN()` aggregates which return `interface{}`), always use these helpers rather than `time.Parse(time.RFC3339, s)` directly.

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
