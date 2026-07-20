package fs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// assigneeHandle returns the handle for an assignee (prefers DisplayName, falls
// back to email local part). safeName is the final safety pass: it strips
// traversal/control chars from the handle and, critically for #332, escapes a
// handle that lands exactly on the "unassigned" bucket literal so a real user
// named "unassigned" cannot shadow the unassigned view. Both the by/assignee
// value list and resolveAssigneeID derive through this one function, so the
// sanitized handle stays a consistent resolution key.
func assigneeHandle(user *api.User) string {
	if user == nil {
		return ""
	}
	handle := user.DisplayName
	if handle == "" {
		// Fallback to email local part
		if idx := strings.Index(user.Email, "@"); idx != -1 {
			handle = user.Email[:idx]
		} else {
			handle = user.Email
		}
	}
	return safeName(handle, user.ID)
}

// FilterRootNode represents the by/ directory. It holds a team snapshot and
// reports the team's times; Getattr comes from the attrNode mixin.
type FilterRootNode struct {
	attrNode
	entityCell[api.Team]
}

var _ fs.NodeReaddirer = (*FilterRootNode)(nil)
var _ fs.NodeLookuper = (*FilterRootNode)(nil)
var _ fs.NodeGetattrer = (*FilterRootNode)(nil)

var filterCategories = []string{"status", "label", "assignee"}

// entity()/setEntity() are promoted from the embedded entityCell[api.Team].
// refreshFrom is the nodeRefresher seam (refresh.go).
func (f *FilterRootNode) refreshFrom(fresh fs.InodeEmbedder) {
	if fr, ok := fresh.(*FilterRootNode); ok {
		f.setEntity(fr.entity())
	}
}

func (f *FilterRootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := make([]fuse.DirEntry, len(filterCategories))
	for i, cat := range filterCategories {
		entries[i] = fuse.DirEntry{
			Name: cat,
			Mode: syscall.S_IFDIR,
		}
	}
	return fs.NewListDirStream(entries), 0
}

func (f *FilterRootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	team := f.entity()
	for _, cat := range filterCategories {
		if cat == name {
			node := &FilterCategoryNode{
				attrNode:   attrNode{BaseNode: BaseNode{lfs: f.lfs}},
				entityCell: entityCell[api.Team]{val: team},
				category:   name,
			}
			return f.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), byCategoryIno(team.ID, name), inheritTimeout), 0
		}
	}
	return nil, syscall.ENOENT
}

// FilterCategoryNode represents a filter category directory (e.g., by/status/).
// The category is immutable identity; the team snapshot is the volatile half.
type FilterCategoryNode struct {
	attrNode
	entityCell[api.Team]
	category string
}

var _ fs.NodeReaddirer = (*FilterCategoryNode)(nil)
var _ fs.NodeLookuper = (*FilterCategoryNode)(nil)
var _ fs.NodeGetattrer = (*FilterCategoryNode)(nil)

// entity()/setEntity() are promoted from the embedded entityCell[api.Team]; the
// category is immutable identity. refreshFrom is the nodeRefresher seam.
func (f *FilterCategoryNode) refreshFrom(fresh fs.InodeEmbedder) {
	if fr, ok := fresh.(*FilterCategoryNode); ok {
		f.setEntity(fr.entity())
	}
}

func (f *FilterCategoryNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	values, err := f.getUniqueValues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(values))
	for i, val := range values {
		entries[i] = fuse.DirEntry{
			Name: val,
			Mode: syscall.S_IFDIR,
		}
	}
	return fs.NewListDirStream(entries), 0
}

func (f *FilterCategoryNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	values, err := f.getUniqueValues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	team := f.entity()
	for _, val := range values {
		if val == name {
			node := &FilterValueNode{
				attrNode:   attrNode{BaseNode: BaseNode{lfs: f.lfs}},
				entityCell: entityCell[api.Team]{val: team},
				category:   f.category,
				value:      name,
			}
			return f.newDirInode(ctx, out, name, node, dirAttr(team.CreatedAt, team.UpdatedAt), byValueIno(team.ID, f.category, name), inheritTimeout), 0
		}
	}
	return nil, syscall.ENOENT
}

func (f *FilterCategoryNode) getUniqueValues(ctx context.Context) ([]string, error) {
	teamID := f.entity().ID
	switch f.category {
	case "status":
		// Use team states from API - much faster than scanning all issues.
		// The state name is a remote string, so the directory value is the
		// safeName of it (traversal/control chars, reserved-literal escape).
		states, err := f.lfs.repo.GetTeamStates(ctx, teamID)
		if err != nil {
			return nil, err
		}
		values := make([]string, len(states))
		for i, state := range states {
			values[i] = safeName(state.Name, state.ID)
		}
		sort.Strings(values)
		return values, nil

	case "label":
		// Use team labels from API - much faster than scanning all issues.
		// The label name is a remote string; the directory value is its safeName.
		labels, err := f.lfs.repo.GetTeamLabels(ctx, teamID)
		if err != nil {
			return nil, err
		}
		values := make([]string, len(labels))
		for i, label := range labels {
			values[i] = safeName(label.Name, label.ID)
		}
		sort.Strings(values)
		return values, nil

	case "assignee":
		// Use team members - show only users who are members of this team plus "unassigned"
		users, err := f.lfs.repo.GetTeamMembers(ctx, teamID)
		if err != nil {
			return nil, err
		}
		values := make([]string, 0, len(users)+1)
		values = append(values, "unassigned")
		for _, user := range users {
			values = append(values, assigneeHandle(&user))
		}
		sort.Strings(values)
		return values, nil
	}

	return nil, nil
}

