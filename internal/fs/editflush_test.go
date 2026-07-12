package fs

import (
	"context"
	"syscall"
	"testing"
)

// editFlush's shell is the branchy part where drift lived (dirty on the wrong
// outcome, invalidate before persist, a forgotten coherence inode). These pin
// the three outcomes, the coherence-set exactness, and the persist-before-
// invalidate ordering through a recording seam — no FUSE mount, SQLite, or API.

type fakeEntity struct{ v int }

// recordingFlushSink satisfies editFlushSink and logs the persist/invalidate
// order so the ordering test can assert persist precedes every invalidation.
type recordingFlushSink struct {
	sets        int
	clears      int
	invalidated []uint64
	order       []string
}

func (r *recordingFlushSink) SetWriteError(key, message string) { r.sets++ }
func (r *recordingFlushSink) ClearWriteError(key string)        { r.clears++ }
func (r *recordingFlushSink) InvalidateUpdated(ino uint64) {
	r.invalidated = append(r.invalidated, ino)
	r.order = append(r.order, "invalidate")
}

func dirtyBuffer() *editBuffer { return &editBuffer{content: []byte("x"), dirty: true} }

func TestEditFlushFailKeepsDirtyNoCommit(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	fetched := false
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate: func(context.Context) (bool, syscall.Errno) { return false, syscall.EINVAL },
		writeBack: writeBackSpec[fakeEntity]{
			errKey: "k",
			fetch:  func(context.Context) (*fakeEntity, error) { fetched = true; return &fakeEntity{}, nil },
		},
		adopt:     func(*fakeEntity) {},
		coherence: []uint64{1, 2},
	})
	if errno != syscall.EINVAL {
		t.Errorf("errno = %v, want EINVAL", errno)
	}
	if !eb.dirty {
		t.Error("dirty cleared on a front-half failure; a corrected re-save cannot retry")
	}
	if fetched {
		t.Error("commit tail ran despite the front half failing")
	}
	if len(sink.invalidated) != 0 {
		t.Errorf("invalidated %v on failure, want none", sink.invalidated)
	}
}

func TestEditFlushNoChangeClearsDirtyNoCommit(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	fetched := false
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate: func(context.Context) (bool, syscall.Errno) { return false, 0 },
		writeBack: writeBackSpec[fakeEntity]{
			errKey: "k",
			fetch:  func(context.Context) (*fakeEntity, error) { fetched = true; return &fakeEntity{}, nil },
		},
		adopt:     func(*fakeEntity) {},
		coherence: []uint64{1, 2},
	})
	if errno != 0 {
		t.Errorf("errno = %v, want 0", errno)
	}
	if eb.dirty {
		t.Error("dirty not cleared on a no-op flush")
	}
	if fetched {
		t.Error("commit tail ran on a no-op flush")
	}
	if len(sink.invalidated) != 0 {
		t.Errorf("invalidated %v on a no-op, want none", sink.invalidated)
	}
}

func TestEditFlushProceedCommitsAdoptsInvalidates(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	var adopted *fakeEntity
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate: func(context.Context) (bool, syscall.Errno) { return true, 0 },
		writeBack: writeBackSpec[fakeEntity]{
			errKey:  "k",
			fetch:   func(context.Context) (*fakeEntity, error) { return &fakeEntity{v: 7}, nil },
			compare: func(*fakeEntity) []writeBackResult { return nil },
		},
		adopt:     func(f *fakeEntity) { adopted = f },
		coherence: []uint64{10, 20},
	})
	if errno != 0 {
		t.Errorf("errno = %v, want 0", errno)
	}
	if adopted == nil || adopted.v != 7 {
		t.Errorf("adopt got %+v, want the fresh {v:7}", adopted)
	}
	if eb.dirty {
		t.Error("dirty not cleared after a completed write")
	}
	// Coherence-set exactness: exactly the spec's inodes, in order.
	if len(sink.invalidated) != 2 || sink.invalidated[0] != 10 || sink.invalidated[1] != 20 {
		t.Errorf("invalidated = %v, want [10 20] (the exact coherence set)", sink.invalidated)
	}
}

func TestEditFlushInvalidatesAfterPersist(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate: func(context.Context) (bool, syscall.Errno) { return true, 0 },
		writeBack: writeBackSpec[fakeEntity]{
			errKey:  "k",
			fetch:   func(context.Context) (*fakeEntity, error) { return &fakeEntity{}, nil },
			persist: func(context.Context, *fakeEntity) error { sink.order = append(sink.order, "persist"); return nil },
			compare: func(*fakeEntity) []writeBackResult { return nil },
		},
		adopt:     func(*fakeEntity) {},
		coherence: []uint64{1},
	})
	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	// The stale-repopulation window closes only if persist precedes invalidate.
	if len(sink.order) < 2 || sink.order[0] != "persist" || sink.order[1] != "invalidate" {
		t.Errorf("order = %v, want [persist invalidate] (invalidate must follow persist)", sink.order)
	}
}

func TestEditFlushCleanBufferIsNoOp(t *testing.T) {
	t.Parallel()
	sink := &recordingFlushSink{}
	called := false
	// dirty=false: the guard short-circuits before mutate.
	errno := editFlush(context.Background(), sink, &editBuffer{content: []byte("x")}, editFlushSpec[fakeEntity]{
		mutate:    func(context.Context) (bool, syscall.Errno) { called = true; return true, 0 },
		coherence: []uint64{1},
	})
	if errno != 0 || called || len(sink.invalidated) != 0 {
		t.Errorf("clean buffer: errno=%v mutateCalled=%v invalidated=%v, want 0/false/none", errno, called, sink.invalidated)
	}
}
