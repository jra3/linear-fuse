package fs

import (
	"errors"
	"testing"
)

// A write-only buffer (nil loader) starts empty and grows on write.
func TestContentBuffer_WriteOnlyGrows(t *testing.T) {
	var c contentBuffer
	n, err := c.writeAt(0, []byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("writeAt = (%d,%v), want (5,nil)", n, err)
	}
	if !c.isDirty() {
		t.Error("buffer should be dirty after write")
	}
	got, _ := c.bytes()
	if string(got) != "hello" {
		t.Errorf("bytes = %q, want %q", got, "hello")
	}
}

// Writing past the current end zero-fills the gap.
func TestContentBuffer_WriteAtOffsetZeroFills(t *testing.T) {
	var c contentBuffer
	if _, err := c.writeAt(3, []byte("X")); err != nil {
		t.Fatal(err)
	}
	got, _ := c.bytes()
	want := []byte{0, 0, 0, 'X'}
	if string(got) != string(want) {
		t.Errorf("bytes = %v, want %v", got, want)
	}
}

// Truncate grows (zero-fill) and shrinks.
func TestContentBuffer_Truncate(t *testing.T) {
	var c contentBuffer
	_, _ = c.writeAt(0, []byte("abcdef"))

	if err := c.truncate(3); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.bytes(); string(got) != "abc" {
		t.Errorf("after shrink bytes = %q, want %q", got, "abc")
	}

	if err := c.truncate(5); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.size(); got != 5 {
		t.Errorf("after grow size = %d, want 5", got)
	}
	if got, _ := c.bytes(); string(got) != "abc\x00\x00" {
		t.Errorf("after grow bytes = %q, want %q", got, "abc\x00\x00")
	}
}

// The load hook runs at most once, on first access — not at construction.
func TestContentBuffer_LazyLoadsOnce(t *testing.T) {
	calls := 0
	c := contentBuffer{load: func() ([]byte, error) {
		calls++
		return []byte("loaded"), nil
	}}
	if calls != 0 {
		t.Fatalf("loader ran at construction (%d calls)", calls)
	}

	got, _ := c.bytes()
	if string(got) != "loaded" {
		t.Errorf("bytes = %q, want %q", got, "loaded")
	}
	if _, _ = c.size(); calls != 1 {
		t.Errorf("loader ran %d times across two reads, want 1", calls)
	}
}

// The bug this module retires: size() on an unloaded buffer must materialize
// content first, never report a premature 0.
func TestContentBuffer_SizeForcesLoad(t *testing.T) {
	c := contentBuffer{load: func() ([]byte, error) { return []byte("twelve chars"), nil }}
	got, err := c.size()
	if err != nil {
		t.Fatal(err)
	}
	if got != len("twelve chars") {
		t.Errorf("size = %d, want %d — size must load before measuring", got, len("twelve chars"))
	}
}

// writeAt on a lazy buffer loads first, then appends onto the loaded content.
func TestContentBuffer_WriteAfterLoad(t *testing.T) {
	c := contentBuffer{load: func() ([]byte, error) { return []byte("base"), nil }}
	if _, err := c.writeAt(4, []byte("+more")); err != nil {
		t.Fatal(err)
	}
	got, _ := c.bytes()
	if string(got) != "base+more" {
		t.Errorf("bytes = %q, want %q", got, "base+more")
	}
}

// A load error propagates out of every access method and leaves the buffer unloaded.
func TestContentBuffer_LoadError(t *testing.T) {
	boom := errors.New("boom")
	c := contentBuffer{load: func() ([]byte, error) { return nil, boom }}

	if _, err := c.size(); !errors.Is(err, boom) {
		t.Errorf("size err = %v, want boom", err)
	}
	if _, err := c.bytes(); !errors.Is(err, boom) {
		t.Errorf("bytes err = %v, want boom", err)
	}
	if _, err := c.writeAt(0, []byte("x")); !errors.Is(err, boom) {
		t.Errorf("writeAt err = %v, want boom", err)
	}
}

// invalidate() drops loaded content so the next access re-runs the loader against
// a now-fresh entity, and clears dirty.
func TestContentBuffer_Invalidate(t *testing.T) {
	version := "v1"
	c := contentBuffer{load: func() ([]byte, error) { return []byte(version), nil }}

	if got, _ := c.bytes(); string(got) != "v1" {
		t.Fatalf("bytes = %q, want v1", got)
	}
	_, _ = c.writeAt(2, []byte("!"))
	if !c.isDirty() {
		t.Fatal("expected dirty after write")
	}

	version = "v2-fresh"
	c.invalidate()
	if c.isDirty() {
		t.Error("invalidate should clear dirty")
	}
	if got, _ := c.bytes(); string(got) != "v2-fresh" {
		t.Errorf("after invalidate bytes = %q, want reloaded %q", got, "v2-fresh")
	}
}

// markClean() clears dirty but keeps the buffered content (eager nodes, no loader).
func TestContentBuffer_MarkCleanKeepsContent(t *testing.T) {
	c := contentBuffer{buf: []byte("eager"), loaded: true}
	_, _ = c.writeAt(5, []byte("!"))
	c.markClean()
	if c.isDirty() {
		t.Error("markClean should clear dirty")
	}
	if got, _ := c.bytes(); string(got) != "eager!" {
		t.Errorf("markClean must keep content: bytes = %q, want %q", got, "eager!")
	}
}

// A pre-seeded eager buffer reports its content without any loader.
func TestContentBuffer_EagerPreseed(t *testing.T) {
	c := contentBuffer{buf: []byte("preseeded"), loaded: true}
	if got, _ := c.size(); got != len("preseeded") {
		t.Errorf("size = %d, want %d", got, len("preseeded"))
	}
}
