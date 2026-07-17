package fs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// listingDir is the read-only twin of collectionDir — the Readdir+Lookup head
// shared by the info-listing directories attachments/, relations/, and links/.
// Those three list read-only info files fetched from a collection (embedded
// files + external attachments, issue relations, project/initiative links),
// each with a _create/.error/.last trio and rm-to-delete, but NONE has the
// editable "{base}.md" / "{base}.meta" pair that collectionDir serves. So they
// share collectionDir's orchestration shape without its meta-sidecar and
// overwrite-in-place machinery, and before this each hand-copied the same
// skeleton three times:
//
//	Readdir: [optional refresh] trio.entries() + one DirEntry per listing entry
//	Lookup:  trio short-circuit → [optional preFilter] → find → EIO/ENOENT/build
//
// The naming round-trip stays in the per-node listing modules (attachmentListing
// / relationListing / linkListing, via the infoListing seam); the create trigger
// and its _create/.error/.last surfaces stay in collectionTrio. listingDir owns
// only the orchestration ABOVE them: the trio short-circuit, the Readdir
// assembly, the find-or-ENOENT/EIO symmetry, and the on-error Readdir policy.
// build is the one opaque per-node closure — the read-only node TYPE (and, for
// attachments, the embedded-vs-external dispatch) is what genuinely varies, and
// each node owns it.
//
// Constructed per-call by the node's dir() method (the collectionDir.collection()
// grain), then delegated to — listingDir holds no state the node doesn't already
// have.
//
// Deletion (Unlink) lives here on the DIRECTORY node, not the file node:
// go-fuse dispatches unlink to the parent directory's ops (bridge.go), never to
// the child file, and reports success when the parent has no NodeUnlinker. So
// listingDir owns the resolve→EIO/ENOENT symmetry and defers the per-entity
// mutation (and any reject guard — inverse relations, embedded files) to
// unlinkEntry. Creation stays on collectionTrio.
type listingDir[E any] struct {
	// parent is the collection directory node itself, satisfying InodeEmbedder
	// for the trio short-circuit and supplying the dir inode for kernel notify.
	parent fs.InodeEmbedder
	lfs    *LinearFS

	// trio names the writable surfaces (_create/.error/.last).
	trio collectionTrio

	// refresh kicks a background staleness refresh before a Readdir; nil for
	// listings that are not SWR sub-resources (relations, links). attachments
	// uses it for MaybeRefreshIssueDetails.
	refresh func(ctx context.Context)

	// listing fetches the current items and builds the name-derivation module.
	// A failed fetch records the first error via fetchErr so Lookup separates
	// "not found" (ENOENT) from "couldn't look" (EIO); pass nil to ignore it.
	listing func(ctx context.Context, fetchErr *error) infoListing[E]

	// nameOf projects an entry's directory name (the Readdir DirEntry name).
	nameOf func(E) string

	// failReaddirOnError decides the Readdir on-fetch-error policy: relations
	// fail the whole directory (both fetches hit the same table — a partial
	// answer would lie), links/attachments list best-effort (a family that
	// failed lists empty).
	failReaddirOnError bool

	// preFilter, when set, short-circuits Lookup to ENOENT before the fetch for
	// names it rejects — relations skips the two repo reads for any name not
	// ending ".rel". nil proceeds to the fetch for every non-trio name.
	preFilter func(name string) bool

	// build mounts the read-only file node for one resolved entry.
	build func(ctx context.Context, name string, entry E, out *fuse.EntryOut) (*fs.Inode, syscall.Errno)

	// unlinkEntry deletes a resolved entry, returning the delete's errno. It
	// owns the per-entity mutation (commitDelete) and any reject guard (EPERM
	// for entries that cannot be deleted — inverse relations, embedded files).
	// nil means the directory is not deletable (unlink is EPERM for every real
	// entry). listingDir owns the resolve→EIO/ENOENT dispatch above it.
	unlinkEntry func(ctx context.Context, name string, entry E) syscall.Errno
}

