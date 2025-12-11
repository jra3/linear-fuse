# linear-fuse

Interact with Linear.app issues as a series of text files in your filesystem using FUSE.

## Overview

linear-fuse is a FUSE (Filesystem in Userspace) module that mounts Linear.app issues as markdown files with YAML frontmatter. This allows you to view, edit, and manage your Linear issues using your favorite text editor or command-line tools.

## Features

### Phase 1: Foundation ✅
- ✅ Read-only mount support
- ✅ Linear GraphQL API client
- ✅ In-memory caching with TTL
- ✅ YAML frontmatter for metadata
- ✅ CLI with Cobra/Viper

### Phase 2: Write Support ✅
- ✅ Edit frontmatter to update issues
- ✅ Update issue title, description, and priority

### Phase 3: Issue Creation ✅
- ✅ Create new issues by creating files
- ✅ Parse file content to extract issue details

### Phase 4-5: Future Enhancements
- [ ] Full directory structure (projects, teams, views)
- [ ] Comprehensive error handling
- [ ] Advanced filtering and views

## Installation

### Prerequisites

- Go 1.20 or later
- FUSE support on your system:
  - **Linux**: Install `fuse` or `fuse3` package
  - **macOS**: Install [macFUSE](https://osxfuse.github.io/)

### Build from Source

```bash
git clone https://github.com/jra3/linear-fuse.git
cd linear-fuse
go build -o linear-fuse ./cmd/linear-fuse
```

## Configuration

You need a Linear API key to use linear-fuse. You can generate one from your [Linear settings](https://linear.app/settings/api).

### Option 1: Environment Variable

```bash
export LINEAR_API_KEY="your-api-key-here"
```

### Option 2: Configuration File

Create `~/.linear-fuse.yaml`:

```yaml
api-key: your-api-key-here
```

### Option 3: Command-Line Flag

```bash
linear-fuse mount --api-key="your-api-key-here" /path/to/mountpoint
```

## Usage

### Mount the Filesystem

```bash
# Create a mountpoint directory
mkdir ~/linear

# Mount Linear issues
linear-fuse mount ~/linear
```

### Browse Issues

```bash
# List all issues
ls ~/linear/

# View an issue
cat ~/linear/ENG-123.md

# Edit an issue with your favorite editor
vim ~/linear/ENG-123.md
```

### Issue File Format

Each issue is represented as a markdown file with YAML frontmatter:

```markdown
---
id: issue-id-uuid
identifier: ENG-123
title: Fix bug in authentication
state: In Progress
priority: 1
assignee: John Doe
creator: Jane Smith
team: Engineering
labels:
  - bug
  - authentication
created_at: 2024-01-15T10:30:00Z
updated_at: 2024-01-16T14:45:00Z
---

This is the issue description in markdown format.

## Details

- Found in production
- Affects user login flow
```

### Editing Issues

You can edit the following fields:
- `title`: Issue title
- `description`: The content below the frontmatter
- `priority`: Priority level (0-4)

Changes are automatically synced to Linear when you save the file.

### Creating New Issues

Create a new issue by creating a new markdown file:

```bash
# Create a simple issue with just title and description
cat > ~/linear/NEW-ISSUE.md << EOF
Fix login bug

Users are experiencing errors when logging in with social accounts.
EOF
```

Or with full frontmatter:

```bash
cat > ~/linear/NEW-ISSUE.md << EOF
---
title: Implement dark mode
priority: 2
---

Add dark mode support across the application.

## Requirements
- Toggle in settings
- Persist user preference
- Update all components
EOF
```

The issue will be created in Linear when you save the file. The filename can be anything ending in `.md` - Linear will assign the actual identifier (e.g., `ENG-123`).

### Unmount

Press `Ctrl+C` in the terminal where linear-fuse is running, or:

```bash
# Linux
fusermount -u ~/linear

# macOS
umount ~/linear
```

## Architecture

```
linear-fuse/
├── cmd/
│   └── linear-fuse/         # Main application and CLI
│       ├── main.go
│       └── commands/
│           ├── root.go      # Root command with config
│           └── mount.go     # Mount command
├── pkg/
│   ├── linear/              # Linear API client
│   │   ├── client.go       # GraphQL client
│   │   ├── types.go        # Type definitions
│   │   └── issues.go       # Issue operations
│   └── fuse/                # FUSE filesystem
│       ├── fs.go           # Root filesystem
│       ├── file.go         # Issue file nodes
│       └── markdown.go     # Markdown conversion
└── internal/
    └── cache/               # Caching layer
        └── cache.go        # In-memory cache with TTL
```

## Tech Stack

- **[hanwen/go-fuse/v2](https://github.com/hanwen/go-fuse)**: FUSE bindings for Go
- **[Linear GraphQL API](https://developers.linear.app/docs/graphql/working-with-the-graphql-api)**: Linear API integration
- **[gopkg.in/yaml.v3](https://gopkg.in/yaml.v3)**: YAML frontmatter parsing
- **[spf13/cobra](https://github.com/spf13/cobra)**: CLI framework
- **[spf13/viper](https://github.com/spf13/viper)**: Configuration management

## Development

### Run in Debug Mode

```bash
linear-fuse mount --debug ~/linear
```

### Project Structure

The implementation follows a phased approach:

1. **Foundation**: Basic read-only FUSE filesystem with API client and caching
2. **Write Support**: Edit files to update issues
3. **Issue Creation**: Create files to create new issues
4. **Projects & Views**: Full directory structure with filtering
5. **Polish**: Error handling, documentation, and reliability improvements

## Limitations

- Currently supports basic issue operations (read, update, create)
- No support for comments, attachments, or sub-issues
- New issues are created in the first available team
- Cache TTL is fixed at 5 minutes
- No offline mode
- No support for deleting issues

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

See [LICENSE](LICENSE) file for details.
