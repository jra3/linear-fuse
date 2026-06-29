package fs

import (
	"context"
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
