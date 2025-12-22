package fs

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// MyNode represents the /my directory (personal views)
type MyNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*MyNode)(nil)
var _ fs.NodeLookuper = (*MyNode)(nil)
var _ fs.NodeGetattrer = (*MyNode)(nil)

func (m *MyNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	return 0
}

func (m *MyNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "assigned", Mode: syscall.S_IFDIR},
		{Name: "created", Mode: syscall.S_IFDIR},
		{Name: "active", Mode: syscall.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (m *MyNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	now := time.Now()
	out.Attr.Mode = 0755 | syscall.S_IFDIR
	out.Attr.SetTimes(&now, &now, &now)

	switch name {
	case "assigned":
		node := &MyIssuesNode{lfs: m.lfs, issueType: "assigned"}
		return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	case "created":
		node := &MyIssuesNode{lfs: m.lfs, issueType: "created"}
		return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	case "active":
		node := &MyIssuesNode{lfs: m.lfs, issueType: "active"}
		return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	default:
		return nil, syscall.ENOENT
	}
}

// MyIssuesNode represents /my/{assigned,created,active} directories
type MyIssuesNode struct {
	fs.Inode
	lfs       *LinearFS
	issueType string // "assigned", "created", or "active"
}

var _ fs.NodeReaddirer = (*MyIssuesNode)(nil)
var _ fs.NodeLookuper = (*MyIssuesNode)(nil)
var _ fs.NodeGetattrer = (*MyIssuesNode)(nil)

func (m *MyIssuesNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	return 0
}

func (m *MyIssuesNode) getIssues(ctx context.Context) ([]api.Issue, error) {
	switch m.issueType {
	case "created":
		return m.lfs.GetMyCreatedIssues(ctx)
	case "active":
		return m.lfs.GetMyActiveIssues(ctx)
	default:
		return m.lfs.GetMyIssues(ctx)
	}
}

func (m *MyIssuesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := m.getIssues(ctx)
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

func (m *MyIssuesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle search directory
	if name == "search" {
		now := time.Now()
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.SetTimes(&now, &now, &now)
		node := &ScopedSearchNode{
			source:       IssueSourceFunc(m.getIssues),
			symlinkDepth: 3, // /my/assigned/search/{query}/ -> need 4 "../" to reach root
		}
		return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	issues, err := m.getIssues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			teamKey := ""
			if issue.Team != nil {
				teamKey = issue.Team.Key
			}
			node := &IssueDirSymlink{
				teamKey:    teamKey,
				identifier: issue.Identifier,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}

	return nil, syscall.ENOENT
}
