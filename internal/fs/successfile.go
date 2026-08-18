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
// A create records its *outcome*: a success appends the resulting identity, a
// failed create appends an `outcome: failed` entry (#370). So a scripted batch
// that writes `_create` N times — where every write shares a latent bug and all
// N fail — leaves N countable failure entries here instead of `.error`
// collapsing to only the last one; `.error` still holds the full most-recent
// reason. Edits report success via read-your-writes (writeback.go), not `.last`.

// maxWriteResults caps the append log so a long-lived mount doesn't grow it
// unbounded; the newest entries are kept (last in the slice).
const maxWriteResults = 50

// WriteResult is one create's outcome, surfaced as a YAML list entry in `.last`.
// A success captures what *persisted* (from the returned entity), never what was
// sent; a failure (Error != "") captures a compact reason and leaves the
// identity fields empty.
type WriteResult struct {
	Identifier string // e.g. "ENG-1234" (issues); entity name/slug for others
	URL        string // Linear URL, where the entity has one
	Path       string // the addressable on-disk name (cures typed-name != slug)
	Title      string // human title/name
	Status     string // workflow state name, where applicable
	Error      string // non-empty marks a failed create; identity fields are empty
	Timestamp  time.Time
}

// writeResultYAML is the on-disk projection of a successful WriteResult (no timestamp).
type writeResultYAML struct {
	Identifier string `yaml:"identifier"`
	URL        string `yaml:"url"`
	Path       string `yaml:"path"`
	Title      string `yaml:"title"`
	Status     string `yaml:"status"`
}

// writeFailureYAML is the on-disk projection of a failed create in `.last`. The
// distinct `outcome: failed` shape lets a scripted batch count failures
// (grep -c 'outcome: failed') without mistaking one for a created entity.
type writeFailureYAML struct {
	Outcome string `yaml:"outcome"` // always "failed"
	Error   string `yaml:"error"`   // the distilled reason (see failureReason)
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

// failureReason distills a multi-line `.error` message (Field:/Value:/Error: or
// Operation:/Error:) to the single most informative line for a `.last` failure
// entry: the "Error:" line's content where present, else the first non-empty
// line. Capped like firstLine so the log stays scannable; the full reason still
// lives in `.error`.
func failureReason(msg string) string {
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "Error: "); ok {
			return firstLine(after)
		}
	}
	return firstLine(msg)
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
	wf.appendOutcome(key, &r)
}

// AppendWriteFailure records a failed create for a collection key in the same
// capped, newest-last `.last` log. It exists so a scripted batch of failing
// `_create` writes leaves N countable `outcome: failed` entries rather than a
// single overwritten `.error` (#370); msg is the same reason `.error` carries.
func (wf *writeFeedback) AppendWriteFailure(key, msg string) {
	wf.appendOutcome(key, &WriteResult{Error: msg})
}

// appendOutcome is the shared append tail for AppendWriteSuccess/Failure: stamp
// the time if unset, append newest-last under the cap, and drop the `.last`
// inode so the next read reflects it.
func (wf *writeFeedback) appendOutcome(key string, r *WriteResult) {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	wf.outcomesMu.Lock()
	if wf.outcomes == nil {
		wf.outcomes = make(map[string][]*WriteResult)
	}
	list := append(wf.outcomes[key], r)
	if len(list) > maxWriteResults {
		list = list[len(list)-maxWriteResults:]
	}
	wf.outcomes[key] = list
	wf.outcomesMu.Unlock()
	wf.invalidate(successIno(key))
}

// GetWriteSuccess returns a copy of the recorded *successful* creates for a key
// (failures excluded), nil if none. A copy — not the internal slice — so a
// caller can't race a concurrent append that re-slices under the write lock.
// See GetWriteOutcomes for the full success+failure log that `.last` renders.
func (wf *writeFeedback) GetWriteSuccess(key string) []*WriteResult {
	wf.outcomesMu.RLock()
	defer wf.outcomesMu.RUnlock()
	var out []*WriteResult
	for _, r := range wf.outcomes[key] {
		if r.Error == "" {
			out = append(out, r)
		}
	}
	return out
}

// GetWriteOutcomes returns a copy of the full ordered create log for a key —
// successes and failures, as rendered into `.last` — or nil if empty.
func (wf *writeFeedback) GetWriteOutcomes(key string) []*WriteResult {
	wf.outcomesMu.RLock()
	defer wf.outcomesMu.RUnlock()
	src := wf.outcomes[key]
	if len(src) == 0 {
		return nil
	}
	out := make([]*WriteResult, len(src))
	copy(out, src)
	return out
}

// ClearWriteSuccess drops the recorded outcomes for a collection key (used by tests).
func (wf *writeFeedback) ClearWriteSuccess(key string) {
	wf.outcomesMu.Lock()
	_, had := wf.outcomes[key]
	delete(wf.outcomes, key)
	wf.outcomesMu.Unlock()
	if had {
		wf.invalidate(successIno(key))
	}
}

// renderWriteSuccess renders the recorded create outcomes for a key as a YAML
// list — a success as its identity fields, a failure as `outcome: failed` plus
// the reason's first line. Returns empty (size 0) when there are none, mirroring
// an empty `.error`.
func (lfs *LinearFS) renderWriteSuccess(key string) []byte {
	results := lfs.GetWriteOutcomes(key)
	if len(results) == 0 {
		return nil
	}
	projected := make([]any, len(results))
	for i, r := range results {
		if r.Error != "" {
			projected[i] = writeFailureYAML{Outcome: "failed", Error: failureReason(r.Error)}
			continue
		}
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
	render := func(context.Context) ([]byte, time.Time, time.Time) {
		content := lfs.renderWriteSuccess(key)
		if content == nil {
			return nil, time.Time{}, time.Time{}
		}
		var latest time.Time
		for _, r := range lfs.GetWriteOutcomes(key) {
			if r.Timestamp.After(latest) {
				latest = r.Timestamp
			}
		}
		return content, latest, latest
	}
	return lfs.mountRenderFile(ctx, parent, ".last", render, successIno(key), mountDefaultTimeout, out)
}
