package fs

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
)

// renameRecorder records the sink interactions so a test can assert what the
// tail did without a LinearFS or a FUSE mount. It satisfies renameSink.
type renameRecorder struct {
	setKey, setMsg string
	sets           int
	clears         int
	invalidates    []string
	pins           []string
	// events orders the coherence calls against each other: the pin has to be in
	// place before the invalidation that triggers the re-Lookup which consumes it.
	events []string
}

func (r *renameRecorder) SetWriteError(key, message string) {
	r.setKey, r.setMsg = key, message
	r.sets++
}
func (r *renameRecorder) ClearWriteError(key string) { r.clears++ }
func (r *renameRecorder) InvalidateRenamed(dirIno uint64, oldName, newName string, fileIno uint64) {
	r.invalidates = append(r.invalidates,
		fmt.Sprintf("renamed(%d,%q,%q,%d)", dirIno, oldName, newName, fileIno))
	r.events = append(r.events, "invalidate")
}
func (r *renameRecorder) PinWritten(fileIno uint64, content []byte) {
	r.pins = append(r.pins, fmt.Sprintf("pin(%d,%q)", fileIno, content))
	r.events = append(r.events, "pin")
}

// renameParent is a bare InodeEmbedder whose zero-value inode has ino 0, so a
// spec with dirIno 0 reads as "same directory" and any nonzero dirIno as a
// cross-directory rename — no inode tree needed.
type renameParent struct{ fs.Inode }

// recordingRenameSpec builds a spec whose closures record their calls.
type recordingRenameSpec struct {
	spec         renameSaveSpec
	scratchCalls int
	consumeCalls int
	flushCalls   int
	flushContent []byte
	adoptCalls   int
}

func newRecordingRenameSpec(scratchOK, flushCommitted bool, flushErrno syscall.Errno) *recordingRenameSpec {
	r := &recordingRenameSpec{}
	r.spec = renameSaveSpec{
		targetName: "issue.md",
		errKey:     "issue-1",
		dirIno:     0, // matches the zero-value renameParent inode
		fileIno:    99,
		scratch: func(name string) ([]byte, func(), bool) {
			r.scratchCalls++
			return []byte("scratch bytes"), func() { r.consumeCalls++ }, scratchOK
		},
		flush: func(ctx context.Context, content []byte) (bool, syscall.Errno) {
			r.flushCalls++
			r.flushContent = content
			return flushCommitted, flushErrno
		},
		adopt: func() { r.adoptCalls++ },
	}
	return r
}

func TestRenameSave_FlushOutcomes(t *testing.T) {
	cases := []struct {
		name            string
		flushCommitted  bool
		flushErrno      syscall.Errno
		wantAdopts      int
		wantInvalidates int
		wantPins        int
	}{
		// A clean save adopts the fresh entity, pins the written bytes for the
		// re-Lookup to serve back (#379), and drops the kernel caches.
		{"flush success adopts, pins and invalidates", true, 0, 1, 1, 1},
		// A save that resolved to no changes also returns 0. The tail still
		// adopts/consumes/invalidates, but it must NOT pin: echoing the written
		// bytes back would report an edit Linear never took as a byte-for-byte
		// success.
		{"flush that committed nothing never pins", false, 0, 1, 1, 0},
		// The policy under test: Flush returns EIO only on a fatal
		// read-your-writes divergence — the write still reached Linear, so the
		// fresh entity is adopted (refusing would serve stale content while
		// .error explains the divergence). It must NOT pin: the write did not
		// persist as written, and serving the written bytes back would hide that
		// from the re-read .error asks for (#365's errno == 0 rule).
		{"flush EIO adopts but never pins", true, syscall.EIO, 1, 1, 0},
		// EINVAL means the write never reached Linear (parse/validation
		// failure): nothing to adopt, nothing to pin, nothing to invalidate.
		{"flush EINVAL adopts nothing", false, syscall.EINVAL, 0, 0, 0},
	}
	// The scratch buffer is consumed exactly when the rename succeeds (the same
	// {0, EIO} branch that adopts): go-fuse has moved the spent node over the
	// canonical name, so it must reject further access. A rejected save leaves
	// the scratch usable.

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &renameRecorder{}
			rec := newRecordingRenameSpec(true, tc.flushCommitted, tc.flushErrno)

			errno := renameSave(context.Background(), sink, "issue.md.tmp.1",
				&renameParent{}, "issue.md", rec.spec)

			if errno != tc.flushErrno {
				t.Errorf("errno = %v, want %v", errno, tc.flushErrno)
			}
			if rec.flushCalls != 1 {
				t.Errorf("flush calls = %d, want 1", rec.flushCalls)
			}
			if string(rec.flushContent) != "scratch bytes" {
				t.Errorf("flush content = %q, want the scratch buffer", rec.flushContent)
			}
			if rec.adoptCalls != tc.wantAdopts {
				t.Errorf("adopt calls = %d, want %d", rec.adoptCalls, tc.wantAdopts)
			}
			if rec.consumeCalls != tc.wantAdopts {
				t.Errorf("consume calls = %d, want %d (consume rides the adopt branch)", rec.consumeCalls, tc.wantAdopts)
			}
			if len(sink.invalidates) != tc.wantInvalidates {
				t.Fatalf("invalidates = %v, want %d call(s)", sink.invalidates, tc.wantInvalidates)
			}
			if tc.wantInvalidates == 1 {
				want := `renamed(0,"issue.md.tmp.1","issue.md",99)`
				if sink.invalidates[0] != want {
					t.Errorf("invalidate = %q, want %q", sink.invalidates[0], want)
				}
			}
			if len(sink.pins) != tc.wantPins {
				t.Fatalf("pins = %v, want %d call(s)", sink.pins, tc.wantPins)
			}
			if tc.wantPins == 1 {
				// The pin carries the written bytes under the CANONICAL file's
				// inode — that is the Lookup that will serve them back.
				want := `pin(99,"scratch bytes")`
				if sink.pins[0] != want {
					t.Errorf("pin = %q, want %q", sink.pins[0], want)
				}
				// Ordering matters: the invalidation is what forces the
				// re-Lookup, so the pin has to be waiting before it fires.
				if len(sink.events) != 2 || sink.events[0] != "pin" {
					t.Errorf("event order = %v, want the pin before the invalidation", sink.events)
				}
			}
			if sink.sets != 0 {
				t.Errorf("SetWriteError calls = %d, want 0", sink.sets)
			}
		})
	}
}

