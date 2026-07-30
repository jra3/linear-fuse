package fs

import (
	"bytes"
	"context"
	"fmt"
	"syscall"
	"time"
)

// emptyWriteMessage is the .error an emptied editable file gets. It follows the
// Field/Value/Error shape the rest of the write feedback uses, and it says what
// the writer must do next — the file's current contents are still on the server,
// so a re-read recovers them.
func emptyWriteMessage(op string) string {
	return fmt.Sprintf("Empty write rejected\nOperation: %s\n"+
		"Error: the file was written with no content. An empty file carries no fields, so applying it "+
		"would clear every removable field at once rather than change the one you meant. Nothing was written.\n"+
		"Fix: re-read the file to get its current contents, change the field you mean to change, and write "+
		"the whole document back. To clear a single field, omit that field's key (or empty the body) and keep the rest.", op)
}

// The edit-flush shell.
//
// Every editable file node (issue.md, project.md, initiative.md, and a
// comment/doc/label/milestone .md) drives its FUSE Flush through the same
// invariant shell around its per-entity front half: take the buffer lock, skip
// a clean/empty buffer, bound the API work with a timeout, run the front half
// (parse → resolve → mutate), and on success run the commit tail
// ([[edit-commit]], commitWriteBack), adopt the fresh value, invalidate the
// node's kernel-cache set, and clear the dirty flag. That shell was copy-pasted
// across all seven handlers, and it had drifted: issues invalidated *before*
// persisting the fresh value (a stale-repopulation window), and the
// invalidation set — which inodes an edit dirties — lived as loose
// InvalidateUpdated calls each handler had to remember.
//
// editFlush is the one deep module that owns the shell. Each handler keeps its
// per-entity front half as the mutate closure and its commit tail as a
// writeBackSpec, then declares its invalidation set as data (coherence []uint64)
// and hands editFlush a small spec. The module depends only on the
// editFlushSink seam plus the spec's closures, so it is unit-tested with a
// recording fake — no FUSE mount, SQLite, or API.
//
// Invalidate-after-persist is uniform here by construction: the shell
// invalidates only after commitWriteBack has upserted the fresh value, so a
// racing read can never repopulate the kernel cache from a not-yet-written row.
//
// The shell is also the single place serve-your-own-writes arms (#365, #379,
// #381). A committed clean write both marks the buffer authored — the node-local
// half — and pins the written bytes under the file's inode, the half that
// survives the node. Both halves therefore key off one condition in one place, so
// the in-place and atomic-save paths cannot disagree about whether a write is
// servable, and the later of two writes to a file always wins the pin.

// editFlushSink is the minimal surface the shell needs: the errorSink the commit
// tail already requires, the kernel-cache invalidation the shell owns, and the
// serve-your-own-writes pin it arms. *LinearFS satisfies it directly
// (SetWriteError/ClearWriteError via writeFeedback, InvalidateUpdated via
// kernelNotify, PinWritten via authoredPins), so production wiring needs no
// adapter while tests inject a recording fake.
type editFlushSink interface {
	errorSink
	InvalidateUpdated(fileIno uint64)
	PinWritten(fileIno uint64, content []byte)
}

// editFlushSpec describes the per-entity parts of an edit's flush. T is the
// entity type (api.Issue, api.Label, …). The front half and the commit tail
// stay T-specific; the shell is fully generic.
type editFlushSpec[T any] struct {
	// mutate runs the per-entity front half — parse, resolve, and call the API —
	// and reports one of three outcomes:
	//   - errno != 0            → the front half failed (parse/resolve/mutation);
	//     the shell returns errno and LEAVES the buffer dirty so a corrected
	//     re-save retries. mutate owns its own .error message.
	//   - errno == 0, !proceed  → nothing changed; the shell clears dirty and
	//     returns 0 without committing.
	//   - errno == 0, proceed   → the API accepted a write; the shell runs the
	//     commit tail, adopts, invalidates, and clears dirty.
	mutate func(ctx context.Context) (proceed bool, errno syscall.Errno)
	// writeBack is the commit tail spec (see commitWriteBack).
	writeBack writeBackSpec[T]
	// adopt installs the fresh value onto the node (n.entity = *fresh). Runs
	// after commitWriteBack's compare has read the pre-write originals.
	adopt func(fresh *T)
	// coherence lists the kernel inodes this edit dirties (the entity file plus
	// its .meta sidecar, and any dependent listing). Declared as data so a
	// forgotten sidecar is a visible one-line omission, not a missing call
	// buried in a handler. Invalidated only after the commit tail persists.
	coherence []uint64
	// pinIno is the inode of the canonical file whose Lookup seeds from
	// authoredPins. A committed clean write pins its bytes there, which is what
	// makes serve-your-own-writes survive the node — a dentry forget and
	// re-Lookup inside the window, and the transient node the atomic-save path
	// flushes through.
	//
	// Set by all seven editable files since #387. It was originally the three
	// that also accept an atomic save (issue.md, project.md, initiative.md),
	// because only those consulted pins when building a node; a
	// comment/doc/label/milestone .md left it zero, so its written bytes survived
	// only as long as the node did. Seeding now happens in newFileInode, the one
	// builder they all pass through, so the reader exists for every editable file
	// and the pin has somewhere to land. Zero remains the correct value for any
	// future spec whose file is not built through that path — an unread pin is
	// just bytes held for the TTL and swept.
	pinIno uint64
}

