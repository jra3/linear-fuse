package fs

import (
	"bytes"
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
)

// emptyWriteMessage is the .error an emptied editable file gets. It follows the
// Field/Value/Error shape the rest of the write feedback uses, and it says what
// the writer must do next — the shell puts the file's current contents back into
// the buffer on this rejection, so a re-read recovers them.
//
// It deliberately does NOT offer "or empty the body" as a way to clear one
// field: it is shared by all seven editable surfaces, and on project.md /
// initiative.md emptying the body is the one write Linear declines to apply
// (#398).
//
// It also does not assert ONE clearing mechanism for all seven surfaces (#476).
// Omitting a key clears the field on issue.md and milestones/*.md, but on
// labels/*.md an absent key means "leave that field alone" — a label writer who
// followed the old wording got a silent no-op, which is the failure family this
// message exists to prevent. So it points at the surface's own documented
// clearing idiom instead of naming one.
func emptyWriteMessage(op string) string {
	return fmt.Sprintf("Empty write rejected\nOperation: %s\n"+
		"Error: the file was written with no content. An empty file carries no fields, so applying it "+
		"would clear every removable field at once rather than change the one you meant. Nothing was written.\n"+
		"Fix: re-read the file to get its current contents, change the field you mean to change, and write "+
		"the whole document back. To clear one field, use the clearing idiom this "+
		"file documents — on issue.md omit the key; on labels/*.md write description: \"\". See the README's "+
		"frontmatter section for this surface.", op)
}

// holeWriteMessage is the .error a zero-filled write gets (#472). Same shape and
// same recovery as emptyWriteMessage — the buffer is restored on this rejection
// too — but it names the cause, because the writer's mistake is not "I wrote
// nothing", it is "I wrote at an offset past the end of the file", and only the
// second sentence tells them which syscall to change.
func holeWriteMessage(op string) string {
	return fmt.Sprintf("Zero-filled write rejected\nOperation: %s\n"+
		"Error: the file contains NUL bytes. A NUL is what the filesystem fills a hole with when a write "+
		"starts past the end of the file, or when a resize grows it — not something a writer composes. "+
		"Applying it would store those bytes as the body, and because the document no longer starts with "+
		"its frontmatter it would clear every removable field at once as well. Nothing was written.\n"+
		"Fix: re-read the file to get its current contents, change what you mean to change, and write the "+
		"WHOLE document back from offset 0 (truncate first, rather than seeking past the end or resizing). "+
		"To clear one field, use the clearing idiom this "+
		"file documents — on issue.md omit the key; on labels/*.md write description: \"\". See the README's "+
		"frontmatter section for this surface.", op)
}

// writeVerdict classifies a dirty buffer before the front half ever sees it:
// either it is a document the writer composed, or it is one of the two accidents
// the shell refuses outright.
type writeVerdict int

const (
	// writeIsDocument — flush it.
	writeIsDocument writeVerdict = iota
	// writeIsEmpty — nothing but whitespace (#397).
	writeIsEmpty
	// writeIsHole — the buffer carries filesystem zero-fill (#472).
	writeIsHole
)

// classifyWrite is the pure predicate behind the shell's rejection. It is the
// whole decision, extracted so the contract all seven editable surfaces share is
// tested as a table rather than through a mount.
//
// The NUL arm is #472, and it exists because bytes.TrimSpace does NOT strip NUL:
// a buffer of nothing but zero-fill sailed past the empty guard that exists
// precisely to stop a fieldless document from wiping every field, and was
// persisted to Linear as a description of NUL bytes plus assignee/dueDate/
// parent/project/milestone/cycle/labels all cleared — exit 0, no .error. The
// zero-fill itself is correct filesystem behavior (a write starting past EOF, or
// a grow-resize, leaves a hole), which is why the accident is invisible to the
// writer: the document just no longer begins with `---`, so marshal.Parse reads
// it as empty frontmatter with the whole string as body.
//
// The predicate is "a NUL anywhere", not "a leading NUL". The measured case is a
// hole at offset 0, but a hole in the MIDDLE of an otherwise-parseable document
// is the same accident with a smaller blast radius — it keeps the frontmatter
// and stores the zero-fill as the body — and NUL is never a byte one of these
// markdown surfaces should carry either way. One predicate, no offset subtlety.
func classifyWrite(content []byte) writeVerdict {
	if len(bytes.TrimSpace(content)) == 0 {
		return writeIsEmpty
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return writeIsHole
	}
	return writeIsDocument
}

