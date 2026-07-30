package fs

import (
	"context"
	"fmt"
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
	pins        []string
	order       []string
}

func (r *recordingFlushSink) SetWriteError(key, message string) { r.sets++ }
func (r *recordingFlushSink) ClearWriteError(key string)        { r.clears++ }
func (r *recordingFlushSink) InvalidateUpdated(ino uint64) {
	r.invalidated = append(r.invalidated, ino)
	r.order = append(r.order, "invalidate")
}
func (r *recordingFlushSink) PinWritten(fileIno uint64, content []byte) {
	r.pins = append(r.pins, fmt.Sprintf("pin(%d,%q)", fileIno, content))
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

func TestEditFlushProceedMarksAuthored(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate: func(context.Context) (bool, syscall.Errno) { return true, 0 },
		writeBack: writeBackSpec[fakeEntity]{
			errKey:  "k",
			fetch:   func(context.Context) (*fakeEntity, error) { return &fakeEntity{v: 7}, nil },
			compare: func(*fakeEntity) []writeBackResult { return nil },
		},
		adopt:     func(*fakeEntity) {},
		coherence: []uint64{10},
		pinIno:    10,
	})
	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	// A completed write opens the serve-your-own-writes window (#365): the buffer
	// still holds the exact written bytes and must resist a background refresh
	// until the next fresh Open.
	if !eb.authored {
		t.Error("a completed write did not mark the buffer authored; the read-back window is unprotected")
	}
	// The same window, for the bytes rather than the buffer (#379, #381): the flag
	// dies with the node, so the written bytes are also pinned under pinIno for the
	// next Lookup of the file to seed from.
	if len(sink.pins) != 1 || sink.pins[0] != `pin(10,"x")` {
		t.Errorf("pins = %v, want [pin(10,\"x\")] — the written bytes under pinIno", sink.pins)
	}
}

func TestEditFlushZeroPinInoNeverPins(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	// pinIno zero is a comment/doc/label/milestone .md: its Lookup does not call
	// seedAuthored, so a pin would be bytes nobody can ever read, held for the TTL.
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate: func(context.Context) (bool, syscall.Errno) { return true, 0 },
		writeBack: writeBackSpec[fakeEntity]{
			errKey:  "k",
			fetch:   func(context.Context) (*fakeEntity, error) { return &fakeEntity{v: 7}, nil },
			compare: func(*fakeEntity) []writeBackResult { return nil },
		},
		adopt:     func(*fakeEntity) {},
		coherence: []uint64{10},
	})
	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	if !eb.authored {
		t.Error("a completed write did not mark the buffer authored; pinIno is about the pin, not the flag")
	}
	if len(sink.pins) != 0 {
		t.Errorf("pins = %v, want none for an entity with no pin-seeded Lookup", sink.pins)
	}
}

// pinningFlushSink is the recording sink over a REAL authoredPins, so a test can
// read a pin back the way a Lookup does (seedAuthored) instead of asserting on the
// call log. Both embedded types define PinWritten, so the override is required.
type pinningFlushSink struct {
	recordingFlushSink
	authoredPins
}

func (s *pinningFlushSink) PinWritten(fileIno uint64, content []byte) {
	s.recordingFlushSink.PinWritten(fileIno, content)
	s.authoredPins.PinWritten(fileIno, content)
}

