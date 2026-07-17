package fs

import (
	"context"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// collectionDir is the item-file surface of a dynamic collection directory —
// the read+delete lifecycle shared by comments/, docs/, labels/, milestones/.
// It is the dynamic-collection sibling of dirManifest (which serves static
// entity-directory children): given a per-collection spec, it owns Readdir,
// Lookup (including the "{base}.meta" shadow sidecar) and Unlink, so those four
// nodes stop hand-copying the same branchy head.
//
// The naming round-trip stays in the listing modules (indexedListing /
// namedListing, via the collectionListing seam); the create trigger and its
// _create/.error/.last surfaces stay in collectionTrio. collectionDir owns only
// the orchestration ABOVE them: the trio short-circuit, the .meta shadow, the
// find-or-ENOENT symmetry, the on-error degradation, and the delete guards +
// kernel-notify coherence. buildFile is the one opaque per-node closure — the
// writable node TYPE is the thing that genuinely varies, and each node owns it.
//
// Constructed per-call by the node's collection() method (the manifest()/
// listing() grain), then delegated to — collectionDir holds no state the node
// doesn't already have.
//
// Excluded by design: Create and Rename stay on the nodes (Create is 3/4 with an
// overwrite branch; Rename is 2/4 and divergent). relations/ and attachments/
// have their own listing modules and no .meta sidecar.
type collectionDir[T any] struct {
	// parent is the collection directory node itself, satisfying InodeEmbedder
	// for NewInode/mountRenderFile and supplying the dir inode for kernel notify.
	parent fs.InodeEmbedder
	lfs    *LinearFS

	// trio names the writable surfaces (_create/.error/.last). Its kind +
	// parentID also derive the delete's error key — no separate field.
	trio collectionTrio
	// noun is the singular entity word for .error op messages and debug logs
	// (kind is plural: "comments" vs "comment").
	noun string

	// refresh kicks a background staleness refresh before a Readdir; nil for
	// collections that are not SWR sub-resources (labels, milestones).
	refresh func(ctx context.Context)
	// fetch returns the collection's current items (the SQLite-backed read).
	fetch func(ctx context.Context) ([]T, error)
	// listing names the item files; indexedListing/namedListing satisfy the seam.
	listing func([]T) collectionListing[T]
	// idOf extracts an item's stable ID (for the .meta freshest-by-id re-read).
	idOf func(T) string

	// buildFile mounts the read/write file node for an existing item. The one
	// opaque per-node closure — the writable node type is what varies.
	buildFile func(ctx context.Context, name string, item T, out *fuse.EntryOut) (*fs.Inode, syscall.Errno)

	// metaMarshal/metaTimes render the read-only "{base}.meta" sidecar; metaIno
	// is its stable inode. metaTimes returns zero for entities without
	// timestamps (an honest "unknown", never a fabricated now()).
	metaMarshal func(*T) ([]byte, error)
	metaTimes   func(T) (mtime, ctime time.Time)
	metaIno     func(T) uint64

	// deleteMutate archives/deletes via the API; deleteForget removes the row
	// from SQLite (the listing source of truth). See deleteSpec.
	deleteMutate func(ctx context.Context, target *T) error
	deleteForget func(ctx context.Context, target *T) error
}

// collectionListing is the naming round-trip seam collectionDir needs: derive
// the directory entries and resolve one name back to its item. indexedListing
// (creation-ordered %04d-date names) and namedListing (title-derived names)
// both satisfy it structurally.
type collectionListing[T any] interface {
	entries() []fuse.DirEntry
	find(name string) (T, bool)
}

// readdir lists the trio surfaces, the item .md files, and their .meta
// sidecars. A fetch error degrades to the trio alone (the writable surfaces
// stay usable) rather than failing the listing.
func (c collectionDir[T]) readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if c.refresh != nil {
		c.refresh(ctx)
	}
	items, err := c.fetch(ctx)
	if err != nil {
		return fs.NewListDirStream(c.trio.entries()), 0
	}
	return fs.NewListDirStream(c.entries(items)), 0
}

// entries assembles the full directory listing: trio, then item .md files, then
// their .meta sidecars. Pure — the Readdir assembly under test without a mount.
func (c collectionDir[T]) entries(items []T) []fuse.DirEntry {
	files := c.listing(items).entries()
	out := append(c.trio.entries(), files...)
	out = append(out, metaSidecarEntries(files)...)
	return out
}

// lookupKind classifies a non-trio name within the collection.
type lookupKind int

const (
	lookupNotFound lookupKind = iota
	lookupMeta                // "{base}.meta" — the read-only sidecar
	lookupFile                // "{base}.md" — the read/write item file
)

// lookupResult is classify's verdict: the kind and, for a hit, the item.
type lookupResult[T any] struct {
	kind lookupKind
	item T
}

