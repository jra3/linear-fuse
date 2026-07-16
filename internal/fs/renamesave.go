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

// renameSink is the minimal surface the atomic-save tail needs: .error
// reporting for the wrong-target guard and the kernel-cache coherence policy
// for the consumed scratch entry. *LinearFS satisfies it directly (writeFeedback
// and kernelNotify promotions), so production wiring needs no adapter while
// tests inject a fake.
type renameSink interface {
	errorSink
	InvalidateRenamed(dirIno uint64, oldName, newName string, fileIno uint64)
}

// renameSaveSpec describes the per-entity parts of an atomic save. Everything
// entity-specific lives in these fields and closures; the tail itself is
// entity-neutral.
type renameSaveSpec struct {
	// targetName is the one writable file in the directory ("issue.md",
	// "project.md", "initiative.md") — the only rename destination a scratch
	// buffer has somewhere to persist to.
	targetName string
	// errKey identifies the entity's .error file for the wrong-target message
	// (the entity ID).
	errKey string
	// dirIno is the entity directory's inode. The EXDEV same-directory check
	// and the rename invalidation both key off it.
	dirIno uint64
	// fileIno is the canonical file's inode, dropped after a persisted save so
	// the file re-Looks-up to a fresh node instead of serving the spent scratch
	// inode go-fuse moved into place.
	fileIno uint64
	// scratch reports the buffered contents of the scratch file named name, a
	// closure that marks that scratch node consumed, and whether name refers to
	// a scratch file this filesystem created. scratchRenameBytes(dir, name) in
	// production; a seam so the tail is testable without an inode tree. The tail
	// calls consume only on a persisted save (see renameSave).
	scratch func(name string) (content []byte, consume func(), ok bool)
	// flush routes the scratch bytes through the entity file's normal edit path:
	// construct a transient file node with a dirty editBuffer and Flush it
	// (frontmatter validation, read-your-writes verification, .error handling,
	// cache invalidation). The closure captures the transient node so adopt can
	// read the flushed entity back.
	flush func(ctx context.Context, content []byte) syscall.Errno
	// adopt stores the flushed node's fresh entity on the directory node so the
	// canonical file re-renders the persisted content. Called exactly when the
	// write reached Linear — flush errno 0 or EIO (see renameSave).
	adopt func()
}

// renameSave persists an editor's atomic save: when a scratch temp file is
// renamed onto spec.targetName, its buffered bytes are written through the same
// path a direct in-place edit uses. The canonical file is the only writable
// file in the directory, so renames onto any other target — or of the canonical
// files themselves — are rejected.
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

	if newName != spec.targetName {
		// A scratch file only has somewhere to persist when renamed onto the one
		// editable file in this directory.
		sink.SetWriteError(spec.errKey, fmt.Sprintf("Operation: rename %s -> %s\nError: only %s is writable in this directory; save your changes onto %s (atomic save-via-rename onto %s is supported).", name, newName, spec.targetName, spec.targetName, spec.targetName))
		return syscall.ENOTSUP
	}

	errno := spec.flush(ctx, content)

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
		spec.adopt()
		consume()
		sink.InvalidateRenamed(spec.dirIno, name, newName, spec.fileIno)
	}

	return errno
}
