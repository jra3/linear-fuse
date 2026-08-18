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
	start := time.Now()
	defer func() { recordFuseOp(ctx, "read", start, 0) }()

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

// The named kernel-caching policies. Every timeout argument at a node build
// site is one of these, `lfs.entryTimeout()` (the mount's configured bound), or
// a new named constant with a comment saying why — never an inline duration.
// TestNoHardcodedKernelTimeouts enforces that, because an inline duration is
// exactly how WithKernelCacheTimeouts came to govern nothing beneath six
// directories (#414) and three render files (#449).
//
// Two of them mean the same thing on the wire, and the names say so rather than
// pretending otherwise. go-fuse's bridge fills in the mount's configured
// defaults for any reply whose timeouts read back as zero
// (`rawBridge.setEntryOutTimeout`, fs/bridge.go — `if out.EntryTimeout() == 0`),
// and `SetEntryTimeout(0)` leaves `EntryTimeout()` at 0. So writing an explicit
// zero and writing nothing at all produce byte-identical replies, and neither
// can express "do not cache this": the smallest bound the kernel will actually
// be told about is a non-zero one. `TestMountDefaultTimeoutEqualsInherit` pins
// the equivalence.
const (
	// inheritTimeout means "do not touch the reply's timeouts" — applyNodeTimeout
	// skips the setters entirely, so the mount's defaults apply. The spelling for
	// nodes that have no per-node policy of their own.
	inheritTimeout = time.Duration(-1)

	// mountDefaultTimeout is the same policy written as an explicit zero, and is
	// what the sites that used to pass a bare `0` pass now: the write-through
	// collection directories and the `.meta`/`.error`/`.last` sidecars. It is
	// spelled as a name so the guard can forbid bare literals outright.
	//
	// It is NOT "no kernel caching" — an earlier name for it claimed that, and
	// the claim was false in both directions: these surfaces run at the mount's
	// 30s entry / 60s attr in production, and at the integration fixture's
	// configured defaults under test. What actually keeps them fresh is that
	// their content is FOPEN_DIRECT_IO (re-rendered on every read) and their
	// writers invalidate explicitly (`wf.invalidate(errorIno/successIno)`), not
	// this constant. Making them genuinely uncacheable would mean a non-zero
	// minimum here, which is a real change to production cache behavior and
	// wants its own justification.
	mountDefaultTimeout = time.Duration(0)

	// editableFileTimeout is the short bound the editable `.md` files
	// (comment/document/label/milestone) hand the kernel. It is deliberately NOT
	// the mount's entry timeout — these files are written through the mount, and
	// the bound that matters is how long a stale render can outlive the writer's
	// own save, not how long a remote change may go unnoticed.
	editableFileTimeout = 5 * time.Second

	// transientFileTimeout is the bound for the synthetic files that exist only
	// across a single write sequence: the write-only `_create` trigger and the
	// scratch inode an editor's atomic save renames over. Neither should linger
	// in the kernel cache once its sequence completes. These two are built by
	// hand rather than through a node builder, so before #449's rework they were
	// the one class of site the guard could not see.
	transientFileTimeout = 1 * time.Second
)

// applyNodeTimeout writes one caching policy onto a Lookup reply — the single
// place in the package that touches SetAttrTimeout/SetEntryTimeout, which is
// what lets TestNoHardcodedKernelTimeouts check every site by checking the
// arguments to this function and the builders that call it.
//
// A negative bound means inheritTimeout: leave the reply alone. The guard is not
// cosmetic. fuse.EntryOut.SetAttrTimeout does `AttrValid = uint64(ns / 1e9)`
// with no sign check, so a negative reaching the setters becomes a ~584-billion-
// year TTL rather than a short one. newDirInode and fillRenderEntry had this
// check; newFileInode did not, so a negative bound arriving at a file build site
// pinned an effectively immortal kernel cache entry.
func applyNodeTimeout(out *fuse.EntryOut, timeout time.Duration) {
	if timeout < 0 {
		return
	}
	out.SetAttrTimeout(timeout)
	out.SetEntryTimeout(timeout)
}

// fillRenderEntry fills a Lookup EntryOut from the child's first render — the
// same renderAttr() path its Getattr uses, so the two can never disagree — and
// applies the timeout (< 0 inherits the mount default). Shared by both mount
// paths below.
func fillRenderEntry(ctx context.Context, out *fuse.EntryOut, child renderChild, timeout time.Duration) {
	child.renderAttr(ctx).fill(&out.Attr, child.baseNode())
	applyNodeTimeout(out, timeout)
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
