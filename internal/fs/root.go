package fs

import (
	"context"
	"fmt"
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
	mp := r.lfs.MountPoint()
	return []byte(generateReadme(mp))
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

func generateReadme(mountPoint string) string {
	return fmt.Sprintf(`# Linear Filesystem

<purpose>
FUSE filesystem exposing Linear.app as editable markdown files. Edit YAML frontmatter to update issues.
Mount point: %s (all paths below are relative to this mount point)
</purpose>

<directory_structure>
teams/{KEY}/
  team.md, states.md, labels.md     [read-only metadata]
  issues/{ID}/
    issue.md                        [read/write]
    .error                          [read-only: last validation error]
    comments/{id}.md                [read/write, _create=trigger]
    docs/{slug}.md                  [read/write, _create=trigger]
    attachments/                    [embedded files + external links]
      _create                       [write "URL [title]" to link]
      *.png, *.pdf                  [read-only: embedded images/files]
      *.link                        [read-only: external link info]
    relations/                      [issue dependencies/links]
      _create                       [write "type ID" to create]
      {type}-{ID}.rel               [read-only info, rm to delete]
    children/                       [symlinks to sub-issues, mkdir to create]
  by/status|label|assignee/{value}/ [issue symlinks]
  projects/{slug}/
    project.md                      [read/write]
    docs/                           [same as issues]
    updates/                        [_create with health: onTrack|atRisk|offTrack]
    milestones/                     [project milestones]
      _create                       [write "name\ndescription" to create]
      {name}.md                     [read/write, rm to delete]
    {ISSUE-ID} symlinks
  cycles/
    current                         [symlink to active cycle]
    {name}/                         [issue symlinks]

initiatives/{slug}/
  initiative.md                     [read/write]
  docs/{slug}.md                    [read/write, _create=trigger]
  projects/                         [symlinks to team projects]
    {project-slug}                  [symlink to ../../teams/{KEY}/projects/{slug}]
  updates/                          [status updates]
    _create                         [write with health: onTrack|atRisk|offTrack]
    {seq}-{date}-{health}.md        [read-only]

users/{name}/                       [issue symlinks + user.md]
my/assigned|created|active/         [your issue symlinks]
</directory_structure>

<operations>
READ:    cat %s/teams/ENG/issues/ENG-123/issue.md
EDIT:    vim issue.md                 (edit frontmatter, save)
CREATE:  mkdir %s/teams/ENG/issues/"New Issue Title"
         mkdir children/"Sub-task Title"   (creates child issue)
         mkdir %s/teams/ENG/projects/"New Project"
         echo "text" > comments/_create
         echo "text" > docs/"Title.md"
         echo "---\nhealth: atRisk\n---\nBlocked" > updates/_create
LINK:    echo "https://github.com/org/repo/pull/123" > attachments/_create
         echo "blocks ENG-456" > relations/_create
         echo -e "Phase 1\nInitial milestone" > milestones/_create
INITIATIVES:
         vim initiatives/platform-modernization/initiative.md  (edit projects: list)
         echo "text" > initiatives/my-initiative/docs/"Title.md"
         echo "---\nhealth: atRisk\n---\nUpdate text" > initiatives/my-initiative/updates/_create
DELETE:  rm relations/blocks-ENG-456.rel
         rm milestones/"Phase 1.md"
ARCHIVE: rmdir %s/teams/ENG/issues/ENG-123
SORT:    ls -lt %s/my/active/           (mtime = updatedAt)
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

<initiative_frontmatter>
---
id: 719a9756-326e-40cf-935d-38cf899a1f50   [read-only]
name: "Platform Modernization"              [read-only]
slug: 77d439e363bb                          [read-only]
status: Active                              [read-only]
projects:                                   [editable - project slugs]
  - "api-gateway"
  - "auth-service"
  - "data-pipeline"
owner:                                      [read-only]
  id: df7cbe14-f8c2-4096-b812-73fa9d39f19f
  name: "John Doe"
  email: john@example.com
targetDate: "2026-03-31"                    [read-only]
created: "2026-01-24T22:15:26Z"             [read-only]
updated: "2026-01-27T16:03:38Z"             [read-only]
---
Initiative description (read-only)

Usage:
- Edit projects: list to link/unlink projects (use project slugs)
- Projects are resolved workspace-wide across all teams
- Changes sync immediately to Linear API and SQLite cache
- Other fields are read-only (Linear API doesn't support editing)
</initiative_frontmatter>

<permissions>
-r--r--r--  Read-only     team.md, states.md, user.md
-rw-r--r--  Editable      issue.md, project.md, initiative.md, comments/*.md, docs/*.md, milestones/*.md
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

<validation_errors>
Writes fail with EINVAL (Invalid argument) for invalid frontmatter values.
After a failed write, cat .error to see what went wrong:

  $ echo "priority: critical" >> issue.md  # invalid priority
  $ cat .error
  Field: priority
  Value: "critical"
  Error: invalid priority "critical": must be none, low, medium, high, or urgent

Validated fields: status, assignee, labels, priority, project, milestone, cycle, parent
Reference files: states.md (valid statuses), labels.md (valid labels)
The .error file is cleared on successful writes.
</validation_errors>

<important_notes>
- Clear optional fields by deleting the line entirely
- Set parent: add "parent: ENG-100" | Remove: delete line
- Link project to initiative: add "initiatives: [Name]" to project.md
- Link initiative to projects: edit "projects: [slugs]" in initiative.md
- Relation types: blocks, duplicate, related, similar
- Inverse relations shown as: blocked-by, duplicated-by, related-to, similar-to
- Cache TTL: 60s for issues, 10min for states/labels/users
- Timestamps: mtime=updatedAt, ctime=createdAt from Linear
- Project slugs: Use slug (e.g., "api-gateway"), not name, in initiative.md
</important_notes>
`, mountPoint, mountPoint, mountPoint, mountPoint, mountPoint, mountPoint)
}
