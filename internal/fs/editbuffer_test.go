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

// TestEditBufferTruncatingWriteAtOffsetIgnoresTheRestoredImage covers the
// pending truncation at a non-zero offset (#454): the truncation makes the file
// logically empty, so the truncating handle's later write at offset N must
// produce exactly N zero bytes plus the data — never the bytes an intervening
// restore left sitting in the buffer under it.
func TestEditBufferTruncatingWriteAtOffsetIgnoresTheRestoredImage(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("hello world")}
	h, _, _ := b.Open(context.Background(), 0)

	zero := &fuse.SetAttrIn{}
	zero.Valid = fuse.FATTR_SIZE
	zero.Size = 0
	if errno := b.Setattr(context.Background(), h, zero, &fuse.AttrOut{}); errno != 0 {
		t.Fatalf("Setattr truncate errno = %d", errno)
	}
	if b.size() != 0 {
		t.Fatalf("size after the truncate = %d, want 0", b.size())
	}

	// The intervening flush: content comes back for reads, attributed to the
	// handle that emptied the buffer.
	b.content = []byte("hello world")
	b.markRestored(h)

	b.Write(context.Background(), h, []byte("hi"), 4)
	if string(b.content) != "\x00\x00\x00\x00hi" {
		t.Errorf("content = %q, want four zero bytes then \"hi\" — the restored image must not show through", b.content)
	}

	// Consumed: a second write on the same handle edits in place.
	b.Write(context.Background(), h, []byte("!"), 6)
	if string(b.content) != "\x00\x00\x00\x00hi!" {
		t.Errorf("content = %q — the write did not consume the pending truncation", b.content)
	}
}

// TestEditBufferRestoreDoesNotFollowTheWriterOffTheHandle is the other half of
// #454, and the reason the pending truncation is scoped to the file handle.
//
// A `: > issue.md` truncates, gets the empty write rejected (#397), and is
// restored — then goes away without ever writing. The NEXT writer opens fresh and
// appends. It truncated nothing, so clearing the buffer under it would send
// Linear a document of NUL bytes; and because that document does not start with
// `---`, marshal.Parse reads it as body with EMPTY frontmatter, so the same
// mutation also nils assignee, due, parent, project, milestone, cycle and labels.
// A buffer-scoped mark does exactly that, measured at the mount.
func TestEditBufferRestoreDoesNotFollowTheWriterOffTheHandle(t *testing.T) {
	t.Parallel()
	const image = "---\ntitle: \"probe\"\n---\nBODY\n"
	b := &editBuffer{content: []byte(image)}

	truncator, _, _ := b.Open(context.Background(), 0)
	zero := &fuse.SetAttrIn{}
	zero.Valid = fuse.FATTR_SIZE
	zero.Size = 0
	b.Setattr(context.Background(), truncator, zero, &fuse.AttrOut{})
	b.content = []byte(image) // the rejection restores it
	b.markRestored(truncator)

	appender, _, _ := b.Open(context.Background(), 0)
	b.Write(context.Background(), appender, []byte("MORE\n"), int64(len(image)))

	if want := image + "MORE\n"; string(b.content) != want {
		t.Errorf("content = %q, want %q — an abandoned truncate clipped a later writer's file", b.content, want)
	}
}

// TestEditBufferOpenHandlesAreDistinct pins the property the whole handle
// scoping rests on. editHandle must never become a zero-size struct: Go gives
// every allocation of one the same address, so two opens would compare equal and
// the pending truncation would silently go back to being buffer-scoped — the
// #454 regression, with all the tests still passing.
func TestEditBufferOpenHandlesAreDistinct(t *testing.T) {
	t.Parallel()
	b := &editBuffer{content: []byte("x")}
	first, _, _ := b.Open(context.Background(), 0)
	second, _, _ := b.Open(context.Background(), 0)
	if first == second {
		t.Fatal("two opens returned the same handle — is editHandle a zero-size struct?")
	}
	b.markRestored(first)
	if b.takeTruncation(second) {
		t.Error("a second handle consumed the first handle's pending truncation")
	}
	if !b.takeTruncation(first) {
		t.Error("the handle the restore was attributed to did not carry it")
	}
}

// TestEditBufferSetattrNonZeroUnderPendingTruncateSizesTheEmptyFile: an
// ftruncate(2) to a non-zero size after the restore must size the LOGICALLY
// EMPTY file — all zero bytes — not the image the restore put back. Sizing the
// restored render instead would hand the following flush the old body to persist,
// with the pending truncation now spent and nothing left to correct it.
func TestEditBufferSetattrNonZeroUnderPendingTruncateSizesTheEmptyFile(t *testing.T) {
	t.Parallel()
	const image = "---\ntitle: \"probe\"\n---\nSECRET-OLD-BODY\n"
	b := &editBuffer{content: []byte(image)}
	h, _, _ := b.Open(context.Background(), 0)

	zero := &fuse.SetAttrIn{}
	zero.Valid = fuse.FATTR_SIZE
	zero.Size = 0
	b.Setattr(context.Background(), h, zero, &fuse.AttrOut{})
	b.content = []byte(image) // the rejection restores it
	b.markRestored(h)

	grow := &fuse.SetAttrIn{}
	grow.Valid = fuse.FATTR_SIZE
	grow.Size = uint64(len(image) + 8)
	b.Setattr(context.Background(), h, grow, &fuse.AttrOut{})

	if len(b.content) != len(image)+8 {
		t.Fatalf("size after the resize = %d, want %d", len(b.content), len(image)+8)
	}
	for i, c := range b.content {
		if c != 0 {
			t.Fatalf("byte %d = %q; the resize sized the restored image, not the emptied file: %q", i, c, b.content)
		}
	}

	// Spent: the resize IS the file's new length, so a following write edits it.
	b.Write(context.Background(), h, []byte("hi"), 0)
	if len(b.content) != len(image)+8 || string(b.content[:2]) != "hi" {
		t.Errorf("content after the write = %q, want the sized file with \"hi\" at 0", b.content)
	}
}
