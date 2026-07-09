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

// TeamsNode represents the /teams directory. Stateless container: zero times
// (honest unknown), no refresh needs; Getattr comes from the attrNode mixin.
type TeamsNode struct {
	attrNode
}

var _ fs.NodeReaddirer = (*TeamsNode)(nil)
var _ fs.NodeLookuper = (*TeamsNode)(nil)
var _ fs.NodeGetattrer = (*TeamsNode)(nil)

func (t *TeamsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	teams, err := t.lfs.repo.GetTeams(ctx)
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
	teams, err := t.lfs.repo.GetTeams(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, team := range teams {
		if team.Key == name {
			node := &TeamNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, team: team}
			return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), teamDirIno(team.ID), inheritTimeout), 0
		}
	}

	return nil, syscall.ENOENT
}

// TeamNode represents a single team directory (e.g., /teams/ENG)
type TeamNode struct {
	attrNode
	team api.Team
}

var _ fs.NodeReaddirer = (*TeamNode)(nil)
var _ fs.NodeLookuper = (*TeamNode)(nil)
var _ fs.NodeGetattrer = (*TeamNode)(nil)

// entity/setEntity snapshot and swap the directory's team under the node's
// volatile-state lock: setEntity is written by the nodeRefresher seam
// (refresh.go), which pushes freshly-fetched state into this node when
// go-fuse dedups a later Lookup onto it.
func (t *TeamNode) entity() api.Team {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.team
}

func (t *TeamNode) setEntity(team api.Team) {
	t.stateMu.Lock()
	t.team = team
	t.stateMu.Unlock()
}

func (t *TeamNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*TeamNode); ok {
		t.setEntity(f.team)
	}
}

func (t *TeamNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "team.md", Mode: syscall.S_IFREG},
		{Name: "states.md", Mode: syscall.S_IFREG},
		{Name: "labels.md", Mode: syscall.S_IFREG},
		{Name: "project-labels.md", Mode: syscall.S_IFLNK},
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
	team := t.entity() // snapshot captured by the arms and their closures
	switch name {
	case "team.md":
		return t.lookupRenderFile(ctx, out, "team.md", func(context.Context) ([]byte, time.Time, time.Time) {
			return teamMarkdown(team), team.UpdatedAt, team.CreatedAt
		}, 0, inheritTimeout), 0

	case "states.md":
		// states.md has no single mtime (it lists a collection); report the
		// team's times as a stable proxy — never now(). Content is fetched from
		// SQLite on each read (cheap), so no node-level cache is needed.
		lfs := t.lfs
		return t.lookupRenderFile(ctx, out, "states.md", func(ctx context.Context) ([]byte, time.Time, time.Time) {
			states, err := lfs.repo.GetTeamStates(ctx, team.ID)
			if err != nil {
				return []byte("# Error loading states\n"), team.UpdatedAt, team.CreatedAt
			}
			return statesMarkdown(team, states), team.UpdatedAt, team.CreatedAt
		}, 0, inheritTimeout), 0

	case "labels.md":
		lfs := t.lfs
		return t.lookupRenderFile(ctx, out, "labels.md", func(ctx context.Context) ([]byte, time.Time, time.Time) {
			labels, err := lfs.repo.GetTeamLabels(ctx, team.ID)
			if err != nil {
				return []byte("# Error loading labels\n"), team.UpdatedAt, team.CreatedAt
			}
			return labelsMarkdown(team, labels), team.UpdatedAt, team.CreatedAt
		}, 0, inheritTimeout), 0

	case "project-labels.md":
		// Ergonomics alias beside states.md/labels.md, where agents already
		// look for validation references. A symlink (not a per-team file)
		// honestly discloses the workspace scoping — ProjectLabel has no team
		// edge. Zero times: stamping catalog times here would need a catalog
		// load per team-Lookup; a stat THROUGH the link reports the render
		// file's real times.
		return t.newSymlinkInode(ctx, out, "../../project-labels.md", time.Time{}, time.Time{}), 0

	// The team's view subdirectories hold a team snapshot and report the
	// team's times: they are (or contain) projections of the team's state.
	case "by":
		node := &FilterRootNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, team: team}
		return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), byDirIno(team.ID), inheritTimeout), 0

	case "cycles":
		node := &CyclesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, team: team}
		return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), cyclesDirIno(team.ID), inheritTimeout), 0

	case "projects":
		node := &ProjectsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, team: team}
		return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), projectsDirIno(team.ID), inheritTimeout), 0

	case "issues":
		node := &IssuesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, team: team}
		// The stable ino is what makes create/delete invalidations against
		// issuesDirIno reach the kernel; without it InodeNotify targets an
		// inode the kernel never learned.
		return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), issuesDirIno(team.ID), inheritTimeout), 0

	case "recent":
		node := &RecentNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, team: team}
		// 0555: read-only view.
		na := nodeAttr{mode: 0555 | syscall.S_IFDIR, created: team.CreatedAt, updated: team.UpdatedAt}
		return t.newDirInode(ctx, out, name, node, na, recentDirIno(team.ID), inheritTimeout), 0

	case "docs":
		node := &DocsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, teamID: team.ID}
		return t.newDirInode(ctx, out, "docs", node, dirAttr(team.CreatedAt, team.UpdatedAt), docsDirIno(team.ID), 0), 0

	case "labels":
		node := &LabelsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, teamID: team.ID}
		return t.newDirInode(ctx, out, "labels", node, dirAttr(team.CreatedAt, team.UpdatedAt), labelsDirIno(team.ID), 0), 0
	}

	return nil, syscall.ENOENT
}

