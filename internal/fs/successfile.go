package fs

import (
	"context"
	"hash/fnv"
	"strings"
	"syscall"
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

// successIno derives the stable inode for a `.last` file from its key. The
// "last:" prefix keeps it from ever colliding with errorIno ("error:"+key).
func successIno(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("last:" + key))
	return h.Sum64()
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
func (lfs *LinearFS) AppendWriteSuccess(key string, r WriteResult) {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	lfs.writeSuccessesMu.Lock()
	if lfs.writeSuccesses == nil {
		lfs.writeSuccesses = make(map[string][]*WriteResult)
	}
	list := append(lfs.writeSuccesses[key], &r)
	if len(list) > maxWriteResults {
		list = list[len(list)-maxWriteResults:]
	}
	lfs.writeSuccesses[key] = list
	lfs.writeSuccessesMu.Unlock()
	lfs.InvalidateUpdated(successIno(key))
}

// GetWriteSuccess returns a copy of the recorded successes for a collection key
// (nil if none). A copy — not the internal slice — so a caller can't race a
// concurrent AppendWriteSuccess that re-slices/appends under the write lock.
func (lfs *LinearFS) GetWriteSuccess(key string) []*WriteResult {
	lfs.writeSuccessesMu.RLock()
	defer lfs.writeSuccessesMu.RUnlock()
	src := lfs.writeSuccesses[key]
	if len(src) == 0 {
		return nil
	}
	out := make([]*WriteResult, len(src))
	copy(out, src)
	return out
}

// ClearWriteSuccess drops the recorded successes for a collection key (used by tests).
func (lfs *LinearFS) ClearWriteSuccess(key string) {
	lfs.writeSuccessesMu.Lock()
	_, had := lfs.writeSuccesses[key]
	delete(lfs.writeSuccesses, key)
	lfs.writeSuccessesMu.Unlock()
	if had {
		lfs.InvalidateUpdated(successIno(key))
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

// lookupSuccessFile mounts the `.last` virtual file for a collection as a child
// of parent. key is the collectionSuccessKey used with AppendWriteSuccess.
// Timeouts are zero so the file always reflects the most recent create.
func (lfs *LinearFS) lookupSuccessFile(ctx context.Context, parent fs.InodeEmbedder, key string, out *fuse.EntryOut) *fs.Inode {
	node := &SuccessFileNode{BaseNode: BaseNode{lfs: lfs}, key: key}

	size := uint64(len(lfs.renderWriteSuccess(key)))

	now := time.Now()
	out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
	out.Attr.Uid = lfs.uid
	out.Attr.Gid = lfs.gid
	out.Attr.Size = size
	out.SetAttrTimeout(0)
	out.SetEntryTimeout(0)
	out.Attr.SetTimes(&now, &now, &now)

	return parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  successIno(key),
	})
}

// SuccessFileNode is the read-only `.last` virtual file shown alongside any
// writable collection. Reading it returns the YAML list of recent creates (empty
// if none yet). Keyed by the collection's success key.
type SuccessFileNode struct {
	BaseNode
	key string
}

var _ fs.NodeGetattrer = (*SuccessFileNode)(nil)
var _ fs.NodeOpener = (*SuccessFileNode)(nil)
var _ fs.NodeReader = (*SuccessFileNode)(nil)

func (s *SuccessFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0444 // Read-only
	s.SetOwner(out)
	out.Size = uint64(len(s.lfs.renderWriteSuccess(s.key)))
	return 0
}

func (s *SuccessFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// FOPEN_DIRECT_IO: `.last` changes on every create; direct I/O forces a real
	// READ on each open instead of trusting a possibly-stale cached size.
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (s *SuccessFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := s.lfs.renderWriteSuccess(s.key)
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}
