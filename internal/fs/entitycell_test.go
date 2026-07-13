package fs

import (
	"sync"
	"testing"
)

// entityCell's whole job is that the volatile-state lock is unforgettable — a
// node embeds it and inherits the guarded accessors instead of hand-writing
// them. These pin the round-trip and prove the lock holds under concurrency
// (run the package with -race); a node type embeds the cell, so the promoted
// accessors are exercised exactly as the real nodes use them.

func TestEntityCellRoundTrip(t *testing.T) {
	t.Parallel()
	var c entityCell[string]

	// Zero value reads the zero entity.
	if got := c.entity(); got != "" {
		t.Errorf("zero cell entity() = %q, want empty", got)
	}

	c.setEntity("first")
	if got := c.entity(); got != "first" {
		t.Errorf("after setEntity: entity() = %q, want %q", got, "first")
	}

	// A later swap (the nodeRefresher path) replaces it.
	c.setEntity("second")
	if got := c.entity(); got != "second" {
		t.Errorf("after re-set: entity() = %q, want %q", got, "second")
	}
}

// refreshCarrier embeds the cell exactly as a real node does, so refreshFrom's
// setEntity(f.entity()) shape is what we test.
type refreshCarrier struct {
	entityCell[int]
}

func (r *refreshCarrier) refreshFrom(fresh *refreshCarrier) {
	r.setEntity(fresh.entity())
}

func TestEntityCellRefreshFromShape(t *testing.T) {
	t.Parallel()
	old := &refreshCarrier{}
	old.setEntity(1)
	fresh := &refreshCarrier{}
	fresh.setEntity(42)

	old.refreshFrom(fresh)
	if got := old.entity(); got != 42 {
		t.Errorf("refreshFrom did not adopt the fresh entity: got %d, want 42", got)
	}
}

func TestEntityCellConcurrentAccess(t *testing.T) {
	t.Parallel()
	var c entityCell[int]
	var wg sync.WaitGroup

	// Concurrent setEntity/entity — the -race detector proves the lock serializes
	// what the copy-pasted per-node dance used to (and a new node could forget).
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(v int) { defer wg.Done(); c.setEntity(v) }(i)
		go func() { defer wg.Done(); _ = c.entity() }()
	}
	wg.Wait()

	// Whatever landed last, the cell is readable and consistent.
	_ = c.entity()
}
