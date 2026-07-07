package fs

import (
	"context"
	"strings"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"gopkg.in/yaml.v3"
)

// The `.last` success sidecar.
//
// Every writable collection already exposes a `.error` file reporting the last
// failed write (errorfile.go). `.last` is its symmetric twin: after a successful
// create it reports the resulting identity, so an agent that just ran `mkdir` or
// wrote a `_create` trigger can read back the new entity's identifier/url/path in
// one deterministic read instead of re-listing and grepping (#149).
//
// It is a create-scoped append log: each create appends one entry (capped to the
// most recent maxWriteResults), and it is keyed identically to `.error`
// (collectionSuccessKey shares the "kind:parentID" string with collectionErrorKey).
// Edits report success via read-your-writes (writeback.go), not `.last`.

// maxWriteResults caps the append log so a long-lived mount doesn't grow it
// unbounded; the newest entries are kept (last in the slice).
const maxWriteResults = 50

// WriteResult is one successful create, surfaced as a YAML list entry in `.last`.
// It captures what *persisted* (from the returned entity), never what was sent.
type WriteResult struct {
	Identifier string // e.g. "ENG-1234" (issues); entity name/slug for others
	URL        string // Linear URL, where the entity has one
	Path       string // the addressable on-disk name (cures typed-name != slug)
	Title      string // human title/name
	Status     string // workflow state name, where applicable
	Timestamp  time.Time
}

// writeResultYAML is the on-disk projection of a WriteResult (no timestamp).
type writeResultYAML struct {
	Identifier string `yaml:"identifier"`
	URL        string `yaml:"url"`
	Path       string `yaml:"path"`
	Title      string `yaml:"title"`
	Status     string `yaml:"status"`
}

// firstLine returns the first non-empty line of s, trimmed, capped to 80 runes —
// a compact human handle for entities without a title (e.g. a comment body).
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r := []rune(line)
		if len(r) > 80 {
			return string(r[:80]) + "…"
		}
		return line
	}
	return ""
}

// collectionSuccessKey returns the `.last` store key for a collection directory.
// It intentionally returns the SAME string as collectionErrorKey so a surface's
// success and failure sidecars share one namespace (distinct maps and inodes).
func collectionSuccessKey(kind, parentID string) string {
	return kind + ":" + parentID
}

// AppendWriteSuccess records a successful create for a collection key, keeping at
// most maxWriteResults newest entries, and refreshes the `.last` file's cached
// size so the next read reflects it.
func (wf *writeFeedback) AppendWriteSuccess(key string, r WriteResult) {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	wf.successesMu.Lock()
	if wf.successes == nil {
		wf.successes = make(map[string][]*WriteResult)
	}
	list := append(wf.successes[key], &r)
	if len(list) > maxWriteResults {
		list = list[len(list)-maxWriteResults:]
	}
	wf.successes[key] = list
	wf.successesMu.Unlock()
	wf.invalidate(successIno(key))
}

// GetWriteSuccess returns a copy of the recorded successes for a collection key
// (nil if none). A copy — not the internal slice — so a caller can't race a
// concurrent AppendWriteSuccess that re-slices/appends under the write lock.
func (wf *writeFeedback) GetWriteSuccess(key string) []*WriteResult {
	wf.successesMu.RLock()
	defer wf.successesMu.RUnlock()
	src := wf.successes[key]
	if len(src) == 0 {
		return nil
	}
	out := make([]*WriteResult, len(src))
	copy(out, src)
	return out
}

// ClearWriteSuccess drops the recorded successes for a collection key (used by tests).
func (wf *writeFeedback) ClearWriteSuccess(key string) {
	wf.successesMu.Lock()
	_, had := wf.successes[key]
	delete(wf.successes, key)
	wf.successesMu.Unlock()
	if had {
		wf.invalidate(successIno(key))
	}
}

// renderWriteSuccess renders the recorded successes for a key as a YAML list.
// Returns empty (size 0) when there are none, mirroring an empty `.error`.
func (lfs *LinearFS) renderWriteSuccess(key string) []byte {
	results := lfs.GetWriteSuccess(key)
	if len(results) == 0 {
		return nil
	}
	projected := make([]writeResultYAML, len(results))
	for i, r := range results {
		projected[i] = writeResultYAML{
			Identifier: r.Identifier,
			URL:        r.URL,
			Path:       r.Path,
			Title:      r.Title,
			Status:     r.Status,
		}
	}
	out, err := yaml.Marshal(projected)
	if err != nil {
		return nil
	}
	return out
}

// lookupSuccessFile mounts the read-only `.last` virtual file for a collection as
// a child of parent. Reading it returns the YAML list of recent creates (empty if
// none yet), keyed by the collectionSuccessKey used with AppendWriteSuccess. It
// is a plain renderFile with zero timeouts, so it always reflects the most recent
// create; the reported time is the newest recorded create's timestamp.
func (lfs *LinearFS) lookupSuccessFile(ctx context.Context, parent fs.InodeEmbedder, key string, out *fuse.EntryOut) *fs.Inode {
	render := func() ([]byte, time.Time, time.Time) {
		content := lfs.renderWriteSuccess(key)
		if content == nil {
			return nil, time.Time{}, time.Time{}
		}
		var latest time.Time
		for _, r := range lfs.GetWriteSuccess(key) {
			if r.Timestamp.After(latest) {
				latest = r.Timestamp
			}
		}
		return content, latest, latest
	}
	return lfs.mountRenderFile(ctx, parent, render, successIno(key), 0, out)
}
