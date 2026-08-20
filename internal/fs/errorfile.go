package fs

import (
	"context"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// WriteError is the last failed-write message for a writable entity, surfaced
// via that entity's `.error` virtual file. It exists so an LLM or script
// driving the filesystem can read a human-legible reason for a failed save
// instead of having to interpret a bare FUSE errno.
type WriteError struct {
	Message   string
	Timestamp time.Time
}

// SetWriteError records the last failed-write message for an entity, keyed by
// its (globally-unique) Linear ID. Visible at the entity's `.error` file.
func (wf *writeFeedback) SetWriteError(entityID, message string) {
	wf.errorsMu.Lock()
	wf.errors[entityID] = &WriteError{
		Message:   message,
		Timestamp: time.Now(),
	}
	wf.errorsMu.Unlock()
	// Drop the kernel's cached size/content for the .error file so the next
	// stat/read reflects this error instead of a stale (often empty) value.
	wf.invalidate(errorIno(entityID))
}

// ClearWriteError removes the error for an entity (called on a successful write).
func (wf *writeFeedback) ClearWriteError(entityID string) {
	wf.errorsMu.Lock()
	_, had := wf.errors[entityID]
	delete(wf.errors, entityID)
	wf.errorsMu.Unlock()
	if had {
		wf.invalidate(errorIno(entityID))
	}
}

// GetWriteError returns the last failed-write message for an entity, or nil.
func (wf *writeFeedback) GetWriteError(entityID string) *WriteError {
	wf.errorsMu.RLock()
	defer wf.errorsMu.RUnlock()
	return wf.errors[entityID]
}

// SetIssueError / ClearIssueError / GetIssueError are issue-flavored aliases for
// the generic write-error store, retained so issue write handlers read clearly.
func (wf *writeFeedback) SetIssueError(issueID, message string) { wf.SetWriteError(issueID, message) }
func (wf *writeFeedback) ClearIssueError(issueID string)        { wf.ClearWriteError(issueID) }
func (wf *writeFeedback) GetIssueError(issueID string) *WriteError {
	return wf.GetWriteError(issueID)
}

// collectionErrorKey returns the write-error store key for a collection
// directory (comments/, docs/, labels/, milestones/), keyed by its kind and
// parent ID. Collection surfaces hold many files, so their `.error` is
// directory-level: it reflects the last failed write to any file in the
// directory. The "kind:" prefix keeps these keys distinct from the per-entity
// IDs used by issue/project/initiative .error files.
func collectionErrorKey(kind, parentID string) string {
	return kind + ":" + parentID
}

// lookupErrorFile mounts the read-only `.error` virtual file for an entity as a
// child of parent. Reading it returns the last failed-write message (empty if
// the most recent write succeeded), keyed by entityID, followed by a `Time:`
// line carrying when the error was set. The rendered timestamp is what makes a
// STICKY error datable: a collection `.error` is retired only by the next
// successful write to that collection, so an agent that reads one after an
// unrelated write needs to see whether it is about the write it just made
// (#476). It is also the file's atime/mtime, but agents cat, they do not stat.
// It is deliberately absolute (RFC3339) and never a computed "x ago", so the
// rendered length is identical between two reads of the same error and the
// attr-cached size can never disagree with the content.
//
// It is a plain renderFile at mountDefaultTimeout — the mount's configured
// bound, not "uncached" — so what makes a read reflect the
// most recent write rather than a stale cached (often empty) value is the render
// closure running on every read under FOPEN_DIRECT_IO, plus the
// wf.invalidate(errorIno(entityID)) each write does. Within that bound a stat
// can still be answered from the kernel's attr cache.
// renderWriteError is the .error file's content: the recorded message, then the
// `Time:` line. It is split out from the render closure so the rendering is
// testable without a mount — the closure around it only decides WHETHER there is
// an error to render.
func renderWriteError(e *WriteError) []byte {
	body := e.Message + "\n"
	if !e.Timestamp.IsZero() {
		body += "Time: " + e.Timestamp.Format(time.RFC3339) + "\n"
	}
	return []byte(body)
}

func (lfs *LinearFS) lookupErrorFile(ctx context.Context, parent fs.InodeEmbedder, entityID string, out *fuse.EntryOut) *fs.Inode {
	render := func(context.Context) ([]byte, time.Time, time.Time) {
		if e := lfs.GetWriteError(entityID); e != nil {
			return renderWriteError(e), e.Timestamp, e.Timestamp
		}
		return nil, time.Time{}, time.Time{}
	}
	return lfs.mountRenderFile(ctx, parent, ".error", render, errorIno(entityID), mountDefaultTimeout, out)
}
