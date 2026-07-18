package fs

import (
	"fmt"
	"testing"
	"time"
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

func TestInvalidateRenamed(t *testing.T) {
	t.Run("atomic save drops both names and the file inode", func(t *testing.T) {
		r := &recordingNotifier{}
		invalidateRenamed(r, 3, "issue.md.tmp", "issue.md", 99)
		eq(t, r.calls, []string{`entry(3,"issue.md.tmp")`, `entry(3,"issue.md")`, `inode(99)`})
	})
	t.Run("pure entry rename skips the file inode", func(t *testing.T) {
		r := &recordingNotifier{}
		invalidateRenamed(r, 3, "Old.md", "New.md", 0)
		eq(t, r.calls, []string{`entry(3,"Old.md")`, `entry(3,"New.md")`})
	})
}

// TestBoundedNotify_FastPathRunsSynchronously: a notify that returns promptly is
// run to completion before boundedNotify returns — the guard adds only a
// goroutine hop on the happy path, so callers still see synchronous coherence.
func TestBoundedNotify_FastPathRunsSynchronously(t *testing.T) {
	ran := make(chan struct{}, 1)
	boundedNotify("created", func() { ran <- struct{}{} })
	select {
	case <-ran:
	default:
		t.Fatal("intent did not run before boundedNotify returned (happy path must be synchronous)")
	}
}

// TestBoundedNotify_TimesOutLeaksAndCounts is the #277 guard: a wedged notify
// must NOT hang the caller. boundedNotify returns after the deadline, records the
// timeout, and leaves the stuck intent goroutine parked (leaked). Deterministic:
// a block-forever intent + a lowered timeout always trips.
func TestBoundedNotify_TimesOutLeaksAndCounts(t *testing.T) {
	saved := kernelNotifyTimeout
	kernelNotifyTimeout = 10 * time.Millisecond
	t.Cleanup(func() { kernelNotifyTimeout = saved })

	before := counterValue(t, "linearfs.fuse.notify_timeouts", map[string]string{"intent": "updated"})

	block := make(chan struct{})
	t.Cleanup(func() { close(block) }) // release the leaked goroutine when the test ends
	started := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		boundedNotify("updated", func() {
			close(started)
			<-block // wedge: never completes until cleanup
		})
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("boundedNotify hung on a wedged intent (10ms guard) — the whole point of #277 is that it must not")
	}
	<-started // the intent did start and is now leaked, still blocked on `block`

	if after := counterValue(t, "linearfs.fuse.notify_timeouts", map[string]string{"intent": "updated"}); after != before+1 {
		t.Errorf("notify_timeouts{intent=updated} = %d, want %d", after, before+1)
	}
}
