package fs

import (
	"context"
	"hash/fnv"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Atomic save-via-rename support (#145).
//
// Editors and the Claude Code Edit/Write tools never write a file in place.
// They create a sibling temp file (e.g. issue.md.tmp.<pid>.<rand>), write the
// full new contents into it, then rename(2) it over the real file. LinearFS
// can't materialize arbitrary persistent files inside an issue/project/initiative
// directory, so go-fuse rejected the temp-file create with a misleading EROFS
// ("read-only file system") even though the mount is rw — making the documented
// "use your editor / Edit tool" path unusable.
//
// The fix: accept the temp-file create as an in-memory scratch buffer
// (scratchFileNode), then have the directory's Rename handler route the buffered
// bytes into the same write path a direct in-place write uses when the scratch
// file is renamed onto the canonical editable file.

// scratchFileNode is an in-memory scratch file backing an editor's atomic-save
// temp file. It only buffers bytes; it performs no Linear I/O of its own. The
// parent directory's Rename handler is responsible for persisting the buffer
// when the scratch file is renamed onto the directory's editable file.
type scratchFileNode struct {
	BaseNode

	mu       sync.Mutex
	buf      []byte
	consumed bool // a rename took the buffer; further access must fail loud
}

// consume marks the scratch buffer spent after a rename has persisted its bytes.
// go-fuse leaves the spent node serving the canonical file's name (MvChild)
// until the rename invalidation lands; a consumed node returns ESTALE from its
// access ops so the kernel re-Lookups the real node instead of silently
// accepting writes this dead buffer would never persist.
func (n *scratchFileNode) consume() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.consumed = true
}

func (n *scratchFileNode) isConsumed() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.consumed
}

var (
	_ fs.NodeOpener    = (*scratchFileNode)(nil)
	_ fs.NodeReader    = (*scratchFileNode)(nil)
	_ fs.NodeWriter    = (*scratchFileNode)(nil)
	_ fs.NodeGetattrer = (*scratchFileNode)(nil)
	_ fs.NodeSetattrer = (*scratchFileNode)(nil)
	_ fs.NodeFlusher   = (*scratchFileNode)(nil)
	_ fs.NodeFsyncer   = (*scratchFileNode)(nil)
)

func (n *scratchFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.isConsumed() {
		// Spent node still bound to the canonical name after a rename: fail loud
		// so the VFS retries the open with revalidation and re-Lookups the real
		// node (ESTALE drives LOOKUP_REVAL).
		return nil, 0, syscall.ESTALE
	}
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *scratchFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()
	out.Mode = 0644
	n.SetOwner(out)
	out.Size = uint64(len(n.buf))
	return 0
}

func (n *scratchFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if off >= int64(len(n.buf)) {
		return fuse.ReadResultData(nil), 0
	}
	end := min(off+int64(len(dest)), int64(len(n.buf)))
	return fuse.ReadResultData(n.buf[off:end]), 0
}

func (n *scratchFileNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.consumed {
		return 0, syscall.ESTALE
	}
	if newLen := int(off) + len(data); newLen > len(n.buf) {
		grown := make([]byte, newLen)
		copy(grown, n.buf)
		n.buf = grown
	}
	copy(n.buf[off:], data)
	return uint32(len(data)), 0
}

func (n *scratchFileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.consumed {
		return syscall.ESTALE
	}
	if sz, ok := in.GetSize(); ok {
		switch {
		case int(sz) < len(n.buf):
			n.buf = n.buf[:sz]
		case int(sz) > len(n.buf):
			grown := make([]byte, sz)
			copy(grown, n.buf)
			n.buf = grown
		}
	}
	out.Mode = 0644
	out.Size = uint64(len(n.buf))
	return 0
}

func (n *scratchFileNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	if n.isConsumed() {
		return syscall.ESTALE
	}
	return 0
}

func (n *scratchFileNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	if n.isConsumed() {
		return syscall.ESTALE
	}
	return 0
}

// bytes returns a copy-free snapshot of the buffered contents. Safe to read once
// the writer has closed; used by the parent's Rename handler.
func (n *scratchFileNode) bytes() []byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.buf
}

// scratchIno derives a stable inode number for a scratch file from its parent
// directory inode and name, keeping concurrent temp files in different
// directories from colliding.
func scratchIno(parentIno uint64, name string) uint64 {
	h := fnv.New64a()
	var p [8]byte
	for i := range 8 {
		p[i] = byte(parentIno >> (8 * i))
	}
	h.Write(p[:])
	h.Write([]byte("scratch:" + name))
	return h.Sum64()
}

// newScratchInode builds the in-memory scratch inode a directory's Create
// handler returns for an editor's atomic-save temp file. parentIno is the
// directory's inode (used only to derive a stable, collision-free scratch ino).
func newScratchInode(ctx context.Context, parent *BaseNode, parentIno uint64, name string, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	node := &scratchFileNode{BaseNode: BaseNode{lfs: parent.lfs}}
	out.Attr.Mode = 0644 | syscall.S_IFREG
	out.Attr.Uid = parent.lfs.uid
	out.Attr.Gid = parent.lfs.gid
	out.Attr.Size = 0
	// Short timeouts: the scratch file is transient and should not linger in the
	// kernel cache after the rename consumes it.
	out.SetAttrTimeout(time.Second)
	out.SetEntryTimeout(time.Second)
	inode := parent.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  scratchIno(parentIno, name),
	})
	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
}

// scratchRenameBytes reports the buffered contents of the scratch file named
// oldName under parent, a closure that marks that scratch node consumed, and
// whether oldName actually refers to a scratch file this filesystem created. A
// directory's Rename handler (via renameSave) uses it to decide whether a rename
// is an editor's atomic save (ok == true, persist the bytes) or something it
// should reject (ok == false), and to consume the node once the save persists so
// the spent node — which go-fuse moves over the canonical name — fails loud
// rather than serving stale, unpersistable writes.
func scratchRenameBytes(parent fs.InodeEmbedder, oldName string) (content []byte, consume func(), ok bool) {
	child := parent.EmbeddedInode().GetChild(oldName)
	if child == nil {
		return nil, nil, false
	}
	scratch, isScratch := child.Operations().(*scratchFileNode)
	if !isScratch {
		return nil, nil, false
	}
	return scratch.bytes(), scratch.consume, true
}
