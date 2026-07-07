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
func (lfs *LinearFS) SetWriteError(entityID, message string) {
	lfs.writeErrorsMu.Lock()
	lfs.writeErrors[entityID] = &WriteError{
		Message:   message,
		Timestamp: time.Now(),
	}
	lfs.writeErrorsMu.Unlock()
	// Drop the kernel's cached size/content for the .error file so the next
	// stat/read reflects this error instead of a stale (often empty) value.
	lfs.InvalidateUpdated(errorIno(entityID))
}

// ClearWriteError removes the error for an entity (called on a successful write).
func (lfs *LinearFS) ClearWriteError(entityID string) {
	lfs.writeErrorsMu.Lock()
	_, had := lfs.writeErrors[entityID]
	delete(lfs.writeErrors, entityID)
	lfs.writeErrorsMu.Unlock()
	if had {
		lfs.InvalidateUpdated(errorIno(entityID))
	}
}

// GetWriteError returns the last failed-write message for an entity, or nil.
func (lfs *LinearFS) GetWriteError(entityID string) *WriteError {
	lfs.writeErrorsMu.RLock()
	defer lfs.writeErrorsMu.RUnlock()
	return lfs.writeErrors[entityID]
}

// SetIssueError / ClearIssueError / GetIssueError are issue-flavored aliases for
// the generic write-error store, retained so issue write handlers read clearly.
func (lfs *LinearFS) SetIssueError(issueID, message string) { lfs.SetWriteError(issueID, message) }
func (lfs *LinearFS) ClearIssueError(issueID string)        { lfs.ClearWriteError(issueID) }
func (lfs *LinearFS) GetIssueError(issueID string) *WriteError {
	return lfs.GetWriteError(issueID)
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
