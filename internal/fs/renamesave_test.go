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
}

func (r *renameRecorder) SetWriteError(key, message string) {
	r.setKey, r.setMsg = key, message
	r.sets++
}
func (r *renameRecorder) ClearWriteError(key string) { r.clears++ }
func (r *renameRecorder) InvalidateRenamed(dirIno uint64, oldName, newName string, fileIno uint64) {
	r.invalidates = append(r.invalidates,
		fmt.Sprintf("renamed(%d,%q,%q,%d)", dirIno, oldName, newName, fileIno))
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

func newRecordingRenameSpec(sink renameSink, scratchOK bool, flushErrno syscall.Errno) *recordingRenameSpec {
	r := &recordingRenameSpec{}
	r.spec = renameSaveSpec{
		dirIno: 0, // matches the zero-value renameParent inode
		scratch: func(name string) ([]byte, func(), bool) {
			r.scratchCalls++
			return []byte("scratch bytes"), func() { r.consumeCalls++ }, scratchOK
		},
		// The entity-directory resolver, the one production shape with a fixed
		// target; the collection resolver is covered in collectiondir_test.go.
		target: onlyFileTarget{
			sink:    sink,
			errKey:  "issue-1",
			name:    "issue.md",
			fileIno: 99,
			flush: func(ctx context.Context, content []byte) syscall.Errno {
				r.flushCalls++
				r.flushContent = content
				return flushErrno
			},
			adopt: func() { r.adoptCalls++ },
		}.resolve,
	}
	return r
}

func TestRenameSave_FlushOutcomes(t *testing.T) {
	// The serve-your-own-writes pin is not this tail's business — editFlush arms it
	// inside spec.flush, on the one outcome that knows a write committed (#381), and
	// editflush_test.go pins that policy.
	cases := []struct {
		name            string
		flushErrno      syscall.Errno
		wantConsumes    int
		wantInvalidates int
	}{
		// A clean save persists, so it consumes the scratch and drops the
		// kernel caches.
		{"flush success consumes and invalidates", 0, 1, 1},
		// Flush returns EIO only on a fatal read-your-writes divergence — the
		// write still reached Linear, so the save counts as persisted.
		{"flush EIO still consumes", syscall.EIO, 1, 1},
		// A refused save keeps its scratch file usable for a corrected rename,
		// and there is nothing moved into place to invalidate. Since #494 that
		// surviving scratch IS the corrected-re-save affordance, so it is not an
		// incidental detail of this branch.
		{"flush EINVAL keeps the scratch", syscall.EINVAL, 0, 0},
	}
	// adopt is deliberately NOT in the table: it runs on every outcome (#406).
	// The errno cannot separate a post-mutation EINVAL from a parse failure, and
	// it does not have to — adopt copies the flushed node's entity, which
	// editFlush replaces only with what its commit tail fetched back, so on a
	// failure that never reached Linear it copies the directory's own baseline
	// onto itself.

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &renameRecorder{}
			rec := newRecordingRenameSpec(sink, true, tc.flushErrno)

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
			if rec.adoptCalls != 1 {
				t.Errorf("adopt calls = %d, want 1 — every outcome adopts (#406): a post-mutation EINVAL that renamed the entity remotely would otherwise re-render the stale name", rec.adoptCalls)
			}
			if rec.consumeCalls != tc.wantConsumes {
				t.Errorf("consume calls = %d, want %d — the scratch survives a refused save", rec.consumeCalls, tc.wantConsumes)
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
			if sink.sets != 0 {
				t.Errorf("SetWriteError calls = %d, want 0", sink.sets)
			}
		})
	}
}

func TestRenameSave_WrongTarget(t *testing.T) {
	sink := &renameRecorder{}
	rec := newRecordingRenameSpec(sink, true, 0)

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
	rec := newRecordingRenameSpec(sink, true, 0)
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
	rec := newRecordingRenameSpec(sink, false, 0)

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
