# LinearFS

Mount your Linear workspace as a FUSE filesystem. Browse and edit issues as markdown files.

## Features

- Browse teams and issues as directories/files
- Issues rendered as markdown with YAML frontmatter
- Edit frontmatter to update issue status, assignee, priority
- Link issues using `[ENG-XXX]` syntax in content

## Installation

```bash
# Build from source
make build

# Install to ~/bin
make install
```

### Requirements

- Go 1.21+
- FUSE3 (`sudo pacman -S fuse3` on Arch)

## Usage

```bash
# Set your Linear API key
export LINEAR_API_KEY="lin_api_xxxxx"

# Mount the filesystem
linearfs mount /mnt/linear

# Browse your issues
ls /mnt/linear/teams/
ls /mnt/linear/teams/ENG/
cat /mnt/linear/teams/ENG/ENG-123.md

# View your assigned issues
ls /mnt/linear/my/assigned/

# Unmount
fusermount -u /mnt/linear
# or Ctrl+C if running in foreground
```

## Directory Structure

```
/mnt/linear/
├── teams/
│   └── <team-key>/          # e.g., ENG, DES
│       └── <identifier>.md  # e.g., ENG-123.md
└── my/
    └── assigned/            # Issues assigned to you
```

## Issue File Format

```markdown
---
id: "abc123-def456"
identifier: ENG-123
url: "https://linear.app/team/issue/ENG-123"
created: 2025-01-10T10:30:00Z
updated: 2025-01-11T14:22:00Z
title: "Fix authentication bug"
status: "In Progress"
assignee: "alice@example.com"
priority: high
labels:
  - bug
  - backend
---

The login flow fails when users attempt to authenticate with SSO.

See also [ENG-100] for related work.
```

## Configuration

Create `~/.config/linearfs/config.yaml`:

```yaml
api_key: "lin_api_xxxxx"  # or use LINEAR_API_KEY env var

cache:
  ttl: 60s

mount:
  default_path: /mnt/linear

log:
  level: info
```

## License

MIT
