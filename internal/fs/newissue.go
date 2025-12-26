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
	content []byte
	created bool
}

var _ fs.NodeGetattrer = (*NewIssueNode)(nil)
var _ fs.NodeOpener = (*NewIssueNode)(nil)
var _ fs.NodeReader = (*NewIssueNode)(nil)
var _ fs.NodeWriter = (*NewIssueNode)(nil)
var _ fs.NodeFlusher = (*NewIssueNode)(nil)
var _ fs.NodeSetattrer = (*NewIssueNode)(nil)

func (n *NewIssueNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	out.Mode = 0644
	out.Size = uint64(len(n.content))
	out.SetTimes(&now, &now, &now)

	return 0
}

func (n *NewIssueNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewIssueNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if off >= int64(len(n.content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(n.content)) {
		end = int64(len(n.content))
	}

	return fuse.ReadResultData(n.content[off:end]), 0
}

func (n *NewIssueNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("NewIssueNode.Write: offset=%d len=%d", off, len(data))
	}

	// Expand buffer if needed
	newLen := int(off) + len(data)
	if newLen > len(n.content) {
		newContent := make([]byte, newLen)
		copy(newContent, n.content)
		n.content = newContent
	}

	copy(n.content[off:], data)

	return uint32(len(data)), 0
}

func (n *NewIssueNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if int(sz) < len(n.content) {
			n.content = n.content[:sz]
		} else if int(sz) > len(n.content) {
			newContent := make([]byte, sz)
			copy(newContent, n.content)
			n.content = newContent
		}
	}

	out.Mode = 0644
	out.Size = uint64(len(n.content))

	return 0
}

func (n *NewIssueNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created {
		return 0
	}

	if len(n.content) == 0 {
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
	input, err := n.parseContent()
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

func (n *NewIssueNode) parseContent() (map[string]any, error) {
	input := make(map[string]any)

	// If content has frontmatter, parse it
	if len(n.content) > 0 {
		doc, err := marshal.Parse(n.content)
		if err != nil {
			// If parsing fails, treat entire content as description with title from filename
			input["title"] = n.title
			input["description"] = string(n.content)
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