// teamMarkdown renders the team.md content for a team. Frontmatter goes
// through renderWithFrontmatter so hostile names stay valid YAML.
func teamMarkdown(team api.Team) []byte {
	fm := map[string]any{
		"id":      team.ID,
		"key":     team.Key,
		"name":    team.Name,
		"icon":    team.Icon,
		"created": team.CreatedAt.Format(time.RFC3339),
		"updated": team.UpdatedAt.Format(time.RFC3339),
	}
	body := fmt.Sprintf(`
# %s

- **Key:** %s
- **ID:** %s
`, team.Name, team.Key, team.ID)
	return renderWithFrontmatter(fm, body)
}

// statesMarkdown renders the states.md content for a team's workflow states.
// Frontmatter goes through renderWithFrontmatter so a state named with a
// colon (or any YAML-hostile character) stays machine-parseable.
func statesMarkdown(team api.Team, states []api.State) []byte {
	entries := make([]map[string]any, 0, len(states))
	var table string
	for _, state := range states {
		entries = append(entries, map[string]any{
			"id": state.ID, "name": state.Name, "type": state.Type,
		})
		table += fmt.Sprintf("| %s | %s | %s |\n", state.Name, state.Type, state.ID)
	}

	fm := map[string]any{"team": team.Key, "states": entries}
	body := fmt.Sprintf(`
# Workflow States for %s

| Name | Type | ID |
|------|------|-----|
%s`, team.Key, table)
	return renderWithFrontmatter(fm, body)
}

// labelsMarkdown renders the labels.md content for a team's labels.
// Frontmatter goes through renderWithFrontmatter so a label named with a
// colon (or any YAML-hostile character) stays machine-parseable.
func labelsMarkdown(team api.Team, labels []api.Label) []byte {
	entries := make([]map[string]any, 0, len(labels))
	var table string
	for _, label := range labels {
		entry := map[string]any{
			"id": label.ID, "name": label.Name, "color": label.Color,
		}
		if label.Description != "" {
			entry["description"] = label.Description
		}
		entries = append(entries, entry)
		table += fmt.Sprintf("| %s | %s | %s |\n", label.Name, label.Color, label.ID)
	}

	fm := map[string]any{"team": team.Key, "labels": entries}
	body := fmt.Sprintf(`
# Labels for %s

| Name | Color | ID |
|------|-------|-----|
%s`, team.Key, table)
	return renderWithFrontmatter(fm, body)
}
