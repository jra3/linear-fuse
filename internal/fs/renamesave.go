package fs

import (
	"context"
	"fmt"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// The atomic-save Rename tail.
//
// Editors and the Claude Code Edit/Write tools never write a file in place:
// they write a sibling scratch temp file (atomicwrite.go, #145) and rename(2)
// it over the canonical .md. The three entity directories that accept this
// (issues/{ID}, projects/{slug}, initiatives/{slug}) each ended their Rename
// handler with the same hand-copied tail: same-directory check → scratch-buffer
// lookup → target-name guard → flush the buffered bytes through the file's
// normal edit path → adopt the flushed entity → kernel-cache coherence. The
// subtlest line — adopt even on EIO — was restated three times and tested
// nowhere.
//
// renameSave is the module that owns that tail, the rename-shaped sibling of
// commitWriteBack (editcommit.go) in the edit-path family. Each directory keeps
// a per-entity spec (which file is writable, how to flush, how to adopt); the
// policy lives here once. It depends only on the renameSink seam plus the
// spec's closures — the scratch lookup is a closure too, so the tail needs no
// inode tree — and is unit-tested with a recording sink and stub closures: no
// FUSE mount, SQLite, or API.

// The serve-your-own-writes pin this path needs (authoredpin.go, #379) is NOT
// armed here: it rides the flush, because editFlush is the one place that knows a
// write actually committed (#381).
//
// renameSink is the minimal surface a rename tail needs: .error reporting for
// the guards and the kernel-cache coherence policy for the renamed entry.
// *LinearFS satisfies it directly (writeFeedback and kernelNotify promotions),
// so production wiring needs no adapter while tests inject a fake. Shared with
// commitRename (renamecommit.go), the title-rename tail.
type renameSink interface {
	errorSink
	InvalidateRenamed(dirIno uint64, oldName, newName string, fileIno uint64)
}

// renameSaveSpec describes the per-directory parts of an atomic save. Everything
// directory-specific lives in these fields and closures; the tail itself is
// neutral.
type renameSaveSpec struct {
	// dirIno is the directory's inode. The EXDEV same-directory check and the
	// rename invalidation both key off it.
	dirIno uint64
	// scratch reports the buffered contents of the scratch file named name, a
	// closure that marks that scratch node consumed, and whether name refers to
	// a scratch file this filesystem created. scratchRenameBytes(dir, name) in
	// production; a seam so the tail is testable without an inode tree. The tail
	// calls consume only on a persisted save (see renameSave).
	scratch func(name string) (content []byte, consume func(), ok bool)
	// target resolves the destination name to the write that persists the
	// scratch bytes, or refuses it with an errno (and an .error explaining
	// where a save CAN land — the resolver owns that message, because what is
	// writable is exactly what varies between directories). It takes both names
	// so the refusal can describe the whole rejected rename.
	//
	// The two resolvers: onlyFileTarget for the entity directories, which hold
	// one writable file each, and collectionDir.itemFileTarget for the dynamic
	// collections (docs/, comments/, labels/), where any {name}.md is a
	// destination — an existing item to overwrite, or a new one to create.
	target func(ctx context.Context, oldName, newName string) (renameSaveTarget, syscall.Errno)
}

// renameSaveTarget is a resolved save destination: where the buffered bytes go,
// and what the tail must re-cohere once they land.
type renameSaveTarget struct {
	// flush routes the scratch bytes through the destination's normal write
	// path — for an existing file, a transient file node with a dirty
	// editBuffer, Flushed (frontmatter validation, read-your-writes
	// verification, .error handling, cache invalidation, and the
	// serve-your-own-writes pin — see editFlush); for a name that names no
	// entity yet, the collection's create trigger.
	flush func(ctx context.Context, content []byte) syscall.Errno
	// adopt stores the flushed node's fresh entity on the directory node so the
	// canonical file re-renders the persisted content. Called exactly when the
	// write reached Linear — flush errno 0 or EIO (see renameSave). Nil where
	// the directory node caches no entity for the destination: a collection item
	// is re-read from SQLite, not held on the collection node.
	adopt func()
	// fileIno is the destination file's inode, dropped after a persisted save so
	// the file re-Looks-up to a fresh node instead of serving the spent scratch
	// inode go-fuse moved into place. Zero means "nothing to drop here": a save
	// that CREATES an item has no prior inode, and a collection item's inode is
	// already in its editFlush coherence list.
	fileIno uint64
}

// renameSave persists an editor's atomic save: when a scratch temp file is
// renamed onto a name spec.target accepts, its buffered bytes are written
// through the same path a direct in-place edit uses. Renames of anything that
// is not a scratch file — the canonical .md files themselves — are rejected
// here; which destinations are writable is the target resolver's call.
func renameSave(ctx context.Context, sink renameSink, name string, newParent fs.InodeEmbedder, newName string, spec renameSaveSpec) syscall.Errno {
	// The atomic-save pattern keeps the temp file a sibling of the canonical file.
	if newParent.EmbeddedInode().StableAttr().Ino != spec.dirIno {
		return syscall.EXDEV
	}

	content, consume, ok := spec.scratch(name)
	if !ok {
		// name isn't a scratch file we created — e.g. an attempt to rename the
		// canonical .md itself. The canonical files aren't renamable.
		return syscall.ENOTSUP
	}

	target, errno := spec.target(ctx, name, newName)
	if errno != 0 {
		// Nowhere to persist to. The resolver has already said why in .error;
		// the scratch stays usable (unconsumed) so a corrected rename can follow.
		return errno
	}

	errno = target.flush(ctx, content)

	if errno == 0 || errno == syscall.EIO {
		// Adopt on EIO too: Flush returns EIO only on a fatal read-your-writes
		// divergence, and by then the write has already reached Linear. Refusing
		// to adopt would keep serving stale content while .error explains the
		// divergence. Adopt the fresh entity so the canonical file re-renders
		// the stored content, and drop the kernel caches: go-fuse will MvChild
		// the spent scratch inode over the canonical file, so the file must
		// re-Lookup to a fresh node rather than serve the consumed scratch node.
		// Consume the scratch node so that, until the async invalidation lands,
		// the spent node moved over the canonical name fails loud (ESTALE)
		// instead of silently accepting writes it can no longer persist — that
		// ESTALE drives the VFS to re-Lookup the real node.
		if target.adopt != nil {
			target.adopt()
		}
		consume()
		// Serve-your-own-writes (#379) needs no work here: the flush above armed
		// the pin under the destination's inode if — and only if — it committed a clean write
		// (editFlush, #381), which is already before the invalidation that forces
		// the re-Lookup seeding from it. This tail used to arm the pin itself, off
		// a `committed` flag the flush closure reported back, because errno alone
		// cannot tell a real write from a save that resolved to no changes. Inside
		// editFlush that distinction is just the `proceed` it already has, and one
		// pin site is what lets a later in-place edit supersede this save's pin.
		sink.InvalidateRenamed(spec.dirIno, name, newName, target.fileIno)
	}

	return errno
}

// onlyFileTarget is the renameSaveSpec.target resolver for a directory holding
// exactly ONE writable file — the three entity directories (issues/{ID},
// projects/{slug}, initiatives/{slug}). Its resolve method accepts that file and
// nothing else, so a save aimed anywhere else is refused with ENOTSUP and an
// .error naming where the save CAN land.
//
// E is the entity type the directory carries (api.Issue, api.Project,
// api.Initiative). It is a type parameter for one reason: the baseline below has
// to be an entity, and making the caller name which one is what stops a call
// site from quietly skipping it.
type onlyFileTarget[E any] struct {
	sink   errorSink
	errKey string // the entity's .error key (the entity ID)
	// name is the one writable file ("issue.md", "project.md",
	// "initiative.md") — the only destination a scratch buffer has somewhere to
	// persist to.
	name    string
	fileIno uint64
	// baseline returns the entity the incoming bytes must be diffed against: the
	// freshest PERSISTED copy, read at save time, not the snapshot the directory
	// node captured whenever it was built.
	//
	// This is the whole of #415. Every editable file's Flush decides what to send
	// by diffing the written document against the entity its node carries, and
	// the transient node this save flushes through is built here. Handed the
	// directory node's snapshot, that diff answers a question nobody asked —
	// "does this differ from what the entity looked like when this directory node
	// was built" — and an in-place save (which commits through the FILE node and
	// leaves the directory's copy untouched) makes the two disagree. Restoring the
	// body an in-place save had replaced then diffed as unchanged: no mutation,
	// no .error, success returned, write lost.
	//
	// Reading through at save time is also what the sibling resolver already does
	// — collectionDir.itemFileTarget resolves its destination out of a fresh
	// listing (collectiondir.go) — and what the read side does for the same reason
	// (the <entity>.meta closures re-read rather than serve their snapshot). The
	// entity directories were the one place still diffing against a captured copy.
	//
	// The closure owns its own staleness fallback: a store that cannot serve the
	// entity yields the caller's snapshot, which is strictly no worse than the
	// behaviour this replaced.
	baseline func(ctx context.Context) E
	// flush builds the transient file node from base and writes content through
	// it. base is baseline's result, never the caller's captured snapshot.
	flush func(ctx context.Context, base E, content []byte) syscall.Errno
	adopt func()
}

func (t onlyFileTarget[E]) resolve(_ context.Context, oldName, newName string) (renameSaveTarget, syscall.Errno) {
	if newName != t.name {
		t.sink.SetWriteError(t.errKey, fmt.Sprintf("Operation: rename %s -> %s\nError: only %s is writable in this directory; save your changes onto %s (atomic save-via-rename onto %s is supported).", oldName, newName, t.name, t.name, t.name))
		return renameSaveTarget{}, syscall.ENOTSUP
	}
	return renameSaveTarget{
		// The baseline is read HERE, inside the flush, rather than at resolve
		// time: it must be the freshest copy at the moment the bytes are written,
		// and resolve runs before renameSave has decided the save will proceed.
		flush: func(ctx context.Context, content []byte) syscall.Errno {
			return t.flush(ctx, t.baseline(ctx), content)
		},
		adopt:   t.adopt,
		fileIno: t.fileIno,
	}, 0
}
