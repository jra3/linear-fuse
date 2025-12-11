package fs

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/marshal"
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
			Mode: syscall.S_IFREG,
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
			// Pre-generate content so Getattr returns correct size
			content, err := marshal.IssueToMarkdown(&issue)
			if err != nil {
				return nil, syscall.EIO
			}
			node := &IssueNode{
				lfs:          m.lfs,
				issue:        issue,
				content:      content,
				contentReady: true,
			}
			// Set attributes on EntryOut so ls shows correct size/times
			out.Attr.Mode = 0644 | syscall.S_IFREG
			out.Attr.Size = uint64(len(content))
			out.SetAttrTimeout(30 * time.Second)
			out.SetEntryTimeout(30 * time.Second)
			out.Attr.SetTimes(&issue.UpdatedAt, &issue.UpdatedAt, &issue.CreatedAt)
			return m.NewInode(ctx, node, fs.StableAttr{
				Mode: syscall.S_IFREG,
				Ino:  issueIno(issue.ID),
			}), 0
		}
	}

	return nil, syscall.ENOENT
}
