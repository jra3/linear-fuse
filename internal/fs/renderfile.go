package fs

import (
	"context"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// renderFunc produces a read-only generated file's current bytes plus the times
// it should report (mtime=updated, ctime=created), from a live source. A zero
// time reports "unknown" (nonZeroTime renders it as an unset attr), never a
// fabricated now() — that fabrication was the drift this module exists to kill.
//
// The ctx is the FUSE handler's: a closure whose source is a synchronous
// Linear API call (a live user is blocked on it) wraps it with
// api.WithInteractive(ctx) at the call so the request spends from the
// interactive budget reserve. SQLite-only closures ignore it.
type renderFunc func(ctx context.Context) (content []byte, mtime, ctime time.Time)

// renderFile is the mixin every read-only generated file embeds — the
// render-through file complement to attrNode (the directory mixin) and the
// read-side twin of editBuffer. It owns the three FUSE operations a generated
// file needs (Open/Read/Getattr, promoted into whatever embeds it) and holds a
// single render closure.
//
// It renders on every read (FOPEN_DIRECT_IO), so content and times can never
// freeze at first Lookup — go-fuse dedups inodes by StableAttr.Ino and reuses
// the first node for a given ino, so baking bytes or times would serve stale
// values for the life of the mount (the reasoning that already made the
// .meta/.error/.last nodes DIRECT_IO). These files are tiny and read
// interactively, so the per-read FUSE round-trip is imperceptible.
//
// A node that is purely a generated file *is* a renderFile (constructed via
// lookupRenderFile). A node that needs more — RelationFileNode and
// ExternalAttachmentNode add Unlink (rm-to-delete) — embeds renderFile and keeps
// only its extra methods. See CONTEXT.md "Render file".
type renderFile struct {
	BaseNode
	// renderMu guards render: a reused node's closure is swapped for the
	// fresh one by the nodeRefresher seam (refresh.go) while concurrent
	// reads snapshot it.
	renderMu sync.Mutex
	render   renderFunc
}

// renderNow snapshots the closure under the lock and runs it outside it (a
// render may do I/O; holding the lock across it would serialize readers).
func (r *renderFile) renderNow(ctx context.Context) ([]byte, time.Time, time.Time) {
	r.renderMu.Lock()
	render := r.render
	r.renderMu.Unlock()
	return render(ctx)
}

// refreshRender adopts a fresh twin's closure — the renderFile half of a
// nodeRefresher implementation (the embedding type swaps its own fields).
func (r *renderFile) refreshRender(fresh *renderFile) {
	r.renderMu.Lock()
	r.render = fresh.render
	r.renderMu.Unlock()
}

// refreshFrom makes a bare renderFile adopt a fresh twin's closure — a
// stable-ino render file whose closure captures entity state (a status
// update's body, history.md's issue times) would otherwise serve the
// first-Lookup capture forever (refresh.go). Types embedding renderFile with
// extra fields shadow this with their own implementation.
func (r *renderFile) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*renderFile); ok {
		r.refreshRender(f)
	}
}

var _ fs.NodeGetattrer = (*renderFile)(nil)
var _ fs.NodeOpener = (*renderFile)(nil)
var _ fs.NodeReader = (*renderFile)(nil)
var _ renderChild = (*renderFile)(nil)

// renderAttr renders the current content and returns the reporting identity a
// Getattr and a Lookup must agree on. Both go through this one path, so — as
// with attrNode — the two can never disagree.
func (r *renderFile) renderAttr(ctx context.Context) nodeAttr {
	content, mtime, ctime := r.renderNow(ctx)
	return nodeAttr{mode: 0444 | syscall.S_IFREG, size: uint64(len(content)), created: ctime, updated: mtime}
}

func (r *renderFile) baseNode() *BaseNode { return &r.BaseNode }

func (r *renderFile) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	r.renderAttr(ctx).fill(&out.Attr, &r.BaseNode)
	return 0
}

