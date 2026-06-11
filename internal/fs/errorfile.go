package fs

import (
	"context"
	"syscall"
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
	lfs.InvalidateKernelInode(errorIno(entityID))
}

// ClearWriteError removes the error for an entity (called on a successful write).
func (lfs *LinearFS) ClearWriteError(entityID string) {
	lfs.writeErrorsMu.Lock()
	_, had := lfs.writeErrors[entityID]
	delete(lfs.writeErrors, entityID)
	lfs.writeErrorsMu.Unlock()
	if had {
		lfs.InvalidateKernelInode(errorIno(entityID))
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

// lookupErrorFile mounts the `.error` virtual file for an entity as a child of
// parent and fills out the lookup attrs. entityID is the key used with
// SetWriteError. Entry/attr timeouts are short so the file reflects the result
// of the most recent write rather than a stale cached value.
func (lfs *LinearFS) lookupErrorFile(ctx context.Context, parent fs.InodeEmbedder, entityID string, out *fuse.EntryOut) *fs.Inode {
	node := &ErrorFileNode{BaseNode: BaseNode{lfs: lfs}, entityID: entityID}

	size := uint64(0)
	if e := lfs.GetWriteError(entityID); e != nil {
		size = uint64(len(e.Message) + 1) // +1 for trailing newline
	}

	now := time.Now()
	out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
	out.Attr.Uid = lfs.uid
	out.Attr.Gid = lfs.gid
	out.Attr.Size = size
	// Do not cache: a feedback file must always reflect the most recent write.
	// Any nonzero timeout lets the kernel serve a stale (often empty) size and
	// short-circuit the read.
	out.SetAttrTimeout(0)
	out.SetEntryTimeout(0)
	out.Attr.SetTimes(&now, &now, &now)

	return parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  errorIno(entityID),
	})
}

// ErrorFileNode is the read-only `.error` virtual file shown alongside any
// writable entity. Reading it returns the last failed-write message (empty if
// the most recent write succeeded). It is keyed by the entity's Linear ID.
type ErrorFileNode struct {
	BaseNode
	entityID string
}

var _ fs.NodeGetattrer = (*ErrorFileNode)(nil)
var _ fs.NodeOpener = (*ErrorFileNode)(nil)
var _ fs.NodeReader = (*ErrorFileNode)(nil)

func (e *ErrorFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0444 // Read-only
	e.SetOwner(out)

	if writeErr := e.lfs.GetWriteError(e.entityID); writeErr != nil {
		out.Size = uint64(len(writeErr.Message) + 1) // +1 for newline
	} else {
		out.Size = 0
	}

	return 0
}

func (e *ErrorFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// No FOPEN_KEEP_CACHE: the content is volatile and must be re-read each open.
	return nil, 0, 0
}

func (e *ErrorFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	writeErr := e.lfs.GetWriteError(e.entityID)
	if writeErr == nil {
		return fuse.ReadResultData(nil), 0
	}

	content := []byte(writeErr.Message + "\n")

	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}

	return fuse.ReadResultData(content[off:end]), 0
}
