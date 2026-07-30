package fs

import (
	"sync"
	"time"
)

// Serve-your-own-writes across the atomic-save path (#379).
//
// #365 pins the bytes a client wrote into the editBuffer it wrote them through,
// so a write→verify re-read sees its own bytes instead of Linear's normalized
// render. That covers the in-place write path only, and editors don't use it:
// they write a sibling scratch file and rename(2) it over the canonical .md
// (atomicwrite.go, #145). On that path renameSave flushes the bytes through a
// TRANSIENT file node — issues.go/projects.go/initiatives.go build one just to
// run Flush — so editFlush arms `authored` on a buffer that is discarded a line
// later, and renameSave then deliberately drops the canonical file's inode so it
// re-Looks-up and re-renders what PERSISTED. The written bytes were therefore
// never served back, and any server-side reformat that changed the byte count
// reached the client as a size mismatch on a write that fully succeeded: editors
// report that as "the filesystem may have silently truncated the write" (#379).
//
// authoredPins carries the pin across the node boundary the rename path crosses.
// editFlush records the written bytes under the canonical file's inode on every
// committed clean write (#381), and a Lookup that builds that file seeds its
// editBuffer with them — marked authored, so a background refresh leaves them
// alone — instead of the fresh render.
//
// Arming it in editFlush rather than in renameSave is what keeps the two write
// paths from disagreeing. A pin is superseded by the next PinWritten for the same
// inode, so whichever path wrote LAST owns the pin; arming it only on the rename
// tail meant a later in-place edit left the older atomic-save bytes pinned, and a
// forget-and-re-Lookup inside the window served them — read-your-writes going
// backwards. It also generalises the guarantee: the in-place path's `authored`
// flag lives on the node (#365), so a forget loses it there too, and the pin is
// what both paths now survive one through.
//
// The pin is bounded by TIME, not by one Lookup: a client's verification is
// several syscalls (stat, then open+read), each of which can drive its own
// Lookup after the rename invalidation, and a pin consumed by the first would
// leave the second answering from the render again — which is the bug. Every
// Lookup inside the window therefore reads the same pinned bytes, and once the
// window closes reads converge to what Linear stored. pinTTL is what keeps a pin
// nobody ever looked up from becoming a stale read minutes later; it is a
// tighter bound than the in-place path's "until the next Open" (#365).
//
// The TTL remains the outer bound on how long a pin can outlive its truth — a
// write that lands in Linear and is then changed by someone else, say — but it is
// no longer what covers a newer local write: that supersedes the pin outright.
const pinTTL = 10 * time.Second

// authoredPin is one pinned write: the exact bytes a client last wrote to the
// file, and the deadline past which they must not be served.
type authoredPin struct {
	content  []byte
	deadline time.Time
}

// authoredPins is the mount-wide store of pinned writes, keyed by the canonical
// file's inode. Embedded in LinearFS (zero value ready to use), so PinWritten
// promotes onto it for the editFlushSink seam.
type authoredPins struct {
	mu   sync.Mutex
	pins map[uint64]authoredPin
}

// PinWritten records content as the bytes a client just wrote to the file at
// fileIno, for the next Lookup of that file to serve. It replaces any pin already
// standing for that inode, so the newest write wins. Called only on a committed
// clean write — see editFlush.
func (p *authoredPins) PinWritten(fileIno uint64, content []byte) {
	if len(content) == 0 {
		return
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pins == nil {
		p.pins = make(map[uint64]authoredPin)
	}
	// Sweep here rather than from a goroutine: pins are few (one per file being
	// edited) and a write is the only thing that grows the map, so the write is
	// also the right place to drop what expired unread.
	for ino, pin := range p.pins {
		if now.After(pin.deadline) {
			delete(p.pins, ino)
		}
	}
	p.pins[fileIno] = authoredPin{
		content:  append([]byte(nil), content...),
		deadline: now.Add(pinTTL),
	}
}

// writtenBytes returns the pinned write for fileIno, or nil when there is none or
// its window has closed. It does NOT consume the pin — every Lookup in the window
// must answer alike (see above) — but it does drop one that expired, so the store
// never holds bytes it can no longer serve.
func (p *authoredPins) writtenBytes(fileIno uint64) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	pin, ok := p.pins[fileIno]
	if !ok {
		return nil
	}
	if time.Now().After(pin.deadline) {
		delete(p.pins, fileIno)
		return nil
	}
	return pin.content
}

// seedAuthored fills a freshly-built editable node's buffer and reports the bytes
// its Lookup must publish as the file's size: the rendered content normally, or
// a pinned write when one is waiting for this inode. The two must be
// the same bytes — a Lookup that published the render's length while the buffer
// served the pin would clamp the client's own read to the wrong size, which is
// the very mismatch this module exists to remove.
//
// The buffer gets its own copy of the pin: the pin is not consumed, so every
// Lookup in the window is handed the same bytes, and editBuffer.Write mutates a
// buffer in place whenever the write fits — one node's edit would otherwise
// rewrite what the next Lookup serves.
func (p *authoredPins) seedAuthored(b *editBuffer, fileIno uint64, rendered []byte) []byte {
	pinned := p.writtenBytes(fileIno)
	b.mu.Lock()
	defer b.mu.Unlock()
	if pinned == nil {
		b.content = rendered
		return rendered
	}
	owned := append([]byte(nil), pinned...)
	b.content = owned
	b.authored = true
	return owned
}