// FilterValueNode represents a filter value directory (e.g., by/status/In Progress/).
// category/value are immutable identity; the team snapshot is the volatile half.
type FilterValueNode struct {
	attrNode
	entityCell[api.Team]
	category string
	value    string
}

var _ fs.NodeReaddirer = (*FilterValueNode)(nil)
var _ fs.NodeLookuper = (*FilterValueNode)(nil)
var _ fs.NodeGetattrer = (*FilterValueNode)(nil)

// entity()/setEntity() are promoted from the embedded entityCell[api.Team];
// category/value are immutable identity. refreshFrom is the nodeRefresher seam.
func (f *FilterValueNode) refreshFrom(fresh fs.InodeEmbedder) {
	if fr, ok := fresh.(*FilterValueNode); ok {
		f.setEntity(fr.entity())
	}
}

func (f *FilterValueNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := f.getFilteredIssues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(issues))
	for i, issue := range issues {
		entries[i] = fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFLNK, // Symlink to issue directory
		}
	}
	return fs.NewListDirStream(entries), 0
}

func (f *FilterValueNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	issues, err := f.getFilteredIssues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			// From by/category/value/ go up 3 levels to team dir, then into issues/
			target := fmt.Sprintf("../../../issues/%s", safeName(issue.Identifier, issue.ID))
			return f.newSymlinkInode(ctx, out, target, issue.CreatedAt, issue.UpdatedAt), 0
		}
	}
	return nil, syscall.ENOENT
}

func (f *FilterValueNode) getFilteredIssues(ctx context.Context) ([]api.Issue, error) {
	teamID := f.entity().ID
	// Use server-side filtering for much better performance. f.value is the
	// safeName'd directory name, so resolve it back to the entity's real name
	// (GetStateByName/GetLabelByName match the raw remote name) before filtering.
	switch f.category {
	case "status":
		name, err := f.resolveStateName(ctx)
		if err != nil {
			return nil, err
		}
		return f.lfs.GetFilteredIssuesByStatus(ctx, teamID, name)
	case "label":
		name, err := f.resolveLabelName(ctx)
		if err != nil {
			return nil, err
		}
		return f.lfs.GetFilteredIssuesByLabel(ctx, teamID, name)
	case "assignee":
		if f.value == "unassigned" {
			return f.lfs.repo.GetUnassignedIssues(ctx, teamID)
		}
		// Need to resolve assignee handle to ID
		assigneeID, err := f.resolveAssigneeID(ctx)
		if err != nil {
			return nil, err
		}
		return f.lfs.repo.GetIssuesByAssignee(ctx, teamID, assigneeID)
	default:
		return nil, fmt.Errorf("unknown filter category: %s", f.category)
	}
}

// resolveStateName maps the safeName'd status directory value back to a state's
// real remote name, which the name-keyed filter query matches. An unresolvable
// value (a state that vanished since the listing) yields the raw value, which
// GetStateByName then reports as no-match (empty result).
func (f *FilterValueNode) resolveStateName(ctx context.Context) (string, error) {
	states, err := f.lfs.repo.GetTeamStates(ctx, f.entity().ID)
	if err != nil {
		return "", err
	}
	for _, state := range states {
		if safeName(state.Name, state.ID) == f.value {
			return state.Name, nil // safename:ok resolution key (feeds GetStateByName, not a path)
		}
	}
	return f.value, nil
}

// resolveLabelName maps the safeName'd label directory value back to a label's
// real remote name for the name-keyed filter query, mirroring resolveStateName.
func (f *FilterValueNode) resolveLabelName(ctx context.Context) (string, error) {
	labels, err := f.lfs.repo.GetTeamLabels(ctx, f.entity().ID)
	if err != nil {
		return "", err
	}
	for _, label := range labels {
		if safeName(label.Name, label.ID) == f.value {
			return label.Name, nil // safename:ok resolution key (feeds GetLabelByName, not a path)
		}
	}
	return f.value, nil
}

// resolveAssigneeID converts an assignee handle (display name or email prefix) to user ID
func (f *FilterValueNode) resolveAssigneeID(ctx context.Context) (string, error) {
	users, err := f.lfs.repo.GetTeamMembers(ctx, f.entity().ID)
	if err != nil {
		return "", err
	}

	for _, user := range users {
		if assigneeHandle(&user) == f.value {
			return user.ID, nil
		}
	}
	return "", fmt.Errorf("unknown assignee: %s", f.value)
}
