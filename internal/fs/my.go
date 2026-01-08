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
	BaseNode
}

var _ fs.NodeReaddirer = (*MyNode)(nil)
var _ fs.NodeLookuper = (*MyNode)(nil)
var _ fs.NodeGetattrer = (*MyNode)(nil)

func (m *MyNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	m.SetOwner(out)
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
	out.Attr.Uid = m.lfs.uid
	out.Attr.Gid = m.lfs.gid
	out.Attr.SetTimes(&now, &now, &now)

	switch name {
	case "assigned":
		node := &MyIssuesNode{BaseNode: BaseNode{lfs: m.lfs}, issueType: "assigned"}
		return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	case "created":
		node := &MyIssuesNode{BaseNode: BaseNode{lfs: m.lfs}, issueType: "created"}
		return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	case "active":
		node := &MyIssuesNode{BaseNode: BaseNode{lfs: m.lfs}, issueType: "active"}
		return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	default:
		return nil, syscall.ENOENT
	}
}

// MyIssuesNode represents /my/{assigned,created,active} directories
type MyIssuesNode struct {
	BaseNode
	issueType string // "assigned", "created", or "active"
}

var _ fs.NodeReaddirer = (*MyIssuesNode)(nil)
var _ fs.NodeLookuper = (*MyIssuesNode)(nil)
var _ fs.NodeGetattrer = (*MyIssuesNode)(nil)

func (m *MyIssuesNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	m.SetOwner(out)
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

	entries := make([]fuse.DirEntry, len(issues))
	for i, issue := range issues {
		entries[i] = fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFLNK, // Symlink to issue directory
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (m *MyIssuesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
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
				BaseNode:   BaseNode{lfs: m.lfs},
				teamKey:    teamKey,
				identifier: issue.Identifier,
				createdAt:  issue.CreatedAt,
				updatedAt:  issue.UpdatedAt,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			out.Attr.Uid = m.lfs.uid
			out.Attr.Gid = m.lfs.gid
			out.Attr.SetTimes(&issue.UpdatedAt, &issue.UpdatedAt, &issue.CreatedAt)
			return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}

	return nil, syscall.ENOENT
}
