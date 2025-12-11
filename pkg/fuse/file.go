package fuse

import (
	"context"
	"log"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/cache"
	"github.com/jra3/linear-fuse/pkg/linear"
)

// IssueFileNode represents a single issue file
type IssueFileNode struct {
	fs.Inode
	issue  *linear.Issue
	client *linear.Client
	cache  *cache.Cache
	debug  bool

	// For write support
	content []byte
}

// Ensure IssueFileNode implements necessary interfaces
var _ = (fs.NodeOpener)((*IssueFileNode)(nil))
var _ = (fs.NodeReader)((*IssueFileNode)(nil))
var _ = (fs.NodeWriter)((*IssueFileNode)(nil))
var _ = (fs.NodeGetattrer)((*IssueFileNode)(nil))
var _ = (fs.NodeSetattrer)((*IssueFileNode)(nil))

// Open opens the file
func (n *IssueFileNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	if n.debug {
		log.Printf("Open called for issue: %s", n.issue.Identifier)
	}
	return nil, fuse.FOPEN_DIRECT_IO, fs.OK
}

// Read reads the file contents
func (n *IssueFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if n.debug {
		log.Printf("Read called for issue: %s, offset: %d", n.issue.Identifier, off)
	}

	// Generate content if not already generated or if we have pending writes
	if n.content == nil {
		content, err := issueToMarkdown(n.issue)
		if err != nil {
			log.Printf("Failed to convert issue to markdown: %v", err)
			return nil, syscall.EIO
		}
		n.content = []byte(content)
	}

	// Handle offset
	if off >= int64(len(n.content)) {
		return fuse.ReadResultData([]byte{}), fs.OK
	}

	end := int(off) + len(dest)
	if end > len(n.content) {
		end = len(n.content)
	}

	return fuse.ReadResultData(n.content[off:end]), fs.OK
}

// Write writes to the file
func (n *IssueFileNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (written uint32, errno syscall.Errno) {
	if n.debug {
		log.Printf("Write called for issue: %s, offset: %d, len: %d", n.issue.Identifier, off, len(data))
	}

	// Initialize content buffer if needed
	if n.content == nil {
		content, err := issueToMarkdown(n.issue)
		if err != nil {
			log.Printf("Failed to convert issue to markdown: %v", err)
			return 0, syscall.EIO
		}
		n.content = []byte(content)
	}

	// Extend buffer if necessary
	newSize := int(off) + len(data)
	if newSize > len(n.content) {
		newContent := make([]byte, newSize)
		copy(newContent, n.content)
		n.content = newContent
	}

	// Write data
	copy(n.content[off:], data)

	return uint32(len(data)), fs.OK
}

// Getattr gets file attributes
func (n *IssueFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if n.debug {
		log.Printf("Getattr called for issue: %s", n.issue.Identifier)
	}

	// Generate content to get size
	var content []byte
	if n.content != nil {
		content = n.content
	} else {
		contentStr, err := issueToMarkdown(n.issue)
		if err != nil {
			log.Printf("Failed to convert issue to markdown: %v", err)
			return syscall.EIO
		}
		content = []byte(contentStr)
	}

	out.Mode = 0644
	out.Size = uint64(len(content))
	out.Mtime = uint64(n.issue.UpdatedAt.Unix())
	out.Atime = uint64(time.Now().Unix())
	out.Ctime = uint64(n.issue.CreatedAt.Unix())

	return fs.OK
}

// Setattr sets file attributes (e.g., on close with modifications)
func (n *IssueFileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if n.debug {
		log.Printf("Setattr called for issue: %s", n.issue.Identifier)
	}

	// If we have modified content, parse and update the issue
	if n.content != nil {
		// Parse the markdown and update the issue
		updates, err := parseMarkdownToIssue(string(n.content))
		if err != nil {
			log.Printf("Failed to parse markdown: %v", err)
			return syscall.EIO
		}

		if len(updates) > 0 {
			updatedIssue, err := n.client.UpdateIssue(n.issue.ID, updates)
			if err != nil {
				log.Printf("Failed to update issue: %v", err)
				return syscall.EIO
			}

			// Update cached issue
			n.issue = updatedIssue
			n.cache.Set(updatedIssue.ID, updatedIssue)

			// Clear content buffer so it will be regenerated on next read
			n.content = nil
		}
	}

	return n.Getattr(ctx, f, out)
}
