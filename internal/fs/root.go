package fs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// RootNode is the root directory of the filesystem
type RootNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*RootNode)(nil)
var _ fs.NodeLookuper = (*RootNode)(nil)

func (r *RootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "README.md", Mode: syscall.S_IFREG},
		{Name: "teams", Mode: syscall.S_IFDIR},
		{Name: "users", Mode: syscall.S_IFDIR},
		{Name: "my", Mode: syscall.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (r *RootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "README.md":
		node := &ReadmeNode{}
		content := node.generateContent()
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0

	case "teams":
		node := &TeamsNode{lfs: r.lfs}
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "users":
		node := &UsersNode{lfs: r.lfs}
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "my":
		node := &MyNode{lfs: r.lfs}
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	default:
		return nil, syscall.ENOENT
	}
}

// ReadmeNode is a virtual file containing filesystem documentation
type ReadmeNode struct {
	fs.Inode
}

var _ fs.NodeGetattrer = (*ReadmeNode)(nil)
var _ fs.NodeOpener = (*ReadmeNode)(nil)
var _ fs.NodeReader = (*ReadmeNode)(nil)

func (r *ReadmeNode) generateContent() []byte {
	return []byte(readmeContent)
}

func (r *ReadmeNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := r.generateContent()
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(content))
	return 0
}

func (r *ReadmeNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (r *ReadmeNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := r.generateContent()
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}

const readmeContent = `# Linear Filesystem

This is a FUSE filesystem that exposes Linear issues as markdown files.

## Directory Structure

` + "```" + `
/
├── README.md              # This file
├── teams/                 # Issues organized by team
│   └── {KEY}/             # Team directory (e.g., ENG)
│       ├── .team.md       # Team metadata (read-only)
│       ├── .states.md     # Workflow states with IDs (read-only)
│       ├── .labels.md     # Available labels with IDs (read-only)
│       ├── issues/        # Team issues
│       │   └── {ID}/      # Issue directory (e.g., ENG-123/)
│       │       ├── issue.md     # Issue content (read/write)
│       │       └── comments/    # Issue comments
│       │           ├── 001-2025-01-10T14-30.md  # Comments (read-only)
│       │           └── new.md   # Write here to create comments
│       ├── cycles/        # Sprint cycles
│       │   ├── current -> Cycle-22  # Symlink to active cycle
│       │   └── {name}/    # Cycle directory (e.g., Cycle-22/)
│       │       ├── cycle.md    # Cycle metadata
│       │       └── {ID} -> ../../issues/{ID}  # Symlinks to issues
│       └── projects/      # Team projects
│           └── {slug}/    # Project directory
│               ├── .project.md  # Project metadata
│               └── {ID} -> ../../issues/{ID}  # Symlinks to issues
├── users/                 # Issues organized by assignee
│   └── {username}/
│       ├── .user.md       # User metadata (read-only)
│       └── {ID} -> ../../teams/{KEY}/issues/{ID}  # Symlinks
└── my/                    # Your personal views
    ├── assigned/          # All issues assigned to you
    │   └── {ID} -> ../../teams/{KEY}/issues/{ID}  # Symlinks to issue dirs
    ├── created/           # Issues you created
    └── active/            # Assigned issues not done/canceled
` + "```" + `

## Issue Files

Issue files are markdown with YAML frontmatter:

` + "```" + `markdown
---
id: abc123-...
identifier: ENG-123
title: Fix the bug
status: In Progress
assignee: user@example.com
priority: high
labels:
  - Bug
  - Urgent
dueDate: "2025-01-15"
estimate: 3
project: Project Name
created: "2025-01-01T00:00:00Z"
updated: "2025-01-10T00:00:00Z"
url: https://linear.app/...
---

Issue description in markdown...
` + "```" + `

### Editable Fields

You can edit these fields by modifying the frontmatter and saving:

| Field      | Description                          | Example              |
|------------|--------------------------------------|----------------------|
| title      | Issue title                          | "Fix login bug"      |
| status     | Workflow state name                  | "In Progress"        |
| assignee   | User email or name                   | "user@example.com"   |
| priority   | none/low/medium/high/urgent          | "high"               |
| dueDate    | Due date (ISO format or YYYY-MM-DD)  | "2025-01-15"         |
| estimate   | Point estimate                       | 3                    |

The description (content after frontmatter) is also editable.

### Read-Only Fields

These fields are informational and changes will be ignored:
- id, identifier, url, created, updated, labels, project

## Comments

Each issue has a comments/ subdirectory containing:

- Numbered comment files (001-timestamp.md, 002-timestamp.md, etc.)
- Comments are read-only and include author/timestamp in frontmatter

### Reading Comments

` + "```" + `bash
ls /teams/ENG/issues/ENG-123/comments/
cat /teams/ENG/issues/ENG-123/comments/001-2025-01-10T14-30.md
` + "```" + `

### Creating Comments

Write to new.md (or any new .md file) in the comments directory:

` + "```" + `bash
echo "My comment here" > /teams/ENG/issues/ENG-123/comments/new.md
` + "```" + `

The file is consumed: comment is created via API, and the new numbered
comment file will appear on the next directory listing.

## Creating Issues

Create a new issue by making a directory in a team's issues directory:

` + "```" + `bash
mkdir /teams/ENG/issues/"New issue title"
` + "```" + `

The directory name becomes the issue title.

## Symlinks

All issue symlinks point to issue directories (not files):

- /my/* → /teams/{KEY}/issues/{ID}/
- /users/* → /teams/{KEY}/issues/{ID}/
- /projects/* → /teams/{KEY}/issues/{ID}/
- /cycles/* → /teams/{KEY}/issues/{ID}/

Edits to issue.md anywhere affect the same underlying issue.

## Metadata Files

Hidden metadata files (.team.md, .states.md, .labels.md, .user.md, .project.md)
contain YAML frontmatter with IDs. Use these to look up valid values:

- Check .states.md for valid status names
- Check .labels.md for available labels
- Check .user.md for user IDs and emails

## Tips for AI Assistants

1. Read .states.md before changing issue status to get valid state names
2. Use the frontmatter 'id' field when you need to reference entities via API
3. The 'identifier' (e.g., ENG-123) is the human-readable issue key
4. All symlinks point to issue directories containing issue.md and comments/
5. Filter views: /my/active/ shows only non-completed assigned issues
6. To add a comment, write to comments/new.md in an issue directory
7. Use /cycles/current/ to access the active sprint cycle
`
