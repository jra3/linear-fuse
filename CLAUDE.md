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

## Architecture

LinearFS exposes Linear issues as a FUSE filesystem. The data flow is:

```
Linear API → api.Client → LinearFS (caching) → FUSE nodes → kernel → user
```

### Key Packages

- **internal/api**: GraphQL client for Linear. Handles pagination, queries, and mutations. Types in `types.go` mirror Linear's schema.

- **internal/fs**: FUSE filesystem implementation using go-fuse/v2. Each node type implements `fs.Node*` interfaces:
  - `RootNode` → `/` with `teams/`, `users/`, and `my/` directories
  - `TeamsNode`/`TeamNode` → `/teams/<KEY>/`
  - `IssuesNode` → `/teams/<KEY>/issues/` (lists issue directories)
  - `IssueDirectoryNode` → `/teams/<KEY>/issues/<ID>/` (contains issue.md and comments/)
  - `IssueFileNode` → `/teams/<KEY>/issues/<ID>/issue.md` (read/write)
  - `CommentsNode` → `/teams/<KEY>/issues/<ID>/comments/` (lists comments)
  - `CommentNode` → Individual comment files (read-only)
  - `NewCommentNode` → Handles comment creation via new.md
  - `MyNode`/`MyIssuesNode` → `/my/assigned/`, `/my/created/`, `/my/active/`
  - `UsersNode`/`UserNode` → `/users/<name>/` (symlinks to team issues)

- **internal/marshal**: Converts between Linear issues and markdown with YAML frontmatter. `IssueToMarkdown` for reads, `MarkdownToIssueUpdate` for writes (computes diff).

- **internal/cache**: Generic TTL cache wrapping a sync.Map.

- **internal/config**: Loads config from `~/.config/linearfs/config.yaml` with env var override for API key.

### Directory Structure

Issues are directories containing `issue.md` and a `comments/` subdirectory:

```
/teams/ENG/issues/
├── ENG-123/
│   ├── issue.md           # Issue content (read/write)
│   └── comments/
│       ├── 001-2025-01-10T14-30.md  # Comments (read-only)
│       ├── 002-2025-01-10T15-00.md
│       └── new.md         # Write here to create comment
```

### Write Flow

When a user edits an issue file:
1. `IssueFileNode.Write()` buffers changes
2. `IssueFileNode.Flush()` (on save) parses markdown via `marshal.MarkdownToIssueUpdate`
3. Status names resolved to IDs via `LinearFS.ResolveStateID`
4. `api.Client.UpdateIssue()` sends mutation to Linear
5. Cache invalidated for fresh data on next read

### Comment Creation Flow

1. User writes to `comments/new.md` (or creates any `.md` file)
2. `NewCommentNode.Write()` buffers content
3. `NewCommentNode.Flush()` calls `LinearFS.CreateComment()`
4. Comment cache invalidated, new comment appears on next listing

### File Attributes

Issue files get their mtime/ctime from Linear's `updatedAt`/`createdAt`. This is set in `Lookup()` methods via `out.Attr.SetTimes()`, not just `Getattr()`.

## Configuration

API key via environment variable `LINEAR_API_KEY` or config file:
```yaml
api_key: "lin_api_xxxxx"
cache:
  ttl: 60s
```
- I am ok with any and all breaking changes. we are FIRMLY in the prototype phase with this project