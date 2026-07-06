package fs

import (
	"context"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
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
