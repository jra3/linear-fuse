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
		{Name: "initiatives", Mode: syscall.S_IFDIR},
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

	case "initiatives":
		node := &InitiativesNode{lfs: r.lfs}
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

Issues as markdown files. Edit frontmatter to update Linear.

## Quick Reference

| Task | Command |
|------|---------|
| List teams | ` + "`" + `ls /teams/` + "`" + ` |
| My active issues | ` + "`" + `ls /my/active/` + "`" + ` |
| List initiatives | ` + "`" + `ls /initiatives/` + "`" + ` |
| Issues by status | ` + "`" + `ls /teams/ENG/by/status/Todo/` + "`" + ` |
| Read issue | ` + "`" + `cat /teams/ENG/issues/ENG-123/issue.md` + "`" + ` |
| Update issue | Edit issue.md frontmatter, save |
| Add comment | ` + "`" + `echo "text" > .../comments/new.md` + "`" + ` (write-only) |
| Create document | ` + "`" + `echo "text" > .../docs/"Title.md"` + "`" + ` (filename = title) |
| Create issue | ` + "`" + `mkdir /teams/ENG/issues/"Title"` + "`" + ` |
| Create project | ` + "`" + `mkdir /teams/ENG/projects/"Name"` + "`" + ` |
| Archive issue | ` + "`" + `rmdir /teams/ENG/issues/ENG-123` + "`" + ` |
| Archive project | ` + "`" + `rmdir /teams/ENG/projects/{slug}` + "`" + ` |
| View sub-issues | ` + "`" + `ls /teams/ENG/issues/ENG-123/children/` + "`" + ` |
| Post project update | ` + "`" + `echo "text" > .../updates/new.md` + "`" + ` (write-only) |
| Post initiative update | ` + "`" + `echo "text" > .../updates/new.md` + "`" + ` (write-only) |
| Edit existing comment | Edit ` + "`" + `comments/{id}.md` + "`" + ` directly |
| Edit existing document | Edit ` + "`" + `docs/{slug}.md` + "`" + ` directly |
| Search issues | ` + "`" + `ls /teams/ENG/search/bug/` + "`" + ` |
| Multi-word search | ` + "`" + `ls /teams/ENG/search/login+error/` + "`" + ` (+ = space) |
| Search my issues | ` + "`" + `ls /my/assigned/search/experiment/` + "`" + ` |
| Search filtered view | ` + "`" + `ls /teams/ENG/by/status/Todo/search/bug/` + "`" + ` |

## File Permissions

Use ` + "`" + `ls -l` + "`" + ` to see what operations are allowed:

| Permission | Meaning | Example |
|------------|---------|---------|
| ` + "`" + `-r--r--r--` + "`" + ` | Read-only | team.md, states.md, initiative.md |
| ` + "`" + `-rw-r--r--` + "`" + ` | **Editable** | issue.md, project.md, existing docs/comments |
| ` + "`" + `--w-------` + "`" + ` | Write-only trigger | new.md (creates new items) |
| ` + "`" + `lrwxrwxrwx` + "`" + ` | Symlink | Issues in cycles/projects/filtered views |

**Important:** Existing documents and comments are editable. Edit them directly—don't
write to new.md to update existing content.

## Directory Structure

` + "```" + `
/teams/{KEY}/
├── team.md, states.md, labels.md  # read-only metadata
├── by/status|label|assignee/      # symlinks to issues
├── search/{query}/                # full-text search results
├── issues/{ID}/
│   ├── issue.md                   # EDITABLE
│   ├── comments/
│   │   ├── new.md                 # write-only: creates comment
│   │   └── {id}.md                # EDITABLE: existing comments
│   ├── docs/
│   │   ├── "Title.md"             # create: filename becomes title
│   │   └── {slug}.md              # EDITABLE: existing documents
│   └── children/                  # symlinks to sub-issues
├── cycles/{name}/                 # symlinks to issues
└── projects/{slug}/
    ├── project.md                 # EDITABLE
    ├── docs/                      # same as issue docs
    └── updates/
        ├── new.md                 # write-only: posts update
        └── {id}.md                # read-only: existing updates

/initiatives/{slug}/
├── initiative.md                  # read-only
├── projects/                      # symlinks to projects
└── updates/                       # same as project updates

/users/{name}/                     # symlinks to issues
/my/assigned|created|active/       # symlinks to your issues
` + "```" + `

## Issue Frontmatter

` + "```" + `yaml
---
identifier: ENG-123
title: "Fix bug"          # editable
status: "In Progress"     # editable (see states.md)
assignee: "user@example"  # editable
priority: high            # editable: none/low/medium/high/urgent
labels:                   # editable (see labels.md)
  - Bug
  - Backend
