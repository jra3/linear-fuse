package fs

import (
	"strings"
	"testing"
	"time"
)

// TestWriteFeedbackInvalidateSeam exercises the store in isolation — no LinearFS,
// no mount — through its one dependency, the invalidate seam. It proves setting
// and clearing an error, and appending a success, each drop the right inode, and
// that a no-op clear/append does not.
func TestWriteFeedbackInvalidateSeam(t *testing.T) {
	t.Parallel()
	var dropped []uint64
	wf := newWriteFeedback(func(ino uint64) { dropped = append(dropped, ino) })

	// Set → stored and the .error inode is dropped.
	wf.SetWriteError("ENT-1", "boom")
	if e := wf.GetWriteError("ENT-1"); e == nil || e.Message != "boom" {
		t.Fatalf("GetWriteError = %+v, want message boom", e)
	}
	if len(dropped) != 1 || dropped[0] != errorIno("ENT-1") {
		t.Fatalf("set dropped = %v, want [errorIno(ENT-1)]", dropped)
	}

	// Clear a present error drops again; clearing an absent one does not.
	wf.ClearWriteError("ENT-1")
	wf.ClearWriteError("ENT-absent")
	if len(dropped) != 2 || dropped[1] != errorIno("ENT-1") {
		t.Fatalf("clear dropped = %v, want one more errorIno(ENT-1)", dropped)
	}

	// Append success drops the .last inode for the collection key.
	key := collectionSuccessKey("issues", "team-1")
	wf.AppendWriteSuccess(key, WriteResult{Identifier: "TST-1"})
	if got := wf.GetWriteSuccess(key); len(got) != 1 || got[0].Identifier != "TST-1" {
		t.Fatalf("GetWriteSuccess = %+v, want one TST-1", got)
	}
	if len(dropped) != 3 || dropped[2] != successIno(key) {
		t.Fatalf("append dropped = %v, want successIno(key)", dropped)
	}

	// Append failure drops the same .last inode, is excluded from GetWriteSuccess,
	// but is present in the full GetWriteOutcomes log (#370).
	wf.AppendWriteFailure(key, "boom")
	if len(dropped) != 4 || dropped[3] != successIno(key) {
		t.Fatalf("append-failure dropped = %v, want one more successIno(key)", dropped)
	}
	if got := wf.GetWriteSuccess(key); len(got) != 1 {
		t.Fatalf("GetWriteSuccess = %+v, want still one (failure not counted as success)", got)
	}
	if got := wf.GetWriteOutcomes(key); len(got) != 2 {
		t.Fatalf("GetWriteOutcomes len = %d, want 2 (success + failure)", len(got))
	}
}

// TestWriteFeedbackNilInvalidate: a nil seam degrades to a no-op, so a bare
// store needs no server.
func TestWriteFeedbackNilInvalidate(t *testing.T) {
	t.Parallel()
	wf := newWriteFeedback(nil)
	wf.SetWriteError("X", "msg") // must not panic
	if wf.GetWriteError("X") == nil {
		t.Error("SetWriteError did not store with a nil invalidate seam")
	}
}

// TestRenderWriteErrorCarriesTime pins the #476 addition: an agent reads .error
// with cat, not stat, so the timestamp that was already the file's mtime has to
// be IN the bytes. A collection .error is retired only by the next successful
// write to that directory, so a sticky one is only datable if the content says
// when it was recorded.
func TestRenderWriteErrorCarriesTime(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 8, 19, 9, 41, 7, 0, time.UTC)
	got := string(renderWriteError(&WriteError{Message: "Field: parent\nError: unknown field.", Timestamp: stamp}))

	if !strings.HasPrefix(got, "Field: parent\nError: unknown field.\n") {
		t.Errorf("rendered .error does not lead with the message:\n%s", got)
	}
	line, ok := strings.CutPrefix(strings.TrimSuffix(got, "\n"), "Field: parent\nError: unknown field.\nTime: ")
	if !ok {
		t.Fatalf("rendered .error carries no Time: line:\n%s", got)
	}
	parsed, err := time.Parse(time.RFC3339, line)
	if err != nil {
		t.Fatalf("Time: line %q is not RFC3339: %v", line, err)
	}
	if !parsed.Equal(stamp) {
		t.Errorf("Time: line = %v, want the recorded timestamp %v", parsed, stamp)
	}

	// Absolute, never a computed "x ago": the rendered length must be identical
	// between two reads of the same error, or the attr-cached size disagrees with
	// the content.
	if second := string(renderWriteError(&WriteError{Message: "Field: parent\nError: unknown field.", Timestamp: stamp})); second != got {
		t.Errorf("two renders of the same error differ:\n%q\n%q", got, second)
	}

	// A zero timestamp (an error minted without SetWriteError) renders no line
	// rather than a meaningless year 1.
	if bare := string(renderWriteError(&WriteError{Message: "boom"})); bare != "boom\n" {
		t.Errorf("unstamped error rendered %q, want %q", bare, "boom\n")
	}
}
