package fs

import (
	"context"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// TestEditBufferWriteExpands covers a write past the current end: the buffer
// grows to fit and the tail before the offset stays zero-filled.
func TestEditBufferWriteExpands(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("hello")}

	n, errno := b.Write(context.Background(), nil, []byte("X"), 10)
	if errno != 0 || n != 1 {
		t.Fatalf("Write = (%d, %d), want (1, 0)", n, errno)
	}
	if b.size() != 11 {
		t.Errorf("size = %d, want 11", b.size())
	}
	if !b.dirty {
		t.Error("write did not mark the buffer dirty")
	}
	if got := b.content[10]; got != 'X' {
		t.Errorf("content[10] = %q, want X", got)
	}
	if b.content[5] != 0 {
		t.Errorf("gap byte content[5] = %d, want 0", b.content[5])
	}
}

// TestEditBufferWriteInPlace overwrites within the existing length without
// growing.
func TestEditBufferWriteInPlace(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("hello")}
	b.Write(context.Background(), nil, []byte("A"), 1)
	if string(b.content) != "hAllo" {
		t.Errorf("content = %q, want hAllo", b.content)
	}
	if b.size() != 5 {
		t.Errorf("size = %d, want 5 (no growth)", b.size())
	}
}

// TestEditBufferTruncateBufferClearsStaleTail covers the #289 O_TRUNC path for a
// collectionDir overwrite Create: truncateBuffer empties the buffer (dirty), so a
// subsequent shorter write leaves no stale tail from the prior content.
func TestEditBufferTruncateBufferClearsStaleTail(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("hello world")}

	b.truncateBuffer()
	if b.size() != 0 {
		t.Fatalf("size after truncateBuffer = %d, want 0", b.size())
	}
	if !b.dirty {
		t.Error("truncateBuffer did not mark the buffer dirty")
	}

	// A shorter rewrite must produce exactly the new bytes, not overlay a stale tail.
	b.Write(context.Background(), nil, []byte("hi"), 0)
	dest := make([]byte, 32)
	res, errno := b.Read(context.Background(), nil, dest, 0)
	if errno != 0 {
		t.Fatalf("Read errno = %d", errno)
	}
	got, _ := res.Bytes(dest)
	if string(got) != "hi" {
		t.Errorf("Read after truncate+rewrite = %q, want \"hi\" (no stale tail)", got)
	}
}

// TestEditBufferTruncate covers Setattr shrinking and growing the buffer.
func TestEditBufferTruncate(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("hello world")}

	shrink := &fuse.SetAttrIn{}
	shrink.Valid = fuse.FATTR_SIZE
	shrink.Size = 5
	var out fuse.AttrOut
	if errno := b.Setattr(context.Background(), nil, shrink, &out); errno != 0 {
		t.Fatalf("Setattr shrink errno = %d", errno)
	}
	if string(b.content) != "hello" {
		t.Errorf("after shrink content = %q, want hello", b.content)
	}
	if out.Size != 5 {
		t.Errorf("Setattr out.Size = %d, want 5", out.Size)
	}

	grow := &fuse.SetAttrIn{}
	grow.Valid = fuse.FATTR_SIZE
	grow.Size = 8
	b.Setattr(context.Background(), nil, grow, &out)
	if b.size() != 8 {
		t.Errorf("after grow size = %d, want 8", b.size())
	}
	if !b.dirty {
		t.Error("truncate did not mark the buffer dirty")
	}
}

// TestEditBufferRefreshSkipsDirty pins the dirty-buffer-wins rule: a background
// refresh never clobbers an in-flight edit, and the entity swap is skipped too.
func TestEditBufferRefreshSkipsDirty(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("written"), dirty: true}

	swapped := false
	b.refresh([]byte("normalized"), func() { swapped = true })

	if string(b.content) != "written" {
		t.Errorf("dirty buffer content = %q, want the in-flight edit %q", b.content, "written")
	}
	if swapped {
		t.Error("entity swap ran on a dirty buffer; content and entity must stay paired")
	}
}

// TestEditBufferRefreshSkipsAuthored is the serve-your-own-writes guard (#365):
// a just-authored buffer (clean, but holding the exact bytes the user wrote)
// refuses a background refresh so a client's read-back == what-it-wrote across
// the write→verify window. The entity swap is skipped too, keeping the
// (written-content, normalized-entity) pair Flush installed.
func TestEditBufferRefreshSkipsAuthored(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("written"), authored: true}

	swapped := false
	b.refresh([]byte("normalized"), func() { swapped = true })

	if string(b.content) != "written" {
		t.Errorf("authored buffer content = %q, want the just-written %q", b.content, "written")
	}
	if swapped {
		t.Error("entity swap ran on an authored buffer; the post-flush pair must stay intact")
	}
}

