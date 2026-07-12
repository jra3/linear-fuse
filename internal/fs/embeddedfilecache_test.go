package fs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// TestEmbeddedFileCacheTiers drives the three-tier fetch against an httptest CDN
// — the download→disk→memory layering that used to hit http.DefaultClient inline
// and could not be tested. It proves the CDN is called at most once, the auth
// seam is applied, the persist seam records the on-disk path/size, and both the
// memory and disk tiers then serve without a second network hit.
func TestEmbeddedFileCacheTiers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	served := 0
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()

	var persistedID, persistedPath string
	var persistedSize int64
	cdn := api.NewCDNClient(func() string { return "Bearer test" })
	cdn.SetHTTPClient(srv.Client())
	c := newEmbeddedFileCache(dir, cdn,
		func(_ context.Context, id, path string, size int64) error {
			persistedID, persistedPath, persistedSize = id, path, size
			return nil
		},
	)

	file := api.EmbeddedFile{ID: "f1", URL: srv.URL + "/f1.png", Filename: "f1.png"}

	// Tier 3: download.
	got, err := c.FetchEmbeddedFile(ctx, file)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if string(got) != "PNGDATA" {
		t.Errorf("content = %q, want PNGDATA", got)
	}
	if served != 1 {
		t.Errorf("CDN served %d times, want 1", served)
	}
	if gotAuth != "Bearer test" {
		t.Errorf("auth header = %q, want Bearer test", gotAuth)
	}
	if persistedID != "f1" || persistedSize != int64(len("PNGDATA")) {
		t.Errorf("persist = {%q,%q,%d}, want f1/…/7", persistedID, persistedPath, persistedSize)
	}
	if _, err := os.Stat(filepath.Join(dir, "f1")); err != nil {
		t.Errorf("file not written to disk cache: %v", err)
	}

	// Tier 1: memory hit — no new network call.
	if _, err := c.FetchEmbeddedFile(ctx, file); err != nil {
		t.Fatalf("memory fetch: %v", err)
	}
	if served != 1 {
		t.Errorf("memory tier hit the CDN: served=%d", served)
	}

	// Tier 2: disk hit — drop memory, must read the disk file, still no network.
	c.mu.Lock()
	c.mem = make(map[string][]byte)
	c.mu.Unlock()
	got, err = c.FetchEmbeddedFile(ctx, file)
	if err != nil {
		t.Fatalf("disk fetch: %v", err)
	}
	if string(got) != "PNGDATA" {
		t.Errorf("disk content = %q, want PNGDATA", got)
	}
	if served != 1 {
		t.Errorf("disk tier hit the CDN: served=%d", served)
	}
}

// TestEmbeddedFileCacheDownloadError: a non-200 CDN response is an error, not a
// cached empty file.
func TestEmbeddedFileCacheDownloadError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	cdn := api.NewCDNClient(func() string { return "" })
	cdn.SetHTTPClient(srv.Client())
	c := newEmbeddedFileCache(t.TempDir(), cdn, nil)

	if _, err := c.FetchEmbeddedFile(context.Background(), api.EmbeddedFile{ID: "x", URL: srv.URL}); err == nil {
		t.Error("expected an error on a 403 CDN response, got nil")
	}
}
