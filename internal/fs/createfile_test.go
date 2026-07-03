package fs

import (
	"context"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// newTestCreateFile returns a createFileNode whose onFlush records what it
// receives, plus the recorder.
func newTestCreateFile(errno syscall.Errno) (*createFileNode, *[][]byte) {
	var got [][]byte
	node := newCreateFile(nil, func(ctx context.Context, content []byte) syscall.Errno {
		got = append(got, content)
		return errno
	})
	return node, &got
}

func TestCreateFileWriteThenFlush(t *testing.T) {
	t.Parallel()
	node, got := newTestCreateFile(0)
	ctx := context.Background()

	fh, _, errno := node.Open(ctx, 0)
	if errno != 0 {
		t.Fatalf("Open() = %v", errno)
	}
	if _, errno := node.Write(ctx, fh, []byte("hello "), 0); errno != 0 {
		t.Fatalf("Write() = %v", errno)
	}
	if _, errno := node.Write(ctx, fh, []byte("world"), 6); errno != 0 {
		t.Fatalf("Write() = %v", errno)
	}
	if errno := node.Flush(ctx, fh); errno != 0 {
		t.Fatalf("Flush() = %v", errno)
	}
	if len(*got) != 1 || string((*got)[0]) != "hello world" {
		t.Errorf("onFlush got %q, want one call with %q", *got, "hello world")
	}
}

func TestCreateFileEmptyFlushSkipsOnFlush(t *testing.T) {
	t.Parallel()
	node, got := newTestCreateFile(0)
	ctx := context.Background()

	fh, _, _ := node.Open(ctx, 0)
	if errno := node.Flush(ctx, fh); errno != 0 {
		t.Fatalf("Flush() = %v", errno)
	}
	if errno := node.Flush(ctx, nil); errno != 0 {
		t.Fatalf("Flush(nil handle) = %v", errno)
	}
	if len(*got) != 0 {
		t.Errorf("onFlush called %d times for empty flushes, want 0", len(*got))
	}
}

func TestCreateFileDoubleFlushCreatesOnce(t *testing.T) {
	t.Parallel()
	node, got := newTestCreateFile(0)
	ctx := context.Background()

	fh, _, _ := node.Open(ctx, 0)
	_, _ = node.Write(ctx, fh, []byte("payload"), 0)
	if errno := node.Flush(ctx, fh); errno != 0 {
		t.Fatalf("first Flush() = %v", errno)
	}
	// A dup'd descriptor flushes the same handle again: consumed buffer, no-op.
	if errno := node.Flush(ctx, fh); errno != 0 {
		t.Fatalf("second Flush() = %v", errno)
	}
	if len(*got) != 1 {
		t.Errorf("onFlush called %d times, want 1", len(*got))
	}
}

func TestCreateFileSecondOpenCreatesAgain(t *testing.T) {
	t.Parallel()
	node, got := newTestCreateFile(0)
	ctx := context.Background()

	// The kernel reuses the cached inode for a second open-write-close cycle;
	// each cycle's fresh handle must produce its own create. (The old
	// node-level buffers latched `created` and silently swallowed this.)
	for _, payload := range []string{"first", "second"} {
		fh, _, _ := node.Open(ctx, 0)
		_, _ = node.Write(ctx, fh, []byte(payload), 0)
		if errno := node.Flush(ctx, fh); errno != 0 {
			t.Fatalf("Flush(%q) = %v", payload, errno)
		}
	}
	if len(*got) != 2 || string((*got)[0]) != "first" || string((*got)[1]) != "second" {
		t.Errorf("onFlush got %q, want [first second]", *got)
	}
}

func TestCreateFileFlushErrnoPropagates(t *testing.T) {
	t.Parallel()
	node, got := newTestCreateFile(syscall.EINVAL)
	ctx := context.Background()

	fh, _, _ := node.Open(ctx, 0)
	_, _ = node.Write(ctx, fh, []byte("bad input"), 0)
	if errno := node.Flush(ctx, fh); errno != syscall.EINVAL {
		t.Fatalf("Flush() = %v, want EINVAL", errno)
	}
	if len(*got) != 1 {
		t.Fatalf("onFlush called %d times, want 1", len(*got))
	}
	// The buffer was consumed by the failed attempt; a retry needs a new
	// open-write-close cycle, not a re-flush of the dead handle.
	if errno := node.Flush(ctx, fh); errno != 0 {
		t.Errorf("Flush() after failure = %v, want 0 (consumed buffer)", errno)
	}
}

func TestCreateFileReadDenied(t *testing.T) {
	t.Parallel()
	node, _ := newTestCreateFile(0)
	if _, errno := node.Read(context.Background(), nil, make([]byte, 8), 0); errno != syscall.EACCES {
		t.Errorf("Read() = %v, want EACCES", errno)
	}
}

func TestCreateFileWriteWrongHandle(t *testing.T) {
	t.Parallel()
	node, _ := newTestCreateFile(0)
	if _, errno := node.Write(context.Background(), nil, []byte("x"), 0); errno != syscall.EIO {
		t.Errorf("Write(nil handle) = %v, want EIO", errno)
	}
}

func TestCreateFileSetattrTruncatesHandle(t *testing.T) {
	t.Parallel()
	node, got := newTestCreateFile(0)
	ctx := context.Background()

	fh, _, _ := node.Open(ctx, 0)
	_, _ = node.Write(ctx, fh, []byte("stale content"), 0)
	// The O_TRUNC of a `>` redirect arrives as Setattr(size=0) on the handle.
	in := &fuse.SetAttrIn{}
	in.Valid = fuse.FATTR_SIZE
	in.Size = 0
	if errno := node.Setattr(ctx, fh, in, &fuse.AttrOut{}); errno != 0 {
		t.Fatalf("Setattr() = %v", errno)
	}
	_, _ = node.Write(ctx, fh, []byte("fresh"), 0)
	if errno := node.Flush(ctx, fh); errno != 0 {
		t.Fatalf("Flush() = %v", errno)
	}
	if len(*got) != 1 || string((*got)[0]) != "fresh" {
		t.Errorf("onFlush got %q, want [fresh]", *got)
	}
}
