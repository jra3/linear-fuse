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
// MetaFileNode is READ-THROUGH: it holds a render closure (not baked bytes) and
// renders current content inside Read/Getattr, exactly like ErrorFileNode and
// SuccessFileNode. This is load-bearing: go-fuse dedups inodes by StableAttr.Ino,
// so a second Lookup for the same entity returns the *original* node and discards
// any freshly-constructed one — baking bytes at Lookup would serve stale content
// for the life of the mount. Rendering on demand from a live source (the repo)
// means the served node always reflects current state. Editors of the sibling
// editable file call InvalidateUpdated(metaIno(id)) so the kernel drops its
// cached size/attrs and re-reads.

// metaIno derives the stable inode for an `<entity>.meta` file from a key
// (typically the entity's Linear ID). The "meta:" prefix keeps it from colliding
// with error/success/entity inodes.
func metaIno(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("meta:" + key))
	return h.Sum64()
}

// lookupMetaFile mounts a read-only `.meta` virtual file backed by a render
// closure as a child of parent. render is called on demand (Lookup/Read/Getattr)
// and must return the current meta bytes from a live source. mtime/ctime are the
// entity's updated/created times (best-effort; refreshed on the next Lookup).
func (lfs *LinearFS) lookupMetaFile(ctx context.Context, parent fs.InodeEmbedder, key string, render func() []byte, mtime, ctime time.Time, out *fuse.EntryOut) *fs.Inode {
	node := &MetaFileNode{BaseNode: BaseNode{lfs: lfs}, render: render, mtime: mtime, ctime: ctime}

	out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
	out.Attr.Uid = lfs.uid
	out.Attr.Gid = lfs.gid
	out.Attr.Size = uint64(len(render()))
	// Feedback-file caching model: don't trust a cached size. Editors of the
	// sibling editable file InvalidateUpdated(metaIno) to force a refresh.
	out.SetAttrTimeout(0)
	out.SetEntryTimeout(0)
	out.Attr.SetTimes(&mtime, &mtime, &ctime)

	return parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  metaIno(key),
	})
}

// MetaFileNode is a read-only virtual file that renders `.meta` content on demand
// from a live source (never baked bytes — see the package comment on go-fuse
// inode dedup).
type MetaFileNode struct {
	BaseNode
	render func() []byte
	mtime  time.Time
	ctime  time.Time
}

var _ fs.NodeGetattrer = (*MetaFileNode)(nil)
var _ fs.NodeOpener = (*MetaFileNode)(nil)
var _ fs.NodeReader = (*MetaFileNode)(nil)

func (m *MetaFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0444 // Read-only
	m.SetOwner(out)
	out.Size = uint64(len(m.render()))
	out.SetTimes(&m.mtime, &m.mtime, &m.ctime)
	return 0
}

func (m *MetaFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Reject writes explicitly (read-only sidecar).
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		return nil, 0, syscall.EACCES
	}
	// DIRECT_IO: content is volatile; force a real READ (through render) on each
	// open instead of trusting a cached page.
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (m *MetaFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := m.render()
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}
