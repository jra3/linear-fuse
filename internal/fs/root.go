package fs

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// RootNode is the root directory of the filesystem
type RootNode struct {
	BaseNode
}

var _ fs.NodeReaddirer = (*RootNode)(nil)
var _ fs.NodeLookuper = (*RootNode)(nil)
var _ fs.NodeGetattrer = (*RootNode)(nil)

func (r *RootNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	r.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

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
	now := time.Now()
	switch name {
	case "README.md":
		node := &ReadmeNode{BaseNode: BaseNode{lfs: r.lfs}}
		content := node.generateContent()
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		out.Attr.Uid = r.lfs.uid
		out.Attr.Gid = r.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0

	case "teams":
		node := &TeamsNode{BaseNode: BaseNode{lfs: r.lfs}}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = r.lfs.uid
		out.Attr.Gid = r.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "users":
		node := &UsersNode{BaseNode: BaseNode{lfs: r.lfs}}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = r.lfs.uid
		out.Attr.Gid = r.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "my":
		node := &MyNode{BaseNode: BaseNode{lfs: r.lfs}}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = r.lfs.uid
		out.Attr.Gid = r.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "initiatives":
		node := &InitiativesNode{BaseNode: BaseNode{lfs: r.lfs}}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = r.lfs.uid
		out.Attr.Gid = r.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	default:
		return nil, syscall.ENOENT
	}
}

// ReadmeNode is a virtual file containing filesystem documentation
type ReadmeNode struct {
	BaseNode
}

var _ fs.NodeGetattrer = (*ReadmeNode)(nil)
var _ fs.NodeOpener = (*ReadmeNode)(nil)
var _ fs.NodeReader = (*ReadmeNode)(nil)

func (r *ReadmeNode) generateContent() []byte {
	return []byte(readmeContent)
}

func (r *ReadmeNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	content := r.generateContent()
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(content))
	r.SetOwner(out)
	out.SetTimes(&now, &now, &now)
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

<purpose>
FUSE filesystem exposing Linear.app as editable markdown files. Edit YAML frontmatter to update issues.
</purpose>

<directory_structure>
/teams/{KEY}/
  team.md, states.md, labels.md     [read-only metadata]
  issues/{ID}/
    issue.md                        [read/write]
    comments/{id}.md                [read/write, _create=trigger]
    docs/{slug}.md                  [read/write, _create=trigger]
    attachments/                    [read-only: ls to list, cat to download]
    children/                       [symlinks to sub-issues]
  by/status|label|assignee/{value}/ [issue symlinks]
  search/{query}/                   [FTS5 results, use + for spaces]
  projects/{slug}/
    project.md                      [read/write]
    docs/                           [same as issues]
    updates/                        [_create with health: onTrack|atRisk|offTrack]
    {ISSUE-ID} symlinks
  cycles/
    current                         [symlink to active cycle]
    {name}/                         [issue symlinks]

/initiatives/{slug}/
  initiative.md                     [read-only]
  projects/, updates/

/users/{name}/                      [issue symlinks + user.md]
/my/assigned|created|active/        [your issue symlinks]
</directory_structure>

<operations>
READ:    cat /teams/ENG/issues/ENG-123/issue.md
EDIT:    vim issue.md                 (edit frontmatter, save)
CREATE:  mkdir /teams/ENG/issues/"New Issue Title"
         echo "text" > comments/_create
         echo "text" > docs/"Title.md"
         echo "---\nhealth: atRisk\n---\nBlocked" > updates/_create
ARCHIVE: rmdir /teams/ENG/issues/ENG-123
SEARCH:  ls /teams/ENG/search/bug/
         ls /teams/ENG/search/auth+token/    (+ = space)
         ls /teams/ENG/search/ENG-12*/       (* = prefix match)
         ls /my/assigned/search/experiment/  (scoped to view)
         ls /teams/ENG/by/status/Todo/search/urgent/
SORT:    ls -lt /my/active/           (mtime = updatedAt)
</operations>

<issue_frontmatter>
---
identifier: ENG-123                 [read-only]
branch: "john/eng-123-fix-bug"      [read-only, suggested git branch]
title: "Fix bug"                    [editable]
status: "In Progress"               [must match states.md]
assignee: "user@example.com"        [email or display name]
priority: high                      [none|low|medium|high|urgent]
labels: [Bug, Backend]              [must match labels.md]
due: "2025-01-15"                   [YYYY-MM-DD]
estimate: 3                         [points]
parent: ENG-100                     [parent issue identifier]
project: "Project Name"
milestone: "Phase 1"                [milestone within project]
cycle: "Sprint 42"
links:                              [read-only, external attachments]
  - {type: github-pr, title: "...", url: "..."}
---
Description body (editable)
</issue_frontmatter>

<permissions>
-r--r--r--  Read-only     team.md, states.md, initiative.md
-rw-r--r--  Editable      issue.md, project.md, comments/*.md, docs/*.md
--w-------  Write-only    _create (write triggers creation, always empty)
lrwxrwxrwx  Symlink       Issues in by/, cycles/, projects/, users/
</permissions>

<_create_behavior>
_create is a write-only trigger file (like /proc/sysrq-trigger):
- Reading always returns empty (size 0)
- Writing creates a new item and consumes the content
- Editors fail because they read-before-write (vim, vscode)
- Use piped output: echo "text" > _create, cat file > _create
- Created items appear as separate files (e.g., 001-2025-01-15.md)
- For docs/, prefer named files: echo "x" > docs/"Title.md"
</_create_behavior>

<important_notes>
- Validate status/labels against states.md and labels.md before editing
- Invalid values fail the write silently (no error returned)
- Clear optional fields by deleting the line entirely
- Set parent: add "parent: ENG-100" | Remove: delete line
- Link project to initiative: add "initiatives: [Name]" to project.md
- Cache TTL: 60s for issues, 10min for states/labels/users
- Timestamps: mtime=updatedAt, ctime=createdAt from Linear
</important_notes>
`
