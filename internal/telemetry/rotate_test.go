package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestRotatingWriterTightensArtifacts guards #339: the telemetry dir is 0700
// and both the live log and its rotated .1 sidecar are 0600. Run under a zero
// umask so the assertion reflects the requested mode, not the ambient umask.
func TestRotatingWriterTightensArtifacts(t *testing.T) {
	prev := syscall.Umask(0)
	defer syscall.Umask(prev)

	// A nested dir the writer must create, pre-loosened to prove self-heal.
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "requests.jsonl")

	w, err := newRotatingWriter(path, 4)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	// Two writes past the tiny cap force a rollover, creating path.1.
	if _, err := w.Write([]byte("aaaa\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write([]byte("bbbb\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	check := func(p string, want os.FileMode) {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", p, got, want)
		}
	}
	check(dir, 0o700)
	check(path, 0o600)
	check(path+".1", 0o600)
}

// TestRotatingWriterSelfHealsLooseFile guards the self-heal contract: an
// existing 0644 log left by an older binary is tightened to 0600 when the
// writer reopens it (O_APPEND leaves an existing file's mode untouched).
func TestRotatingWriterSelfHealsLooseFile(t *testing.T) {
	prev := syscall.Umask(0)
	defer syscall.Umask(prev)

	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := newRotatingWriter(path, 1024)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("reopened log mode = %04o, want 0600 (self-heal)", got)
	}
}

func TestRotatingWriterAppendsUnderCap(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	w, err := newRotatingWriter(path, 1024)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	for _, line := range []string{"one\n", "two\n"} {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "one\ntwo\n" {
		t.Errorf("content = %q, want %q", got, "one\ntwo\n")
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("no rollover expected under cap, stat .1 err = %v", err)
	}
}

func TestRotatingWriterRollsOverAtCap(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	w, err := newRotatingWriter(path, 20)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	first := "aaaaaaaaaaaaaaa\n" // 16 bytes, under cap
	second := "bbbbbbbbbb\n"     // would push past 20 -> rotate first
	if _, err := w.Write([]byte(first)); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if _, err := w.Write([]byte(second)); err != nil {
		t.Fatalf("Write second: %v", err)
	}

	rolled, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("ReadFile .1: %v", err)
	}
	if string(rolled) != first {
		t.Errorf(".1 content = %q, want %q", rolled, first)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(current) != second {
		t.Errorf("current content = %q, want %q", current, second)
	}
}

func TestRotatingWriterSecondRolloverReplacesNotAppends(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	w, err := newRotatingWriter(path, 10)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	// Each write is 8 bytes; every second write triggers a rotation.
	lines := []string{"aaaaaaa\n", "bbbbbbb\n", "ccccccc\n", "ddddddd\n"}
	for _, line := range lines {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("Write %q: %v", line, err)
		}
	}

	rolled, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("ReadFile .1: %v", err)
	}
	// .1 must hold only the most recently rotated generation — replaced, not
	// accumulated.
	if string(rolled) != "ccccccc\n" {
		t.Errorf(".1 content = %q, want %q (single generation)", rolled, "ccccccc\n")
	}
	if strings.Contains(string(rolled), "aaaaaaa") {
		t.Error(".1 still contains the first generation — rollover appended instead of replacing")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(current) != "ddddddd\n" {
		t.Errorf("current content = %q, want %q", current, "ddddddd\n")
	}
}

func TestRotatingWriterOversizedWriteLandsWhole(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	w, err := newRotatingWriter(path, 4)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	big := "a line much larger than the cap\n"
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != big {
		t.Errorf("content = %q, want the whole oversized write", got)
	}
}

func TestRotatingWriterCreatesParentDirs(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "dir", "metrics.jsonl")
	w, err := newRotatingWriter(path, 1024)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("x\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Stat: %v", err)
	}
}
