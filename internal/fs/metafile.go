package fs

import (
	"context"
	"hash/fnv"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// The read-only `<entity>.meta` sidecar.
//
// Under the "editable in, server-managed out" write contract (#150), each
// editable file (issue.md, project.md, initiative.md) carries only the fields a
// writer may set. The server-managed, write-volatile fields (id, url, updated,
// slug, branch, timestamps, links, relations, …) live here instead, so a
// successful write to the editable file never rewrites the bytes the writer
// wrote — the staleness/"modified since read" churn goes away.
//
// MetaFileNode holds pre-rendered content and serves it read-only with
// FOPEN_DIRECT_IO + zero attr/entry timeouts. Because the content is regenerated
// from the freshly-fetched entity on every Lookup (and DIRECT_IO forces a real
// READ), freshness after a Flush is automatic — no per-Flush meta-inode
// invalidation is needed.

// metaIno derives the stable inode for an `<entity>.meta` file from a key
// (typically the entity's Linear ID). The "meta:" prefix keeps it from colliding
// with error/success/entity inodes.
func metaIno(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("meta:" + key))
	return h.Sum64()
}

// lookupMetaFile mounts a read-only `.meta` virtual file with the given
// pre-rendered content as a child of parent. key drives the stable inode.
func (lfs *LinearFS) lookupMetaFile(ctx context.Context, parent fs.InodeEmbedder, key string, content []byte, out *fuse.EntryOut) *fs.Inode {
	node := &MetaFileNode{BaseNode: BaseNode{lfs: lfs}, content: content}

	now := time.Now()
	out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
	out.Attr.Uid = lfs.uid
	out.Attr.Gid = lfs.gid
	out.Attr.Size = uint64(len(content))
	// Zero timeouts + DIRECT_IO: always reflect the latest server state.
	out.SetAttrTimeout(0)
	out.SetEntryTimeout(0)
	out.Attr.SetTimes(&now, &now, &now)

	return parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  metaIno(key),
	})
}

// MetaFileNode is a read-only virtual file serving pre-rendered `.meta` content.
type MetaFileNode struct {
	BaseNode
	content []byte
}

var _ fs.NodeGetattrer = (*MetaFileNode)(nil)
var _ fs.NodeOpener = (*MetaFileNode)(nil)
var _ fs.NodeReader = (*MetaFileNode)(nil)

func (m *MetaFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0444 // Read-only
	m.SetOwner(out)
	out.Size = uint64(len(m.content))
	return 0
}

func (m *MetaFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Reject writes explicitly (read-only sidecar).
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		return nil, 0, syscall.EACCES
	}
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (m *MetaFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(m.content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(m.content)) {
		end = int64(len(m.content))
	}
	return fuse.ReadResultData(m.content[off:end]), 0
}
