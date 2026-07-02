package fs

import (
	"context"
	"testing"
)

// TestMetaFileNodeReadThrough guards the blocker the naive review caught: a
// MetaFileNode must render current content on every Read, not serve bytes baked
// at construction time. go-fuse dedups inodes by StableAttr.Ino, so a stale
// baked-bytes node would be served for the life of the mount after the first
// lookup. This test fails if MetaFileNode ever regresses to holding fixed bytes.
func TestMetaFileNodeReadThrough(t *testing.T) {
	current := "id: X\nupdated: T1\n"
	node := &MetaFileNode{render: func() []byte { return []byte(current) }}

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
	if got := read(); got != current {
		t.Fatalf("read-through failed: got %q, want current render output %q "+
			"(MetaFileNode is serving stale baked bytes — the #148 meta regression)", got, current)
	}
}
