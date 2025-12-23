package fs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// assigneeHandle returns the handle for an assignee (prefers DisplayName, falls back to email local part)
func assigneeHandle(user *api.User) string {
	if user == nil {
		return ""
	}
	if user.DisplayName != "" {
		return user.DisplayName
	}
	// Fallback to email local part
	if idx := strings.Index(user.Email, "@"); idx != -1 {
		return user.Email[:idx]
	}
	return user.Email
}

// FilterRootNode represents the by/ directory
type FilterRootNode struct {
	fs.Inode
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeReaddirer = (*FilterRootNode)(nil)
var _ fs.NodeLookuper = (*FilterRootNode)(nil)
var _ fs.NodeGetattrer = (*FilterRootNode)(nil)

var filterCategories = []string{"status", "label", "assignee"}

func (f *FilterRootNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	return 0
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
	for _, cat := range filterCategories {
		if cat == name {
			now := time.Now()
			out.Attr.Mode = 0755 | syscall.S_IFDIR
			out.Attr.SetTimes(&now, &now, &now)
			node := &FilterCategoryNode{
				lfs:      f.lfs,
				team:     f.team,
				category: name,
			}
			return f.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}
	return nil, syscall.ENOENT
}

// FilterCategoryNode represents a filter category directory (e.g., by/status/)
type FilterCategoryNode struct {
	fs.Inode
	lfs      *LinearFS
	team     api.Team
	category string
}

var _ fs.NodeReaddirer = (*FilterCategoryNode)(nil)
var _ fs.NodeLookuper = (*FilterCategoryNode)(nil)
var _ fs.NodeGetattrer = (*FilterCategoryNode)(nil)

func (f *FilterCategoryNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	return 0
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

	for _, val := range values {
		if val == name {
			now := time.Now()
			out.Attr.Mode = 0755 | syscall.S_IFDIR
			out.Attr.SetTimes(&now, &now, &now)
			node := &FilterValueNode{
				lfs:      f.lfs,
				team:     f.team,
				category: f.category,
				value:    name,
			}
			return f.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}
	return nil, syscall.ENOENT
}

func (f *FilterCategoryNode) getUniqueValues(ctx context.Context) ([]string, error) {
	switch f.category {
	case "status":
		// Use team states from API - much faster than scanning all issues
		states, err := f.lfs.GetTeamStates(ctx, f.team.ID)
		if err != nil {
			return nil, err
		}
		values := make([]string, len(states))
		for i, state := range states {
			values[i] = state.Name
		}
		sort.Strings(values)
		return values, nil

	case "label":
		// Use team labels from API - much faster than scanning all issues
		labels, err := f.lfs.GetTeamLabels(ctx, f.team.ID)
		if err != nil {
			return nil, err
		}
		values := make([]string, len(labels))
		for i, label := range labels {
			values[i] = label.Name
		}
		sort.Strings(values)
		return values, nil

	case "assignee":
		// Use team members - show only users who are members of this team plus "unassigned"
		users, err := f.lfs.GetTeamMembers(ctx, f.team.ID)
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

// FilterValueNode represents a filter value directory (e.g., by/status/In Progress/)
type FilterValueNode struct {
	fs.Inode
	lfs      *LinearFS
	team     api.Team
	category string
	value    string
}

var _ fs.NodeReaddirer = (*FilterValueNode)(nil)
var _ fs.NodeLookuper = (*FilterValueNode)(nil)
var _ fs.NodeGetattrer = (*FilterValueNode)(nil)

func (f *FilterValueNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	return 0
}

func (f *FilterValueNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := f.getFilteredIssues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	// +1 for search directory
	entries := make([]fuse.DirEntry, len(issues)+1)
	entries[0] = fuse.DirEntry{
		Name: "search",
		Mode: syscall.S_IFDIR,
	}
	for i, issue := range issues {
		entries[i+1] = fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFLNK, // Symlink to issue directory
		}
	}
	return fs.NewListDirStream(entries), 0
}

func (f *FilterValueNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle search directory
	if name == "search" {
		now := time.Now()
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.SetTimes(&now, &now, &now)
		node := &ScopedSearchNode{
			source:       IssueSourceFunc(f.getFilteredIssues),
			symlinkDepth: 6, // /teams/ENG/by/status/Todo/search/{query}/ -> need 7 "../" to reach root
		}
		return f.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	issues, err := f.getFilteredIssues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			node := &FilterIssueSymlink{
				identifier: issue.Identifier,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			return f.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}
	return nil, syscall.ENOENT
}

func (f *FilterValueNode) getFilteredIssues(ctx context.Context) ([]api.Issue, error) {
	// Use server-side filtering for much better performance
	switch f.category {
	case "status":
		return f.lfs.GetFilteredIssuesByStatus(ctx, f.team.ID, f.value)
	case "label":
		return f.lfs.GetFilteredIssuesByLabel(ctx, f.team.ID, f.value)
	case "assignee":
		if f.value == "unassigned" {
			return f.lfs.GetFilteredIssuesUnassigned(ctx, f.team.ID)
		}
		// Need to resolve assignee handle to ID
		assigneeID, err := f.resolveAssigneeID(ctx)
		if err != nil {
			return nil, err
		}
		return f.lfs.GetFilteredIssuesByAssignee(ctx, f.team.ID, assigneeID)
	default:
		return nil, fmt.Errorf("unknown filter category: %s", f.category)
	}
}

// resolveAssigneeID converts an assignee handle (display name or email prefix) to user ID
func (f *FilterValueNode) resolveAssigneeID(ctx context.Context) (string, error) {
	users, err := f.lfs.GetTeamMembers(ctx, f.team.ID)
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

// FilterIssueSymlink is a symlink pointing to an issue directory
// Path from by/category/value/ to issues/ is ../../../issues/
type FilterIssueSymlink struct {
	fs.Inode
	identifier string
}

var _ fs.NodeReadlinker = (*FilterIssueSymlink)(nil)
var _ fs.NodeGetattrer = (*FilterIssueSymlink)(nil)

func (s *FilterIssueSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	// From by/category/value/ go up 3 levels to team dir, then into issues/
	target := fmt.Sprintf("../../../issues/%s", s.identifier)
	return []byte(target), 0
}

func (s *FilterIssueSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	target := fmt.Sprintf("../../../issues/%s", s.identifier)
	out.Mode = 0777 | syscall.S_IFLNK
	out.Size = uint64(len(target))
	return 0
}
