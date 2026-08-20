package fs

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
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
	eb := &editBuffer{content: []byte("the text the writer meant"), dirty: true}
	sink := &recordingFlushSink{}
	fetched := false
	restored := false
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
		mutate: func(context.Context) (bool, syscall.Errno) { return false, syscall.EINVAL },
		writeBack: writeBackSpec[fakeEntity]{
			errKey: "k",
			fetch:  func(context.Context) (*fakeEntity, error) { fetched = true; return &fakeEntity{}, nil },
		},
		adopt:     func(*fakeEntity) {},
		restore:   func() []byte { restored = true; return []byte("the entity's current render") },
		coherence: []uint64{1, 2},
	})
	if errno != syscall.EINVAL {
		t.Errorf("errno = %v, want EINVAL", errno)
	}
	if !eb.dirty {
		t.Error("dirty cleared on a front-half failure; a corrected re-save cannot retry")
	}
	// The restore is for the EMPTY-write rejection only. A parse/resolve/mutation
	// failure holds text the writer meant, and overwriting it with the server's
	// render would destroy the very document a corrected re-save edits.
	if restored || string(eb.content) != "the text the writer meant" {
		t.Errorf("front-half failure restored the buffer (called=%v, content=%q); the writer's text must survive",
			restored, eb.content)
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
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
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
	// A no-op is a SUCCESS, so it retires the entity's .error (#400). Every
	// other success path clears through commitWriteBack, which this branch
	// returns before reaching — so without the clear here, the reason a
	// PREVIOUS write was rejected outlives the corrected document: the writer
	// re-reads the file, saves it back unmodified, gets 0, and .error still
	// accuses a file that is now fine.
	if sink.clears != 1 {
		t.Errorf("ClearWriteError called %d times on a no-op flush, want 1 — a stale rejection would outlive the write that fixed it", sink.clears)
	}
}

func TestEditFlushProceedCommitsAdoptsInvalidates(t *testing.T) {
	t.Parallel()
	eb := dirtyBuffer()
	sink := &recordingFlushSink{}
	var adopted *fakeEntity
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
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
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
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
	// No shipped file is in this situation since #387 — all seven set pinIno, and
	// TestEverySpecSetsPinIno requires it. The shell must still honour zero as "do
	// not pin" for a future spec whose file is not built through newFileInode:
	// nothing would ever seed from that pin, so it would be bytes nobody can read,
	// held for the TTL.
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
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
// read a pin back the way a Lookup does (seedBuilt) instead of asserting on the
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
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
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
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
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
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
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
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
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
	errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
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

// TestEditFlushEmptiedFileIsRejected pins #397: a file truncated to zero bytes
// must not reach the front half. Before this, an empty issue.md parsed as "a
// document with no fields", which diffs against a populated issue as "remove all
// of them" — one measured write emitted assigneeId=nil, dueDate=nil,
// estimate=nil, labelIds=[] and description="" together. The write that caused it
// is the one a crashed editor or a botched Write tool call makes, so it has to
// fail loudly rather than apply.
func TestEditFlushEmptiedFileIsRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		content []byte
	}{
		{"zero bytes", []byte{}},
		{"newline only", []byte("\n")},
		{"whitespace only", []byte("  \n\t\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eb := &editBuffer{content: tc.content, dirty: true}
			sink := &recordingFlushSink{}
			called := false
			errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
				mutate:    func(context.Context) (bool, syscall.Errno) { called = true; return true, 0 },
				writeBack: writeBackSpec[fakeEntity]{errKey: "k", op: "save issue ENG-1"},
				adopt:     func(*fakeEntity) {},
				restore:   func() []byte { return []byte("the entity's current render") },
				coherence: []uint64{1},
				pinIno:    1,
			})
			if errno != syscall.EINVAL {
				t.Errorf("errno = %v, want EINVAL", errno)
			}
			if called {
				t.Error("front half ran on an emptied file; the mutation would clear every removable field")
			}
			if sink.sets != 1 {
				t.Errorf("SetWriteError called %d times, want 1 — the rejection must be legible in .error", sink.sets)
			}
			// The recovery the .error prescribes is "re-read the file to get its
			// current contents", and on the in-place path this buffer IS the file:
			// leaving it empty and dirty would strand the canonical node serving
			// zero bytes, since refresh refuses a dirty buffer and only a
			// successful flush clears the flag.
			if string(eb.content) != "the entity's current render" {
				t.Errorf("buffer = %q after a rejected empty write, want the entity's current render — "+
					"the .error tells the writer to re-read the file", eb.content)
			}
			if eb.dirty {
				t.Error("dirty left set after restoring the buffer; a background refresh would stay blocked forever")
			}
			if eb.authored || len(sink.pins) != 0 {
				t.Errorf("rejected write armed serve-your-own-writes (authored=%v pins=%v); nothing persisted",
					eb.authored, sink.pins)
			}
			if len(sink.invalidated) != 0 {
				t.Errorf("invalidated %v on a rejected write, want none", sink.invalidated)
			}
		})
	}
}

