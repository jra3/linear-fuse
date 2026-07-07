package fs

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	gosync "sync"

	"github.com/jra3/linear-fuse/internal/api"
)

// embeddedFileCache owns the bytes of embedded attachment files (the *.png/*.pdf
// a comment or description links to on Linear's CDN). A read walks three tiers —
// in-memory, on-disk, then a CDN download that back-fills both — so a file is
// fetched from the network at most once per mount. It was three loose fields and
// two methods on the LinearFS god-object; gathering them keeps the tiers and the
// state they cache together.
//
// Its dependencies on the rest of the mount are three seams: auth (the CDN
// requires the client's auth header), persist (record the on-disk path back to
// SQLite), and httpClient (the CDN GET). httpClient defaults to
// http.DefaultClient but is injectable, so the download→disk→memory layering is
// unit-testable against an httptest server with no real network — the byte-fetch
// path that used to hit http.DefaultClient inline and could not be tested.
type embeddedFileCache struct {
	dir        string
	auth       func() string
	persist    func(ctx context.Context, fileID, path string, size int64) error
	httpClient *http.Client

	mu  gosync.RWMutex
	mem map[string][]byte
}

// newEmbeddedFileCache builds the cache rooted at dir. auth supplies the CDN
// Authorization header; persist records a freshly-cached file's on-disk path and
// size (best-effort). Both are late-bound closures because the repo they reach is
// wired after the LinearFS exists.
func newEmbeddedFileCache(dir string, auth func() string, persist func(ctx context.Context, fileID, path string, size int64) error) *embeddedFileCache {
	return &embeddedFileCache{
		dir:        dir,
		auth:       auth,
		persist:    persist,
		httpClient: http.DefaultClient,
		mem:        make(map[string][]byte),
	}
}

// FetchEmbeddedFile returns the file's bytes, fetching from the CDN and caching
// to disk + memory on a miss. Memory hit → disk hit → download.
func (c *embeddedFileCache) FetchEmbeddedFile(ctx context.Context, file api.EmbeddedFile) ([]byte, error) {
	c.mu.RLock()
	if content, ok := c.mem[file.ID]; ok {
		c.mu.RUnlock()
		return content, nil
	}
	c.mu.RUnlock()

	diskPath := filepath.Join(c.dir, file.ID)
	if file.CachePath != "" {
		diskPath = file.CachePath
	}

	if content, err := os.ReadFile(diskPath); err == nil {
		c.store(file.ID, content)
		return content, nil
	}

	content, err := c.download(ctx, file.URL)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}

	if err := os.WriteFile(diskPath, content, 0644); err != nil {
		log.Printf("[cache] Warning: failed to cache file %s: %v", file.Filename, err)
	} else if c.persist != nil {
		if err := c.persist(ctx, file.ID, diskPath, int64(len(content))); err != nil {
			log.Printf("[cache] Warning: failed to update cache path: %v", err)
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

// download fetches a file from Linear's CDN, authenticating with c.auth().
func (c *embeddedFileCache) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if c.auth != nil {
		req.Header.Set("Authorization", c.auth())
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
