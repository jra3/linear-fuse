package fs

import (
	"context"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// nodeAttr is the reporting identity of a directory or file node: the mode,
// size, and times a Lookup answer and a later Getattr must agree on. Fixing it
// at construction is what makes the two render identically — the non-symlink
// complement to symlinkNode. See CONTEXT.md "Attr construction".
//
// The time convention matches the rest of the filesystem: atime/mtime report
// the entity's updatedAt, ctime reports its createdAt.
type nodeAttr struct {
	mode    uint32 // full mode incl. the S_IFDIR/S_IFREG type bits
	size    uint64
	created time.Time
	updated time.Time
}

// fill renders the nodeAttr into a bare fuse.Attr. Both the directory mixin's
// Getattr and the newDirInode/newFileInode Lookup constructors call this, so a
// Lookup answer and a subsequent stat cannot disagree. A zero time stays a zero
// attr (nonZeroTime), never a wrapped year-584-billion timestamp.
func (na nodeAttr) fill(attr *fuse.Attr, b *BaseNode) {
	attr.Mode = na.mode
	b.setOwnerAttr(attr)
	attr.Size = na.size
	attr.SetTimes(nonZeroTime(na.updated), nonZeroTime(na.updated), nonZeroTime(na.created))
}

// dirAttr is the nodeAttr for a standard 0755 directory reporting an entity's
// times — the shape almost every entity subdirectory (comments/, docs/, …)
// uses.
func dirAttr(created, updated time.Time) nodeAttr {
	return nodeAttr{mode: 0755 | syscall.S_IFDIR, created: created, updated: updated}
}

// fileAttr is the nodeAttr for a standard 0644 read/write file reporting an
// entity's times and its current content size.
func fileAttr(size int, created, updated time.Time) nodeAttr {
	return nodeAttr{mode: 0644 | syscall.S_IFREG, size: uint64(size), created: created, updated: updated}
}

// attrNode is the mixin every static-attr directory node embeds instead of
// BaseNode. It stores the nodeAttr and provides the default Getattr, so a
// directory node cannot hand-write a divergent one (the drift that had
// DocsNode/AttachmentsNode reporting time.Now()).
//
// The nodeAttr is mutex-guarded because it is no longer write-once: when
// go-fuse dedups a Lookup onto an already-known node, newDirInode re-runs
// setAttr on the OLD node with the freshly-fetched times (the attr half of
// the nodeRefresher seam — see refresh.go), racing with concurrent Getattrs.
// stateMu doubles as the volatile-state lock for the embedding entity dir
// nodes (their entity()/setEntity accessors), so an entity swap and its
// consumers serialize on one lock.
type attrNode struct {
	BaseNode
	stateMu sync.Mutex
	na      nodeAttr
}

var _ fs.NodeGetattrer = (*attrNode)(nil)

func (n *attrNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.fillAttr(&out.Attr)
	return 0
}

// fillAttr is the one renderer shared by Getattr and newDirInode.
func (n *attrNode) fillAttr(attr *fuse.Attr) {
	n.stateMu.Lock()
	na := n.na
	n.stateMu.Unlock()
	na.fill(attr, &n.BaseNode)
}

// setAttr stashes the reporting identity; called by newDirInode on the child
// (and re-run on a reused node to refresh its times).
func (n *attrNode) setAttr(na nodeAttr) {
	n.stateMu.Lock()
	n.na = na
	n.stateMu.Unlock()
}

// dirChild is a node that embeds attrNode.
type dirChild interface {
	fs.InodeEmbedder
	setAttr(nodeAttr)
	fillAttr(*fuse.Attr)
}

// newDirInode builds a static-attr directory child from a parent's Lookup. It
// fixes the child's reporting identity, fills the Lookup EntryOut by calling the
// child's own fillAttr — the exact method its Getattr uses — sets the entry
// timeout, and returns the inode. A Lookup answer and a later stat therefore
// render identically by construction.
func (b *BaseNode) newDirInode(ctx context.Context, out *fuse.EntryOut, name string, child dirChild, na nodeAttr, ino uint64, timeout time.Duration) *fs.Inode {
	child.setAttr(na)
	child.fillAttr(&out.Attr)
	out.SetAttrTimeout(timeout)
	out.SetEntryTimeout(timeout)
	// The bridge dedups AFTER this handler returns: if it still knows a node
	// under this name, that one will serve — push the freshly-fetched times
	// and entity into it (see refresh.go).
	if existing := b.EmbeddedInode().GetChild(name); existing != nil {
		if dc, ok := existing.Operations().(dirChild); ok && existing.Operations() != fs.InodeEmbedder(child) {
			dc.setAttr(na)
		}
	}
	refreshExisting(b, name, child)
	return b.NewInode(ctx, child, fs.StableAttr{Mode: na.mode & syscall.S_IFMT, Ino: ino})
}

// newFileInode fills a file's Lookup EntryOut from a nodeAttr and returns the
// inode. Unlike a directory a file keeps its own Getattr — its size is a
// legitimately dynamic edit-buffer value that is meant to diverge from what
// Lookup first reported — so this installs no inherited Getattr; it only shares
// the immutable-field construction (mode/uid/gid/times) and the initial size.
func (b *BaseNode) newFileInode(ctx context.Context, out *fuse.EntryOut, name string, child fs.InodeEmbedder, na nodeAttr, ino uint64, timeout time.Duration) *fs.Inode {
	na.fill(&out.Attr, b)
	out.SetAttrTimeout(timeout)
	out.SetEntryTimeout(timeout)
	// The bridge dedups AFTER this handler returns: push fresh content/entity
	// into the node it will keep (a dirty edit buffer wins — see refresh.go).
	refreshExisting(b, name, child)
	return b.NewInode(ctx, child, fs.StableAttr{Mode: na.mode & syscall.S_IFMT, Ino: ino})
}
