package fs

import (
	"context"
	"testing"
	"time"
)

// TestAuthoredPins_ServesEveryLookupInTheWindow is the core contract, and the
// reason the pin is not consume-once: a client's verification is several syscalls
// (stat, then open+read), each able to drive its own Lookup after the rename
// invalidation. A pin the first Lookup consumed would leave the second answering
// from the render again — exactly the mismatch this fixes.
func TestAuthoredPins_ServesEveryLookupInTheWindow(t *testing.T) {
	var p authoredPins
	p.PinWritten(42, []byte("written bytes"))

	for i, want := range []string{"written bytes", "written bytes", "written bytes"} {
		if got := string(p.writtenBytes(42)); got != want {
			t.Errorf("lookup %d served %q, want the pinned write %q", i+1, got, want)
		}
	}
}

func TestAuthoredPins_UnpinnedInodes(t *testing.T) {
	var p authoredPins
	// Zero value, no map allocated yet: a read must not panic.
	if got := p.writtenBytes(1); got != nil {
		t.Fatalf("read from an empty store = %q, want nil", got)
	}
	p.PinWritten(1, []byte("one"))
	if got := p.writtenBytes(2); got != nil {
		t.Errorf("read of an unpinned inode = %q, want nil (pins are per-file)", got)
	}
	// An empty write pins nothing: there would be no size mismatch to fix, and a
	// zero-length pin is indistinguishable from "no pin" downstream.
	p.PinWritten(3, nil)
	if got := p.writtenBytes(3); got != nil {
		t.Errorf("read of an empty write = %q, want nil", got)
	}
}

// TestAuthoredPins_LatestWriteWins: a second save inside the window supersedes
// the first, so a verify never reads the previous body back.
func TestAuthoredPins_LatestWriteWins(t *testing.T) {
	var p authoredPins
	p.PinWritten(9, []byte("first save"))
	p.PinWritten(9, []byte("second save"))

	if got := string(p.writtenBytes(9)); got != "second save" {
		t.Errorf("pinned bytes = %q, want the most recent write", got)
	}
}

// TestAuthoredPins_ExpiredPinIsNotServed guards the staleness bound: a pin no
// Lookup ever consumed must not be served later, when Linear's stored body may
// have moved on.
func TestAuthoredPins_ExpiredPinIsNotServed(t *testing.T) {
	var p authoredPins
	p.PinWritten(7, []byte("stale write"))

	// Age the pin past its deadline without sleeping.
	p.mu.Lock()
	pin := p.pins[7]
	pin.deadline = time.Now().Add(-time.Second)
	p.pins[7] = pin
	p.mu.Unlock()

	if got := p.writtenBytes(7); got != nil {
		t.Errorf("read of an expired pin = %q, want nil", got)
	}
	p.mu.Lock()
	_, still := p.pins[7]
	p.mu.Unlock()
	if still {
		t.Error("an expired pin must be dropped when read, not left holding bytes")
	}
}

// TestAuthoredPins_PinSweepsExpired keeps the store from growing without bound
// when writes are never followed by a Lookup.
func TestAuthoredPins_PinSweepsExpired(t *testing.T) {
	var p authoredPins
	p.PinWritten(1, []byte("first"))

	p.mu.Lock()
	pin := p.pins[1]
	pin.deadline = time.Now().Add(-time.Second)
	p.pins[1] = pin
	p.mu.Unlock()

	p.PinWritten(2, []byte("second"))

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.pins[1]; ok {
		t.Error("a later write must sweep pins that expired unread")
	}
	if _, ok := p.pins[2]; !ok {
		t.Error("the fresh pin must survive the sweep")
	}
}

// TestAuthoredPins_PinCopiesContent: renameSave hands over the scratch node's
// live buffer, which the node may reuse or a later writer may overwrite. The pin
// must own its bytes.
func TestAuthoredPins_PinCopiesContent(t *testing.T) {
	var p authoredPins
	content := []byte("original")
	p.PinWritten(5, content)
	copy(content, "MANGLED")

	if got := string(p.writtenBytes(5)); got != "original" {
		t.Errorf("pinned bytes = %q, want an unaliased copy of the write", got)
	}
}

// TestSeedAuthored_PinnedWriteWinsOverRender is the seeding half of the fix: the
// buffer AND the size the Lookup publishes both come from the pin, and the buffer
// is marked authored so a background refresh cannot replace the client's bytes
// before it re-reads them.
func TestSeedAuthored_PinnedWriteWinsOverRender(t *testing.T) {
	var p authoredPins
	written := []byte("body the client wrote  ") // trailing space Linear strips
	rendered := []byte("body the client wrote")  // what persisted, 2 bytes shorter
	p.PinWritten(11, written)

	var b editBuffer
	served := p.seedAuthored(&b, 11, rendered)

	if string(served) != string(written) {
		t.Errorf("published size bytes = %q, want the pinned write %q", served, written)
	}
	if string(b.content) != string(written) {
		t.Errorf("buffer = %q, want the pinned write %q", b.content, written)
	}
	if !b.authored {
		t.Error("a pin-seeded buffer must be authored, or a refresh can drop the client's bytes mid-verify")
	}
}

// TestSeedAuthored_BufferWriteCannotCorruptThePin: the pin is not consumed, so a
// node seeded from it must own its bytes — editBuffer.Write rewrites a buffer in
// place whenever the write fits, and the next Lookup in the same window still has
// to serve the bytes the client actually saved.
func TestSeedAuthored_BufferWriteCannotCorruptThePin(t *testing.T) {
	var p authoredPins
	written := []byte("body the client wrote")
	p.PinWritten(13, written)

	var first editBuffer
	p.seedAuthored(&first, 13, []byte("what Linear stored"))
	if _, errno := first.Write(context.Background(), nil, []byte("MANGLED"), 0); errno != 0 {
		t.Fatalf("write through the seeded buffer = %v, want 0", errno)
	}

	var second editBuffer
	served := p.seedAuthored(&second, 13, []byte("what Linear stored"))
	if string(served) != string(written) || string(second.content) != string(written) {
		t.Errorf("second lookup served %q / buffer %q, want the pinned write %q",
			served, second.content, written)
	}
}

func TestSeedAuthored_NoPinServesTheRender(t *testing.T) {
	var p authoredPins
	rendered := []byte("what Linear stored")

	var b editBuffer
	served := p.seedAuthored(&b, 12, rendered)

	if string(served) != string(rendered) || string(b.content) != string(rendered) {
		t.Errorf("served %q / buffer %q, want the render %q", served, b.content, rendered)
	}
	if b.authored {
		t.Error("an ordinary Lookup must not be authored — later reads have to converge to what persisted")
	}
}
