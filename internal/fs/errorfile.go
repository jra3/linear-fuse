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
// the most recent write succeeded), keyed by entityID. It is a plain renderFile
// with zero timeouts, so it always reflects the most recent write rather than a
// stale cached (often empty) value; the reported time is when the error was set.
func (lfs *LinearFS) lookupErrorFile(ctx context.Context, parent fs.InodeEmbedder, entityID string, out *fuse.EntryOut) *fs.Inode {
	render := func() ([]byte, time.Time, time.Time) {
		if e := lfs.GetWriteError(entityID); e != nil {
			return []byte(e.Message + "\n"), e.Timestamp, e.Timestamp
		}
		return nil, time.Time{}, time.Time{}
	}
	return lfs.mountRenderFile(ctx, parent, render, errorIno(entityID), 0, out)
}
