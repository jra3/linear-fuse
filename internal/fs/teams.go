package fs

import (
	"context"
	"fmt"
	"strings"
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
			Name: teamDirName(team),
			Mode: syscall.S_IFDIR,
		}
	}

	return fs.NewListDirStream(entries), 0
}

// teamDirName is the single owner of "what a team's directory under teams/ is
// called". Readdir, Lookup, the parent/subteam symlink targets and the
// frontmatter cross-references all derive from here, so a key safeName rewrites
// stays listable, resolvable and traversable rather than agreeing in one place
// and diverging in another.
func teamDirName(team api.Team) string {
	return safeName(team.Key, team.ID)
}

func (t *TeamsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	teams, err := t.lfs.repo.GetTeams(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, team := range teams {
		if teamDirName(team) == name {
			node := &TeamNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, entityCell: entityCell[api.Team]{val: team}}
			return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), teamDirIno(team.ID), inheritTimeout), 0
		}
	}

	return nil, syscall.ENOENT
}

// TeamNode represents a single team directory (e.g., /teams/ENG)
type TeamNode struct {
	attrNode
	entityCell[api.Team]
}

var _ fs.NodeReaddirer = (*TeamNode)(nil)
var _ fs.NodeLookuper = (*TeamNode)(nil)
var _ fs.NodeGetattrer = (*TeamNode)(nil)

// entity()/setEntity() are promoted from the embedded entityCell[api.Team].
// refreshFrom is the nodeRefresher seam (refresh.go): it pushes freshly-fetched
// state into this node when go-fuse dedups a later Lookup onto it.
func (t *TeamNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*TeamNode); ok {
		t.setEntity(f.entity())
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
		{Name: "subteams", Mode: syscall.S_IFDIR},
	}

	// The parent symlink is listed only when the team HAS a resolvable parent:
	// a top-level team is the common case, and an unconditional entry would
	// dangle for every one of them. subteams/ stays unconditional (an empty
	// directory is honest and needs no query), matching issues' children/.
	if parentLinkTarget(t.entity()) != "" {
		entries = append(entries, fuse.DirEntry{Name: "parent", Mode: syscall.S_IFLNK})
	}

	return fs.NewListDirStream(entries), 0
}

// parentLinkTarget returns the symlink target for a team's parent, or "" when
// the team has no parent — or has one the local workspace copy cannot name (an
// edge to a team this API key never listed). Readdir and Lookup must agree on
// that verdict, so both ask here.
func parentLinkTarget(team api.Team) string {
	if team.Parent == nil || team.Parent.Key == "" {
		return ""
	}
	// From teams/{KEY}/parent, ".." is teams/ — the sibling team dir is one
	// level up, named exactly as TeamsNode lists it.
	return "../" + teamDirName(*team.Parent)
}

// subteamLinkTarget is the target of teams/{KEY}/subteams/{CKEY}: the sibling
// team dir, two levels up. Readdir names the entry and Lookup builds the
// target, so both go through teamDirName here and cannot disagree.
func subteamLinkTarget(child api.Team) string {
	return "../../" + teamDirName(child)
}

func (t *TeamNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	team := t.entity() // snapshot captured by the arms and their closures
	switch name {
	case "team.md":
		// The sub-team edges are rendered here, so team.md needs the children
		// listing — a SQLite read per render, like states.md/labels.md. The
		// team's own times stay the reported ones: a child appearing is a
		// change to the child's row, not to this team.
		lfs := t.lfs
		return t.lookupRenderFile(ctx, out, "team.md", func(ctx context.Context) ([]byte, time.Time, time.Time) {
			children, err := lfs.repo.GetTeamChildren(ctx, team.ID)
			return teamMarkdown(team, children, err), team.UpdatedAt, team.CreatedAt
		}, 0, inheritTimeout), 0

	case "parent":
		target := parentLinkTarget(team)
		if target == "" {
			return nil, syscall.ENOENT
		}
		// The parent's times, not this team's: a stat through the link reports
		// the parent dir's own attributes anyway, and these are what the
		// stitched parent carries.
		return t.newSymlinkInode(ctx, out, target, team.Parent.CreatedAt, team.Parent.UpdatedAt), 0

	case "subteams":
		node := &SubteamsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, entityCell: entityCell[api.Team]{val: team}}
		// 0555: a read-only view. Sub-team membership is set in Linear, not by
		// mkdir here.
		na := nodeAttr{mode: 0555 | syscall.S_IFDIR, created: team.CreatedAt, updated: team.UpdatedAt}
		return t.newDirInode(ctx, out, name, node, na, subteamsDirIno(team.ID), inheritTimeout), 0

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
		node := &FilterRootNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, entityCell: entityCell[api.Team]{val: team}}
		return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), byDirIno(team.ID), inheritTimeout), 0

	case "cycles":
		node := &CyclesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, entityCell: entityCell[api.Team]{val: team}}
		return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), cyclesDirIno(team.ID), inheritTimeout), 0

	case "projects":
		node := &ProjectsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, entityCell: entityCell[api.Team]{val: team}}
		return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), projectsDirIno(team.ID), inheritTimeout), 0

	case "issues":
		node := &IssuesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, entityCell: entityCell[api.Team]{val: team}}
		// The stable ino is what makes create/delete invalidations against
		// issuesDirIno reach the kernel; without it InodeNotify targets an
		// inode the kernel never learned.
		return t.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), issuesDirIno(team.ID), inheritTimeout), 0

	case "recent":
		node := &RecentNode{attrNode: attrNode{BaseNode: BaseNode{lfs: t.lfs}}, entityCell: entityCell[api.Team]{val: team}}
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