func TestRenameSave_WrongTarget(t *testing.T) {
	sink := &renameRecorder{}
	rec := newRecordingRenameSpec(true, true, 0)

	errno := renameSave(context.Background(), sink, "issue.md.tmp.1",
		&renameParent{}, "notes.md", rec.spec)

	if errno != syscall.ENOTSUP {
		t.Errorf("errno = %v, want ENOTSUP", errno)
	}
	if sink.sets != 1 || sink.setKey != "issue-1" {
		t.Fatalf("SetWriteError calls = %d (key %q), want 1 on %q", sink.sets, sink.setKey, "issue-1")
	}
	// The .error message must name the one writable target so an agent knows
	// where to save.
	if !strings.Contains(sink.setMsg, "only issue.md is writable") {
		t.Errorf(".error message %q does not name the writable target", sink.setMsg)
	}
	if !strings.Contains(sink.setMsg, "rename issue.md.tmp.1 -> notes.md") {
		t.Errorf(".error message %q does not describe the rejected rename", sink.setMsg)
	}
	if rec.flushCalls != 0 || rec.adoptCalls != 0 || len(sink.invalidates) != 0 {
		t.Errorf("wrong target must stop before flush: flush=%d adopt=%d invalidates=%v",
			rec.flushCalls, rec.adoptCalls, sink.invalidates)
	}
	if rec.consumeCalls != 0 {
		t.Errorf("consume calls = %d, want 0 (a rejected rename leaves the scratch usable)", rec.consumeCalls)
	}
}

func TestRenameSave_CrossDirectory(t *testing.T) {
	sink := &renameRecorder{}
	rec := newRecordingRenameSpec(true, true, 0)
	// The zero-value parent inode has ino 0; a nonzero dirIno makes the rename
	// cross-directory.
	rec.spec.dirIno = 7

	errno := renameSave(context.Background(), sink, "issue.md.tmp.1",
		&renameParent{}, "issue.md", rec.spec)

	if errno != syscall.EXDEV {
		t.Errorf("errno = %v, want EXDEV", errno)
	}
	if rec.scratchCalls != 0 || rec.flushCalls != 0 || rec.adoptCalls != 0 {
		t.Errorf("cross-directory rename must stop first: scratch=%d flush=%d adopt=%d",
			rec.scratchCalls, rec.flushCalls, rec.adoptCalls)
	}
	if sink.sets != 0 || len(sink.invalidates) != 0 {
		t.Errorf("cross-directory rename must touch nothing: sets=%d invalidates=%v",
			sink.sets, sink.invalidates)
	}
}

func TestRenameSave_NotAScratchFile(t *testing.T) {
	sink := &renameRecorder{}
	// e.g. an attempt to rename issue.md itself: the canonical files aren't
	// renamable, and no .error is recorded (there is nothing to persist).
	rec := newRecordingRenameSpec(false, true, 0)

	errno := renameSave(context.Background(), sink, "issue.md",
		&renameParent{}, "renamed.md", rec.spec)

	if errno != syscall.ENOTSUP {
		t.Errorf("errno = %v, want ENOTSUP", errno)
	}
	if rec.scratchCalls != 1 {
		t.Errorf("scratch calls = %d, want 1", rec.scratchCalls)
	}
	if rec.flushCalls != 0 || rec.adoptCalls != 0 {
		t.Errorf("non-scratch rename must stop before flush: flush=%d adopt=%d",
			rec.flushCalls, rec.adoptCalls)
	}
	if sink.sets != 0 || len(sink.invalidates) != 0 {
		t.Errorf("non-scratch rename must touch nothing: sets=%d invalidates=%v",
			sink.sets, sink.invalidates)
	}
}
