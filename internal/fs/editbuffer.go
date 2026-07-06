package fs

import (
	"context"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// editBuffer is the read/write byte buffer every editable file node embeds — the
// edit-side twin of createFileNode's buffer. It owns the FUSE buffer operations
// (Open/Read/Write/Setattr/Fsync, promoted into the node) plus the content and
// the dirty flag. The node keeps only its Getattr (entity times + size, via
// fileAttr) and its Flush (the per-entity parse → API → write-back tail).
//
// Content is EAGERLY seeded at construction, never lazily generated. That is
// forced, not a shortcut: Lookup must report an accurate size (the kernel skips
// READ entirely when size is 0), and the size is len(markdown), so every Lookup
// already materialises the content for the size — a lazy path could only
// duplicate that work, never avoid it. See CONTEXT.md "Edit buffer".
type editBuffer struct {
	mu      sync.Mutex
	content []byte
	dirty   bool
}

// size is the current buffer length, for a node's Getattr.
func (b *editBuffer) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.content)
}

func (b *editBuffer) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (b *editBuffer) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if off >= int64(len(b.content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(b.content)) {
		end = int64(len(b.content))
	}
	return fuse.ReadResultData(b.content[off:end]), 0
}

func (b *editBuffer) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	b.mu.Lock()
	defer b.mu.Unlock()

	newLen := int(off) + len(data)
	if newLen > len(b.content) {
		grown := make([]byte, newLen)
		copy(grown, b.content)
		b.content = grown
	}
	copy(b.content[off:], data)
	b.dirty = true
	return uint32(len(data)), 0
}

func (b *editBuffer) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	b.mu.Lock()
	defer b.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if int(sz) < len(b.content) {
			b.content = b.content[:sz]
		} else if int(sz) > len(b.content) {
			grown := make([]byte, sz)
			copy(grown, b.content)
			b.content = grown
		}
		b.dirty = true
	}

	out.Mode = 0644
	out.Size = uint64(len(b.content))
	return 0
}

// Fsync is a no-op — persistence happens in the node's Flush — but it must be
// implemented (not ENOTSUP) so editors that write-then-fsync (vim, VS Code,
// Claude Code's Edit) can save.
func (b *editBuffer) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}
