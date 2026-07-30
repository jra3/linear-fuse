package fs

import (
	"context"
	"sync"
	"syscall"
	"time"

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
	// authored is the serve-your-own-writes flag (#365). editFlush sets it after
	// a write persists — the buffer is now clean but still holds the exact bytes
	// the user wrote (adopt swaps only the entity, not content). While set,
	// refresh refuses to replace the buffer with Linear's normalized render, so a
	// client that verifies a write by re-reading sees its own bytes byte-for-byte
	// across the write→verify window instead of racing an async refresh. A fresh
	// Open clears it on THIS node; the window itself outlives that if the pin
	// below is still standing (see Open).
	//
	// It protects the bytes only as long as this node lives — a dentry forget
	// rebuilds the node with an empty buffer and no flag. editFlush therefore arms
	// the authoredPins pin on the same condition, and a Lookup re-seeds both from
	// it (#379, #381); that pin, not this flag, is what survives the node.
	authored bool
}

// editable exposes the embedded buffer to newFileInode, the one builder every
// editable file node passes through. Having the buffer reachable from the
// generic builder is what lets serve-your-own-writes seed in a single place
// (#387): embedding editBuffer is what makes a node editable, so a new editable
// surface inherits the guarantee by construction instead of by remembering to
// call it. Nothing else should reach a node's buffer this way.
func (b *editBuffer) editable() *editBuffer { return b }

// size is the current buffer length, for a node's Getattr.
func (b *editBuffer) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.content)
}

// refresh adopts freshly-rendered content — the editBuffer half of a node's
// nodeRefresher implementation (see refresh.go) — UNLESS the buffer is already
// serving the user's own bytes: a dirty buffer is an in-flight edit, and an
// authored buffer holds a just-persisted write (#365). Both always win over a
// background sync, so the served bytes stay the user's for the life of the
// window. entitySwap runs under the same lock iff the refresh proceeds, so
// the node's entity fields and its content swap atomically.
func (b *editBuffer) refresh(freshContent []byte, entitySwap func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dirty || b.authored {
		return
	}
	b.content = append([]byte(nil), freshContent...)
	entitySwap()
}

// truncateBuffer clears the buffer to empty and marks it dirty. It is the
// O_TRUNC path for a collectionDir overwrite-in-place Create (#289): unlike an
// O_TRUNC *open* of an existing file — where the kernel sends a separate
// setattr(size=0) that editBuffer.Setattr handles — a Create carries O_TRUNC in
// its own flags and no setattr follows, so without this the node keeps its
// pre-existing content and a shorter rewrite leaves stale tail bytes.
func (b *editBuffer) truncateBuffer() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.content = b.content[:0]
	b.dirty = true
}

// Open clears this node's serve-your-own-writes flag (#365): a fresh open means
// the write→verify cycle that authored the buffer is over, so let the next
// background refresh converge to what actually persisted. The write's own Open
// ran before Flush set the flag, so a write can never clear its own authored
// bytes — only a genuinely later open does.
//
// It does NOT end the window outright: the flag is the node-local half, and a
// Lookup that rebuilds (or re-seeds) the node while the authoredPins pin still
// stands re-arms it from the pin. Until pinTTL expires, that is the bound — a
// deliberate one, recorded in #388.
func (b *editBuffer) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	b.mu.Lock()
	b.authored = false
	b.mu.Unlock()
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (b *editBuffer) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (res fuse.ReadResult, errno syscall.Errno) {
	start := time.Now()
	defer func() { recordFuseOp(ctx, "read", start, errno) }()

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

func (b *editBuffer) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (n uint32, errno syscall.Errno) {
	start := time.Now()
	defer func() { recordFuseOp(ctx, "write", start, errno) }()

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
