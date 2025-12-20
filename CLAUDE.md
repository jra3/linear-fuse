# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
make build          # Build binary to bin/linearfs
make install        # Build and copy to ~/bin
make test           # Run all tests
make run            # Build and mount to /tmp/linear
make fmt            # Format code
make lint           # Run golangci-lint
```

To test manually:
```bash
./bin/linearfs mount -f -d /tmp/linear  # Foreground with debug
fusermount3 -u /tmp/linear              # Unmount
```

Integration tests (requires LINEAR_API_KEY):
```bash
LINEARFS_INTEGRATION=1 go test -v ./internal/integration/...
LINEARFS_INTEGRATION=1 LINEARFS_WRITE_TESTS=1 go test -v ./internal/integration/...  # Include write tests
```

## Claude Code Integration

To allow Claude Code to read from the mounted filesystem, add these permissions to `~/.claude/settings.json`:

```json
{
  "allow": [
    "Read(//mnt/linear/**)",
    "Bash(ls /mnt/linear/:*)",
    "Bash(cat /mnt/linear/:*)"
  ]
}
```

Also add to your global `~/.claude/CLAUDE.md`:
```markdown
# Linear.app issues via FUSE mount
- Linear data is available at /mnt/linear
- Read /mnt/linear/README.md for usage instructions
```

## Architecture

LinearFS exposes Linear as a FUSE filesystem:

```
Linear API → api.Client → LinearFS (caching) → FUSE nodes → kernel → user
```

### Directory Structure

```
/mnt/linear/
├── teams/<KEY>/
│   ├── team.md, states.md, labels.md    # Team metadata (read-only)
│   ├── issues/
│   │   └── <ID>/
│   │       ├── issue.md                  # Issue content (read/write)
│   │       ├── comments/*.md             # Comments (read/write/delete)
│   │       ├── docs/*.md                 # Documents (read/write/delete)
│   │       └── children/                 # Sub-issue symlinks
│   ├── by/                               # Filtered views
│   │   ├── status/<state>/               # Issues by workflow state
│   │   ├── priority/<level>/             # Issues by priority
│   │   ├── assignee/<email>/             # Issues by assignee
│   │   ├── label/<name>/                 # Issues by label
│   │   └── unassigned/                   # Unassigned issues
│   ├── labels/*.md                       # Label CRUD via new.md
│   ├── projects/<slug>/
│   │   ├── project.md                    # Project metadata (read/write)
│   │   ├── docs/*.md                     # Project documents
│   │   └── updates/*.md                  # Status updates via new.md
│   └── cycles/<name>/                    # Sprint/cycle issues
├── initiatives/<slug>/
│   ├── initiative.md                     # Initiative metadata
│   ├── projects/                         # Linked project symlinks
│   └── updates/*.md                      # Status updates via new.md
├── users/<name>/                         # Per-user issue symlinks
└── my/
    ├── assigned/, created/, active/      # Personal issue views
```

### Key Packages

- **internal/api**: GraphQL client for Linear. Types in `types.go` mirror Linear's schema. Queries in `queries.go`.
- **internal/fs**: FUSE implementation using go-fuse/v2. Key node types:
  - `LinearFS` - Main struct with caches and server reference
  - `IssueFileNode` - Read/write issue.md files
  - `CommentsNode`/`CommentNode` - Comment listing and CRUD
  - `DocsNode`/`DocumentFileNode` - Document CRUD
  - `LabelsNode`/`LabelFileNode` - Label CRUD
  - `ProjectsNode`/`ProjectInfoNode` - Project management
  - `ByNode`/`FilteredIssuesNode` - Server-side filtered queries
- **internal/marshal**: Markdown ↔ Linear issue conversion with YAML frontmatter
- **internal/cache**: Generic TTL cache with `DeleteByPrefix` for bulk invalidation

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
3. Internal caches invalidated (`InvalidateTeamIssues`, `InvalidateFilteredIssues`, etc.)
4. Kernel cache invalidated via `server.InodeNotify()` / `server.EntryNotify()`
5. Subsequent reads see fresh data immediately

### Cache Invalidation

After writes, both internal and kernel caches must be invalidated:
- Internal: `lfs.issueCache.Delete()`, `lfs.InvalidateFilteredIssues()`, etc.
- Kernel: `lfs.InvalidateKernelInode(ino)`, `lfs.InvalidateKernelEntry(parent, name)`

Each writable node type has stable inode generation via fnv hash (e.g., `issueIno()`, `commentIno()`).

## Configuration

API key via `LINEAR_API_KEY` env var or `~/.config/linearfs/config.yaml`:
```yaml
api_key: "lin_api_xxxxx"
cache:
  ttl: 60s
```

## Development Notes

- Breaking changes are acceptable - this is a prototype
- Integration tests use TST team by preference (falls back to first team)
- Test cache TTL is 100ms for fast tests; waits removed after filesystem writes
