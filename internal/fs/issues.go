package fs

import (
	"context"
	"hash/fnv"
	"log"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// issueIno generates a stable inode number from an issue ID
func issueIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(issueID))
	return h.Sum64()
}

// IssueNode represents an issue file (e.g., ENG-123.md)
type IssueNode struct {
	fs.Inode
	lfs   *LinearFS
	issue api.Issue

	// Write buffer and cached content
	mu           sync.Mutex
	content      []byte
	contentReady bool
	dirty        bool
}

var _ fs.NodeGetattrer = (*IssueNode)(nil)
var _ fs.NodeOpener = (*IssueNode)(nil)
var _ fs.NodeReader = (*IssueNode)(nil)
var _ fs.NodeWriter = (*IssueNode)(nil)
var _ fs.NodeFlusher = (*IssueNode)(nil)
var _ fs.NodeSetattrer = (*IssueNode)(nil)

// ensureContent generates markdown content if not already cached
func (i *IssueNode) ensureContent() error {
	if i.contentReady {
		return nil
	}
	content, err := marshal.IssueToMarkdown(&i.issue)
	if err != nil {
		return err
	}
	i.content = content
	i.contentReady = true
	return nil
}

func (i *IssueNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := i.ensureContent(); err != nil {
		return syscall.EIO
	}

	out.Mode = 0644
	out.Size = uint64(len(i.content))
	out.SetTimes(nil, &i.issue.UpdatedAt, &i.issue.CreatedAt)

	return 0
}

func (i *IssueNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Use kernel caching for better performance
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (i *IssueNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := i.ensureContent(); err != nil {
		return nil, syscall.EIO
	}

	if off >= int64(len(i.content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(i.content)) {
		end = int64(len(i.content))
	}

	return fuse.ReadResultData(i.content[off:end]), 0
}

func (i *IssueNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.lfs.debug {
		log.Printf("Write: %s offset=%d len=%d", i.issue.Identifier, off, len(data))
	}

	// Initialize content buffer if needed
	if err := i.ensureContent(); err != nil {
		return 0, syscall.EIO
	}

	// Expand buffer if needed
	newLen := int(off) + len(data)
	if newLen > len(i.content) {
		newContent := make([]byte, newLen)
		copy(newContent, i.content)
		i.content = newContent
	}

	// Write data at offset
	copy(i.content[off:], data)
	i.dirty = true

	return uint32(len(data)), 0
}

func (i *IssueNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Handle truncate
	if sz, ok := in.GetSize(); ok {
		if i.lfs.debug {
			log.Printf("Setattr truncate: %s size=%d", i.issue.Identifier, sz)
		}

		if err := i.ensureContent(); err != nil {
			return syscall.EIO
		}

		if int(sz) < len(i.content) {
			i.content = i.content[:sz]
		} else if int(sz) > len(i.content) {
			newContent := make([]byte, sz)
			copy(newContent, i.content)
			i.content = newContent
		}
		i.dirty = true
	}

	out.Mode = 0644
	if i.content != nil {
		out.Size = uint64(len(i.content))
	}

	return 0
}

func (i *IssueNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.dirty || i.content == nil {
		return 0
	}

	if i.lfs.debug {
		log.Printf("Flush: %s (saving changes)", i.issue.Identifier)
	}

	// Parse the modified content and compute updates
	updates, err := marshal.MarkdownToIssueUpdate(i.content, &i.issue)
	if err != nil {
		log.Printf("Failed to parse changes for %s: %v", i.issue.Identifier, err)
		return syscall.EIO
	}

	if len(updates) == 0 {
		if i.lfs.debug {
			log.Printf("Flush: %s no changes detected", i.issue.Identifier)
		}
		i.dirty = false
		return 0
	}

	// Resolve status name to state ID if needed
	if stateName, ok := updates["stateId"].(string); ok {
		stateID, err := i.lfs.ResolveStateID(ctx, i.issue.Team.ID, stateName)
		if err != nil {
			log.Printf("Failed to resolve state '%s': %v", stateName, err)
			return syscall.EIO
		}
		updates["stateId"] = stateID
	}

	// Resolve assignee email/name to user ID if needed
	if assigneeID, ok := updates["assigneeId"].(string); ok {
		userID, err := i.lfs.ResolveUserID(ctx, assigneeID)
		if err != nil {
			log.Printf("Failed to resolve assignee '%s': %v", assigneeID, err)
			return syscall.EIO
		}
		updates["assigneeId"] = userID
	}

	// Call Linear API to update
	if err := i.lfs.client.UpdateIssue(ctx, i.issue.ID, updates); err != nil {
		log.Printf("Failed to update issue %s: %v", i.issue.Identifier, err)
		return syscall.EIO
	}

	if i.lfs.debug {
		log.Printf("Flush: %s updated successfully", i.issue.Identifier)
	}

	// Invalidate caches so next read gets fresh data
	if i.issue.Team != nil {
		i.lfs.InvalidateTeamIssues(i.issue.Team.ID)
	}
	i.lfs.InvalidateMyIssues()
	if i.issue.Assignee != nil {
		i.lfs.InvalidateUserIssues(i.issue.Assignee.ID)
	}

	i.dirty = false
	i.contentReady = false // Force re-generate on next read

	return 0
}