// rejectedWriteMessage renders the .error for a refused buffer. Not called for
// writeIsDocument.
func rejectedWriteMessage(v writeVerdict, op string) string {
	if v == writeIsHole {
		return holeWriteMessage(op)
	}
	return emptyWriteMessage(op)
}

// mutateOutcome is what a front half reports back to the shell alongside its
// errno. It carries two independent facts, because the shell needs both and
// neither can be read off the errno: whether there is a write to commit, and —
// when the front half failed — whether its request had already left the process.
//
// The second one is the whole of #439's "does the answer differ by failure
// stage?". A parse failure, an unknown frontmatter key and a name that resolved
// nowhere never touched Linear, so the entity's local render is still the truth
// and the buffer can be restored from it. A mutation Linear rejected did reach
// Linear — and #399 already established that "did it reach Linear" changes the
// safe follow-up — so the local render is a guess, and the shell asks for fresh
// data instead of asserting one.
type mutateOutcome struct {
	// proceed says the API accepted a write, so the commit tail must run.
	proceed bool
	// sent says the mutation request left the process. Read only on the failure
	// arm; a committed write is sent by definition.
	sent bool
}

// mutateUnsent reports a front half that failed BEFORE its request left the
// process — a parse failure, an unknown key, a name that resolved nowhere.
// Paired with a non-zero errno.
func mutateUnsent() mutateOutcome { return mutateOutcome{} }

// mutateSent reports a front half whose mutation reached Linear and came back a
// failure. Paired with a non-zero errno.
func mutateSent() mutateOutcome { return mutateOutcome{sent: true} }

// mutateFailed reports a front half that failed, saying for itself whether any
// of its requests had already reached Linear. The two named constructors above
// are the readable spelling where the answer is fixed; this one is for a front
// half that sends MORE THAN ONE request (the project/initiative link
// reconciles), where a later step can fail locally after an earlier one has
// already changed something upstream.
func mutateFailed(sent bool) mutateOutcome { return mutateOutcome{sent: sent} }

// mutateNoChange reports a document that resolved to no changes. Paired with a
// zero errno: this is a success, not a failure.
func mutateNoChange() mutateOutcome { return mutateOutcome{} }

// mutateWrote reports a write the API accepted. Paired with a zero errno.
func mutateWrote() mutateOutcome { return mutateOutcome{proceed: true} }

// failedWriteRecovery names what the shell leaves in the buffer of a write that
// failed. Clearing dirty is NOT one of the choices — that half is unconditional
// (see editFlush) — so this is only the best-effort half.
type failedWriteRecovery int

const (
	// recoverByRestore — put the entity's current local render back into the
	// buffer, and attribute it to the flushing handle.
	recoverByRestore failedWriteRecovery = iota
	// recoverByRefresh — leave the (now clean) buffer alone and trigger the
	// entity's SWR refresh, so what replaces it comes from Linear rather than
	// from a local render of a possibly-stale entity.
	recoverByRefresh
)

// recoverFailedWrite is the pure decision behind editFlush's failure arms, and
// the reason it is a named function rather than an inline `if` is that the whole
// contract of #494 lives in it: a failed write clears dirty either way, and what
// it leaves behind turns on one fact — whether the request reached Linear.
func recoverFailedWrite(sent bool) failedWriteRecovery {
	if sent {
		return recoverByRefresh
	}
	return recoverByRestore
}

