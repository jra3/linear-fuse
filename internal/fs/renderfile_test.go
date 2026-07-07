package fs

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// renderFile's interface is its test surface: a render closure. These exercise
// the byte-window slice, the write-open rejection, and the size/time reporting
// directly on the struct — no FUSE mount, SQLite, or API.

func TestReadWindowClampsAtEOF(t *testing.T) {
	content := []byte("hello world")
	cases := []struct {
		off  int64
		size int
		want string
	}{
		{0, 5, "hello"},
		{6, 100, "world"},       // dest larger than remaining -> clamps to EOF
		{0, 100, "hello world"}, // whole thing
		{11, 10, ""},            // off == len -> empty
		{50, 10, ""},            // off past len -> empty
	}
	for _, c := range cases {
		res := readWindow(content, make([]byte, c.size), c.off)
		got, status := res.Bytes(make([]byte, c.size))
		if !status.Ok() {
			t.Fatalf("off=%d size=%d: status=%v", c.off, c.size, status)
		}
		if string(got) != c.want {
			t.Errorf("off=%d size=%d: got %q, want %q", c.off, c.size, string(got), c.want)
		}
	}
}

func TestRenderFileOpenRejectsWrites(t *testing.T) {
	r := &renderFile{render: func(context.Context) ([]byte, time.Time, time.Time) {
		return []byte("x"), time.Time{}, time.Time{}
	}}
	if _, _, errno := r.Open(context.Background(), syscall.O_RDONLY); errno != 0 {
		t.Errorf("read-open errno = %v, want 0", errno)
	}
	for _, flag := range []uint32{syscall.O_WRONLY, syscall.O_RDWR} {
		if _, _, errno := r.Open(context.Background(), flag); errno != syscall.EACCES {
			t.Errorf("write-open(%d) errno = %v, want EACCES", flag, errno)
		}
	}
}

func TestRenderFileGetattrReportsRenderedSizeAndTimes(t *testing.T) {
	mtime := time.Unix(2000, 0)
	ctime := time.Unix(1000, 0)
	body := []byte("id: X\n")
	r := &renderFile{render: func(context.Context) ([]byte, time.Time, time.Time) {
		return body, mtime, ctime
	}}

	var out fuse.AttrOut
	if errno := r.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr errno=%v", errno)
	}
	if out.Size != uint64(len(body)) {
		t.Errorf("size = %d, want %d", out.Size, len(body))
	}
	if out.Mtime != uint64(mtime.Unix()) {
		t.Errorf("mtime = %d, want %d", out.Mtime, mtime.Unix())
	}
	if out.Ctime != uint64(ctime.Unix()) {
		t.Errorf("ctime = %d, want %d", out.Ctime, ctime.Unix())
	}
	if out.Mode&0o777 != 0o444 {
		t.Errorf("mode = %o, want read-only 0444", out.Mode&0o777)
	}
}

// A zero time reports as an unset attr (nonZeroTime), never a fabricated now()
// or a wrapped garbage timestamp — the drift this module exists to kill.
func TestRenderFileZeroTimeReportsUnset(t *testing.T) {
	r := &renderFile{render: func(context.Context) ([]byte, time.Time, time.Time) {
		return []byte("no times"), time.Time{}, time.Time{}
	}}
	var out fuse.AttrOut
	if errno := r.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr errno=%v", errno)
	}
	if out.Mtime != 0 || out.Ctime != 0 {
		t.Errorf("zero times reported as mtime=%d ctime=%d, want 0/0", out.Mtime, out.Ctime)
	}
}

// TestRenderFileThreadsContext pins the ctx contract: the FUSE handler's ctx
// reaches the render closure on every path (Read, Getattr, and the Lookup-time
// renderAttr), so a closure that promotes it with api.WithInteractive actually
// affects the request — a context.Background() substitution anywhere in the
// chain would silently strip the promotion (the pre-fix behavior).
func TestRenderFileThreadsContext(t *testing.T) {
	type ctxKey struct{}
	want := context.WithValue(context.Background(), ctxKey{}, "marker")

	var got []any
	r := &renderFile{render: func(ctx context.Context) ([]byte, time.Time, time.Time) {
		got = append(got, ctx.Value(ctxKey{}))
		return []byte("x"), time.Time{}, time.Time{}
	}}

	if _, errno := r.Read(want, nil, make([]byte, 8), 0); errno != 0 {
		t.Fatalf("Read errno=%v", errno)
	}
	var out fuse.AttrOut
	if errno := r.Getattr(want, nil, &out); errno != 0 {
		t.Fatalf("Getattr errno=%v", errno)
	}
	r.renderAttr(want)

	if len(got) != 3 {
		t.Fatalf("render called %d times, want 3", len(got))
	}
	for i, v := range got {
		if v != "marker" {
			t.Errorf("call %d: render did not receive the handler ctx (got %v)", i, v)
		}
	}
}