// editFlush runs the invariant shell of a file node's Flush. eb is the node's
// embedded editBuffer (the shell owns the lock, the clean/empty guard, and the
// dirty flag); sink carries the error + invalidation surfaces; spec supplies the
// per-entity front half, commit tail, adopt, and invalidation set.
//
// Returns the errno the Flush should surface: the front half's errno on
// failure, 0 on a no-op, or the commit tail's errno on a completed write. The
// buffer lock is held across the whole shell, exactly as the hand-written
// handlers held n.mu.
func editFlush[T any](ctx context.Context, sink editFlushSink, eb *editBuffer, spec editFlushSpec[T]) syscall.Errno {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if !eb.dirty {
		return 0
	}

	// An emptied file is a truncation accident, not an edit (#397). A crashed
	// editor, a `> file`, or a botched Write tool call leaves zero bytes, and
	// nothing downstream reads that as "no change": the parse yields a document
	// with no fields at all, and a diff against an entity that HAS fields
	// therefore reads as "the writer removed every one of them". On issue.md that
	// is measured, not hypothetical — a zero-byte write emits
	// assigneeId=nil, dueDate=nil, estimate=nil, labelIds=[], description=""
	// in one mutation, wiping five fields the writer never named. (The title
	// survives; it is not a removable field. The live run that filed #397 read a
	// cleared title because the emptied buffer was being served back, not because
	// Linear had lost it.)
	//
	// So the shell refuses it here, before any handler's front half: EINVAL plus a
	// legible .error, buffer left dirty for a corrected re-save, exactly like a
	// parse failure. Clearing ONE field stays expressible — drop its key, or empty
	// the body while keeping the frontmatter. What is no longer expressible is
	// "clear everything by accident".
	//
	// This subsumes the old `eb.content == nil` half of the guard above, and that
	// is the point: a nil buffer is not a third state, it is how the ATOMIC-SAVE
	// path spells an emptied file (a zero-byte scratch file renamed over the
	// target hands the flush nil bytes). Treating nil as "nothing to do" while an
	// O_TRUNC of the same file was a real edit gave the two save paths opposite
	// answers to `> issue.md` — silent success on one, a mutation on the other.
	if len(bytes.TrimSpace(eb.content)) == 0 {
		sink.SetWriteError(spec.writeBack.errKey, emptyWriteMessage(spec.writeBack.op))
		return syscall.EINVAL
	}

	// Bound the API work — the front half and the commit tail both call Linear.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	proceed, errno := spec.mutate(ctx)
	if errno != 0 {
		// Front half failed: leave the buffer dirty for a corrected re-save.
		return errno
	}
	if !proceed {
		// Nothing changed.
		eb.dirty = false
		return 0
	}

	// The API accepted a write. Run the commit tail, then — and only then —
	// adopt and invalidate: invalidating before the tail persists would let a
	// racing read repopulate the kernel cache from the stale row.
	fresh, errno := commitWriteBack(ctx, sink, spec.writeBack)
	if fresh != nil {
		spec.adopt(fresh)
	}
	for _, ino := range spec.coherence {
		sink.InvalidateUpdated(ino)
	}
	eb.dirty = false
	// Serve-your-own-writes (#365): adopt swapped only the entity, so the buffer
	// still holds the exact bytes the user wrote while SQLite and i.<entity> hold
	// Linear's normalized render. Mark it authored so a background refresh leaves
	// those bytes intact until the next fresh Open — a client verifying the write
	// by re-reading gets a byte-for-byte match instead of racing the refresh.
	//
	// ONLY on a fully successful commit (errno == 0). A fatal read-your-writes
	// divergence — a silent revert or substantial truncation (commitWriteBack
	// returns EIO) — means the write did NOT persist as written; serving W there
	// would hide the loss from the very byte-count re-read the .error tells the
	// agent to make ("re-read to see the stored value"). The benign reformat this
	// flag exists to smooth over is errno == 0, so the fix still lands.
	//
	// The pin (#379) is that same guarantee for the bytes rather than for the
	// buffer, armed on the same condition, and it is what carries a write across a
	// node boundary the flag cannot: the atomic-save path flushes through a
	// transient node and then drops the canonical inode on purpose, and on either
	// path a dentry forget inside the window rebuilds the node with an empty
	// buffer. Pinning HERE rather than in renameSave is also what makes a later
	// in-place edit supersede an earlier atomic save (#381) — one pin site, so the
	// pinned bytes are always the newest committed ones instead of whichever path
	// recorded last.
	if errno == 0 {
		eb.authored = true
		if spec.pinIno != 0 {
			sink.PinWritten(spec.pinIno, eb.content)
		}
	}
	return errno
}
