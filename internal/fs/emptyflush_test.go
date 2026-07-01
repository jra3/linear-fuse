package fs

import (
	"context"
	"testing"
)

// Closing a _create node without writing anything must be a silent no-op — not a
// spurious validation error on the sibling .error. Every write-only _create node
// shares this contract; these guard the two (label, document) that once regressed.

func TestNewLabelNode_FlushEmptyIsNoOp(t *testing.T) {
	t.Parallel()
	n := &NewLabelNode{} // no content written, not yet created
	if errno := n.Flush(context.Background(), nil); errno != 0 {
		t.Errorf("empty NewLabelNode.Flush errno = %v, want 0", errno)
	}
	if n.created {
		t.Error("empty flush must not mark the label created")
	}
}

func TestNewDocumentNode_FlushEmptyIsNoOp(t *testing.T) {
	t.Parallel()
	n := &NewDocumentNode{} // no content written, not yet created
	if errno := n.Flush(context.Background(), nil); errno != 0 {
		t.Errorf("empty NewDocumentNode.Flush errno = %v, want 0", errno)
	}
	if n.created {
		t.Error("empty flush must not mark the document created")
	}
}