// SubteamsNode represents the /teams/{KEY}/subteams/ directory: symlinks to
// the sibling directories of this team's sub-teams. Read-only — the hierarchy
// is set in Linear, and teams/ stays flat, so a sub-team is reachable both by
// its own key and through here.
type SubteamsNode struct {
	attrNode
	entityCell[api.Team]
}

var _ fs.NodeReaddirer = (*SubteamsNode)(nil)
var _ fs.NodeLookuper = (*SubteamsNode)(nil)
var _ fs.NodeGetattrer = (*SubteamsNode)(nil)

func (n *SubteamsNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*SubteamsNode); ok {
		n.setEntity(f.entity())
	}
}

func (n *SubteamsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	children, err := n.lfs.repo.GetTeamChildren(ctx, n.entity().ID)
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, len(children))
	for i, child := range children {
		entries[i] = fuse.DirEntry{
			Name: teamDirName(child),
			Mode: syscall.S_IFLNK,
		}
	}
	return fs.NewListDirStream(entries), 0
}

func (n *SubteamsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	children, err := n.lfs.repo.GetTeamChildren(ctx, n.entity().ID)
	if err != nil {
		return nil, syscall.EIO
	}
	for _, child := range children {
		if teamDirName(child) != name {
			continue
		}
		return n.newSymlinkInode(ctx, out, subteamLinkTarget(child), child.CreatedAt, child.UpdatedAt), 0
	}
	return nil, syscall.ENOENT
}

// teamMarkdown renders the team.md content for a team. Frontmatter goes
// through renderWithFrontmatter so hostile names stay valid YAML.
//
// The hierarchy is disclosed in both directions: `parent` and `subteams` carry
// the directory names the symlinks point at (safeName'd, so frontmatter and
// listing agree), and `parent_id` carries the raw edge — which is all that is
// left when the parent team itself is not in the local workspace copy.
//
// childrenErr is the verdict of the sub-team load, and it is NOT the same as an
// empty list: a failed load omits the `subteams` key entirely (absence means
// unknown) and says so in the body, because rendering "no sub-teams" would
// contradict subteams/ Readdir, which returns EIO for the same failure. This
// follows states.md/labels.md, which emit "# Error loading …" rather than an
// empty catalog.
func teamMarkdown(team api.Team, children []api.Team, childrenErr error) []byte {
	fm := map[string]any{
		"id":      team.ID,
		"key":     team.Key,
		"name":    team.Name,
		"icon":    team.Icon,
		"created": team.CreatedAt.Format(time.RFC3339),
		"updated": team.UpdatedAt.Format(time.RFC3339),
	}

	hierarchy := ""
	if team.Parent != nil {
		fm["parent_id"] = team.Parent.ID
		if team.Parent.Key != "" {
			fm["parent"] = teamDirName(*team.Parent)
			hierarchy += fmt.Sprintf("- **Parent team:** %s (`parent` symlink)\n", team.Parent.Key)
		} else {
			hierarchy += fmt.Sprintf("- **Parent team:** %s (not in this workspace copy)\n", team.Parent.ID)
		}
	}
	switch {
	case childrenErr != nil:
		hierarchy += "- **Sub-teams:** error loading sub-teams — unknown, not none (`subteams/` reports the same failure)\n"
	case len(children) > 0:
		keys := make([]string, 0, len(children))
		for _, c := range children {
			keys = append(keys, teamDirName(c))
		}
		fm["subteams"] = keys
		hierarchy += fmt.Sprintf("- **Sub-teams:** %s (`subteams/`)\n", strings.Join(keys, ", "))
	}

	body := fmt.Sprintf(`
# %s

- **Key:** %s
- **ID:** %s
%s`, team.Name, team.Key, team.ID, hierarchy)
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