func (r *renderFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Read-only: reject any write-open explicitly (the 0444 mode already blocks
	// it at the kernel, but this matches the .meta node's belt-and-suspenders).
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		return nil, 0, syscall.EACCES
	}
	// DIRECT_IO: content is volatile; force a real READ (through render) on each
	// open instead of trusting a cached page.
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (r *renderFile) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content, _, _ := r.renderNow(ctx)
	return readWindow(content, dest, off), 0
}

// readWindow slices the [off, off+len(dest)) byte window from content — the one
// copy of the offset-clamp that every read-only file node used to hand-roll (it
// appeared verbatim a dozen times across the package).
func readWindow(content, dest []byte, off int64) fuse.ReadResult {
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil)
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end])
}

// renderChild is a node that embeds renderFile: a bare renderFile, or a type
// embedding one plus extra methods (Unlink). newRenderInode builds any of them.
type renderChild interface {
	fs.InodeEmbedder
	renderAttr(ctx context.Context) nodeAttr
	baseNode() *BaseNode
}

// inheritTimeout, passed as the timeout to newRenderInode/lookupRenderFile, means
// "leave the mount's default attr/entry timeout" — for the nodes that never set a
// per-file timeout. Any value >= 0 is applied to both attr and entry.
const inheritTimeout = time.Duration(-1)

// fillRenderEntry fills a Lookup EntryOut from the child's first render — the
// same renderAttr() path its Getattr uses, so the two can never disagree — and
// applies the timeout (< 0 inherits the mount default). Shared by both mount
// paths below.
func fillRenderEntry(ctx context.Context, out *fuse.EntryOut, child renderChild, timeout time.Duration) {
	child.renderAttr(ctx).fill(&out.Attr, child.baseNode())
	if timeout >= 0 {
		out.SetAttrTimeout(timeout)
		out.SetEntryTimeout(timeout)
	}
}

// newRenderInode fills a read-only render file's Lookup EntryOut and returns its
// inode — the render-through file counterpart to newDirInode, called on the
// parent. Used by the nodes that embed renderFile plus extra methods
// (RelationFileNode/ExternalAttachmentNode). ino 0 auto-assigns.
func (b *BaseNode) newRenderInode(ctx context.Context, out *fuse.EntryOut, name string, child renderChild, ino uint64, timeout time.Duration) *fs.Inode {
	// The bridge dedups AFTER this handler returns: push the fresh
	// closure/entity into the node it will keep (see refresh.go).
	refreshExisting(b, name, child)
	fillRenderEntry(ctx, out, child, timeout)
	return b.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG, Ino: ino})
}

// lookupRenderFile mounts a bare read-only render file (no extra methods) backed
// by a render closure as a child of the receiver's node — the one-liner the pure
// generated-file sites (team.md, states.md, user.md, README.md, …) use in place
// of a hand-rolled node type.
func (b *BaseNode) lookupRenderFile(ctx context.Context, out *fuse.EntryOut, name string, render renderFunc, ino uint64, timeout time.Duration) *fs.Inode {
	node := &renderFile{BaseNode: BaseNode{lfs: b.lfs}, render: render}
	return b.newRenderInode(ctx, out, name, node, ino, timeout)
}

// mountRenderFile mounts a bare render file under an arbitrary parent embedder —
// the variant the .meta/.error/.last helpers use, where the parent is handed in
// as an fs.InodeEmbedder rather than a *BaseNode.
func (lfs *LinearFS) mountRenderFile(ctx context.Context, parent fs.InodeEmbedder, name string, render renderFunc, ino uint64, timeout time.Duration, out *fuse.EntryOut) *fs.Inode {
	node := &renderFile{BaseNode: BaseNode{lfs: lfs}, render: render}
	// The bridge dedups AFTER this handler returns: push the fresh closure
	// into the node it will keep (see refresh.go).
	refreshExisting(parent, name, node)
	fillRenderEntry(ctx, out, node, timeout)
	return parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG, Ino: ino})
}
