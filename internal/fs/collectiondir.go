package fs

import (
	"context"
	"fmt"
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
	if mdName, ok := metaSidecarSource(name); ok {
		if item, found := c.resolveItem(mdName, items); found {
			return lookupResult[T]{kind: lookupMeta, item: item}
		}
		return lookupResult[T]{kind: lookupNotFound}
	}
	if item, ok := c.resolveItem(name, items); ok {
		return lookupResult[T]{kind: lookupFile, item: item}
	}
	return lookupResult[T]{kind: lookupNotFound}
}

// resolveItem is the pure filename→item round-trip: match name against an
// already-fetched slice via the collection's listing. classify, resolve, and
// the Create-overwrite check all go through it, so the filename↔name mapping
// (sanitizing, collision policy, unicode) lives in one place.
func (c collectionDir[T]) resolveItem(name string, items []T) (T, bool) {
	return c.listing(items).find(name)
}

// resolve is the ctx-ful find the delete tail and both node Rename specs share:
// fetch the collection, then resolve one name to its item. A fetch failure is an
// error; a clean miss is (nil, nil) — the (nil, nil)-on-miss contract commitDelete
// and commitRename both expect. Routing Unlink and Rename through it (alongside
// Lookup, which reaches resolveItem via classify) keeps the resolve round-trip in
// one tested place, so a Rename can never ENOENT an entity Lookup/Unlink resolve.
func (c collectionDir[T]) resolve(ctx context.Context, name string) (*T, error) {
	items, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	if item, ok := c.resolveItem(name, items); ok {
		return &item, nil
	}
	return nil, nil
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
//
// Scratch: a name that is not an item filename at all is an editor's atomic-save
// temp file (docs/{doc}.md.tmp.<pid>.<rand>), held as an in-memory buffer for
// renameSave to persist (#438). It used to be EINVAL, which broke every editor
// and the Claude Code Edit tool — they never write in place — at the FIRST
// syscall of a save, before the rename the failure model documented.
func (c collectionDir[T]) create(ctx context.Context, name string, flags uint32, out *fuse.EntryOut, onFlush func(ctx context.Context, content []byte) syscall.Errno) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if !strings.HasSuffix(name, ".md") {
		// A .meta name is the read-only sidecar surface — never a temp file, and
		// creating one is a category error worth failing loud (its entity's .md
		// is what's writable).
		if _, isMeta := metaSidecarSource(name); isMeta {
			return nil, nil, 0, syscall.EINVAL
		}
		return newScratchInode(ctx, c.lfs, c.parent, c.dirIno(), name, out)
	}
	if items, err := c.fetch(ctx); err == nil {
		if item, ok := c.resolveItem(name, items); ok {
			inode, errno := c.buildFile(ctx, name, item, out)
			if errno != 0 {
				return nil, nil, 0, errno
			}
			// This Create IS an open of an existing item, so hand back the same
			// per-open handle Open would (#454): the write it opens for has to be
			// attributable to it, or an intervening flush's restore has nothing to
			// name and the truncate below can still be spliced through.
			//
			// Honor O_TRUNC: a Create carries it in its own flags (no separate
			// setattr follows), so without truncating here a shorter rewrite over
			// the existing content would leave stale tail bytes (#289). Both
			// resolve through editableFile, the package's one seam onto a node's
			// buffer, so the handle and the truncation cannot name different ones.
			var fh fs.FileHandle
			if editable, ok := inode.Operations().(editableFile); ok {
				buf := editable.editable()
				fh = buf.openHandle()
				if flags&syscall.O_TRUNC != 0 {
					buf.truncateBuffer()
				}
			}
			return inode, fh, 0, 0
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
	// An abandoned atomic-save temp file (the save was aborted before the
	// rename): it never existed anywhere but memory, so dropping the entry is
	// the whole delete. Editors clean these up, and an ENOENT here would make a
	// cancelled save look like a broken filesystem.
	if c.isScratch(name) {
		return 0
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
	dir := c.dirIno()

	return commitDelete(ctx, c.lfs, deleteSpec[T]{
		op:     `delete ` + c.noun + ` "` + name + `"`,
		key:    collectionErrorKey(c.trio.kind, c.trio.parentID),
		find:   func(ctx context.Context) (*T, error) { return c.resolve(ctx, name) },
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

// dirIno is the live inode the kernel knows this collection directory by — the
// EXDEV check, the scratch-ino derivation, and every coherence notify key off
// it. Read from the mounted inode rather than recomputed from the parent ID so
// it cannot disagree with what the kernel holds.
func (c collectionDir[T]) dirIno() uint64 {
	return c.parent.EmbeddedInode().StableAttr().Ino
}

// isScratch reports whether name is one of this directory's in-memory
// atomic-save temp files (as opposed to an item .md or a trio surface).
func (c collectionDir[T]) isScratch(name string) bool {
	_, _, ok := scratchRenameBytes(c.parent, name)
	return ok
}

// The collection half of atomic save-via-rename (#438).
//
// The three entity directories have supported the editor save dance since #145,
// but the dynamic collections did not: their Create rejected any name that was
// not "{item}.md" with EINVAL, so a temp file could not even be opened and every
// editor — vim, VS Code, the Claude Code Edit tool — failed at the first syscall
// of a save. Only a whole-file `cat >` overwrite worked.
//
// The tail is the same renameSave module the entity directories use; what
// differs is only where a save may land, which is exactly the piece renameSave
// delegates to a target resolver. Here any "{name}.md" is a destination: an
// existing item to REPLACE, or a new one to CREATE — the same two outcomes the
// directory's named Create already has, reached through the same closures, so
// an atomic save and an in-place write cannot diverge.

// renameSave persists an editor's atomic save into this collection. onCreate is
// the create trigger to run when newName names no item yet — the same one the
// directory's named Create binds, so `mv tmp docs/Title.md` and
// `echo … > docs/Title.md` create identically. A nil onCreate means the
// collection has no create-by-name surface; an unknown name is then refused.
//
// Callers dispatch on isScratch first: renaming an ITEM is that collection's
// own rename (retitle-the-entity, commitRename), a different operation entirely.
func (c collectionDir[T]) renameSave(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, onCreate func(ctx context.Context, content []byte) syscall.Errno) syscall.Errno {
	return renameSave(ctx, c.lfs, name, newParent, newName, renameSaveSpec{
		dirIno:  c.dirIno(),
		scratch: func(oldName string) ([]byte, func(), bool) { return scratchRenameBytes(c.parent, oldName) },
		target:  c.itemFileTarget(onCreate),
	})
}

// itemFileTarget is the renameSaveSpec.target resolver for a dynamic
// collection: resolve the destination name to the write that persists the
// scratch bytes.
func (c collectionDir[T]) itemFileTarget(onCreate func(ctx context.Context, content []byte) syscall.Errno) func(context.Context, string, string) (renameSaveTarget, syscall.Errno) {
	return func(ctx context.Context, oldName, newName string) (renameSaveTarget, syscall.Errno) {
		key := collectionErrorKey(c.trio.kind, c.trio.parentID)
		if !strings.HasSuffix(newName, ".md") {
			c.lfs.SetWriteError(key, fmt.Sprintf("Operation: rename %s -> %s\nError: %s files are named <name>.md; a save must land on a .md name. Rename onto an existing .md to replace that %s, or onto a new one to create it.", oldName, newName, c.trio.kind, c.noun))
			return renameSaveTarget{}, syscall.EINVAL
		}

		items, err := c.fetch(ctx)
		if err != nil {
			// Without the listing we cannot tell a replace from a create, and
			// guessing either way is a wrong mutation. The scratch stays intact.
			c.lfs.SetWriteError(key, fmt.Sprintf("Operation: rename %s -> %s\nError: could not read the %s listing to tell whether %s already exists; nothing was saved. Retry the save.", oldName, newName, c.trio.kind, newName))
			return renameSaveTarget{}, syscall.EIO
		}

		if item, ok := c.resolveItem(newName, items); ok {
			return renameSaveTarget{
				flush: func(ctx context.Context, content []byte) syscall.Errno {
					return c.flushOnto(ctx, newName, item, content)
				},
				// adopt: nothing to adopt. Unlike an entity directory, which caches
				// its entity on the node, a collection item is re-read from SQLite
				// by the Lookup the invalidation below forces — and the flush has
				// already upserted the fresh entity there.
				//
				// fileIno: likewise zero. The item's inode is already in its
				// editFlush coherence list, so naming it again here would only
				// duplicate a notify that has to stay correct in editFlush anyway.
			}, 0
		}

		if onCreate == nil {
			c.lfs.SetWriteError(key, fmt.Sprintf("Operation: rename %s -> %s\nError: %s names no existing %s, and this directory has no create-by-name surface; nothing was saved. Save onto an existing .md, or create through _create.", oldName, newName, newName, c.noun))
			return renameSaveTarget{}, syscall.ENOENT
		}
		// A save onto a name that does not exist yet is a create — the same one
		// `echo … > docs/Title.md` performs. commitCreate owns its own listing
		// invalidation (the created item's real filename is derived from what
		// Linear stored, not from newName).
		return renameSaveTarget{flush: onCreate}, 0
	}
}

// flushOnto writes content through an existing item's OWN edit path: build the
// read/write node Lookup would serve for it, replace its buffer with the saved
// bytes, and Flush. Going through the node rather than reimplementing the write
// is the whole point — an atomic save then gets exactly the parse, validation,
// read-your-writes verification, .error reporting, SQLite upsert, cache
// invalidation, and serve-your-own-writes pin an in-place write gets, and can
// never drift from them.
//
// The node is transient, like the one the entity directories flush through: its
// inode is never handed to the kernel (NewInode outside a Lookup registers
// nothing), and the renamed name re-Looks-up to a fresh node afterwards.
func (c collectionDir[T]) flushOnto(ctx context.Context, name string, item T, content []byte) syscall.Errno {
	var out fuse.EntryOut
	inode, errno := c.buildFile(ctx, name, item, &out)
	if errno != 0 {
		return errno
	}
	ops := inode.Operations()
	editable, isEditable := ops.(editableFile)
	flusher, isFlushable := ops.(fs.NodeFlusher)
	if !isEditable || !isFlushable {
		// Unreachable for the four collections (every item node embeds
		// editBuffer and owns a Flush), but a read-only item file would land
		// here rather than silently swallowing the save.
		return syscall.ENOTSUP
	}
	buf := editable.editable()
	buf.mu.Lock()
	buf.content = content
	buf.dirty = true
	buf.mu.Unlock()
	return flusher.Flush(ctx, nil)
}
