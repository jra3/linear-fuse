package telemetry

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/jra3/linear-fuse/internal/atrest"
)

// rotatingWriter is a size-capped io.Writer over a single file with one
// rollover slot: writes append to path; when a write would push the file past
// maxBytes, path is renamed to path.1 (replacing any previous .1) and a fresh
// path is started. Disk usage is therefore bounded at ~2x maxBytes.
type rotatingWriter struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// newRotatingWriter opens path for appending (creating parent directories as
// needed) and caps it at maxBytes before rollover.
func newRotatingWriter(path string, maxBytes int64) (*rotatingWriter, error) {
	// The telemetry/request logs record every GraphQL request (including the
	// full variables map) and are owner-only (#339): dir 0700, files 0600.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, atrest.DirMode); err != nil {
		return nil, err
	}
	atrest.Chmod(dir, atrest.DirMode, atrest.ArtifactLogs)
	f, err := openLog(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingWriter{path: path, max: maxBytes, f: f, size: st.Size()}, nil
}

// openLog opens path for appending, creating it 0600 and self-healing an
// existing loose (e.g. 0644) log — O_APPEND leaves an existing file's mode
// untouched, so tighten explicitly after open.
func openLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, atrest.FileMode)
	if err != nil {
		return nil, err
	}
	atrest.Chmod(path, atrest.FileMode, atrest.ArtifactLogs)
	return f, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Rotate before a write that would exceed the cap. A single write larger
	// than the cap still lands whole in a fresh file (never split).
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	// os.Rename replaces an existing path.1 — the single-slot rollover. The
	// renamed .1 keeps the original 0600 mode; the fresh path is opened 0600 by
	// openLog. (After the rename path no longer exists, so O_APPEND creates it
	// empty — equivalent to the old O_TRUNC create.)
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return err
	}
	f, err := openLog(w.path)
	if err != nil {
		return err
	}
	w.f = f
	w.size = 0
	return nil
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
