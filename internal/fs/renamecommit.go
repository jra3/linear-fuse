package fs

import (
	"context"
	"log"
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// The entity-rename tail.
//
// This is the entity-rename sibling of commitDelete (deletecommit.go), NOT the
// atomic-save tail — that is renameSave (renamesave.go). renameSave persists an
// editor's scratch temp file onto the one canonical .md; commitRename renames a
// collection item (a label, a document) by changing the entity's own name on
// Linear. They share the renameSink seam but nothing else: renameSave flushes
// buffered bytes through a file node, while commitRename resolves an entity,
// calls its rename mutation, and reflects the result.
//
// Every collection rename ended the same way: reject the special names
// (_create, the .meta sidecar), reject a cross-directory move, parse the new
// name out of the target filename (strip ".md", dashes -> spaces), resolve the
// current entity, call the API rename, gate the local reflection, and re-coher
// the kernel's view of BOTH the item's .md name and its .meta twin. That tail
// was copy-pasted byte-for-byte across LabelsNode.Rename and DocsNode.Rename.
//
// commitRename is the one deep module that owns the tail, the rename-path
// member of the reflection-contract family (commitCreate / commitWriteBack /
// commitDelete). Structured like commitDelete — find -> mutate -> gate ->
// coherence — but with three deliberate differences: no telemetry (matches
// renameSave), the gate is persistOrEIO rather than retrySQLite+forget (a
// rename reflects an updated row, it does not remove one), and success fires
// TWO InvalidateRenamed calls (the .md pair and its .meta sidecar pair). Each
// handler hands the tail a small spec; the module owns the failure model, the
// .error reporting, the persist gate, and the coherence policy. It depends only
// on the renameSink seam plus the spec's closures, so it is unit-tested with a
// recording sink and stub closures — no FUSE mount, SQLite, or API.

// renameSpec describes the per-entity parts of a collection rename. T is the
// entity type (api.Label, api.Document). Everything T-specific lives in these
// fields and closures; the tail itself is fully generic.
type renameSpec[T any] struct {
	// kind names the entity in composed op labels, e.g. "label" or "document".
	// The tail builds the op string from kind + the old and new names.
	kind string
	// errKey identifies the .error sidecar (the collection's shared namespace,
	// e.g. collectionErrorKey("labels", teamID)).
	errKey string
	// dirIno is the entity directory's inode. The cross-directory (EXDEV) check
	// and every rename invalidation key off it.
	dirIno uint64
	// find resolves the CURRENT entity by the old name. Return (nil, nil) when it
	// does not exist -> .error note + ENOENT; a non-nil error is classified like
	// a mutation failure (transient -> EAGAIN, else EIO).
	find func(ctx context.Context) (*T, error)
	// mutate calls the API rename with the parsed new name and RETURNS the
	// server's updated entity — the tail persists that returned value, so any
	// server-side name normalization is captured for free.
	mutate func(ctx context.Context, target *T, newName string) (*T, error)
	// persist upserts the updated entity to SQLite for immediate visibility. Fed
	// to persistOrEIO: a reflection the local cache can't serve fails loud (retry,
	// then EIO) rather than swallowing the divergence (#278).
	persist func(ctx context.Context, fresh *T) error
}

// commitRename runs a collection item rename: reject special names and
// cross-directory moves, parse the new entity name, find, mutate, gate the
// reflection, then re-coher the kernel. It returns the errno the handler should
// return.
//
// Contract (guard order matters):
//   - name == "_create"              -> EPERM, no .error, no invalidation.
//   - name is a .meta sidecar        -> EPERM, no .error, no invalidation.
//   - cross-directory                -> EXDEV, no .error, no invalidation.
//   - newName lacks ".md"            -> EINVAL, a helpful .error naming the rule.
//   - find fails                     -> .error gets the cause, classified errno.
//   - find returns nil               -> .error notes the unknown name, ENOENT.
//   - mutate fails                   -> .error gets the cause, classified errno;
//     NO invalidation (the rename never landed).
//   - persist wedge (retried)        -> .error says re-saving is safe, EIO; NO
//     invalidation (the reflection is unconfirmed).
//   - success                        -> clear .error, invalidate BOTH the .md
//     pair and its .meta twin (exact old->new names), errno 0.
func commitRename[T any](ctx context.Context, sink renameSink, name string, newParent fs.InodeEmbedder, newName string, spec renameSpec[T]) syscall.Errno {
	// The _create trigger and the read-only .meta sidecars are not renamable.
	if name == createTriggerName {
		return syscall.EPERM
	}
	if _, isMeta := metaSidecarSource(name); isMeta {
		return syscall.EPERM
	}

	// A collection rename stays within the collection directory.
	if newParent.EmbeddedInode().StableAttr().Ino != spec.dirIno {
		return syscall.EXDEV
	}

	// The new entity name comes from the target filename: strip ".md" and turn
	// dashes back into spaces (the inverse of the filename sanitizer). A target
	// without ".md" has no editable-item form, so reject it loudly — today's
	// handlers returned a bare EINVAL, which told an agent nothing.
	if !strings.HasSuffix(newName, ".md") {
		sink.SetWriteError(spec.errKey, "Operation: rename "+spec.kind+" "+name+" -> "+newName+
			"\nError: a "+spec.kind+" file must end in .md; rename onto a name like \"new-title.md\".")
		return syscall.EINVAL
	}
	parsedName := strings.ReplaceAll(strings.TrimSuffix(newName, ".md"), "-", " ")

	op := "rename " + spec.kind + " " + name + " -> " + newName

	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	target, err := spec.find(ctx)
	if err != nil {
		msg, errno := classifyMutationErr(op, err)
		log.Printf("Failed to %s: %v", op, err)
		sink.SetWriteError(spec.errKey, msg)
		return errno
	}
	if target == nil {
		sink.SetWriteError(spec.errKey, "Operation: "+op+"\nError: no such entry. It may have been renamed or deleted; list the directory for current names.")
		return syscall.ENOENT
	}

	fresh, err := spec.mutate(ctx, target, parsedName)
	if err != nil {
		msg, errno := classifyMutationErr(op, err)
		log.Printf("Failed to %s: %v", op, err)
		sink.SetWriteError(spec.errKey, msg)
		return errno
	}

	// Persist gates the rename: like the edit tail, a reflection the local cache
	// can't serve fails loud (retry, then EIO) rather than swallowing it. The
	// rename is already on Linear and idempotent, so the .error says re-saving is
	// safe (#278). No invalidation on a wedge — the local view is unconfirmed.
	if errno := persistOrEIO(ctx, sink, spec.errKey,
		func(err error) string { return unconfirmedEditMsg(op, err) },
		spec.persist, fresh); errno != 0 {
		return errno
	}

	sink.ClearWriteError(spec.errKey)

	// Invalidate both the item's .md pair and its .meta twin — the sidecar's name
	// follows the .md's, so both move together.
	sink.InvalidateRenamed(spec.dirIno, name, newName, 0)
	sink.InvalidateRenamed(spec.dirIno, metaSidecarName(name), metaSidecarName(newName), 0)
	return 0
}
