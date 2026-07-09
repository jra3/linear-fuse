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
		{Name: "project-labels.md", Mode: syscall.S_IFREG},
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
		// The generated docs have no natural entity time; report zero (unknown).
		lfs := r.lfs
		return r.lookupRenderFile(ctx, out, "README.md", func(context.Context) ([]byte, time.Time, time.Time) {
			return []byte(generateReadme(lfs.MountPoint())), time.Time{}, time.Time{}
		}, 0, inheritTimeout), 0

	case "project-labels.md":
		// The workspace project-label catalog (ProjectLabel has no team edge,
		// so this is a root surface like initiatives/). SQLite-only read; an
		// error or empty catalog still renders — the surface never ENOENTs.
		lfs := r.lfs
		return r.lookupRenderFile(ctx, out, "project-labels.md",
			func(ctx context.Context) ([]byte, time.Time, time.Time) {
				labels, _ := lfs.GetProjectLabels(ctx)
				mtime, ctime := projectLabelCatalogTimes(labels)
				return projectLabelsMarkdown(labels), mtime, ctime
			}, projectLabelsCatalogIno(), inheritTimeout), 0

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

func generateReadme(mountPoint string) string {
	return fmt.Sprintf(`# Linear Filesystem

<purpose>
FUSE filesystem exposing Linear.app as editable markdown files. Edit YAML frontmatter to update issues.
Mount point: %s (all paths below are relative to this mount point)
</purpose>

<directory_structure>
teams/{KEY}/
  team.md, states.md, labels.md     [read-only metadata]
  project-labels.md                 [symlink to ../../project-labels.md]
  issues/                           [mkdir "Title" for quick create]
    _create                         [write full frontmatter+body to create one issue with all fields]
    .error                          [read-only: last failed issue creation]
    .last                           [read-only: YAML list of recent creations {identifier,url,path,title,status}]
  recent/                           [read-only: issue symlinks, newest-first by updatedAt (ls recent/ | head)]
  issues/{ID}/
    issue.md                        [read/write: editable fields + body ONLY]
    issue.meta                      [read-only: id, identifier, url, branch, created, updated, links, relations]
    .error                          [read-only: last failed write here]
    .last                           [read-only: sub-issues created via children/]
    comments/                       [_create=trigger, .error=feedback, .last=created ids]
      {id}.md                       [read/write: comment body ONLY, no frontmatter]
      {id}.meta                     [read-only: id, author, created, updated]
    docs/                           [_create=trigger, .error=feedback, .last=created docs]
      {slug}.md                     [read/write: title, icon, color + body]
      {slug}.meta                   [read-only: id, url, creator, created, updated]
    attachments/                    [embedded files + external links]
      _create                       [write "URL [title]" to link]
      .error                        [read-only: last failed write here]
      .last                         [read-only: recent successful links]
      *.png, *.pdf                  [read-only: embedded images/files]
      *.link                        [read-only: external link info]
    relations/                      [issue dependencies/links]
      _create                       [write "type ID" to create]
      .error                        [read-only: last failed write here]
      .last                         [read-only: recent created relations]
      {type}-{ID}.rel               [read-only info, rm to delete]
    children/                       [symlinks to sub-issues, mkdir to create]
  by/status|label|assignee/{value}/ [issue symlinks]
  labels/                           [_create=trigger, .error=feedback, .last=created labels]
    {name}.md                       [read/write: name, color, description; rm to delete]
    {name}.meta                     [read-only: id]
  projects/                         [mkdir "Name" to create a project]
    .error                          [read-only: last failed project creation]
    .last                           [read-only: recent project creations]
  projects/{slug}/
    project.md                      [read/write: editable fields + body ONLY]
    project.meta                    [read-only: id, slug, url, status, lead, dates]
    .error                          [read-only: last failed write here]
    docs/                           [same as issues]
    updates/                        [status updates]
      _create                       [write with health: onTrack|atRisk|offTrack]
      .error                        [read-only: last failed write here]
      .last                         [read-only: recent created updates]
      {seq}-{date}-{health}.md      [read-only]
    milestones/                     [project milestones]
      _create                       [write "name\ndescription" to create]
      .error                        [read-only: last failed write here]
      .last                         [read-only: recent created milestones]
      {name}.md                     [read/write: name, targetDate, sortOrder + body; rm to delete]
      {name}.meta                   [read-only: id]
    {ISSUE-ID} symlinks
  cycles/
    current                         [symlink to active cycle]
    {name}/                         [issue symlinks]

project-labels.md                   [read-only: workspace project-label catalog (groups, retired)]

initiatives/{slug}/
  initiative.md                     [read/write: editable fields + body ONLY]
  initiative.meta                   [read-only: id, slug, url, status, owner, dates]
  .error                            [read-only: last failed write here]
  docs/                             [_create=trigger, .error=feedback]
    {slug}.md                       [read/write: title, icon, color + body]
    {slug}.meta                     [read-only: id, url, creator, created, updated]
  projects/                         [symlinks to team projects]
    {project-slug}                  [symlink to ../../../teams/{KEY}/projects/{slug}]
  updates/                          [status updates]
    _create                         [write with health: onTrack|atRisk|offTrack]
    .error                          [read-only: last failed write here]
    .last                           [read-only: recent created updates]
    {seq}-{date}-{health}.md        [read-only]

users/{name}/                       [issue symlinks + user.md]
my/assigned|created|active/         [your issue symlinks]
</directory_structure>

<operations>
READ:    cat %s/teams/ENG/issues/ENG-123/issue.md
EDIT:    vim issue.md                 (edit frontmatter, save)
CREATE:  mkdir %s/teams/ENG/issues/"New Issue Title"   (quick: title only)
         printf -- '---\ntitle: Full Issue\npriority: high\nlabels: [Bug]\n---\nBody.\n' > issues/_create
         cat issues/.last                  (read back the new identifier/url/path)
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
issue.md holds only editable fields (below) + the description body. Read-only
identity/timestamps/links live in the sibling issue.meta (identifier, url,
branch, created, updated, …). A successful write never rewrites issue.md.
---
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
---
Description body (editable)
</issue_frontmatter>

<project_frontmatter>
project.md holds only editable fields (below) + the description body. Read-only
identity/status/lead/dates live in the sibling project.meta. A successful write
never rewrites project.md.
---
name: "API Gateway"                         [editable]
initiatives: ["Platform Modernization"]     [names; see initiatives/]
labels: [Backend, Q3-Bet]                   [must match project-labels.md; groups
                                             cannot be applied; max one child per
                                             group; retired = existing-only; raw
                                             label IDs also accepted]
---
Project description (editable - the body maps to the description)
</project_frontmatter>

<initiative_frontmatter>
initiative.md holds only editable fields (below) + the description body. Read-only
identity/status/owner/dates live in the sibling initiative.meta (id, slug, status,
url, owner, targetDate, created, updated). A successful write never rewrites
initiative.md.
---
name: "Platform Modernization"              [editable]
projects:                                   [editable - project slugs]
  - "api-gateway"
  - "auth-service"
  - "data-pipeline"
---
Initiative description (editable - the body maps to the description)

Usage:
- Edit name (frontmatter) and description (body); they sync to Linear
- Edit projects: list to link/unlink projects (use project slugs)
- Projects are resolved workspace-wide across all teams
- Changes sync immediately to Linear API and SQLite cache
- Read-only server fields (id, slug, status, owner, dates) live in initiative.meta
</initiative_frontmatter>

<permissions>
-r--r--r--  Read-only     team.md, states.md, user.md, every *.meta sidecar
-rw-r--r--  Editable      issue.md, project.md, initiative.md, comments/*.md, docs/*.md, milestones/*.md, labels/*.md
--w-------  Write-only    _create (write triggers creation; reads are rejected)
lrwxrwxrwx  Symlink       Issues in by/, cycles/, projects/, users/

Every editable file holds ONLY its editable fields; the server-managed fields
(id, url, timestamps, author, …) live in a read-only sidecar named after it:
issue.md/issue.meta, and per collection item {name}.md/{name}.meta. Comment
.md files are the pure body with no frontmatter at all. Editing a server
field is impossible by construction — it is not in the editable file.
</permissions>

<_create_behavior>
_create is a write-only trigger file (like /proc/sysrq-trigger):
- Reading is rejected (EACCES) — the file is write-only
- Writing creates a new item and consumes the content
- Editors fail because they read-before-write (vim, vscode) and the read is rejected
- Use piped output: echo "text" > _create, cat file > _create
- Created items appear as separate files (e.g., 001-2025-01-15.md). Every create
  surface (issues, children, comments, docs, labels, projects, milestones,
  attachments, relations, updates) exposes a sibling .last with the new identity;
  read .error for a failure.
- Each open-write-close cycle creates one item: writing to _create again creates
  another item, so a repeated identical write creates a duplicate. After a failed
  write (.error explains it), simply write the corrected content again.
- For docs/, prefer named files: echo "x" > docs/"Title.md"
</_create_behavior>

<validation_errors>
Every writable directory has a .error feedback file. After a failed write,
cat the .error next to the file (or _create) you wrote to see what went wrong:

  $ echo "priority: critical" >> issue.md  # invalid priority
  $ cat .error
  Field: priority
  Value: "critical"
  Error: invalid priority "critical": must be none, low, medium, high, or urgent

Failure model (every writable surface follows this contract):
- Bad input (invalid field, unknown name, missing required field) -> EINVAL
- Reference to something that doesn't exist (a relation target, rm of an unknown name) -> ENOENT
- Rate-limited or timed out (the write did not take effect; retry shortly) -> EAGAIN
- Backend/API failure -> EIO
- Whatever the errno, the reason lands in .error; success clears it.
So an edit that "fails" or appears to no-op is explained at the sibling .error.

Validated issue fields: status, assignee, labels, priority, project, milestone, cycle, parent
Validated project fields: initiatives, labels
Reference files: states.md (valid statuses), labels.md (valid issue labels),
project-labels.md (valid project labels), initiatives/ (valid initiatives)
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
- Project labels are workspace-wide (see project-labels.md). A label group cannot
  be applied directly (pick one of its children; max one child per group); a
  retired label stays on existing projects but cannot be newly applied
</important_notes>

<claude_code_instructions>
Instructions for Claude Code agents working with this filesystem:

READING FILES:
- Use the Read tool (not Bash cat) to read issue.md, project.md, etc.
- When reading multiple files, make all Read calls in parallel in a single message
- Never use shell for loops to read multiple files; use parallel Read tool calls instead
- Example: to read ENG-100, ENG-101, ENG-102 — issue three Read tool calls simultaneously

LISTING DIRECTORIES:
- Use Bash(ls %s/teams/ENG/issues/) to list issues
- Use Bash(ls -lt %s/my/active/) to list by recency (mtime = updatedAt)
- ls output shows symlinks; follow them with Read to get content

EDITING FILES:
- Use the Edit tool to modify issue.md, project.md, initiative.md frontmatter
- The Edit tool works correctly because it reads then writes (unlike raw editors on _create)
- After editing, changes sync to Linear immediately

CREATING ITEMS:
- Use Bash(echo "text" > path/_create) — never use the Write tool on _create files
- _create is write-only; reads are rejected (EACCES), so Write/Edit tools (which
  read-before-write) fail — pipe instead, then read the sibling .error (and .last,
  where the surface mints an entity: issues/comments/docs/labels/projects/milestones)
- For docs with a title: Bash(echo "content" > path/docs/"Title.md")

WRITING ISSUE CONTENT:
- To update an issue: use Edit tool on the issue.md file
- Only edit fields you intend to change; leave others untouched
- Check <mount>/teams/ENG/states.md and labels.md for valid values before editing
- Check <mount>/project-labels.md before editing labels: in project.md

BASH PATTERNS TO AVOID:
- Avoid: for x in list; do cat $x; done  → instead: parallel Read tool calls
- Avoid: cat file | grep pattern          → instead: use Grep tool
- Avoid: find . -name "*.md"             → instead: use Glob tool
</claude_code_instructions>
`, mountPoint, mountPoint, mountPoint, mountPoint, mountPoint, mountPoint, mountPoint, mountPoint)
}
