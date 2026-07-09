package fs

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// MyNode represents the /my directory (personal views). Stateless container:
// zero times (honest unknown); Getattr comes from the attrNode mixin.
type MyNode struct {
	attrNode
}

var _ fs.NodeReaddirer = (*MyNode)(nil)
var _ fs.NodeLookuper = (*MyNode)(nil)
var _ fs.NodeGetattrer = (*MyNode)(nil)

func (m *MyNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "assigned", Mode: syscall.S_IFDIR},
		{Name: "created", Mode: syscall.S_IFDIR},
		{Name: "active", Mode: syscall.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (m *MyNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "assigned", "created", "active":
		// Stateless like the parent (the name IS the identity): zero times,
		// ino keyed on the fixed subdir name.
		node := &MyIssuesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: m.lfs}}, issueType: name}
		return m.newDirInode(ctx, out, name, node, dirAttr(time.Time{}, time.Time{}), myDirIno(name), inheritTimeout), 0
	default:
		return nil, syscall.ENOENT
	}
}

// MyIssuesNode represents /my/{assigned,created,active} directories
type MyIssuesNode struct {
	attrNode
	issueType string // "assigned", "created", or "active"
}

var _ fs.NodeReaddirer = (*MyIssuesNode)(nil)
var _ fs.NodeLookuper = (*MyIssuesNode)(nil)
var _ fs.NodeGetattrer = (*MyIssuesNode)(nil)

func (m *MyIssuesNode) getIssues(ctx context.Context) ([]api.Issue, error) {
	switch m.issueType {
	case "created":
		return m.lfs.repo.GetMyCreatedIssues(ctx)
	case "active":
		return m.lfs.repo.GetMyActiveIssues(ctx)
	default:
		return m.lfs.repo.GetMyIssues(ctx)
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
			target, errno := teamIssueTarget(issue)
			if errno != 0 {
				return nil, errno
			}
			return m.newSymlinkInode(ctx, out, target, issue.CreatedAt, issue.UpdatedAt), 0
		}
	}

	return nil, syscall.ENOENT
}
