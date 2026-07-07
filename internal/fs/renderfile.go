package fs

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// renderFileNode is the deep module owning every read-only virtual file that
// renders text on demand — the read-only twin of editBuffer (which owns the
// editable files' read/write buffer). One node type, one render closure, one
// FUSE surface (Getattr/Open/Read) promoted into the node the way attrNode
// promotes a directory's Getattr. It serves `.meta` sidecars, `cycle.md`,
// `team.md`/`states.md`/`labels.md`, `user.md`, the root `README.md`, the
// `{type}-{ID}.rel` relation files, and the `*.link` external-attachment files.
// (Binary embedded files under attachments/ are NOT served here: they lazily
// fetch bytes over HTTP with a placeholder size and their own no-cache policy —
// a different contract, left hand-rolled.)
//
// RENDER-THROUGH, never baked. The node holds a render closure, not fixed bytes,
// and calls it inside every Read/Getattr. This is load-bearing: go-fuse dedups
// inodes by StableAttr.Ino, so a second Lookup for the same entity returns the
// *original* node and discards any freshly-constructed one — baking bytes (or
// times) at Lookup would serve stale content for the life of the mount. Rendering
// on demand from a live source means the served node always reflects current
// state. Times render through for the same reason the bytes do: a baked mtime
// would freeze at first-Lookup forever, breaking the `mtime=updatedAt` contract
// for the very file that exposes `updated:`.
//
// VOLATILITY POLICY, chosen once. Open returns FOPEN_DIRECT_IO (never
// FOPEN_KEEP_CACHE) and construction sets zero attr/entry timeouts, so the kernel
// re-reads content and re-stats attrs on every access rather than trusting a
// cached page. This makes "pick the wrong cache flag" unrepresentable — the bug
// that had cycle.md serve a frozen `status:` (upcoming/current/completed flips
// with the wall clock) under FOPEN_KEEP_CACHE cannot recur here. Renders are
// cheap synchronous string builds (or a single SQLite read), so re-rendering per
// access costs nothing worth a staleness risk.
//
// TIMES: the render closure returns (mtime, ctime); atime is collapsed to mtime.
// Only mtime (=updatedAt, drives `ls -lt`) and ctime (=createdAt, repurposed)
// are consulted by any documented operation — atime is decorative here, so
// carrying it separately would only serve one node's never-read encoding.
// Closures return entity times where the API type carries them (issue/project/
// initiative .meta, team, cycle, external attachment) and time.Now() where it
// genuinely doesn't (user, states, labels, README, relation — the timestamp-less
// exception, same class as labelfile/milestonefile).

// renderFn returns the current bytes plus the entity's mtime/ctime, all from a
// live source. It takes a ctx because some renders read the repo (states/labels);
// the rest ignore it. It is total — a render that fails returns an error-marker
// body (e.g. "# Error loading states"), never an error, so the FUSE surface never
// has to branch on an errno and size is always len(content).
type renderFn func(ctx context.Context) (content []byte, mtime, ctime time.Time)

// renderFileNode is embedded by every read-only rendered file. Plain files use it
// directly (constructed as *renderFileNode); files that also delete (relation,
// external attachment) embed it in their own type and add Unlink.
type renderFileNode struct {
	BaseNode
	render renderFn
}

var _ fs.NodeGetattrer = (*renderFileNode)(nil)
var _ fs.NodeOpener = (*renderFileNode)(nil)
var _ fs.NodeReader = (*renderFileNode)(nil)

func (n *renderFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content, mtime, ctime := n.render(ctx)
	out.Mode = 0444 // Read-only
	n.SetOwner(out)
	out.Size = uint64(len(content))
	out.SetTimes(&mtime, &mtime, &ctime)
	return 0
}

func (n *renderFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Reject writes explicitly (read-only).
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		return nil, 0, syscall.EACCES
	}
	// DIRECT_IO: content is volatile; force a real READ (through render) on each
	// open instead of trusting a cached page.
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *renderFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content, _, _ := n.render(ctx)
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}

// newRenderInode mounts a read-only rendered file as a child of parent. It fills
// the Lookup EntryOut from an initial render (best-effort size/times) and sets the
// zero attr/entry timeouts the volatility policy requires, then builds the inode
// with the given stable ino. node is the concrete embedder (a *renderFileNode for
// plain files, or an outer type embedding one for deletable files); render is that
// node's render closure. This is the single construction path — no parent
// hand-fabricates a rendered file's attributes.
func (lfs *LinearFS) newRenderInode(ctx context.Context, parent, node fs.InodeEmbedder, render renderFn, ino uint64, out *fuse.EntryOut) *fs.Inode {
	content, mtime, ctime := render(ctx)
	out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
	out.Attr.Uid = lfs.uid
	out.Attr.Gid = lfs.gid
	out.Attr.Size = uint64(len(content))
	// Feedback-file caching model: content is volatile, so the kernel must never
	// trust a cached size/attr — always re-ask (Getattr re-renders).
	out.SetAttrTimeout(0)
	out.SetEntryTimeout(0)
	out.Attr.SetTimes(&mtime, &mtime, &ctime)

	return parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  ino,
	})
}

// lookupMetaFile mounts a read-only `<entity>.meta` sidecar backed by a render
// closure. Under the "editable in, server-managed out" write contract (#150) each
// editable file (issue.md, project.md, initiative.md) carries only the fields a
// writer may set; the server-managed, write-volatile fields (id, url, updated,
// slug, branch, timestamps, links, relations, …) live in the sibling `.meta`
// instead, so a successful write to the editable file never rewrites the bytes the
// writer wrote. Editors of the sibling call InvalidateUpdated(metaIno(key)) so the
// kernel drops its cached attrs and re-reads.
func (lfs *LinearFS) lookupMetaFile(ctx context.Context, parent fs.InodeEmbedder, key string, render renderFn, out *fuse.EntryOut) *fs.Inode {
	node := &renderFileNode{BaseNode: BaseNode{lfs: lfs}, render: render}
	return lfs.newRenderInode(ctx, parent, node, render, metaIno(key), out)
}
