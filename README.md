# LinearFS

Mount your Linear workspace as a FUSE filesystem. Browse and edit issues as markdown files.

## Features

- Browse teams, issues, projects, and labels as directories/files
- Issues rendered as markdown with YAML frontmatter
- Edit frontmatter to update issue status, assignee, priority, labels
- Full CRUD for comments, documents, and labels
- Create/archive issues and projects with standard filesystem operations
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
│       ├── .team.md             # Team metadata (read-only)
│       ├── .states.md           # Workflow states (read-only)
│       ├── .labels.md           # Labels reference (read-only)
│       ├── issues/
│       │   └── <identifier>/    # e.g., ENG-123/
│       │       ├── issue.md     # Issue content (read/write)
│       │       ├── comments/
│       │       │   ├── 001-*.md # Comments (read/write/delete)
│       │       │   └── new.md   # Write here to create comment
│       │       └── docs/
│       │           ├── *.md     # Issue documents (read/write/rename/delete)
│       │           └── new.md   # Write here to create document
│       ├── labels/              # Label management
│       │   ├── *.md             # Labels (read/write/rename/delete)
│       │   └── new.md           # Write here to create label
│       ├── docs/                # Team documents
│       │   ├── *.md             # Documents (read/write/rename/delete)
│       │   └── new.md           # Write here to create document
│       ├── cycles/              # Sprint cycles (read-only)
│       └── projects/
│           └── <project-slug>/
│               ├── .project.md  # Project metadata (read-only)
│               ├── docs/        # Project documents
│               └── ENG-*.md     # Symlinks to issues
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

## File Operations

LinearFS maps standard filesystem operations to Linear API actions:

### Issues

| Operation | Command | Effect |
|-----------|---------|--------|
| Create issue | `mkdir issues/"Issue title"` | Creates new issue with title |
| Archive issue | `rmdir issues/ENG-123` | Archives issue (soft delete) |
| Edit issue | Edit `issue.md` and save | Updates issue fields |

```bash
# Create a new issue
mkdir /mnt/linear/teams/ENG/issues/"Fix login bug"

# Archive an issue
rmdir /mnt/linear/teams/ENG/issues/ENG-123
```

### Comments

| Operation | Command | Effect |
|-----------|---------|--------|
| Read comments | `cat comments/001-*.md` | View comment content |
| Create comment | `echo "text" > comments/new.md` | Posts new comment |
| Edit comment | Edit comment file and save | Updates comment |
| Delete comment | `rm comments/001-*.md` | Deletes comment |

```bash
# Add a comment
echo "This needs review" > /mnt/linear/teams/ENG/issues/ENG-123/comments/new.md

# Delete a comment
rm /mnt/linear/teams/ENG/issues/ENG-123/comments/001-2025-01-10T14-30.md
```

### Documents

| Operation | Command | Effect |
|-----------|---------|--------|
| Create document | `echo "..." > docs/new.md` | Creates document with title from frontmatter |
| Edit document | Edit doc file and save | Updates title/content |
| Rename document | `mv docs/old.md docs/new.md` | Renames document title |
| Delete document | `rm docs/spec.md` | Deletes document |

```bash
# Create a document (with YAML frontmatter for title)
cat > /mnt/linear/teams/ENG/issues/ENG-123/docs/new.md << 'EOF'
---
title: "Technical Spec"
---
Document content here...
EOF

# Rename a document
mv docs/old-name.md docs/new-name.md
```

### Labels

| Operation | Command | Effect |
|-----------|---------|--------|
| Create label | `echo "..." > labels/new.md` | Creates label with name/color |
| Edit label | Edit label file and save | Updates name/color/description |
| Rename label | `mv labels/Bug.md labels/Defect.md` | Renames label |
| Delete label | `rm labels/OldLabel.md` | Deletes label |

```bash
# Create a new label
cat > /mnt/linear/teams/ENG/labels/new.md << 'EOF'
---
name: "Critical"
color: "#FF0000"
description: "Critical priority items"
---
EOF

# Rename a label
mv /mnt/linear/teams/ENG/labels/Bug.md /mnt/linear/teams/ENG/labels/Defect.md

# Delete a label
rm /mnt/linear/teams/ENG/labels/OldLabel.md
```

### Projects

| Operation | Command | Effect |
|-----------|---------|--------|
| Create project | `mkdir projects/"Project Name"` | Creates new project |
| Archive project | `rmdir projects/project-slug` | Archives project (soft delete) |

```bash
# Create a new project
mkdir /mnt/linear/teams/ENG/projects/"Q1 Launch"

# Archive a project
rmdir /mnt/linear/teams/ENG/projects/q1-launch
```

### Editing Labels on Issues

Edit the `labels` array in an issue's frontmatter:

```yaml
---
title: "Fix bug"
status: "In Progress"
labels:
  - Bug
  - Backend
  - Critical
---
```

Save the file to update the issue's labels in Linear.

## Caching Strategy

LinearFS caches data locally to minimize API calls and provide responsive filesystem operations. Since Linear's real-time sync engine is not exposed in their public API, LinearFS uses a TTL-based polling strategy with immediate invalidation on writes.

### How It Works

```
Read:   Filesystem → Cache hit? → Return cached data
                   → Cache miss? → Fetch from Linear API → Cache → Return

Write:  Filesystem → Update via Linear API → Invalidate relevant caches
```

### TTL Values

| Data Type | Default TTL | Rationale |
|-----------|-------------|-----------|
| Issues | 60s | Change frequently |
| Comments | 60s | Change frequently |
| Documents | 60s | Change frequently |
| Projects | 60s | Moderate change rate |
| Cycles | 60s | Change with issues |
| **States** | **10 minutes** | Workflow states rarely change |
| **Labels** | **10 minutes** | Team labels rarely change |
| **Users** | **10 minutes** | Team membership rarely changes |

### Write-Through Invalidation

When you modify data through LinearFS, caches are immediately invalidated:

- **Edit issue** → Invalidates team issues, my issues, user issues caches
- **Add comment** → Invalidates comment cache for that issue
- **Archive issue** → Invalidates team, my, and assignee issue caches
- **Create/delete label** → Invalidates team labels cache

This means your own changes appear immediately, but changes made by others (in the Linear app or API) appear after TTL expiry.

### FUSE Kernel Caching

In addition to the application-level cache, the Linux kernel caches filesystem attributes:

- **Entry timeout**: 30 seconds (directory listings)
- **Attr timeout**: 30 seconds (file metadata)

This reduces kernel-to-userspace calls but means `ls` output may lag slightly behind cache invalidations.

### Configuring TTL

Adjust the base TTL in your config file:

```yaml
cache:
  ttl: 60s    # Base TTL (states/labels/users get 10x this value)
```

Lower values = fresher data but more API calls. Higher values = better performance but staler data.

### Limitations

- **No real-time sync**: Linear's WebSocket-based sync engine is internal only; the public API offers webhooks (requires HTTP server) but not subscriptions
- **Eventual consistency**: Changes by teammates appear after TTL expiry
- **Rate limits**: Linear allows 1,500 requests/hour with API key auth

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
