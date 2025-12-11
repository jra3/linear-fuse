package fs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// RootNode is the root directory of the filesystem
type RootNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*RootNode)(nil)
var _ fs.NodeLookuper = (*RootNode)(nil)

func (r *RootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "teams", Mode: syscall.S_IFDIR},
		{Name: "my", Mode: syscall.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (r *RootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "teams":
		node := &TeamsNode{lfs: r.lfs}
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "my":
		node := &MyNode{lfs: r.lfs}
		return r.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	default:
		return nil, syscall.ENOENT
	}
}
