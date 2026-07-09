package fs

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

// fakePreflight builds a mountPreflight with scripted seams and records the
// unmount calls. statfsErrs is consumed in order (probe, then re-probe after
// heal); the last entry repeats if the script runs out.
type fakePreflight struct {
	pf        *mountPreflight
	unmounted []string
}

func newFakePreflight(statfsErrs []error, mountedResults []bool, mountedErr error, unmountErr error) *fakePreflight {
	f := &fakePreflight{}
	statfsCall, mountedCall := 0, 0
	f.pf = &mountPreflight{
		statfs: func(path string) error {
			i := statfsCall
			if i >= len(statfsErrs) {
				i = len(statfsErrs) - 1
			}
			statfsCall++
			return statfsErrs[i]
		},
		isMounted: func(path string) (bool, error) {
			if mountedErr != nil {
				return false, mountedErr
			}
			i := mountedCall
			if i >= len(mountedResults) {
				i = len(mountedResults) - 1
			}
			mountedCall++
			return mountedResults[i], nil
		},
		unmount: func(path string) error {
			f.unmounted = append(f.unmounted, path)
			return unmountErr
		},
		logf: func(format string, args ...interface{}) {},
	}
	return f
}

func TestPreflightPlainDirProceeds(t *testing.T) {
	f := newFakePreflight([]error{nil}, []bool{false}, nil, nil)
	if err := f.pf.run("/mnt/x"); err != nil {
		t.Fatalf("plain dir should proceed, got %v", err)
	}
	if len(f.unmounted) != 0 {
		t.Fatalf("plain dir must not trigger unmount, got %v", f.unmounted)
	}
}

func TestPreflightMissingDirProceeds(t *testing.T) {
	f := newFakePreflight([]error{syscall.ENOENT}, nil, nil, nil)
	if err := f.pf.run("/mnt/x"); err != nil {
		t.Fatalf("missing dir should proceed, got %v", err)
	}
	if len(f.unmounted) != 0 {
		t.Fatalf("missing dir must not trigger unmount, got %v", f.unmounted)
	}
}

func TestPreflightHealthyMountRefuses(t *testing.T) {
	f := newFakePreflight([]error{nil}, []bool{true}, nil, nil)
	err := f.pf.run("/mnt/x")
	if err == nil {
		t.Fatal("healthy live mount must refuse")
	}
	if !strings.Contains(err.Error(), "already a live mount") {
		t.Fatalf("want 'already a live mount' error, got %v", err)
	}
	if len(f.unmounted) != 0 {
		t.Fatalf("healthy mount must NEVER be unmounted, got %v", f.unmounted)
	}
}

func TestPreflightDeadMountHeals(t *testing.T) {
	// First statfs: ENOTCONN (wedged). After heal: statfs OK, not mounted.
	f := newFakePreflight([]error{syscall.ENOTCONN, nil}, []bool{false}, nil, nil)
	if err := f.pf.run("/mnt/x"); err != nil {
		t.Fatalf("dead mount should heal and proceed, got %v", err)
	}
	if len(f.unmounted) != 1 || f.unmounted[0] != "/mnt/x" {
		t.Fatalf("expected one lazy unmount of /mnt/x, got %v", f.unmounted)
	}
}

func TestPreflightDeadMountUnmountFails(t *testing.T) {
	f := newFakePreflight([]error{syscall.ENOTCONN}, nil, nil, errors.New("fusermount3 not found"))
	err := f.pf.run("/mnt/x")
	if err == nil {
		t.Fatal("failed unmount must error")
	}
	if !strings.Contains(err.Error(), "lazy unmount failed") || !strings.Contains(err.Error(), "fusermount3 -uz") {
		t.Fatalf("want clear manual-cleanup error, got %v", err)
	}
}

func TestPreflightStillWedgedAfterUnmount(t *testing.T) {
	// Unmount "succeeds" but the re-probe still sees ENOTCONN.
	f := newFakePreflight([]error{syscall.ENOTCONN, syscall.ENOTCONN}, nil, nil, nil)
	err := f.pf.run("/mnt/x")
	if err == nil {
		t.Fatal("still-wedged re-probe must error")
	}
	if !strings.Contains(err.Error(), "still wedged") {
		t.Fatalf("want 'still wedged' error, got %v", err)
	}
}

func TestPreflightStillMountedAfterUnmount(t *testing.T) {
	// Re-probe statfs succeeds but the mount table still lists the path.
	f := newFakePreflight([]error{syscall.ENOTCONN, nil}, []bool{true}, nil, nil)
	err := f.pf.run("/mnt/x")
	if err == nil {
		t.Fatal("still-mounted re-probe must error")
	}
	if !strings.Contains(err.Error(), "still present") {
		t.Fatalf("want 'still present' error, got %v", err)
	}
}

func TestPreflightOtherStatfsErrorNotMounted(t *testing.T) {
	// A non-ENOTCONN statfs failure on a path the mount table does NOT list
	// is not a dead mount — propagate, don't unmount.
	f := newFakePreflight([]error{syscall.EACCES}, []bool{false}, nil, nil)
	err := f.pf.run("/mnt/x")
	if err == nil {
		t.Fatal("unexplained statfs failure must error")
	}
	if len(f.unmounted) != 0 {
		t.Fatalf("must not unmount an unlisted path, got %v", f.unmounted)
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("want wrapped EACCES, got %v", err)
	}
}

func TestPreflightOtherStatfsErrorButMountedHeals(t *testing.T) {
	// Non-ENOTCONN errno but /proc/self/mounts still lists it: treat as dead.
	f := newFakePreflight([]error{syscall.EIO, nil}, []bool{true, false}, nil, nil)
	if err := f.pf.run("/mnt/x"); err != nil {
		t.Fatalf("mounted-but-erroring path should heal, got %v", err)
	}
	if len(f.unmounted) != 1 {
		t.Fatalf("expected one lazy unmount, got %v", f.unmounted)
	}
}

func TestPreflightMountTableUnavailableProceeds(t *testing.T) {
	// statfs OK + no mount table (non-Linux): proceed.
	f := newFakePreflight([]error{nil}, nil, errors.New("no /proc"), nil)
	if err := f.pf.run("/mnt/x"); err != nil {
		t.Fatalf("healthy statfs without mount table should proceed, got %v", err)
	}
}

func TestUnescapeMountPath(t *testing.T) {
	cases := map[string]string{
		`/tmp/plain`:         "/tmp/plain",
		`/tmp/with\040space`: "/tmp/with space",
		`/tmp/tab\011here`:   "/tmp/tab\there",
		`/tmp/back\134slash`: `/tmp/back\slash`,
		`/tmp/trailing\04`:   `/tmp/trailing\04`, // truncated escape stays literal
		`/tmp/not\999octal`:  `/tmp/not\999octal`,
		`/tmp/a\040b\040c`:   "/tmp/a b c",
	}
	for in, want := range cases {
		if got := unescapeMountPath(in); got != want {
			t.Errorf("unescapeMountPath(%q) = %q, want %q", in, got, want)
		}
	}
}
