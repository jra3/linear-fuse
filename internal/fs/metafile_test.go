package fs

import (
	"context"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// TestMetaFileNodeReadThrough guards the blocker the naive review caught: a
// `.meta` renderFile must render current content on every Read, not serve bytes
// baked at construction time. go-fuse dedups inodes by StableAttr.Ino, so a stale
// baked-bytes node would be served for the life of the mount after the first
// lookup. This test fails if the render-through property ever regresses.
func TestMetaFileNodeReadThrough(t *testing.T) {
	current := "id: X\nupdated: T1\n"
	mtime := time.Unix(1000, 0)
	node := &renderFile{render: func(context.Context) ([]byte, time.Time, time.Time) {
		return []byte(current), mtime, time.Unix(500, 0)
	}}

	read := func() string {
		dest := make([]byte, 256)
		res, errno := node.Read(context.Background(), nil, dest, 0)
		if errno != 0 {
			t.Fatalf("Read errno=%v", errno)
		}
		b, status := res.Bytes(dest)
		if !status.Ok() {
			t.Fatalf("Read status=%v", status)
		}
		return string(b)
	}

	if got := read(); got != current {
		t.Fatalf("initial read = %q, want %q", got, current)
	}

	// The underlying source changes (e.g. issue.md was edited, updated: bumped).
	current = "id: X\nupdated: T2-and-longer\n"
	mtime = time.Unix(2000, 0)
	if got := read(); got != current {
		t.Fatalf("read-through failed: got %q, want current render output %q "+
			"(MetaFileNode is serving stale baked bytes — the #148 meta regression)", got, current)
	}

	// Times must render through too — a baked mtime would freeze at first Lookup
	// and break the mtime=updatedAt contract for the file that exposes updated:.
	var out fuse.AttrOut
	if errno := node.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr errno=%v", errno)
	}
	if out.Mtime != uint64(mtime.Unix()) {
		t.Fatalf("Getattr mtime = %d, want %d (mtime is baked, not rendered through)", out.Mtime, mtime.Unix())
	}
	if out.Size != uint64(len(current)) {
		t.Fatalf("Getattr size = %d, want %d", out.Size, len(current))
	}
}