// TestEditFlushEmptyWriteWithoutRestoreLeavesBufferAlone pins the zero value.
// All seven shipped specs set restore, but the field is optional, and a spec
// that declines it (or a render that fails, which returns nil) must not end up
// with a buffer the shell half-rewrote: the rejection still stands, and the
// buffer is left exactly as the writer left it.
func TestEditFlushEmptyWriteWithoutRestoreLeavesBufferAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		restore func() []byte
	}{
		{"no restore closure", nil},
		{"render failed", func() []byte { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eb := &editBuffer{content: []byte{}, dirty: true}
			sink := &recordingFlushSink{}
			errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
				mutate:    func(context.Context) (bool, syscall.Errno) { return true, 0 },
				writeBack: writeBackSpec[fakeEntity]{errKey: "k", op: "save issue ENG-1"},
				adopt:     func(*fakeEntity) {},
				restore:   tc.restore,
				coherence: []uint64{1},
				pinIno:    1,
			})
			if errno != syscall.EINVAL {
				t.Errorf("errno = %v, want EINVAL — the rejection does not depend on the restore", errno)
			}
			if len(eb.content) != 0 || !eb.dirty {
				t.Errorf("buffer = %q (dirty=%v), want it untouched when there is nothing to restore from",
					eb.content, eb.dirty)
			}
		})
	}
}

// TestEmptyWriteMessageIsActionable: the .error an emptied file leaves must name
// the operation and tell the agent how to recover, since the file it is looking
// at is now the empty one it just wrote — the contents it needs are on the server.
func TestEmptyWriteMessageIsActionable(t *testing.T) {
	t.Parallel()
	msg := emptyWriteMessage("save issue ENG-1")
	for _, want := range []string{"save issue ENG-1", "Nothing was written", "re-read the file", "clear one field"} {
		if !strings.Contains(msg, want) {
			t.Errorf("empty-write .error does not mention %q:\n%s", want, msg)
		}
	}
	assertClearingAdviceIsPerSurface(t, msg)
}

// assertClearingAdviceIsPerSurface pins the #476 correction on both rejection
// messages: they are shared by all seven editable surfaces, so neither may
// assert ONE clearing mechanism. "Omit the key" is true on issue.md and false on
// labels/*.md, where an absent key means "leave that field alone" — a label
// writer who followed the old sentence got exactly the silent no-op these
// messages exist to prevent.
func assertClearingAdviceIsPerSurface(t *testing.T, msg string) {
	t.Helper()
	if strings.Contains(msg, "omit that field's key and keep the rest") {
		t.Errorf("rejection message still asserts one clearing mechanism for every surface:\n%s", msg)
	}
	for _, want := range []string{"issue.md omit the key", `labels/*.md write description: ""`} {
		if !strings.Contains(msg, want) {
			t.Errorf("rejection message does not name the per-surface clearing idiom %q:\n%s", want, msg)
		}
	}
}