// classify resolves a name (already known not to be a trio surface) to an
// action: a .meta sidecar, an item .md, or ENOENT. Pure — the branchy part
// (meta shadowing, find-or-miss) under test without a mount.
func (c collectionDir[T]) classify(name string, items []T) lookupResult[T] {
	l := c.listing(items)
	if mdName, ok := metaSidecarSource(name); ok {
		if item, found := l.find(mdName); found {
			return lookupResult[T]{kind: lookupMeta, item: item}
		}
		return lookupResult[T]{kind: lookupNotFound}
	}
	if item, ok := l.find(name); ok {
		return lookupResult[T]{kind: lookupFile, item: item}
	}
	return lookupResult[T]{kind: lookupNotFound}
}

// lookup resolves a child: trio surface first, then the classified item/meta/
// ENOENT dispatch. A fetch error is EIO (a name lookup has no partial answer).
func (c collectionDir[T]) lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := c.lfs.lookupCollectionTrio(ctx, c.parent, c.trio, name, out); ok {
		return inode, 0
	}

	items, err := c.fetch(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	res := c.classify(name, items)
	switch res.kind {
	case lookupMeta:
		return c.lfs.mountRenderFile(ctx, c.parent, name, c.metaRender(res.item), c.metaIno(res.item), 0, out), 0
	case lookupFile:
		return c.buildFile(ctx, name, res.item, out)
	default:
		return nil, syscall.ENOENT
	}
}

// metaRender builds the .meta sidecar's render closure: re-derive the freshest
// item on every read (renderFile is DIRECT_IO, so baked bytes would go stale
// for the life of the mount), marshal it, and report its real times.
func (c collectionDir[T]) metaRender(item T) renderFunc {
	id := c.idOf(item)
	return func(ctx context.Context) ([]byte, time.Time, time.Time) {
		cur := item
		if items, err := c.fetch(ctx); err == nil {
			cur = freshestByID(items, id, c.idOf, item)
		}
		mtime, ctime := c.metaTimes(cur)
		b, err := c.metaMarshal(&cur)
		if err != nil {
			return nil, mtime, ctime
		}
		return b, mtime, ctime
	}
}

// create binds a new item file. onFlush is the create trigger for this name
// (the trio's onFlush, or a name-bound variant where the filename seeds the
// title, as docs does). Returns the FUSE Create quad.
//
// Overwrite in place: a name that already resolves to an item — a save-over of
// an existing .md (mv/cp/editor) — returns that item's read/write node so the
// write updates it through the normal truncate+flush path, rather than binding
// a write-only _create node to the name and corrupting it (#137). Harmless for
// an indexed collection (comments): Create only fires for a name Lookup missed,
// and a user-chosen name never matches an index-derived filename, so find()
// misses and falls through to the create node.
func (c collectionDir[T]) create(ctx context.Context, name string, flags uint32, out *fuse.EntryOut, onFlush func(ctx context.Context, content []byte) syscall.Errno) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}
	if items, err := c.fetch(ctx); err == nil {
		if item, ok := c.listing(items).find(name); ok {
			inode, errno := c.buildFile(ctx, name, item, out)
			if errno != 0 {
				return nil, nil, 0, errno
			}
			// Honor O_TRUNC: a Create carries it in its own flags (no separate
			// setattr follows), so without truncating here a shorter rewrite over
			// the existing content would leave stale tail bytes (#289).
			if flags&syscall.O_TRUNC != 0 {
				if tr, ok := inode.Operations().(interface{ truncateBuffer() }); ok {
					tr.truncateBuffer()
				}
			}
			return inode, nil, 0, 0
		}
	}
	node := newCreateFile(c.lfs, onFlush)
	inode := c.parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})
	return inode, &createFileHandle{}, fuse.FOPEN_DIRECT_IO, 0
}

// unlink deletes an item file. _create and .meta sidecars are read-only
// (EPERM); a real item routes through the shared delete tail, which drives the
// API delete, the SQLite forget, and the kernel-notify coherence (including the
// item's .meta sidecar entry).
func (c collectionDir[T]) unlink(ctx context.Context, name string) syscall.Errno {
	if c.lfs.debug {
		log.Printf("Unlink %s: %s", c.noun, name)
	}
	if name == "_create" {
		return syscall.EPERM
	}
	// The .meta sidecar is a read-only virtual file; it vanishes with its
	// entity (rm the .md), never on its own.
	if _, isMeta := metaSidecarSource(name); isMeta {
		return syscall.EPERM
	}

	// The node is mounted at its collection's dir inode (xDirIno(parentID)); use
	// the live inode the kernel actually knows for the coherence notify.
	dir := c.parent.EmbeddedInode().StableAttr().Ino

	return commitDelete(ctx, c.lfs, deleteSpec[T]{
		op:  `delete ` + c.noun + ` "` + name + `"`,
		key: collectionErrorKey(c.trio.kind, c.trio.parentID),
		find: func(ctx context.Context) (*T, error) {
			items, err := c.fetch(ctx)
			if err != nil {
				return nil, err
			}
			if item, ok := c.listing(items).find(name); ok {
				return &item, nil
			}
			return nil, nil
		},
		mutate: c.deleteMutate,
		forget: c.deleteForget,
		dir:    dir,
		name:   name,
		// The .meta sidecar renders from the deleted entity: drop its entry too.
		invalidateExtra: func(*T) {
			c.lfs.InvalidateDeleted(dir, metaSidecarName(name))
		},
	})
}
