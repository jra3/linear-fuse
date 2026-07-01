package fs

// contentBuffer holds the in-memory byte content of a writable file node and owns
// *when* that content is materialized. It is the single home of the FUSE write/
// truncate/read mechanics that every writable node used to copy-paste.
//
// It is deliberately NOT self-synchronizing: every method assumes the caller holds
// the owning node's mutex — the same single lock that guards the node entity fields
// the loader reads (e.g. IssueFileNode.issue). go-fuse dispatches Write/Setattr/
// Flush for one inode on concurrent goroutines with no per-inode serialization, so
// that node lock is load-bearing; giving the buffer its own lock would split buffer
// state from the entity state the loader depends on.
//
// Three construction shapes:
//   - lazy:        set load, leave loaded false — content marshaled on first access
//     (Issue/Project/Initiative).
//   - eager:       set buf and loaded=true, no load — content computed in Lookup for
//     the entry size (Comment/Label/Doc/Milestone).
//   - write-only:  zero value (nil load) — starts empty (the _create nodes).
type contentBuffer struct {
	buf    []byte
	loaded bool
	dirty  bool
	load   func() ([]byte, error)
}

// ensureLoaded materializes buf on first access. A nil load means "starts empty":
// the buffer is simply marked loaded. Caller holds the node lock.
func (c *contentBuffer) ensureLoaded() error {
	if c.loaded {
		return nil
	}
	if c.load != nil {
		b, err := c.load()
		if err != nil {
			return err
		}
		c.buf = b
	}
	c.loaded = true
	return nil
}

// writeAt writes data at off, growing (zero-filling) the buffer as needed, and marks
// the buffer dirty. Returns the number of bytes written.
func (c *contentBuffer) writeAt(off int64, data []byte) (int, error) {
	if err := c.ensureLoaded(); err != nil {
		return 0, err
	}
	if newLen := int(off) + len(data); newLen > len(c.buf) {
		grown := make([]byte, newLen)
		copy(grown, c.buf)
		c.buf = grown
	}
	copy(c.buf[off:], data)
	c.dirty = true
	return len(data), nil
}

// truncate grows (zero-filling) or shrinks the buffer to size and marks it dirty.
func (c *contentBuffer) truncate(size int64) error {
	if err := c.ensureLoaded(); err != nil {
		return err
	}
	switch {
	case int(size) < len(c.buf):
		c.buf = c.buf[:size]
	case int(size) > len(c.buf):
		grown := make([]byte, size)
		copy(grown, c.buf)
		c.buf = grown
	}
	c.dirty = true
	return nil
}

// bytes returns the current content, materializing it if needed. The returned slice
// aliases the buffer; the caller must hold the node lock while reading it.
func (c *contentBuffer) bytes() ([]byte, error) {
	if err := c.ensureLoaded(); err != nil {
		return nil, err
	}
	return c.buf, nil
}

// size returns the content length, materializing content first. Because it loads
// before measuring, it can never report a premature 0 for a not-yet-loaded buffer —
// the invariant that retires the old per-node nil-guard drift.
func (c *contentBuffer) size() (int, error) {
	if err := c.ensureLoaded(); err != nil {
		return 0, err
	}
	return len(c.buf), nil
}

func (c *contentBuffer) isDirty() bool { return c.dirty }

// markClean clears the dirty flag but keeps the buffered content. Used by eager
// nodes after a successful write-back (their content is not re-derived).
func (c *contentBuffer) markClean() { c.dirty = false }

// invalidate drops materialized content and the dirty flag so the next access re-runs
// load against the freshly-persisted entity. Used by lazy nodes after write-back.
func (c *contentBuffer) invalidate() {
	c.buf = nil
	c.loaded = false
	c.dirty = false
}
