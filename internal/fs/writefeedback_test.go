package fs

import "testing"

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
