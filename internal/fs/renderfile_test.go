package fs

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// newTestRenderNode builds a bare renderFileNode over a render closure, with a
// zero-value BaseNode (uid/gid 0). Enough to exercise the FUSE surface directly —
// no mount, repo, or API.
func newTestRenderNode(render renderFn) *renderFileNode {
	return &renderFileNode{render: render}
}

func readAll(t *testing.T, n *renderFileNode) string {
	t.Helper()
	dest := make([]byte, 4096)
	res, errno := n.Read(context.Background(), nil, dest, 0)
	if errno != 0 {
		t.Fatalf("Read errno=%v", errno)
	}
	b, status := res.Bytes(dest)
	if !status.Ok() {
		t.Fatalf("Read status=%v", status)
	}
	return string(b)
}

// TestRenderFileNodeReadThrough guards the load-bearing invariant: a render node
// serves *current* content on every Read, never bytes baked at construction.
// go-fuse dedups inodes by StableAttr.Ino, so a stale baked-bytes node would be
// served for the life of the mount after the first lookup. This is the general
// form of the old #148 meta regression, now covering all nine render files.
func TestRenderFileNodeReadThrough(t *testing.T) {
	current := "id: X\nupdated: T1\n"
	mtime := time.Unix(1000, 0)
	ctime := time.Unix(500, 0)
	node := newTestRenderNode(func(context.Context) ([]byte, time.Time, time.Time) {
		return []byte(current), mtime, ctime
	})

	if got := readAll(t, node); got != current {
		t.Fatalf("initial read = %q, want %q", got, current)
	}

	// The underlying source changes (e.g. issue.md edited, updated: bumped; or a
	// cycle's wall-clock status flipped current->completed).
	current = "id: X\nupdated: T2-and-longer\n"
	mtime = time.Unix(2000, 0)
	if got := readAll(t, node); got != current {
		t.Fatalf("read-through failed: got %q, want current render output %q "+
			"(render node is serving stale baked bytes)", got, current)
	}

	// Times render through too — a baked mtime would freeze at first Lookup and
	// break the mtime=updatedAt contract for the file that exposes updated:.
	var out fuse.AttrOut
	if errno := node.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr errno=%v", errno)
	}
	if out.Mtime != uint64(mtime.Unix()) {
		t.Fatalf("Getattr mtime = %d, want %d (mtime baked, not rendered through)", out.Mtime, mtime.Unix())
	}
	if out.Ctime != uint64(ctime.Unix()) {
		t.Fatalf("Getattr ctime = %d, want %d", out.Ctime, ctime.Unix())
	}
	if out.Size != uint64(len(current)) {
		t.Fatalf("Getattr size = %d, want %d", out.Size, len(current))
	}
	// atime is collapsed to mtime (decorative field, see renderfile.go).
	if out.Atime != out.Mtime {
		t.Fatalf("Getattr atime = %d, want atime==mtime (%d)", out.Atime, out.Mtime)
	}
}

// TestRenderFileNodeVolatility is the regression that this whole module exists to
// enforce: content that changes between reads is observed on the *next* read. A
// node that returned FOPEN_KEEP_CACHE (the old CycleFileNode bug) would let the
// kernel serve the first page forever; DIRECT_IO forces the re-read. We assert the
// Open flag directly since the kernel isn't in the loop here.
func TestRenderFileNodeVolatility(t *testing.T) {
	calls := 0
	node := newTestRenderNode(func(context.Context) ([]byte, time.Time, time.Time) {
		calls++
		return []byte("render #" + itoa(calls)), time.Unix(int64(calls), 0), time.Unix(0, 0)
	})

	if got := readAll(t, node); got != "render #1" {
		t.Fatalf("first read = %q, want render #1", got)
	}
	if got := readAll(t, node); got != "render #2" {
		t.Fatalf("second read = %q, want render #2 — render node cached instead of re-rendering", got)
	}

	// The Open flag is what tells the kernel never to cache: must be DIRECT_IO,
	// never KEEP_CACHE.
	_, flags, errno := node.Open(context.Background(), 0)
	if errno != 0 {
		t.Fatalf("Open errno=%v", errno)
	}
	if flags&fuse.FOPEN_DIRECT_IO == 0 {
		t.Fatalf("Open flags = %#x, want FOPEN_DIRECT_IO set", flags)
	}
	if flags&fuse.FOPEN_KEEP_CACHE != 0 {
		t.Fatalf("Open flags = %#x, must not set FOPEN_KEEP_CACHE (staleness bug)", flags)
	}
}

// TestRenderFileNodeReadClamps checks the byte-window arithmetic: offset past EOF
// yields empty, a partial tail is clamped to content length.
func TestRenderFileNodeReadClamps(t *testing.T) {
	body := "0123456789"
	node := newTestRenderNode(func(context.Context) ([]byte, time.Time, time.Time) {
		return []byte(body), time.Unix(1, 0), time.Unix(1, 0)
	})

	// Offset past EOF -> empty, no error.
	res, errno := node.Read(context.Background(), nil, make([]byte, 4), int64(len(body)+5))
	if errno != 0 {
		t.Fatalf("past-EOF Read errno=%v", errno)
	}
	b, _ := res.Bytes(make([]byte, 4))
	if len(b) != 0 {
		t.Fatalf("past-EOF read = %q, want empty", b)
	}

	// Partial tail: read 4 bytes from offset 8 -> only "89" available.
	dest := make([]byte, 4)
	res, errno = node.Read(context.Background(), nil, dest, 8)
	if errno != 0 {
		t.Fatalf("tail Read errno=%v", errno)
	}
	b, _ = res.Bytes(dest)
	if string(b) != "89" {
		t.Fatalf("tail read = %q, want %q", b, "89")
	}
}

// TestRenderFileNodeRejectsWrite: read-only. Opening for write is EACCES.
func TestRenderFileNodeRejectsWrite(t *testing.T) {
	node := newTestRenderNode(func(context.Context) ([]byte, time.Time, time.Time) {
		return []byte("x"), time.Unix(1, 0), time.Unix(1, 0)
	})
	if _, _, errno := node.Open(context.Background(), uint32(syscall.O_WRONLY)); errno != syscall.EACCES {
		t.Fatalf("O_WRONLY Open errno=%v, want EACCES", errno)
	}
	if _, _, errno := node.Open(context.Background(), uint32(syscall.O_RDWR)); errno != syscall.EACCES {
		t.Fatalf("O_RDWR Open errno=%v, want EACCES", errno)
	}
}

// itoa avoids importing strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