// infoListing is the naming round-trip seam listingDir needs: derive the
// directory entries and resolve one name back to its entry. attachmentListing,
// relationListing, and linkListing all satisfy it structurally (their entries
// carry both the name and the payload build dispatches on). It is the read-only
// counterpart to collectionListing[T].
type infoListing[E any] interface {
	entries() []E
	find(name string) (E, bool)
}

// readdir lists the trio surfaces and one entry per listing item. A fetch error
// either fails the directory (relations) or lists best-effort (links,
// attachments), per failReaddirOnError.
func (d listingDir[E]) readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if d.refresh != nil {
		d.refresh(ctx)
	}
	var fetchErr error
	l := d.listing(ctx, &fetchErr)
	if fetchErr != nil && d.failReaddirOnError {
		return nil, syscall.EIO
	}
	entries := d.trio.entries()
	for _, e := range l.entries() {
		entries = append(entries, fuse.DirEntry{Name: d.nameOf(e), Mode: syscall.S_IFREG})
	}
	return fs.NewListDirStream(entries), 0
}

// infoLookupKind classifies a non-trio name within the listing.
type infoLookupKind int

const (
	infoNotFound infoLookupKind = iota // preFilter reject or a clean miss → ENOENT
	infoFetchErr                       // couldn't look → EIO
	infoHit                            // resolved to an entry → build
)

// infoResult is resolve's verdict: the kind and, for a hit, the entry.
type infoResult[E any] struct {
	kind  infoLookupKind
	entry E
}

// resolve classifies a name (already known not to be a trio surface): a
// preFilter reject or a clean find miss is ENOENT, a fetch error is EIO, a find
// hit carries the entry. Pure — the branch table (preFilter, EIO-vs-ENOENT
// symmetry) under test without a mount.
func (d listingDir[E]) resolve(ctx context.Context, name string) infoResult[E] {
	if d.preFilter != nil && !d.preFilter(name) {
		return infoResult[E]{kind: infoNotFound}
	}
	var fetchErr error
	entry, ok := d.listing(ctx, &fetchErr).find(name)
	if !ok {
		if fetchErr != nil {
			return infoResult[E]{kind: infoFetchErr}
		}
		return infoResult[E]{kind: infoNotFound}
	}
	return infoResult[E]{kind: infoHit, entry: entry}
}

// lookup resolves a child: trio surface first, then the classified
// build/EIO/ENOENT dispatch. A fetch error is EIO (a name lookup has no partial
// answer); a hit is handed to build.
func (d listingDir[E]) lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := d.lfs.lookupCollectionTrio(ctx, d.parent, d.trio, name, out); ok {
		return inode, 0
	}
	res := d.resolve(ctx, name)
	switch res.kind {
	case infoHit:
		return d.build(ctx, name, res.entry, out)
	case infoFetchErr:
		return nil, syscall.EIO
	default:
		return nil, syscall.ENOENT
	}
}

// unlink deletes a listed item. The trio surfaces (_create/.error/.last) are
// virtual control files, not deletable (EPERM). A real entry routes through the
// per-node unlinkEntry, which drives the API delete, the SQLite forget, and the
// kernel-notify coherence; a fetch error is EIO and a miss is ENOENT (the same
// resolve() symmetry lookup uses). Wired as the DIRECTORY node's Unlink, since
// go-fuse dispatches unlink to the parent.
func (d listingDir[E]) unlink(ctx context.Context, name string) syscall.Errno {
	switch name {
	case "_create", ".error", ".last":
		return syscall.EPERM
	}
	res := d.resolve(ctx, name)
	switch res.kind {
	case infoHit:
		if d.unlinkEntry == nil {
			return syscall.EPERM
		}
		return d.unlinkEntry(ctx, name, res.entry)
	case infoFetchErr:
		return syscall.EIO
	default:
		return syscall.ENOENT
	}
}
