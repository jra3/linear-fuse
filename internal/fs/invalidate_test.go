package fs

import (
	"fmt"
	"testing"
)

// recordingNotifier captures the notify calls the coherence policy makes, in
// order, so a test can assert the policy without a FUSE server. It satisfies
// kernelNotifier.
type recordingNotifier struct {
	calls []string
}

func (r *recordingNotifier) InvalidateKernelInode(ino uint64) {
	r.calls = append(r.calls, fmt.Sprintf("inode(%d)", ino))
}
func (r *recordingNotifier) InvalidateKernelEntry(parent uint64, name string) {
	r.calls = append(r.calls, fmt.Sprintf("entry(%d,%q)", parent, name))
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("calls = %v, want %v", got, want)
		}
	}
}

func TestInvalidateCreated(t *testing.T) {
	t.Run("with a known name refreshes listing, entry, and _create", func(t *testing.T) {
		r := &recordingNotifier{}
		invalidateCreated(r, 7, "0001-note.md")
		eq(t, r.calls, []string{`inode(7)`, `entry(7,"0001-note.md")`, `entry(7,"_create")`})
	})
	t.Run("without a name still refreshes listing and _create", func(t *testing.T) {
		r := &recordingNotifier{}
		invalidateCreated(r, 7, "")
		eq(t, r.calls, []string{`inode(7)`, `entry(7,"_create")`})
	})
}

func TestInvalidateDeleted(t *testing.T) {
	r := &recordingNotifier{}
	invalidateDeleted(r, 9, "blocks-ENG-5.rel")
	// The bug this prevents: a delete that notifies nothing leaves the entry
	// visible until the cache TTL. Both notifies must fire.
	eq(t, r.calls, []string{`inode(9)`, `entry(9,"blocks-ENG-5.rel")`})
}

func TestInvalidateUpdated(t *testing.T) {
	r := &recordingNotifier{}
	invalidateUpdated(r, 42)
	eq(t, r.calls, []string{`inode(42)`})
}
