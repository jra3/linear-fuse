package fs

import (
	"context"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// writeScratch applies a Write at the given offset and fails the test on error.
func writeScratch(t *testing.T, n *scratchFileNode, off int64, data string) {
	t.Helper()
	got, errno := n.Write(context.Background(), nil, []byte(data), off)
	if errno != 0 {
		t.Fatalf("Write(off=%d) errno=%v", off, errno)
	}
	if int(got) != len(data) {
		t.Fatalf("Write(off=%d) wrote %d, want %d", off, got, len(data))
	}
}

func TestScratchFileNode_WriteRead(t *testing.T) {
	t.Parallel()
	n := &scratchFileNode{}

	// Sequential writes accumulate.
	writeScratch(t, n, 0, "hello ")
	writeScratch(t, n, 6, "world")
	if got := string(n.bytes()); got != "hello world" {
		t.Fatalf("bytes() = %q, want %q", got, "hello world")
	}

	// An offset write past the end grows the buffer (zero-filled gap).
	n2 := &scratchFileNode{}
	writeScratch(t, n2, 3, "abc")
	if got := n2.bytes(); len(got) != 6 || string(got[3:]) != "abc" {
		t.Fatalf("offset write = %q, want 3 zero bytes then \"abc\"", got)
	}

	// Read returns the requested window.
	dest := make([]byte, 5)
	res, errno := n.Read(context.Background(), nil, dest, 6)
	if errno != 0 {
		t.Fatalf("Read errno=%v", errno)
	}
	out, _ := res.Bytes(dest)
	if string(out) != "world" {
		t.Fatalf("Read(off=6) = %q, want %q", out, "world")
	}

	// Read past EOF yields no data, not an error.
	if _, errno := n.Read(context.Background(), nil, dest, 100); errno != 0 {
		t.Fatalf("Read past EOF errno=%v", errno)
	}
}

func TestScratchFileNode_Truncate(t *testing.T) {
	t.Parallel()
	n := &scratchFileNode{}
	writeScratch(t, n, 0, "hello world")

	// Shrink via Setattr size.
	var in fuse.SetAttrIn
	in.Size = 5
	in.Valid = fuse.FATTR_SIZE
	var out fuse.AttrOut
	if errno := n.Setattr(context.Background(), nil, &in, &out); errno != 0 {
		t.Fatalf("Setattr truncate errno=%v", errno)
	}
	if got := string(n.bytes()); got != "hello" {
		t.Fatalf("after truncate to 5: %q, want %q", got, "hello")
	}
	if out.Size != 5 {
		t.Fatalf("Setattr out.Size = %d, want 5", out.Size)
	}

	// O_TRUNC-style truncate to 0 clears the buffer so a save-over replaces
	// rather than appends.
	in.Size = 0
	if errno := n.Setattr(context.Background(), nil, &in, &out); errno != 0 {
		t.Fatalf("Setattr truncate-to-0 errno=%v", errno)
	}
	if got := n.bytes(); len(got) != 0 {
		t.Fatalf("after truncate to 0: %q, want empty", got)
	}
}

// TestScratchFileNode_ConsumedRejectsOpen: after a rename has taken the scratch
// buffer, go-fuse may leave this spent node serving the canonical file's name
// until the rename invalidation lands. Opening it must fail loud with ESTALE so
// the kernel re-Lookups the real node, not resolve to a dead buffer that
// silently accepts writes it will never persist.
func TestScratchFileNode_ConsumedRejectsOpen(t *testing.T) {
	t.Parallel()
	n := &scratchFileNode{}

	// Fresh: Open succeeds — the editor writes the temp file before renaming it.
	if _, _, errno := n.Open(context.Background(), 0); errno != 0 {
		t.Fatalf("fresh Open errno=%v, want 0", errno)
	}

	n.consume()

	if _, _, errno := n.Open(context.Background(), 0); errno != syscall.ESTALE {
		t.Fatalf("consumed Open errno=%v, want ESTALE", errno)
	}
}

// TestScratchFileNode_ConsumedRejectsFlushAndFsync: the flush/fsync of a write
// that lands on a spent scratch node must fail loud, never the old silent
// return-0 that reported a dropped write as a clean save.
func TestScratchFileNode_ConsumedRejectsFlushAndFsync(t *testing.T) {
	t.Parallel()
	n := &scratchFileNode{}

	// Fresh: the editor's own close/fsync of the temp file must succeed.
	if errno := n.Flush(context.Background(), nil); errno != 0 {
		t.Fatalf("fresh Flush errno=%v, want 0", errno)
	}
	if errno := n.Fsync(context.Background(), nil, 0); errno != 0 {
		t.Fatalf("fresh Fsync errno=%v, want 0", errno)
	}

	n.consume()

	if errno := n.Flush(context.Background(), nil); errno != syscall.ESTALE {
		t.Fatalf("consumed Flush errno=%v, want ESTALE", errno)
	}
	if errno := n.Fsync(context.Background(), nil, 0); errno != syscall.ESTALE {
		t.Fatalf("consumed Fsync errno=%v, want ESTALE", errno)
	}
}

// TestScratchFileNode_ConsumedRejectsWrite: a write held on an fd opened before
// the rename consumed the node must not be accepted into a buffer that will
// never persist — it fails loud so the caller learns the write was not stored.
func TestScratchFileNode_ConsumedRejectsWrite(t *testing.T) {
	t.Parallel()
	n := &scratchFileNode{}
	n.consume()

	if _, errno := n.Write(context.Background(), nil, []byte("x"), 0); errno != syscall.ESTALE {
		t.Fatalf("consumed Write errno=%v, want ESTALE", errno)
	}
	var in fuse.SetAttrIn
	in.Size = 0
	in.Valid = fuse.FATTR_SIZE
	var out fuse.AttrOut
	if errno := n.Setattr(context.Background(), nil, &in, &out); errno != syscall.ESTALE {
		t.Fatalf("consumed Setattr errno=%v, want ESTALE", errno)
	}
}

func TestScratchIno_StableAndDistinct(t *testing.T) {
	t.Parallel()
	const parentA, parentB = uint64(111), uint64(222)

	// Stable: same parent+name => same ino across calls.
	first := scratchIno(parentA, "issue.md.tmp.1")
	if second := scratchIno(parentA, "issue.md.tmp.1"); first != second {
		t.Errorf("scratchIno not stable: %d != %d", first, second)
	}
	// Distinct by name within a directory (concurrent temp files must not collide).
	if scratchIno(parentA, "a.tmp") == scratchIno(parentA, "b.tmp") {
		t.Error("scratchIno collided for distinct names in the same directory")
	}
	// Distinct by parent (same temp name in two issue dirs must not collide).
	if scratchIno(parentA, "issue.md.tmp.1") == scratchIno(parentB, "issue.md.tmp.1") {
		t.Error("scratchIno collided for the same name across directories")
	}
}
