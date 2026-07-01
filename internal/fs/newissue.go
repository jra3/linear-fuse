package fs

import (
	"context"
	"log"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// NewIssueNode represents a new issue file being created
type NewIssueNode struct {
	fs.Inode
	lfs    *LinearFS
	teamID string
	title  string

	mu      sync.Mutex
	content contentBuffer
	created bool
}

var _ fs.NodeGetattrer = (*NewIssueNode)(nil)
var _ fs.NodeOpener = (*NewIssueNode)(nil)
var _ fs.NodeReader = (*NewIssueNode)(nil)
var _ fs.NodeWriter = (*NewIssueNode)(nil)
var _ fs.NodeFlusher = (*NewIssueNode)(nil)
var _ fs.NodeFsyncer = (*NewIssueNode)(nil)
var _ fs.NodeSetattrer = (*NewIssueNode)(nil)

func (n *NewIssueNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	out.Mode = 0644
	sz, err := n.content.size()
	if err != nil {
		return syscall.EIO
	}
	out.Size = uint64(sz)
	out.SetTimes(&now, &now, &now)

	return 0
}

func (n *NewIssueNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewIssueNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	b, err := n.content.bytes()
	if err != nil {
		return nil, syscall.EIO
	}

	if off >= int64(len(b)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(b)) {
		end = int64(len(b))
	}

	return fuse.ReadResultData(b[off:end]), 0
}

func (n *NewIssueNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	w, err := n.content.writeAt(off, data)
	if err != nil {
		return 0, syscall.EIO
	}
	return uint32(w), 0
}

func (n *NewIssueNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if err := n.content.truncate(int64(sz)); err != nil {
			return syscall.EIO
		}
	}

	out.Mode = 0644
	sz, err := n.content.size()
	if err != nil {
		return syscall.EIO
	}
	out.Size = uint64(sz)

	return 0
}

// Fsync is a no-op; actual persistence happens in Flush. It must be
// implemented (not return ENOTSUP) so editors that write-then-fsync
// (e.g. Claude Code's Edit tool, vim, VS Code) can save the _create file.
func (n *NewIssueNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

func (n *NewIssueNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created {
		return 0
	}

	b, err := n.content.bytes()
	if err != nil {
		return syscall.EIO
	}

	if len(b) == 0 {
		if n.lfs.debug {
			log.Printf("NewIssueNode.Flush: empty content, skipping")
		}
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if n.lfs.debug {
		log.Printf("NewIssueNode.Flush: creating issue")
	}

	// Parse content to extract issue data
	input, err := n.parseContent(b)
	if err != nil {
		log.Printf("Failed to parse new issue content: %v", err)
		return syscall.EIO
	}

	// Add team ID
	input["teamId"] = n.teamID

	// Create the issue
	issue, err := n.lfs.client.CreateIssue(ctx, input)
	if err != nil {
		log.Printf("Failed to create issue: %v", err)
		return syscall.EIO
	}

	// Upsert to SQLite so it's immediately visible
	if err := n.lfs.UpsertIssue(ctx, *issue); err != nil {
		log.Printf("Warning: failed to upsert issue to SQLite: %v", err)
	}

	if n.lfs.debug {
		log.Printf("Created issue: %s", issue.Identifier)
	}

	// Invalidate cache
	n.lfs.InvalidateTeamIssues(n.teamID)
	n.created = true

	return 0
}

func (n *NewIssueNode) parseContent(b []byte) (map[string]any, error) {
	input := make(map[string]any)

	// If content has frontmatter, parse it
	if len(b) > 0 {
		doc, err := marshal.Parse(b)
		if err != nil {
			// If parsing fails, treat entire content as description with title from filename
			input["title"] = n.title
			input["description"] = string(b)
			return input, nil
		}

		// Extract title from frontmatter or use filename
		if title, ok := doc.Frontmatter["title"].(string); ok && title != "" {
			input["title"] = title
		} else {
			input["title"] = n.title
		}

		// Extract description from body
		if doc.Body != "" {
			input["description"] = doc.Body
		}

		// Extract priority
		if priority, ok := doc.Frontmatter["priority"].(string); ok {
			input["priority"] = priorityValue(priority)
		}

		return input, nil
	}

	// Empty content - just use title from filename
	input["title"] = n.title
	return input, nil
}

func priorityValue(name string) int {
	switch name {
	case "urgent":
		return 1
	case "high":
		return 2
	case "medium":
		return 3
	case "low":
		return 4
	default:
		return 0
	}
}