// TestEditFlushNonEmptyContentStillFlushes guards the obvious over-reach: only a
// content-free file is rejected. A one-byte file, or frontmatter with an empty
// body, is a real edit and must go through.
func TestEditFlushNonEmptyContentStillFlushes(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"x", "---\ntitle: Keep\n---\n", "---\ntitle: Keep\n---\n\n"} {
		eb := &editBuffer{content: []byte(content), dirty: true}
		sink := &recordingFlushSink{}
		called := false
		errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
			mutate: func(context.Context) (bool, syscall.Errno) { called = true; return true, 0 },
			writeBack: writeBackSpec[fakeEntity]{
				errKey:  "k",
				op:      "save issue ENG-1",
				fetch:   func(context.Context) (*fakeEntity, error) { return &fakeEntity{}, nil },
				compare: func(*fakeEntity) []writeBackResult { return nil },
			},
			adopt:     func(*fakeEntity) {},
			coherence: []uint64{1},
		})
		if errno != 0 || !called {
			t.Errorf("content %q: errno=%v mutateCalled=%v, want 0/true — this is a real edit", content, errno, called)
		}
	}
}

func TestEditFlushCleanBufferIsNoOp(t *testing.T) {
	t.Parallel()
	sink := &recordingFlushSink{}
	called := false
	// dirty=false: the guard short-circuits before mutate.
	errno := editFlush(context.Background(), sink, &editBuffer{content: []byte("x")}, nil, editFlushSpec[fakeEntity]{
		mutate:    func(context.Context) (bool, syscall.Errno) { called = true; return true, 0 },
		coherence: []uint64{1},
	})
	if errno != 0 || called || len(sink.invalidated) != 0 {
		t.Errorf("clean buffer: errno=%v mutateCalled=%v invalidated=%v, want 0/false/none", errno, called, sink.invalidated)
	}
}