due: "2025-01-15"         # editable (YYYY-MM-DD)
estimate: 3               # editable
parent: ENG-100           # editable (parent issue identifier)
project: "My Project"     # editable (project name)
milestone: "Phase 1"      # editable (milestone within project)
cycle: "Sprint 42"        # editable (cycle/sprint name)
---
Description here (editable)
` + "```" + `

Read-only: id, identifier, url, created, updated

## Sub-Issues

Issues can have parent/child relationships:
- View children: ` + "`" + `ls /teams/ENG/issues/ENG-123/children/` + "`" + `
- Set parent: Add ` + "`" + `parent: ENG-100` + "`" + ` to frontmatter
- Remove parent: Delete the parent line from frontmatter

## Projects and Initiatives

Projects can be linked to initiatives by editing project.md:
` + "```" + `yaml
---
initiatives:
  - "Q1 Goals"
  - "Platform Improvements"
---
` + "```" + `

Add/remove initiative names to link/unlink the project.

## Status Updates

Post status updates to projects or initiatives by writing to updates/new.md:
` + "```" + `bash
# Project update
echo "Sprint completed" > /teams/ENG/projects/{slug}/updates/new.md

# Initiative update
echo "Q1 goals on track" > /initiatives/{slug}/updates/new.md
` + "```" + `

With health status (onTrack, atRisk, offTrack):
` + "```" + `yaml
---
health: atRisk
---
Blocked on dependency from Team B.
` + "```" + `

Existing updates are read-only: ` + "`" + `001-2025-01-15-ontrack.md` + "`" + `

## Creating Documents

Create documents by writing to a filename in the docs/ directory:

` + "```" + `bash
# Filename becomes the title
echo "content" > docs/"My Document Title.md"

# Dashes convert to spaces
echo "content" > docs/technical-spec.md  # title: "technical spec"
` + "```" + `

Title priority:
1. ` + "`" + `title:` + "`" + ` in YAML frontmatter (if present in content)
2. First ` + "`" + `# Heading` + "`" + ` in content (if present)
3. Filename (minus .md, dashes become spaces)

## Search

Search issues using virtual directories:

` + "```" + `bash
# Team-wide search (uses SQLite FTS5)
ls /teams/ENG/search/bug/
ls /teams/ENG/search/login+error/   # + = space

# Scoped search within filtered views
ls /my/assigned/search/experiment/
ls /teams/ENG/by/status/Todo/search/urgent/
ls /teams/ENG/by/label/Bug/search/login/
` + "```" + `

Search queries match against issue identifier, title, and description.
Results are symlinks pointing to the actual issue directories.

**Query encoding:**
- Use ` + "`" + `+` + "`" + ` for spaces: ` + "`" + `auth+token` + "`" + ` searches for "auth token"
- Use ` + "`" + `*` + "`" + ` for prefix matching: ` + "`" + `ENG-12*` + "`" + ` matches ENG-12, ENG-120, ENG-123

## Notes

- Check states.md for valid status names before changing status
- Check labels.md for available label names
- **Invalid values fail the write** (unknown status, labels, assignee, etc.)
- **Clear optional fields** by deleting the line (assignee, labels, due, estimate, parent, project, milestone, cycle)
- All symlinks resolve to issue directories containing issue.md
- Cache TTL: 60s (external changes may be delayed)

## Write-Only new.md Files

The ` + "`" + `new.md` + "`" + ` files in comments/ and updates/ directories are **write-only
triggers** that create new items in Linear. They work like a mailbox slot: content
goes in but nothing comes out. (For docs/, prefer using a named filename instead.)

**How it works:**
1. Write content to new.md (the file is always empty, size 0)
2. On save/flush, content is sent to Linear and consumed
3. The new item appears as a separate file (e.g., 001-2025-01-15.md)

**Correct usage:**
` + "```" + `bash
# Use echo or cat with redirect
echo "My comment" > /teams/ENG/issues/ENG-123/comments/new.md

# Multi-line with heredoc
cat > /teams/ENG/issues/ENG-123/comments/new.md << 'EOF'
Multi-line content here
EOF
` + "```" + `

**What won't work:**
- Reading new.md (always returns empty)
- Editors that read before writing (vim, vscode)
- Tools that expect to read existing content first

**After writing:**
- Item syncs to Linear within seconds
- Due to cache TTL, ` + "`" + `ls` + "`" + ` may not show it immediately
- Check Linear directly to confirm creation

**To update existing content:**
- List the directory first: ` + "`" + `ls comments/` + "`" + ` or ` + "`" + `ls docs/` + "`" + `
- Edit the existing file directly (e.g., ` + "`" + `vim {id}.md` + "`" + `)
- Do NOT write to new.md—that creates duplicates
`
