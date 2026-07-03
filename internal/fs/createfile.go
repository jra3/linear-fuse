package fs

import (
	"context"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// The write-only create trigger.
//
// Every collection exposes a `_create` file (and the named-file Create paths —
// docs/"Title.md", comments, updates — reuse the same mechanics): bytes written
// are buffered, and the close-time Flush hands the complete buffer to a
// per-surface onFlush closure that parses it and runs the create tail.
// createFileNode is the one deep module owning that contract; the closure is
// its only per-surface part. It replaces nine hand-copied New*Node types.
//
// The buffer lives on the per-open FileHandle, not the node, so its lifetime
// is one open-write-close cycle (the pattern attachments/relations proved).
// That prevents double-creates without a latch: Flush consumes the buffer, so
// a dup'd descriptor's second flush sees it empty and no-ops, while a
// genuinely new open — even through the same kernel-cached inode — gets a
// fresh buffer and really creates. The old per-node buffers needed a `created`
// latch that silently swallowed the second create through a cached node, and
// issues/_create needed zero lookup timeouts to dodge the same bug.
type createFileNode struct {
	BaseNode
	// onFlush receives the complete written content once per open-write-close
	// cycle and owns parsing plus the create-tail call. Empty writes never
	// reach it; whitespace-only handling is the surface's decision.
	onFlush func(ctx context.Context, content []byte) syscall.Errno
}

// newCreateFile builds the write-only trigger node for one create surface.
func newCreateFile(lfs *LinearFS, onFlush func(ctx context.Context, content []byte) syscall.Errno) *createFileNode {
	return &createFileNode{BaseNode: BaseNode{lfs: lfs}, onFlush: onFlush}
}

// createFileHandle is the per-open write buffer. Open (and the directories'
// Create handlers) mint a fresh one per cycle.
type createFileHandle struct {
	mu      sync.Mutex
	content []byte
}

var _ fs.NodeGetattrer = (*createFileNode)(nil)
var _ fs.NodeSetattrer = (*createFileNode)(nil)
var _ fs.NodeOpener = (*createFileNode)(nil)
var _ fs.NodeReader = (*createFileNode)(nil)
var _ fs.NodeWriter = (*createFileNode)(nil)
var _ fs.NodeFlusher = (*createFileNode)(nil)
var _ fs.NodeFsyncer = (*createFileNode)(nil)

func (n *createFileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0200 // Write-only
	n.SetOwner(out)
	out.Size = 0 // reads always see an empty file, as the README documents
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *createFileNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	// Truncation (the O_TRUNC of a `>` redirect) applies to the open handle's
	// buffer when the kernel passes one; a handle-less setattr is accepted as
	// a no-op — every open starts with a fresh, empty buffer anyway.
	if handle, ok := fh.(*createFileHandle); ok {
		if sz, ok := in.GetSize(); ok {
			handle.mu.Lock()
			if int(sz) < len(handle.content) {
				handle.content = handle.content[:sz]
			} else if int(sz) > len(handle.content) {
				grown := make([]byte, sz)
				copy(grown, handle.content)
				handle.content = grown
			}
			handle.mu.Unlock()
		}
	}
	out.Mode = 0200
	n.SetOwner(out)
	out.Size = 0
	return 0
}

func (n *createFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return &createFileHandle{}, fuse.FOPEN_DIRECT_IO, 0
}

func (n *createFileNode) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// _create is write-only - return permission denied
	return nil, syscall.EACCES
}

func (n *createFileNode) Write(ctx context.Context, fh fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	handle, ok := fh.(*createFileHandle)
	if !ok {
		return 0, syscall.EIO
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()

	if newLen := int(off) + len(data); newLen > len(handle.content) {
		grown := make([]byte, newLen)
		copy(grown, handle.content)
		handle.content = grown
	}
	copy(handle.content[off:], data)
	return uint32(len(data)), 0
}

func (n *createFileNode) Flush(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	handle, ok := fh.(*createFileHandle)
	if !ok {
		return 0
	}

	// Consume the buffer atomically so concurrent flushes of dup'd descriptors
	// hand the content to onFlush exactly once, then run the (network-bound)
	// create outside the lock.
	handle.mu.Lock()
	content := handle.content
	handle.content = nil
	handle.mu.Unlock()

	if len(content) == 0 {
		return 0
	}
	return n.onFlush(ctx, content)
}

// Fsync is a no-op; actual persistence happens in Flush. It must be
// implemented (not return ENOTSUP) so editors that write-then-fsync
// (e.g. Claude Code's Edit tool, vim, VS Code) can save the _create file.
func (n *createFileNode) Fsync(ctx context.Context, fh fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}