// TestEditBufferOpenClearsAuthored pins that a fresh Open ends the SYOW window:
// once cleared, the next background refresh adopts the normalized bytes so
// independent later readers converge to what actually persisted.
func TestEditBufferOpenClearsAuthored(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("written"), authored: true}

	if _, _, errno := b.Open(context.Background(), 0); errno != 0 {
		t.Fatalf("Open errno = %d", errno)
	}
	if b.authored {
		t.Fatal("Open did not clear the authored flag")
	}

	// With authored cleared, a refresh now adopts the normalized content.
	swapped := false
	b.refresh([]byte("normalized"), func() { swapped = true })
	if string(b.content) != "normalized" {
		t.Errorf("after Open+refresh content = %q, want the converged %q", b.content, "normalized")
	}
	if !swapped {
		t.Error("entity swap skipped after the authored flag was cleared")
	}
}

// TestEditBufferAuthoredThenWriteStaysDirty covers a genuine re-edit inside the
// authored window: a Write re-dirties the buffer, and dirty (not authored) then
// governs — the buffer still refuses a refresh, but for the in-flight edit.
func TestEditBufferAuthoredThenWriteStaysDirty(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("written"), authored: true}

	b.Write(context.Background(), nil, []byte("Z"), 0)
	if !b.dirty {
		t.Error("re-edit inside the authored window did not mark the buffer dirty")
	}

	swapped := false
	b.refresh([]byte("normalized"), func() { swapped = true })
	if swapped || string(b.content) == "normalized" {
		t.Error("refresh clobbered an in-flight re-edit inside the authored window")
	}
}

// TestIssueFileNodeAuthoredServesWrittenSize wires the SYOW guard (#365) through
// a real editable node: IssueFileNode.refreshFrom → editBuffer.refresh. It is the
// deterministic stand-in for the mount-level write→refresh→verify race — a
// kernel revalidation (refreshFrom) with a divergent fresh twin must NOT shrink
// the served buffer while authored, so a client's stat/read-back still sees the
// exact bytes it wrote. The reported symptom is a byte-count mismatch, so the
// assertion is on size(). After the verify read's Open ends the window, the next
// refresh converges to the normalized twin.
func TestIssueFileNodeAuthoredServesWrittenSize(t *testing.T) {
	t.Parallel()
	written := []byte("the exact bytes the client wrote — deliberately longer than N")
	kept := &IssueFileNode{
		issue:      api.Issue{ID: "I1", Title: "written"},
		editBuffer: editBuffer{content: append([]byte(nil), written...), authored: true},
	}
	fresh := &IssueFileNode{
		issue:      api.Issue{ID: "I1", Title: "normalized"},
		editBuffer: editBuffer{content: []byte("N")},
	}

	// Background refresh while authored: served size stays W, entity stays paired.
	kept.refreshFrom(fresh)
	if kept.size() != len(written) {
		t.Errorf("authored issue.md served size = %d, want %d (a byte-count verifier would trip)", kept.size(), len(written))
	}
	if kept.issue.Title != "written" {
		t.Errorf("authored refresh swapped the entity: title = %q, want it unchanged", kept.issue.Title)
	}

	// The verify read's Open ends the window; the next refresh converges to N.
	kept.Open(context.Background(), 0)
	kept.refreshFrom(fresh)
	if kept.size() != 1 {
		t.Errorf("post-Open served size = %d, want 1 (converged to the normalized render)", kept.size())
	}
	if kept.issue.Title != "normalized" {
		t.Errorf("post-Open entity title = %q, want normalized (converged)", kept.issue.Title)
	}
}

// TestEditBufferRead slices at an offset and clamps at EOF.
func TestEditBufferRead(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("hello world")}

	res, errno := b.Read(context.Background(), nil, make([]byte, 4), 6)
	if errno != 0 {
		t.Fatalf("Read errno = %d", errno)
	}
	got, _ := res.Bytes(make([]byte, 4))
	if string(got) != "worl" {
		t.Errorf("Read at 6 = %q, want worl", got)
	}

	// A dest larger than the remaining bytes clamps to EOF.
	res, _ = b.Read(context.Background(), nil, make([]byte, 100), 6)
	got, _ = res.Bytes(make([]byte, 100))
	if string(got) != "world" {
		t.Errorf("Read at 6 (large dest) = %q, want world", got)
	}

	// An offset at or past EOF yields no bytes.
	res, _ = b.Read(context.Background(), nil, make([]byte, 4), 11)
	got, _ = res.Bytes(make([]byte, 4))
	if len(got) != 0 {
		t.Errorf("Read at EOF = %q, want empty", got)
	}
}

