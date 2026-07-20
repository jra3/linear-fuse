package db

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// withZeroUmask forces a 0 umask for the duration of a test so a mode
// assertion reflects the mode the code requested, not the ambient umask (which
// on a dev box or CI runner is usually 022 and would mask group/other bits the
// test is not exercising). It restores the prior umask on cleanup. Tests using
// it must NOT be t.Parallel() — umask is process-global.
func withZeroUmask(t *testing.T) {
	t.Helper()
	prev := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(prev) })
}

// assertMode fails if path's permission bits differ from want.
func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %04o, want %04o", path, got, want)
	}
}

// TestOpenTightensArtifacts guards #339: the cache dir is 0700 and the created
// cache.db (plus its WAL/SHM sidecars) are 0600. The SQLite driver creates the
// db file, so Open must chmod it after open — the MkdirAll mode alone can't
// reach it.
func TestOpenTightensArtifacts(t *testing.T) {
	withZeroUmask(t)

	dir := filepath.Join(t.TempDir(), "state")
	dbPath := filepath.Join(dir, "cache.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	// A write forces the WAL/SHM sidecars into existence so we can check them.
	if _, err := store.DB().ExecContext(context.Background(),
		"CREATE TABLE IF NOT EXISTS _permcheck (x INTEGER)"); err != nil {
		t.Fatalf("exec: %v", err)
	}

	assertMode(t, dir, 0700)
	assertMode(t, dbPath, 0600)
	for _, sfx := range []string{"-wal", "-shm"} {
		p := dbPath + sfx
		if _, err := os.Stat(p); err == nil {
			assertMode(t, p, 0600)
		}
	}
}

// TestOpenSelfHealsLooseArtifacts guards the self-heal contract: a pre-existing
// 0644 cache.db and 0755 dir left by an older binary are tightened to
// 0600/0700 on the next Open, with no manual chmod.
func TestOpenSelfHealsLooseArtifacts(t *testing.T) {
	withZeroUmask(t)

	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache.db")

	// First open creates the db; loosen everything to simulate an old binary.
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (seed): %v", err)
	}
	store.Close()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dbPath, 0644); err != nil {
		t.Fatal(err)
	}

	// Re-open: the loose artifacts must self-heal.
	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (reopen): %v", err)
	}
	defer store2.Close()

	assertMode(t, dir, 0700)
	assertMode(t, dbPath, 0600)
}
