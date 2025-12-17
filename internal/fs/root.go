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

Issues as markdown files. Edit frontmatter to update Linear.

## Quick Reference

| Task | Command |
|------|---------|
| List teams | ` + "`" + `ls /teams/` + "`" + ` |
| My active issues | ` + "`" + `ls /my/active/` + "`" + ` |
| Issues by status | ` + "`" + `ls /teams/ENG/by/status/Todo/` + "`" + ` |
| Read issue | ` + "`" + `cat /teams/ENG/issues/ENG-123/issue.md` + "`" + ` |
| Update issue | Edit issue.md frontmatter, save |
| Add comment | ` + "`" + `echo "text" > .../comments/new.md` + "`" + ` |
| Create issue | ` + "`" + `mkdir /teams/ENG/issues/"Title"` + "`" + ` |
| Archive issue | ` + "`" + `rmdir /teams/ENG/issues/ENG-123` + "`" + ` |

## Directory Structure

` + "```" + `
/teams/{KEY}/
├── team.md, states.md, labels.md  # Metadata (read-only)
├── by/status/{name}/              # Filtered views (symlinks)
├── by/label/{name}/
├── by/assignee/{name}/
├── issues/{ID}/
│   ├── issue.md                   # Read/write
│   ├── comments/                  # new.md to create
│   └── docs/                      # new.md to create
├── cycles/current/                # Active sprint
└── projects/{slug}/

/users/{name}/                     # Issues by assignee
/my/assigned|created|active/       # Personal views
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
dueDate: "2025-01-15"     # editable
estimate: 3               # editable
---
Description here (editable)
` + "```" + `

Read-only: id, identifier, url, created, updated, project

## Notes

- Check states.md for valid status names before changing status
- Check labels.md for available label names
- All symlinks resolve to issue directories containing issue.md
- Cache TTL: 60s (external changes may be delayed)
`
