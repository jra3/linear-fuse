package fs

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	gosync "sync"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/atrest"
)

// embeddedFileCache owns the bytes of embedded attachment files (the *.png/*.pdf
// a comment or description links to on Linear's CDN). A read walks three tiers —
// in-memory, on-disk, then a CDN download that back-fills both — so a file is
// fetched from the network at most once per mount. It was three loose fields and
// two methods on the LinearFS god-object; gathering them keeps the tiers and the
// state they cache together.
//
// Its dependencies on the rest of the mount are two seams: cdn (the shared
// api.CDNClient that authenticates and instruments every CDN GET) and persist
// (record the on-disk path back to SQLite). cdn's transport is injectable, so
// the download→disk→memory layering stays unit-testable against an httptest
// server with no real network.
type embeddedFileCache struct {
	dir     string
	cdn     *api.CDNClient
	persist func(ctx context.Context, fileID, path string, size int64) error

	mu  gosync.RWMutex
	mem map[string][]byte
}

// embeddedFileCacheDir returns the on-disk byte-cache root under the
// platform's user cache dir — ~/.cache/linearfs/files per XDG on Linux,
// ~/Library/Caches/linearfs/files on macOS (identical to the previously
// hardcoded macOS-only path, so existing caches carry over).
func embeddedFileCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(dir, "linearfs", "files")
}

// newEmbeddedFileCache builds the cache rooted at dir. cdn is the shared CDN
// client (auth + timeout + telemetry); persist records a freshly-cached file's
// on-disk path and size (best-effort), a late-bound closure because the repo it
// reaches is wired after the LinearFS exists.
func newEmbeddedFileCache(dir string, cdn *api.CDNClient, persist func(ctx context.Context, fileID, path string, size int64) error) *embeddedFileCache {
	// The byte cache holds a local copy of the user's attachment files and is
	// owner-only (#339). Create the dir 0700 and self-heal a loose pre-existing
	// one (an older binary made it 0755). Best-effort: a failure here does not
	// block a mount — the 0700 dir bounds reach and a fetch simply re-downloads.
	if err := os.MkdirAll(dir, atrest.DirMode); err != nil {
		log.Printf("[cache] Warning: failed to create cache dir %s: %v", dir, err)
	}
	atrest.Chmod(dir, atrest.DirMode)
	return &embeddedFileCache{
		dir:     dir,
		cdn:     cdn,
		persist: persist,
		mem:     make(map[string][]byte),
	}
}

// FetchEmbeddedFile returns the file's bytes, fetching from the CDN and caching
// to disk + memory on a miss. Memory hit → disk hit → download.
func (c *embeddedFileCache) FetchEmbeddedFile(ctx context.Context, file api.EmbeddedFile) ([]byte, error) {
	c.mu.RLock()
	if content, ok := c.mem[file.ID]; ok {
		c.mu.RUnlock()
		recordEmbeddedFetch(ctx, "memory")
		return content, nil
	}
	c.mu.RUnlock()

	diskPath := filepath.Join(c.dir, file.ID)
	if file.CachePath != "" {
		diskPath = file.CachePath
	}

	if content, err := os.ReadFile(diskPath); err == nil {
		c.store(file.ID, content)
		recordEmbeddedFetch(ctx, "disk")
		return content, nil
	}

	content, err := c.cdn.Get(ctx, file.URL)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	recordEmbeddedFetch(ctx, "cdn")

	// intentionally best-effort: the disk cache is a fetch optimization, not a
	// source of truth. `content` is returned this call regardless, and a cache
	// miss next time simply re-fetches from the CDN — so a failed write self-
	// corrects with no divergence to surface. (#278)
	if err := os.WriteFile(diskPath, content, atrest.FileMode); err != nil {
		log.Printf("[cache] Warning: failed to cache file %s: %v", file.Filename, err)
	} else {
		// Self-heal an existing byte file an older binary wrote 0644; WriteFile
		// leaves an existing file's mode untouched, so tighten explicitly (#339).
		atrest.Chmod(diskPath, atrest.FileMode)
		if c.persist != nil {
			if err := c.persist(ctx, file.ID, diskPath, int64(len(content))); err != nil {
				log.Printf("[cache] Warning: failed to update cache path: %v", err)
			}
		}
	}

	c.store(file.ID, content)
	return content, nil
}

func (c *embeddedFileCache) store(id string, content []byte) {
	c.mu.Lock()
	c.mem[id] = content
	c.mu.Unlock()
}
