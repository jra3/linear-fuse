package fs

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// TeamsNode represents the /teams directory
type TeamsNode struct {
	BaseNode
}

var _ fs.NodeReaddirer = (*TeamsNode)(nil)
var _ fs.NodeLookuper = (*TeamsNode)(nil)
var _ fs.NodeGetattrer = (*TeamsNode)(nil)

func (t *TeamsNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	t.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (t *TeamsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	teams, err := t.lfs.GetTeams(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(teams))
	for i, team := range teams {
		entries[i] = fuse.DirEntry{
			Name: team.Key,
			Mode: syscall.S_IFDIR,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (t *TeamsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	teams, err := t.lfs.GetTeams(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, team := range teams {
		if team.Key == name {
			out.Attr.Mode = 0755 | syscall.S_IFDIR
			out.Attr.Uid = t.lfs.uid
			out.Attr.Gid = t.lfs.gid
			out.Attr.SetTimes(&team.UpdatedAt, &team.UpdatedAt, &team.CreatedAt)
			node := &TeamNode{BaseNode: BaseNode{lfs: t.lfs}, team: team}
			return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	return nil, syscall.ENOENT
}

// TeamNode represents a single team directory (e.g., /teams/ENG)
type TeamNode struct {
	BaseNode
	team api.Team
}

var _ fs.NodeReaddirer = (*TeamNode)(nil)
var _ fs.NodeLookuper = (*TeamNode)(nil)
var _ fs.NodeGetattrer = (*TeamNode)(nil)

func (t *TeamNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	t.SetOwner(out)
	out.SetTimes(&t.team.UpdatedAt, &t.team.UpdatedAt, &t.team.CreatedAt)
	return 0
}

func (t *TeamNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "team.md", Mode: syscall.S_IFREG},
		{Name: "states.md", Mode: syscall.S_IFREG},
		{Name: "labels.md", Mode: syscall.S_IFREG},
		{Name: "by", Mode: syscall.S_IFDIR},
		{Name: "cycles", Mode: syscall.S_IFDIR},
		{Name: "projects", Mode: syscall.S_IFDIR},
		{Name: "issues", Mode: syscall.S_IFDIR},
		{Name: "recent", Mode: syscall.S_IFDIR},
		{Name: "docs", Mode: syscall.S_IFDIR},
		{Name: "labels", Mode: syscall.S_IFDIR},
	}

	return fs.NewListDirStream(entries), 0
}

func (t *TeamNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	now := time.Now()
	switch name {
	case "team.md":
		team := t.team
		return t.lookupRenderFile(ctx, out, "team.md", func(context.Context) ([]byte, time.Time, time.Time) {
			return teamMarkdown(team), team.UpdatedAt, team.CreatedAt
		}, 0, inheritTimeout), 0

	case "states.md":
		// states.md has no single mtime (it lists a collection); report the
		// team's times as a stable proxy — never now(). Content is fetched from
		// SQLite on each read (cheap), so no node-level cache is needed.
		lfs, team := t.lfs, t.team
		return t.lookupRenderFile(ctx, out, "states.md", func(ctx context.Context) ([]byte, time.Time, time.Time) {
			states, err := lfs.GetTeamStates(ctx, team.ID)
			if err != nil {
				return []byte("# Error loading states\n"), team.UpdatedAt, team.CreatedAt
			}
			return statesMarkdown(team, states), team.UpdatedAt, team.CreatedAt
		}, 0, inheritTimeout), 0

	case "labels.md":
		lfs, team := t.lfs, t.team
		return t.lookupRenderFile(ctx, out, "labels.md", func(ctx context.Context) ([]byte, time.Time, time.Time) {
			labels, err := lfs.GetTeamLabels(ctx, team.ID)
			if err != nil {
				return []byte("# Error loading labels\n"), team.UpdatedAt, team.CreatedAt
			}
			return labelsMarkdown(team, labels), team.UpdatedAt, team.CreatedAt
		}, 0, inheritTimeout), 0

	case "by":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		node := &FilterRootNode{BaseNode: BaseNode{lfs: t.lfs}, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "cycles":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		node := &CyclesNode{BaseNode: BaseNode{lfs: t.lfs}, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "projects":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		node := &ProjectsNode{BaseNode: BaseNode{lfs: t.lfs}, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  projectsDirIno(t.team.ID),
		}), 0

	case "issues":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		node := &IssuesNode{BaseNode: BaseNode{lfs: t.lfs}, team: t.team}
		// The stable ino is what makes create/delete invalidations against
		// issuesDirIno reach the kernel; without it InodeNotify targets an
		// inode the kernel never learned.
		return t.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  issuesDirIno(t.team.ID),
		}), 0

	case "recent":
		out.Attr.Mode = 0555 | syscall.S_IFDIR // read-only view
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		out.Attr.SetTimes(&now, &now, &now)
		node := &RecentNode{BaseNode: BaseNode{lfs: t.lfs}, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  recentDirIno(t.team.ID),
		}), 0

	case "docs":
		node := &DocsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, teamID: t.team.ID}
		return t.newDirInode(ctx, out, "docs", node, dirAttr(t.team.CreatedAt, t.team.UpdatedAt), docsDirIno(t.team.ID), 0), 0

	case "labels":
		node := &LabelsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, teamID: t.team.ID}
		return t.newDirInode(ctx, out, "labels", node, dirAttr(t.team.CreatedAt, t.team.UpdatedAt), labelsDirIno(t.team.ID), 0), 0
	}

	return nil, syscall.ENOENT
}

// teamMarkdown renders the team.md content for a team.
func teamMarkdown(team api.Team) []byte {
	return []byte(fmt.Sprintf(`---
id: %s
key: %s
name: %q
icon: %q
created: %q
updated: %q
---

# %s

- **Key:** %s
- **ID:** %s
`,
		team.ID,
		team.Key,
		team.Name,
		team.Icon,
		team.CreatedAt.Format(time.RFC3339),
		team.UpdatedAt.Format(time.RFC3339),
		team.Name,
		team.Key,
		team.ID,
	))
}

// statesMarkdown renders the states.md content for a team's workflow states.
func statesMarkdown(team api.Team, states []api.State) []byte {
	var statesYAML string
	for _, state := range states {
		statesYAML += fmt.Sprintf("  - id: %s\n    name: %s\n    type: %s\n",
			state.ID, state.Name, state.Type)
	}

	var table string
	for _, state := range states {
		table += fmt.Sprintf("| %s | %s | %s |\n", state.Name, state.Type, state.ID)
	}

	return []byte(fmt.Sprintf(`---
team: %s
states:
%s---

# Workflow States for %s

| Name | Type | ID |
|------|------|-----|
%s`,
		team.Key,
		statesYAML,
		team.Key,
		table,
	))
}

// labelsMarkdown renders the labels.md content for a team's labels.
func labelsMarkdown(team api.Team, labels []api.Label) []byte {
	var labelsYAML string
	for _, label := range labels {
		labelsYAML += fmt.Sprintf("  - id: %s\n    name: %s\n    color: %q\n",
			label.ID, label.Name, label.Color)
		if label.Description != "" {
			labelsYAML += fmt.Sprintf("    description: %q\n", label.Description)
		}
	}

	var table string
	for _, label := range labels {
		table += fmt.Sprintf("| %s | %s | %s |\n", label.Name, label.Color, label.ID)
	}

	return []byte(fmt.Sprintf(`---
team: %s
labels:
%s---

# Labels for %s

| Name | Color | ID |
|------|-------|-----|
%s`,
		team.Key,
		labelsYAML,
		team.Key,
		table,
	))
}
