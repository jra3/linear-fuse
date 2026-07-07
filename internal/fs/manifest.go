package fs

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// The entity-directory manifest.
//
// An entity directory (an issue, a project, an initiative) serves a fixed set of
// static children — the .md/.meta/.error/.last files and the comments/docs/…
// subdirs — alongside any dynamic tail (a project's issue symlinks). Before this,
// each directory declared those static children twice: once as a hardcoded
// Readdir []DirEntry, once as a Lookup switch. Two hand-kept lists that could
// drift into a file you can `ls` but not `open`.
//
// dirManifest owns them once. entries() (Readdir) and find(name) (Lookup) are
// both pure projections of one children slice, so they cannot disagree — the
// listed⇔openable guarantee namedListing/indexedListing already give a
// collection's *dynamic* children, lifted one tier up to the static skeleton.
// It is the static twin of those modules. See CONTEXT.md "Entity-directory
// manifest".
//
// A directory declares its manifest with a manifest() method (mirroring trio())
// and delegates:
//
//	func (n *IssueDirectoryNode) Readdir(ctx) (fs.DirStream, syscall.Errno) {
//		return fs.NewListDirStream(n.manifest().entries()), 0
//	}
//	func (n *IssueDirectoryNode) Lookup(ctx, name, out) (*fs.Inode, syscall.Errno) {
//		if child, ok := n.manifest().find(name); ok {
//			return child.build(ctx, out)   // terminal — a matched-but-failed build
//		}                                  // (EIO) never falls through to a tail
//		return nil, syscall.ENOENT
//	}
//
// The dynamic tail (only a project has one) stays outside: Readdir appends its
// symlink dirents after entries(), Lookup runs its symlink loop only on a find
// miss.

// staticChild is one fixed entry in an entity directory: its name, its dirent
// mode (S_IFDIR/S_IFREG, for Readdir), and how to build its inode on Lookup.
type staticChild struct {
	name  string
	mode  uint32
	build func(ctx context.Context, out *fuse.EntryOut) (*fs.Inode, syscall.Errno)
}

// dirManifest is the self-describing builder for an entity directory's static
// children. It carries the facts every child shares once — the parent node
// (source of newDirInode/newFileInode and lfs), the entity id that scopes the
// .error/.last/.meta keys, the entity created/updated times, and the child
// timeout — so each child declares only its difference. This is the directory-
// scale application of attrNode's move: the directory carries the identity it
// reports rather than re-deriving it per child.
type dirManifest struct {
	parent   *BaseNode
	id       string
	created  time.Time
	updated  time.Time
	timeout  time.Duration
	children []staticChild
}

// newDirManifest starts a manifest for one entity directory. created/updated are
// the entity's own times (every static file and subdir reports them); timeout is
// the entry/attr timeout the directory hands its children (uniform within a
// directory).
func newDirManifest(parent *BaseNode, id string, created, updated time.Time, timeout time.Duration) *dirManifest {
	return &dirManifest{parent: parent, id: id, created: created, updated: updated, timeout: timeout}
}

// subdir adds a child directory whose node is built lazily by node(). The child
// reports the entity's times (dirAttr) and the directory-wide timeout.
func (m *dirManifest) subdir(name string, ino uint64, node func() dirChild) {
	m.children = append(m.children, staticChild{
		name: name, mode: syscall.S_IFDIR,
		build: func(ctx context.Context, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
			return m.parent.newDirInode(ctx, out, node(), dirAttr(m.created, m.updated), ino, m.timeout), 0
		},
	})
}

// file adds an editable (0644) regular file. build renders the content (its
// length sets the reported size) and returns the node, or an errno that is
// surfaced verbatim — a marshal failure returns EIO. Its errno is terminal at
// the Lookup call site.
func (m *dirManifest) file(name string, ino uint64, build func(ctx context.Context) (fs.InodeEmbedder, []byte, syscall.Errno)) {
	m.children = append(m.children, staticChild{
		name: name, mode: syscall.S_IFREG,
		build: func(ctx context.Context, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
			node, content, errno := build(ctx)
			if errno != 0 {
				return nil, errno
			}
			na := fileAttr(len(content), m.created, m.updated)
			return m.parent.newFileInode(ctx, out, node, na, ino, m.timeout), 0
		},
	})
}

// renderFile adds a read-only (0444) generated file backed by a render closure
// (history.md) — rendered fresh on every read (DIRECT_IO), the read-side twin of
// the editable file() which bakes its content at Lookup. See the renderFile
// module.
func (m *dirManifest) renderFile(name string, ino uint64, render renderFunc) {
	m.children = append(m.children, staticChild{
		name: name, mode: syscall.S_IFREG,
		build: func(ctx context.Context, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
			return m.parent.lookupRenderFile(ctx, out, render, ino, m.timeout), 0
		},
	})
}

// metaFile adds the read-through <entity>.meta sidecar; render re-derives the
// content and its times on every read (see renderFile/lookupMetaFile).
func (m *dirManifest) metaFile(name string, render renderFunc) {
	m.children = append(m.children, staticChild{
		name: name, mode: syscall.S_IFREG,
		build: func(ctx context.Context, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
			return m.parent.lfs.lookupMetaFile(ctx, m.parent, m.id, render, out), 0
		},
	})
}

// errorFile adds the .error feedback file (last failed write to this entity).
func (m *dirManifest) errorFile(name string) {
	m.children = append(m.children, staticChild{
		name: name, mode: syscall.S_IFREG,
		build: func(ctx context.Context, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
			return m.parent.lfs.lookupErrorFile(ctx, m.parent, m.id, out), 0
		},
	})
}

// lastFile adds the .last sidecar (recent successful creates under this entity).
func (m *dirManifest) lastFile(name string) {
	m.children = append(m.children, staticChild{
		name: name, mode: syscall.S_IFREG,
		build: func(ctx context.Context, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
			return m.parent.lfs.lookupSuccessFile(ctx, m.parent, m.id, out), 0
		},
	})
}

// entries is the Readdir projection: the name+mode of every static child, in
// declaration order.
func (m *dirManifest) entries() []fuse.DirEntry {
	out := make([]fuse.DirEntry, len(m.children))
	for i, c := range m.children {
		out[i] = fuse.DirEntry{Name: c.name, Mode: c.mode}
	}
	return out
}

// find is the Lookup projection: the static child of the given name, if any. It
// is pure — the build closures are captured but not invoked — so the round-trip
// (every entries() name resolves here) is unit-testable with no mount.
func (m *dirManifest) find(name string) (staticChild, bool) {
	for _, c := range m.children {
		if c.name == name {
			return c, true
		}
	}
	return staticChild{}, false
}