// TestClassifyWriteVerdicts is the whole rejection decision as a table. The NUL
// arm is #472: bytes.TrimSpace does not strip NUL, so a zero-filled buffer read
// as a document, and the mutation that followed sent a description of NUL bytes
// AND cleared assignee, due date, parent, project, milestone, cycle and labels
// in one shot — exit 0, no .error.
func TestClassifyWriteVerdicts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		content []byte
		want    writeVerdict
	}{
		{"zero bytes", []byte{}, writeIsEmpty},
		{"nil buffer", nil, writeIsEmpty},
		{"newline only", []byte("\n"), writeIsEmpty},
		{"whitespace only", []byte("  \n\t\n"), writeIsEmpty},
		// ftruncate(fd, 20) after an O_TRUNC: the whole file is hole.
		{"all NUL", bytes.Repeat([]byte{0}, 20), writeIsHole},
		// pwrite(fd, "hello", 10) after an O_TRUNC: hole, then real bytes.
		{"leading hole then text", append(bytes.Repeat([]byte{0}, 10), []byte("hello")...), writeIsHole},
		// A document whose frontmatter parses but whose body is zero-fill is the
		// same accident with a smaller blast radius, and NUL is not content here.
		{"hole after frontmatter", []byte("---\ntitle: Keep\n---\n\x00\x00\x00"), writeIsHole},
		{"trailing NUL", []byte("---\ntitle: Keep\n---\nbody\n\x00"), writeIsHole},
		{"one byte", []byte("x"), writeIsDocument},
		{"frontmatter with empty body", []byte("---\ntitle: Keep\n---\n"), writeIsDocument},
		{"body mentioning NUL by name", []byte("---\ntitle: Keep\n---\nthe \\0 sentinel\n"), writeIsDocument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyWrite(tc.content); got != tc.want {
				t.Errorf("classifyWrite(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// TestEditFlushZeroFilledWriteIsRejected pins #472 at the shell: a buffer
// carrying filesystem zero-fill must be refused on the same terms as an emptied
// one — EINVAL, no front half, a legible .error, the buffer restored, and no
// serve-your-own-writes arming. Measured before the fix, both of these reached
// Linear as a NUL description with every removable field cleared alongside it.
func TestEditFlushZeroFilledWriteIsRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		content []byte
	}{
		{"ftruncate grow", bytes.Repeat([]byte{0}, 20)},
		{"pwrite past EOF", append(bytes.Repeat([]byte{0}, 10), []byte("hello")...)},
		{"hole after frontmatter", []byte("---\ntitle: Keep\n---\n\x00\x00")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eb := &editBuffer{content: tc.content, dirty: true}
			sink := &recordingFlushSink{}
			called := false
			errno := editFlush(context.Background(), sink, eb, nil, editFlushSpec[fakeEntity]{
				mutate:    func(context.Context) (bool, syscall.Errno) { called = true; return true, 0 },
				writeBack: writeBackSpec[fakeEntity]{errKey: "k", op: "save issue ENG-1"},
				adopt:     func(*fakeEntity) {},
				restore:   func() []byte { return []byte("the entity's current render") },
				coherence: []uint64{1},
				pinIno:    1,
			})
			if errno != syscall.EINVAL {
				t.Errorf("errno = %v, want EINVAL", errno)
			}
			if called {
				t.Error("front half ran on a zero-filled file; the mutation would send a NUL description " +
					"and clear every removable field")
			}
			if sink.sets != 1 {
				t.Errorf("SetWriteError called %d times, want 1 — the rejection must be legible in .error", sink.sets)
			}
			if string(eb.content) != "the entity's current render" {
				t.Errorf("buffer = %q after a rejected zero-filled write, want the entity's current render", eb.content)
			}
			if eb.dirty {
				t.Error("dirty left set after restoring the buffer; a background refresh would stay blocked forever")
			}
			if eb.authored || len(sink.pins) != 0 {
				t.Errorf("rejected write armed serve-your-own-writes (authored=%v pins=%v); nothing persisted",
					eb.authored, sink.pins)
			}
			if len(sink.invalidated) != 0 {
				t.Errorf("invalidated %v on a rejected write, want none", sink.invalidated)
			}
		})
	}
}

// TestHoleWriteMessageNamesTheCause: the two rejections share a recovery but not
// a diagnosis. A writer who reads "empty write" after a pwrite at offset 10 has
// been told the wrong thing — the file was not empty, the offset was wrong — so
// the zero-fill verdict has to name the hole and say to write from offset 0.
func TestHoleWriteMessageNamesTheCause(t *testing.T) {
	t.Parallel()
	msg := rejectedWriteMessage(writeIsHole, "save issue ENG-1")
	for _, want := range []string{
		"Zero-filled write rejected", "save issue ENG-1", "NUL", "past the end of the file",
		"Nothing was written", "offset 0", "clear one field",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("zero-fill .error does not mention %q:\n%s", want, msg)
		}
	}
	assertClearingAdviceIsPerSurface(t, msg)
	if got := rejectedWriteMessage(writeIsEmpty, "save issue ENG-1"); !strings.Contains(got, "Empty write rejected") {
		t.Errorf("empty verdict rendered the wrong message:\n%s", got)
	}
}