func TestEditFlushInPlaceWriteSupersedesAtomicSavePin(t *testing.T) {
	t.Parallel()
	const fileIno = 77
	sink := &pinningFlushSink{}
	// An atomic save committed a moment ago and pinned its bytes for the re-Lookup
	// it forces (#379).
	sink.authoredPins.PinWritten(fileIno, []byte("atomic save bytes"))

	// Now the client edits the same file IN PLACE, inside the pin's window, and
	// that write commits cleanly.
	eb := &editBuffer{content: []byte("newer in-place bytes"), dirty: true}
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate: func(context.Context) (bool, syscall.Errno) { return true, 0 },
		writeBack: writeBackSpec[fakeEntity]{
			errKey:  "k",
			fetch:   func(context.Context) (*fakeEntity, error) { return &fakeEntity{v: 7}, nil },
			compare: func(*fakeEntity) []writeBackResult { return nil },
		},
		adopt:     func(*fakeEntity) {},
		coherence: []uint64{fileIno},
		pinIno:    fileIno,
	})
	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}

	// The node is then forgotten and re-Looked-up while the window is still open
	// (dentry eviction, or a fresh open through another path). Before #381 this
	// served the older atomic-save bytes — read-your-writes running BACKWARDS.
	fresh := &editBuffer{content: []byte("Linear's normalized render")}
	size, seeded := sink.seedBuilt(fresh, fileIno)
	if !seeded || size != len("newer in-place bytes") {
		t.Errorf("re-Lookup published size %d (seeded=%v), want the newest committed write's %d",
			size, seeded, len("newer in-place bytes"))
	}
	if string(fresh.content) != "newer in-place bytes" || !fresh.authored {
		t.Errorf("re-seeded buffer = %q (authored=%v), want the newest write, marked authored", fresh.content, fresh.authored)
	}
}

func TestEditFlushNoChangeDoesNotMarkAuthored(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate:    func(context.Context) (bool, syscall.Errno) { return false, 0 },
		writeBack: writeBackSpec[fakeEntity]{errKey: "k"},
		adopt:     func(*fakeEntity) {},
		coherence: []uint64{1},
		pinIno:    1,
	})
	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	if eb.authored {
		t.Error("a no-op flush marked the buffer authored; only a persisted write should")
	}
	// Nor pinned: echoing bytes back for an edit Linear never took would report a
	// dropped write as a byte-for-byte success.
	if len(sink.pins) != 0 {
		t.Errorf("pins = %v on a no-op flush, want none", sink.pins)
	}
}

func TestEditFlushFailDoesNotMarkAuthored(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate:    func(context.Context) (bool, syscall.Errno) { return false, syscall.EINVAL },
		writeBack: writeBackSpec[fakeEntity]{errKey: "k"},
		adopt:     func(*fakeEntity) {},
		coherence: []uint64{1},
		pinIno:    1,
	})
	if errno != syscall.EINVAL {
		t.Fatalf("errno = %v, want EINVAL", errno)
	}
	if eb.authored {
		t.Error("a failed flush marked the buffer authored; nothing persisted")
	}
	if len(sink.pins) != 0 {
		t.Errorf("pins = %v on a failed flush, want none", sink.pins)
	}
}

func TestEditFlushFatalDivergenceDoesNotMarkAuthored(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	// proceed=true, but the read-your-writes compare reports a FATAL divergence
	// (silent revert / truncation) → commitWriteBack returns EIO. Serving the
	// written bytes here would mask real data loss from a re-reading verifier, so
	// the buffer must NOT be armed authored.
	errno := editFlush(context.Background(), sink, eb, editFlushSpec[fakeEntity]{
		mutate: func(context.Context) (bool, syscall.Errno) { return true, 0 },
		writeBack: writeBackSpec[fakeEntity]{
			errKey: "k",
			fetch:  func(context.Context) (*fakeEntity, error) { return &fakeEntity{}, nil },
			compare: func(*fakeEntity) []writeBackResult {
				return []writeBackResult{{message: "Field: body\nError: reverted", fatal: true}}
			},
		},
		adopt:     func(*fakeEntity) {},
		coherence: []uint64{1},
		pinIno:    1,
	})
	if errno != syscall.EIO {
		t.Fatalf("errno = %v, want EIO (fatal divergence)", errno)
	}
	if eb.authored {
		t.Error("a fatal read-your-writes divergence armed authored — SYOW would mask the loss from a byte-count re-read")
	}
	if len(sink.pins) != 0 {
		t.Errorf("pins = %v on a fatal divergence, want none — the pin would hide the loss from the re-read", sink.pins)
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
