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
	flushCalls   int
	flushContent []byte
	adoptCalls   int
}

func newRecordingRenameSpec(scratchOK bool, flushErrno syscall.Errno) *recordingRenameSpec {
	r := &recordingRenameSpec{}
	r.spec = renameSaveSpec{
		targetName: "issue.md",
		errKey:     "issue-1",
		dirIno:     0, // matches the zero-value renameParent inode
		fileIno:    99,
		scratch: func(name string) ([]byte, bool) {
			r.scratchCalls++
			return []byte("scratch bytes"), scratchOK
		},
		flush: func(ctx context.Context, content []byte) syscall.Errno {
			r.flushCalls++
			r.flushContent = content
			return flushErrno
		},
		adopt: func() { r.adoptCalls++ },
	}
	return r
}

func TestRenameSave_FlushOutcomes(t *testing.T) {
	cases := []struct {
		name            string
		flushErrno      syscall.Errno
		wantAdopts      int
		wantInvalidates int
	}{
		// A clean save adopts the fresh entity and drops the kernel caches.
		{"flush success adopts and invalidates", 0, 1, 1},
		// The policy under test: Flush returns EIO only on a fatal
		// read-your-writes divergence — the write still reached Linear, so the
		// fresh entity is adopted (refusing would serve stale content while
		// .error explains the divergence).
		{"flush EIO still adopts and invalidates", syscall.EIO, 1, 1},
		// EINVAL means the write never reached Linear (parse/validation
		// failure): nothing to adopt, nothing to invalidate.
		{"flush EINVAL adopts nothing", syscall.EINVAL, 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &renameRecorder{}
			rec := newRecordingRenameSpec(true, tc.flushErrno)

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
	rec := newRecordingRenameSpec(true, 0)

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
}

func TestRenameSave_CrossDirectory(t *testing.T) {
	sink := &renameRecorder{}
	rec := newRecordingRenameSpec(true, 0)
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
	rec := newRecordingRenameSpec(false, 0)

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
