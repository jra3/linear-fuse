# LinearFS

Mount your Linear workspace as a FUSE filesystem. Browse and edit issues as markdown files.

## Features

- Browse teams and issues as directories/files
- Issues rendered as markdown with YAML frontmatter
- Edit frontmatter to update issue status, assignee, priority
- Read and create comments on issues
- Multiple views: by team, by user, personal (assigned/created/active)

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
ls /mnt/linear/teams/ENG/issues/
cat /mnt/linear/teams/ENG/issues/ENG-123/issue.md

# View comments on an issue
ls /mnt/linear/teams/ENG/issues/ENG-123/comments/
cat /mnt/linear/teams/ENG/issues/ENG-123/comments/001-2025-01-10T14-30.md

# Add a comment
echo "My comment" > /mnt/linear/teams/ENG/issues/ENG-123/comments/new.md

# View your assigned issues
ls /mnt/linear/my/assigned/

# Unmount
fusermount -u /mnt/linear
# or Ctrl+C if running in foreground
```

## Directory Structure

```
/mnt/linear/
├── README.md                    # In-filesystem documentation
├── teams/
│   └── <team-key>/              # e.g., ENG, DES
│       ├── .team.md             # Team metadata
│       ├── .states.md           # Workflow states
│       ├── .labels.md           # Available labels
│       ├── issues/
│       │   └── <identifier>/    # e.g., ENG-123/
│       │       ├── issue.md     # Issue content (read/write)
│       │       └── comments/    # Issue comments
│       │           ├── 001-*.md # Numbered comments (read-only)
│       │           └── new.md   # Write here to create comment
│       ├── cycles/              # Sprint cycles
│       └── projects/            # Team projects
├── users/
│   └── <username>/              # Issues by assignee (symlinks)
└── my/
    ├── assigned/                # Issues assigned to you
    ├── created/                 # Issues you created
    └── active/                  # Non-completed assigned issues
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
```

### Editable Fields

- `title` - Issue title
- `status` - Workflow state name (check .states.md for valid values)
- `assignee` - User email or name
- `priority` - none/low/medium/high/urgent
- `dueDate` - Due date (ISO format)
- `estimate` - Point estimate
- Description (content after frontmatter)

## Creating Issues

Create a new issue by making a directory:

```bash
mkdir /mnt/linear/teams/ENG/issues/"New issue title"
```

## Comments

Read comments from the `comments/` subdirectory of any issue:

```bash
cat /mnt/linear/teams/ENG/issues/ENG-123/comments/001-2025-01-10T14-30.md
```

Create a comment by writing to `new.md`:

```bash
echo "This is my comment" > /mnt/linear/teams/ENG/issues/ENG-123/comments/new.md
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