// #454 at the seam the bug actually lives on: editFlush's empty-write restore
// meeting editBuffer.Write.
//
// A `-d` trace of a live mount shows the kernel's sequence for a shell `>`
// redirect is OPEN, SETATTR(size 0), FLUSH, WRITE, FLUSH — the shell emits that
// middle FLUSH by closing a duplicated descriptor. The restore (#397) answers it
// by putting the entity's render back, so the write that follows used to land at
// offset 0 of the resurrected image and the closing flush persisted the splice:
// the old description's tail survived, and the previous frontmatter leaked into
// the body.
//
// The two subtests are the two halves of the contract, and a fix that trades one
// for the other is what this pair exists to catch: the redirect's own write must
// re-apply the truncation, and a LATER writer's must not.
func TestEditFlushRestoreAndTheWritesThatFollow(t *testing.T) {
	t.Parallel()
	const render = "---\ntitle: \"Truncate probe\"\n---\nAAAA BBBB CCCC DDDD EEEE FFFF GGGG\n"

	t.Run("the truncating writer's own write is still truncated", func(t *testing.T) {
		t.Parallel()
		eb := &editBuffer{content: []byte(render)}
		sink := &recordingFlushSink{}
		spec := editFlushSpec[fakeEntity]{
			mutate: func(context.Context) (bool, syscall.Errno) { return true, 0 },
			writeBack: writeBackSpec[fakeEntity]{
				errKey:  "k",
				op:      "save issue ENG-1",
				fetch:   func(context.Context) (*fakeEntity, error) { return &fakeEntity{v: 1}, nil },
				compare: func(*fakeEntity) []writeBackResult { return nil },
			},
			adopt:     func(*fakeEntity) {},
			restore:   func() []byte { return []byte(render) },
			coherence: []uint64{1},
			pinIno:    1,
		}

		// OPEN — the handle the whole redirect runs on.
		h, _, _ := eb.Open(context.Background(), 0)

		// SETATTR(size 0) — the O_TRUNC of the redirect. Measured against a live
		// mount, this one carries NO file handle, which is why the pending
		// truncation cannot be armed here.
		zero := &fuse.SetAttrIn{}
		zero.Valid = fuse.FATTR_SIZE
		zero.Size = 0
		eb.Setattr(context.Background(), nil, zero, &fuse.AttrOut{})

		// FLUSH from the dup'd descriptor closing. The rejection still stands and
		// the buffer is still restored — reads must keep working.
		if errno := editFlush(context.Background(), sink, eb, h, spec); errno != syscall.EINVAL {
			t.Fatalf("intervening flush errno = %v, want EINVAL (the empty-write rejection is unchanged)", errno)
		}
		if string(eb.content) != render {
			t.Fatalf("intervening flush left content = %q, want the entity's render restored", eb.content)
		}

		// WRITE — the redirect's actual payload, shorter than the render, on the
		// handle the restore was made for.
		const short = "---\ntitle: \"SHORT\"\n---\nSHORT\n"
		eb.Write(context.Background(), h, []byte(short), 0)
		if string(eb.content) != short {
			t.Errorf("buffer after the write = %q, want exactly %q — the restored image spliced through", eb.content, short)
		}

		// FLUSH — what the closing descriptor would persist.
		var sent string
		spec.mutate = func(context.Context) (bool, syscall.Errno) { sent = string(eb.content); return true, 0 }
		if errno := editFlush(context.Background(), sink, eb, h, spec); errno != 0 {
			t.Fatalf("closing flush errno = %v, want 0", errno)
		}
		if sent != short {
			t.Errorf("front half saw %q, want %q — the splice would have reached Linear", sent, short)
		}
	})

	t.Run("a later writer's write is not", func(t *testing.T) {
		t.Parallel()
		// The `> file` above is abandoned without ever writing (`: > file`), so the
		// rejection stands and the restore is all that is left behind. The NEXT
		// writer — a plain `>>` on a fresh open — truncated nothing, and clearing
		// the buffer under it would send Linear a document of NUL bytes whose
		// missing frontmatter also nils every removable field.
		eb := &editBuffer{content: []byte(render)}
		sink := &recordingFlushSink{}
		spec := editFlushSpec[fakeEntity]{
			mutate: func(context.Context) (bool, syscall.Errno) { return true, 0 },
			writeBack: writeBackSpec[fakeEntity]{
				errKey:  "k",
				op:      "save issue ENG-1",
				fetch:   func(context.Context) (*fakeEntity, error) { return &fakeEntity{v: 1}, nil },
				compare: func(*fakeEntity) []writeBackResult { return nil },
			},
			adopt:     func(*fakeEntity) {},
			restore:   func() []byte { return []byte(render) },
			coherence: []uint64{1},
			pinIno:    1,
		}

		truncator, _, _ := eb.Open(context.Background(), 0)
		zero := &fuse.SetAttrIn{}
		zero.Valid = fuse.FATTR_SIZE
		zero.Size = 0
		eb.Setattr(context.Background(), nil, zero, &fuse.AttrOut{})
		if errno := editFlush(context.Background(), sink, eb, truncator, spec); errno != syscall.EINVAL {
			t.Fatalf("intervening flush errno = %v, want EINVAL", errno)
		}
		// …and that writer goes away without writing anything.

		appender, _, _ := eb.Open(context.Background(), 0)
		const tail = "\nAppended line.\n"
		eb.Write(context.Background(), appender, []byte(tail), int64(len(render)))

		want := render + tail
		if string(eb.content) != want {
			t.Errorf("buffer after the append = %q, want %q — the abandoned truncate followed the wrong writer", eb.content, want)
		}
		if i := bytes.IndexByte(eb.content, 0); i >= 0 {
			t.Errorf("buffer carries a NUL byte at %d; the appended document was written over a cleared buffer", i)
		}

		var sent string
		spec.mutate = func(context.Context) (bool, syscall.Errno) { sent = string(eb.content); return true, 0 }
		if errno := editFlush(context.Background(), sink, eb, appender, spec); errno != 0 {
			t.Fatalf("closing flush errno = %v, want 0", errno)
		}
		if sent != want {
			t.Errorf("front half saw %q, want %q", sent, want)
		}
	})

	t.Run("the zero-fill rejection's restore is attributed too", func(t *testing.T) {
		t.Parallel()
		// The zero-fill verdict (#472) restores the render exactly as the empty one
		// does, so it has to arm the same mark: its .error tells the writer to
		// "write the WHOLE document back from offset 0", and on the SAME open fd a
		// document shorter than the restored render would otherwise keep the
		// render's tail — #454's splice, reached by following the instructions.
		eb := &editBuffer{content: []byte(render)}
		sink := &recordingFlushSink{}
		spec := editFlushSpec[fakeEntity]{
			mutate: func(context.Context) (bool, syscall.Errno) { return true, 0 },
			writeBack: writeBackSpec[fakeEntity]{
				errKey:  "k",
				op:      "save issue ENG-1",
				fetch:   func(context.Context) (*fakeEntity, error) { return &fakeEntity{v: 1}, nil },
				compare: func(*fakeEntity) []writeBackResult { return nil },
			},
			adopt:     func(*fakeEntity) {},
			restore:   func() []byte { return []byte(render) },
			coherence: []uint64{1},
			pinIno:    1,
		}

		h, _, _ := eb.Open(context.Background(), 0)

		// O_TRUNC, then a pwrite past EOF: the buffer is hole + bytes.
		zero := &fuse.SetAttrIn{}
		zero.Valid = fuse.FATTR_SIZE
		zero.Size = 0
		eb.Setattr(context.Background(), nil, zero, &fuse.AttrOut{})
		eb.Write(context.Background(), h, []byte("hello"), 10)

		if errno := editFlush(context.Background(), sink, eb, h, spec); errno != syscall.EINVAL {
			t.Fatalf("flush of a zero-filled buffer errno = %v, want EINVAL", errno)
		}
		if string(eb.content) != render {
			t.Fatalf("rejected zero-filled write left content = %q, want the entity's render restored", eb.content)
		}

		// The prescribed recovery, on the same descriptor: the whole document from
		// offset 0, shorter than the render.
		const short = "---\ntitle: \"SHORT\"\n---\nSHORT\n"
		eb.Write(context.Background(), h, []byte(short), 0)
		if string(eb.content) != short {
			t.Errorf("buffer after the corrective write = %q, want exactly %q — the restored image spliced through",
				eb.content, short)
		}
	})
}
