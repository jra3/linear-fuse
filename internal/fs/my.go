package fs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// MyNode represents the /my directory (personal views)
type MyNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*MyNode)(nil)
var _ fs.NodeLookuper = (*MyNode)(nil)

func (m *MyNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "assigned", Mode: syscall.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (m *MyNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "assigned":
		node := &MyAssignedNode{lfs: m.lfs}
		return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	default:
		return nil, syscall.ENOENT
	}
}

// MyAssignedNode represents /my/assigned directory
type MyAssignedNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*MyAssignedNode)(nil)
var _ fs.NodeLookuper = (*MyAssignedNode)(nil)

func (m *MyAssignedNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := m.lfs.GetMyIssues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(issues))
	for i, issue := range issues {
		entries[i] = fuse.DirEntry{
			Name: issue.Identifier + ".md",
			Mode: syscall.S_IFLNK, // Symlink
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (m *MyAssignedNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	issues, err := m.lfs.GetMyIssues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier+".md" == name {
			// Create symlink to team directory
			teamKey := ""
			if issue.Team != nil {
				teamKey = issue.Team.Key
			}
			node := &IssueSymlink{
				teamKey:    teamKey,
				identifier: issue.Identifier,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			return m.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}

	return nil, syscall.ENOENT
}