// restoreBuffer puts the entity's current render back into a buffer whose write
// was rejected, and records it against the handle the rejection arrived on
// (#454, see editHandle): the bytes are ones NOBODY WROTE, so if the same open
// goes on to write — the `>` redirect's kernel sequence puts a flush in the
// middle of one — that write must re-apply the truncation instead of splicing
// into the resurrected image. A flush with no handle (the atomic-save path)
// records nothing; there is no writer to continue.
//
// Best effort by design: a spec with no restore, or a render that declines
// (a marshal error), leaves the buffer as it is. The caller has already cleared
// dirty, which is what lets a background refresh replace it.
//
// The caller holds eb.mu.
func restoreBuffer(eb *editBuffer, fh fs.FileHandle, restore func() []byte) {
	if restore == nil {
		return
	}
	current := restore()
	if len(current) == 0 {
		return
	}
	eb.content = current
	eb.markRestored(fh)
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
	// and reports one of four outcomes, each spelled by a mutateOutcome
	// constructor:
	//   - mutateUnsent(), errno != 0   → the front half failed BEFORE its request
	//     left the process; the shell clears dirty and restores.
	//   - mutateSent(), errno != 0     → the request reached Linear and came back
	//     a failure; the shell clears dirty and refreshes instead of restoring.
	//   - mutateNoChange(), 0          → nothing changed; the shell clears dirty
	//     and returns 0 without committing.
	//   - mutateWrote(), 0             → the API accepted a write; the shell runs
	//     the commit tail, adopts, invalidates, and clears dirty.
	//
	// mutate owns its own .error message on either failure arm.
	mutate func(ctx context.Context) (outcome mutateOutcome, errno syscall.Errno)
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
	// invalidateExtra drops the cache a static inode list cannot name: entries
	// whose DIRECTORY or NAME depends on what the write returned. Optional, and
	// deliberately rare — one edit needs it (a team move, #429: the issue leaves
	// the old team's listings under its old identifier and appears in the new
	// team's under a new one, and only `fresh` knows either). Runs after the
	// coherence set, on the same persisted-first rule; skipped when the commit
	// tail returned no fresh entity, since there is then nothing to key off.
	// The sibling create/delete tails have the same-named hook for the same
	// reason (createcommit.go, deletecommit.go).
	invalidateExtra func(fresh *T)
	// restore re-renders the entity's CURRENT content, exactly as the node's
	// construction seam rendered it. The shell calls it on every failure that
	// never reached Linear (#439/#494): the refused-write rejection below — an
	// emptied or a zero-filled buffer, both arms of classifyWrite — and a front
	// half that failed at parse or resolve. It puts those bytes back into the
	// buffer and attributes them to the flushing handle. Return nil (or an empty
	// render) to decline, and the already-clean buffer is left as-is.
	//
	// Declining is safe; blanking the buffer would not be. Content is eagerly
	// seeded at construction and Read has no fallback render, so nil content is a
	// zero-byte file — the exact bug restore exists to prevent. A clean but
	// unrestored buffer self-heals instead, because being clean is what
	// re-permits refresh to replace it.
	restore func() []byte
	// refresh asks for fresh data about this entity — the entity's existing SWR
	// trigger on the repo, never a cache mutation of the shell's own. The shell
	// calls it on the one failure arm restore must not serve: a front half whose
	// request REACHED Linear. Rendering the in-memory entity there would
	// confidently show pre-write content that may already be wrong upstream
	// (restore renders locally and never fetches), and for the ENOENT verdict —
	// Linear saying the entity is gone — the refresh is also what reaches the
	// repo's orphan prune (#477), which stays owned by the repo layer: it re-asks
	// Linear rather than trusting a not-found that api.IsNotFound matched on
	// message text.
	//
	// Optional. Nil where the entity has no SWR surface to trigger — project.md,
	// initiative.md, labels/*.md and milestones/*.md today — and the clean buffer
	// then converges on the sync worker's schedule instead.
	refresh func()
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
// dirty flag); fh is the FUSE file handle the Flush arrived on, which the
// refused-write restore attributes its bytes to (#454, see editHandle); sink
// carries the error + invalidation surfaces; spec supplies the per-entity front
// half, commit tail, adopt, and invalidation set.
//
// Returns the errno the Flush should surface: the front half's errno on
// failure, 0 on a no-op, or the commit tail's errno on a completed write. The
// buffer lock is held across the whole shell, exactly as the hand-written
// handlers held n.mu.
func editFlush[T any](ctx context.Context, sink editFlushSink, eb *editBuffer, fh fs.FileHandle, spec editFlushSpec[T]) syscall.Errno {
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
	// legible .error. Clearing ONE field stays expressible — drop its key, or empty
	// the body while keeping the frontmatter. What is no longer expressible is
	// "clear everything by accident".
	//
	// Unlike a parse failure, the buffer is then RESTORED rather than left dirty.
	// A parse failure holds text the writer meant and a corrected re-save needs
	// it; an emptied file holds nothing, and on the in-place path (`> issue.md`,
	// or collectiondir.go's O_TRUNC Create) that buffer belongs to the CANONICAL
	// node, which would then serve zero bytes for the rest of its life — nothing
	// clears dirty but a successful flush, and refresh refuses a dirty buffer. The
	// .error tells the writer to re-read the file to recover its contents, so the
	// re-read has to actually return them.
	//
	// This subsumes the old `eb.content == nil` half of the guard above, and that
	// is the point: a nil buffer is not a third state, it is how the ATOMIC-SAVE
	// path spells an emptied file (a zero-byte scratch file renamed over the
	// target hands the flush nil bytes). Treating nil as "nothing to do" while an
	// O_TRUNC of the same file was a real edit gave the two save paths opposite
	// answers to `> issue.md` — silent success on one, a mutation on the other.
	//
	// A zero-filled buffer is the same accident wearing a different mask (#472),
	// and it is rejected on the same terms: bytes.TrimSpace does not strip NUL, so
	// an all-NUL buffer is not "empty", but a document that begins with NUL does
	// not begin with `---` either — marshal.Parse hands back empty frontmatter
	// with the whole string as body, and the mutation that follows sends a
	// description of NUL bytes AND clears assignee, due date, parent, project,
	// milestone, cycle and labels together. classifyWrite owns both predicates.
	if verdict := classifyWrite(eb.content); verdict != writeIsDocument {
		sink.SetWriteError(spec.writeBack.errKey, rejectedWriteMessage(verdict, spec.writeBack.op))
		// Nothing reached Linear, so the entity's local render is still the truth.
		eb.dirty = false
		restoreBuffer(eb, fh, spec.restore)
		return syscall.EINVAL
	}

	// Past the rejection guard the buffer holds content somebody actually wrote,
	// so any restore this handle was still carrying is spent.
	_ = eb.takeTruncation(fh)

	// Bound the API work — the front half and the commit tail both call Linear.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	outcome, errno := spec.mutate(ctx)
	if errno != 0 {
		// The front half failed. Clearing dirty is the invariant here, and it is
		// unconditional (#439/#494): dirty is BUFFER-level, so a buffer left dirty
		// makes every later close(2) on this node — including a pure reader's —
		// re-enter the write path and re-attempt the doomed mutation (#418
		// measured 20 and 18 repeats from single bad writes), and refresh refuses
		// any buffer that is dirty, which freezes the file's content for the life
		// of the node. The affordance the dirty flag protected — fix bad
		// frontmatter and re-save without retyping — is already served on the
		// rename path by the scratch file, which renameSave deliberately leaves
		// unconsumed after a failed flush, and that is the path Edit, vim and VS
		// Code all take.
		//
		// What the cleared buffer is left holding is the one thing that turns on
		// the failure stage; recoverFailedWrite owns that decision.
		eb.dirty = false
		switch recoverFailedWrite(outcome.sent) {
		case recoverByRestore:
			restoreBuffer(eb, fh, spec.restore)
		case recoverByRefresh:
			if spec.refresh != nil {
				spec.refresh()
			}
		}
		return errno
	}
	if !outcome.proceed {
		// Nothing changed — but this IS a successful write, so it clears the
		// entity's .error like any other (#400). Without the clear, a document
		// rejected once (an unknown key, an unresolvable name) left its reason
		// standing after the writer re-read the file and saved it back
		// unmodified: the file was valid, the write returned 0, and .error still
		// accused it. The contract is "success clears it", and a no-op is a
		// success — every other success path clears through commitWriteBack,
		// which this branch returns before reaching.
		sink.ClearWriteError(spec.writeBack.errKey)
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
	if spec.invalidateExtra != nil && fresh != nil {
		spec.invalidateExtra(fresh)
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
