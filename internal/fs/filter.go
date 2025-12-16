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

// FilterRootNode represents the .filter/ directory
type FilterRootNode struct {
	fs.Inode
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeReaddirer = (*FilterRootNode)(nil)
var _ fs.NodeLookuper = (*FilterRootNode)(nil)
var _ fs.NodeGetattrer = (*FilterRootNode)(nil)

var filterCategories = []string{"status", "priority", "label", "assignee"}

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

// FilterCategoryNode represents a filter category directory (e.g., .filter/status/)
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
	// Priority always shows all 4 values
	if f.category == "priority" {
		return []string{"urgent", "high", "medium", "low"}, nil
	}

	issues, err := f.lfs.GetTeamIssues(ctx, f.team.ID)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)

	switch f.category {
	case "status":
		for _, issue := range issues {
			seen[issue.State.Name] = true
		}
	case "label":
		for _, issue := range issues {
			for _, label := range issue.Labels.Nodes {
				seen[label.Name] = true
			}
		}
	case "assignee":
		seen["unassigned"] = true
		for _, issue := range issues {
			if issue.Assignee != nil {
				seen[assigneeHandle(issue.Assignee)] = true
			}
		}
	}

	// Convert to sorted slice
	values := make([]string, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	sort.Strings(values)
	return values, nil
}

// FilterValueNode represents a filter value directory (e.g., .filter/status/In Progress/)
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

	entries := make([]fuse.DirEntry, len(issues))
	for i, issue := range issues {
		entries[i] = fuse.DirEntry{
			Name: issue.Identifier + ".md",
			Mode: syscall.S_IFLNK,
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
		if issue.Identifier+".md" == name {
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
	issues, err := f.lfs.GetTeamIssues(ctx, f.team.ID)
	if err != nil {
		return nil, err
	}

	var filtered []api.Issue
	for _, issue := range issues {
		if f.matchesFilter(issue) {
			filtered = append(filtered, issue)
		}
	}
	return filtered, nil
}

func (f *FilterValueNode) matchesFilter(issue api.Issue) bool {
	switch f.category {
	case "status":
		return issue.State.Name == f.value
	case "priority":
		return api.PriorityName(issue.Priority) == f.value
	case "label":
		for _, label := range issue.Labels.Nodes {
			if label.Name == f.value {
				return true
			}
		}
		return false
	case "assignee":
		if f.value == "unassigned" {
			return issue.Assignee == nil
		}
		return issue.Assignee != nil && assigneeHandle(issue.Assignee) == f.value
	}
	return false
}

// FilterIssueSymlink is a symlink pointing to an issue in issues/<identifier>/issue.md
// Path from .filter/category/value/ to issues/ is ../../../issues/
type FilterIssueSymlink struct {
	fs.Inode
	identifier string
}

var _ fs.NodeReadlinker = (*FilterIssueSymlink)(nil)
var _ fs.NodeGetattrer = (*FilterIssueSymlink)(nil)

func (s *FilterIssueSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	// From .filter/category/value/ go up 3 levels to team dir, then into issues/
	target := fmt.Sprintf("../../../issues/%s/issue.md", s.identifier)
	return []byte(target), 0
}

func (s *FilterIssueSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0777 | syscall.S_IFLNK
	target := fmt.Sprintf("../../../issues/%s/issue.md", s.identifier)
	out.Size = uint64(len(target))
	return 0
}