// TestEditBufferWriteDropsRestoredContent pins the consuming half of the #454
// fix. A shell `>` redirect emits OPEN, SETATTR(size 0), FLUSH, WRITE, FLUSH:
// the middle flush is rejected as an empty write and restores the entity's
// render, and the write that follows belongs to the truncate, not to those
// bytes. Overwriting a prefix of them is exactly the corruption #454 reports.
func TestEditBufferWriteDropsRestoredContent(t *testing.T) {
	t.Parallel()
	b := &editBuffer{
		content:          []byte("---\ntitle: Old\n---\nthe entity's long previous body\n"),
		restoredForReads: true,
	}

	const short = "---\ntitle: New\n---\nshort\n"
	n, errno := b.Write(context.Background(), nil, []byte(short), 0)
	if errno != 0 {
		t.Fatalf("Write errno = %v", errno)
	}
	if int(n) != len(short) {
		t.Errorf("wrote %d bytes, want %d", n, len(short))
	}
	if string(b.content) != short {
		t.Errorf("buffer = %q, want exactly the written bytes — restored content must not survive under them", b.content)
	}
	if b.restoredForReads {
		t.Error("flag still set after the write consumed it; a second write would wrongly clear the buffer")
	}
	if !b.dirty {
		t.Error("write left the buffer clean")
	}
}

// TestEditBufferWriteKeepsOrdinaryContent is the control: without the mark, a
// write is an ordinary in-place edit and must not clear anything. This is what
// separates the fix from "truncate on every write".
func TestEditBufferWriteKeepsOrdinaryContent(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("0123456789")}

	if _, errno := b.Write(context.Background(), nil, []byte("ABC"), 2); errno != 0 {
		t.Fatalf("Write errno = %v", errno)
	}
	if got, want := string(b.content), "01ABC56789"; got != want {
		t.Errorf("buffer = %q, want %q — an unmarked write is an in-place edit", got, want)
	}
}

// TestEditBufferRestoredMarkSurvivesOpenAndRefresh pins the lifetime rule, and
// it is the inverse of what it looks like it should be. The mark describes the
// CONTENT, not the handle that produced it, so an unrelated opener or a
// background refresh must not clear it.
//
// Clearing on either reopens #454 in full: the window between the intervening
// flush and the write is exactly when a reader can `cat` the file, and the
// rejection sets dirty = false, which makes the buffer refresh-eligible right
// then. Either one unmarks the buffer and the pending write splices itself onto
// the old image.
func TestEditBufferRestoredMarkSurvivesOpenAndRefresh(t *testing.T) {
	t.Parallel()

	t.Run("a reader's open does not clear it", func(t *testing.T) {
		t.Parallel()
		b := &editBuffer{content: []byte("the entity's current render"), restoredForReads: true}

		if _, _, errno := b.Open(context.Background(), 0); errno != 0 {
			t.Fatalf("Open errno = %v", errno)
		}
		if !b.restoredForReads {
			t.Fatal("open cleared the mark; a cat racing a `>` would reopen #454")
		}

		if _, errno := b.Write(context.Background(), nil, []byte("XX"), 0); errno != 0 {
			t.Fatalf("Write errno = %v", errno)
		}
		if got := string(b.content); got != "XX" {
			t.Errorf("buffer = %q, want %q — the interrupted truncate still owns this write", got, "XX")
		}
	})

	t.Run("a background refresh does not clear it", func(t *testing.T) {
		t.Parallel()
		b := &editBuffer{content: []byte("the entity's current render"), restoredForReads: true}

		swapped := false
		b.refresh([]byte("a newer render from the sync worker"), func() { swapped = true })
		if !swapped || string(b.content) != "a newer render from the sync worker" {
			t.Fatalf("refresh did not adopt (swapped=%v content=%q)", swapped, b.content)
		}
		if !b.restoredForReads {
			t.Fatal("refresh cleared the mark; the adopted render is equally nobody's write")
		}

		if _, errno := b.Write(context.Background(), nil, []byte("YY"), 0); errno != 0 {
			t.Fatalf("Write errno = %v", errno)
		}
		if got := string(b.content); got != "YY" {
			t.Errorf("buffer = %q, want %q", got, "YY")
		}
	})

	t.Run("an explicit resize clears it", func(t *testing.T) {
		t.Parallel()
		b := &editBuffer{content: []byte("0123456789"), restoredForReads: true}

		in := &fuse.SetAttrIn{}
		in.Size = 4
		in.Valid = fuse.FATTR_SIZE
		if errno := b.Setattr(context.Background(), nil, in, &fuse.AttrOut{}); errno != 0 {
			t.Fatalf("Setattr errno = %v", errno)
		}
		if b.restoredForReads {
			t.Error("resize left the mark set; the writer has spoken and owns the buffer again")
		}
		if _, errno := b.Write(context.Background(), nil, []byte("AB"), 4); errno != 0 {
			t.Fatalf("Write errno = %v", errno)
		}
		if got, want := string(b.content), "0123AB"; got != want {
			t.Errorf("buffer = %q, want %q — an in-place write after a resize must not clear", got, want)
		}
	})
}
