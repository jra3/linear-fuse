package fs

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/repo"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// linkTestLFS builds a LinearFS with a real store and a succeeding mock mutator,
// the harness the links create-path tests drive.
func linkTestLFS(t *testing.T) (*LinearFS, *db.Store) {
	t.Helper()
	cfg := &config.Config{APIKey: "test-key", Cache: config.CacheConfig{TTL: 100 * time.Millisecond, MaxEntries: 100}}
	lfs, err := NewLinearFS(cfg, true)
	if err != nil {
		t.Fatalf("NewLinearFS: %v", err)
	}
	t.Cleanup(func() { lfs.Close() })

	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	lfs.store = store
	lfs.repo = repo.NewSQLiteRepository(store, nil)
	// WithStore lets the mock overlay untouched fields from current state (like the
	// real whole-entity mutation responses); reads are guarded, so a closed store
	// in the persist-failure tests just skips the overlay.
	lfs.InjectTestMutationClient(mockmutation.New(mockmutation.WithStore(store)))
	return lfs, store
}

// TestCreateLinkPersistFailureFailsLoud is the #283 regression: an external-link
// create whose SQLite reflection fails must fail loud (EIO) with a de-dupe .error,
// not report success — commitCreate's #276 persist gate has to fire. The bug was
// that the persist closure called the void upsertLink and returned nil regardless,
// so a wedged upsert returned 0 with a clean .error and a .last advertising a link
// the store never got. That is a duplicate-mint window, since Linear does not dedup
// external links server-side and the cache pre-check is the only guard.
func TestCreateLinkPersistFailureFailsLoud(t *testing.T) {
	lfs, store := linkTestLFS(t)

	const projectID = "proj-1"
	dir := &LinksNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: projectID}

	// Close the store so the persist (UpsertEntityExternalLink) fails while the
	// mock mutation still succeeds — the #276 confirmed-reflection wedge condition.
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	errno := dir.createLink(context.Background(), []byte("https://example.com/x Probe Link"))
	if errno != syscall.EIO {
		t.Fatalf("createLink on a failed persist: errno = %v, want EIO", errno)
	}

	key := collectionErrorKey("links", dir.parentID())
	if e := lfs.GetWriteError(key); e == nil {
		t.Errorf(".error must be set on an unconfirmed reflection")
	}
	// A create the local cache can't serve must never be advertised via .last.
	if got := lfs.GetWriteSuccess(key); len(got) != 0 {
		t.Errorf(".last advertised a link the cache can't serve: %+v", got)
	}
}
