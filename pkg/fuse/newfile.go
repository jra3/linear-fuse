package fuse

import (
	"context"
	"fmt"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/cache"
	"github.com/jra3/linear-fuse/pkg/linear"
)

// NewIssueFileNode represents a newly created issue file that hasn't been synced yet
type NewIssueFileNode struct {
	fs.Inode
	filename string
	client   *linear.Client
	cache    *cache.Cache
	debug    bool
	content  []byte
	created  bool
	issue    *linear.Issue
}

// Ensure NewIssueFileNode implements necessary interfaces
var _ = (fs.NodeOpener)((*NewIssueFileNode)(nil))
var _ = (fs.NodeReader)((*NewIssueFileNode)(nil))
var _ = (fs.NodeWriter)((*NewIssueFileNode)(nil))
var _ = (fs.NodeGetattrer)((*NewIssueFileNode)(nil))
var _ = (fs.NodeFlusher)((*NewIssueFileNode)(nil))

// Open opens the new file
func (n *NewIssueFileNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	if n.debug {
		log.Printf("Open called for new file: %s", n.filename)
	}
	return nil, fuse.FOPEN_DIRECT_IO, fs.OK
}

// Read reads the file contents
func (n *NewIssueFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if n.debug {
		log.Printf("Read called for new file: %s, offset: %d", n.filename, off)
	}

	if n.content == nil {
		return fuse.ReadResultData([]byte{}), fs.OK
	}

	if off >= int64(len(n.content)) {
		return fuse.ReadResultData([]byte{}), fs.OK
	}

	end := int(off) + len(dest)
	if end > len(n.content) {
		end = len(n.content)
	}

	return fuse.ReadResultData(n.content[off:end]), fs.OK
}

// Write writes to the new file
func (n *NewIssueFileNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (written uint32, errno syscall.Errno) {
	if n.debug {
		log.Printf("Write called for new file: %s, offset: %d, len: %d", n.filename, off, len(data))
	}

	// Initialize or extend buffer
	newSize := int(off) + len(data)
	if n.content == nil {
		n.content = make([]byte, newSize)
	} else if newSize > len(n.content) {
		newContent := make([]byte, newSize)
		copy(newContent, n.content)
		n.content = newContent
	}

	copy(n.content[off:], data)

	return uint32(len(data)), fs.OK
}

// Flush is called when the file is closed, this is where we create the issue
func (n *NewIssueFileNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	if n.debug {
		log.Printf("Flush called for new file: %s", n.filename)
	}

	// Only create the issue once
	if n.created || n.content == nil || len(n.content) == 0 {
		return fs.OK
	}

	// Parse the content to extract issue details
	input, err := parseNewIssueContent(string(n.content))
	if err != nil {
		log.Printf("Failed to parse new issue content: %v", err)
		return syscall.EINVAL
	}

	// Create the issue in Linear
	issue, err := n.client.CreateIssue(input)
	if err != nil {
		log.Printf("Failed to create issue: %v", err)
		return syscall.EIO
	}

	n.issue = issue
	n.created = true

	// Add to cache
	n.cache.Set(issue.ID, issue)

	// Clear the list cache so it will be refreshed
	n.cache.Clear()

	log.Printf("Successfully created issue: %s", issue.Identifier)

	return fs.OK
}

// Getattr gets file attributes
func (n *NewIssueFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if n.debug {
		log.Printf("Getattr called for new file: %s", n.filename)
	}

	size := 0
	if n.content != nil {
		size = len(n.content)
	}

	out.Mode = 0644
	out.Size = uint64(size)
	now := uint64(time.Now().Unix())
	out.Mtime = now
	out.Atime = now
	out.Ctime = now

	return fs.OK
}

// parseNewIssueContent parses the content of a new issue file
func parseNewIssueContent(content string) (map[string]interface{}, error) {
	// If content has frontmatter, parse it
	if strings.HasPrefix(content, "---\n") {
		return parseMarkdownToNewIssue(content)
	}

	// Otherwise, treat the entire content as title + description
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty content")
	}

	input := make(map[string]interface{})

	// First line is the title
	title := strings.TrimSpace(lines[0])
	if title == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}
	input["title"] = title

	// Rest is description
	if len(lines) > 1 {
		description := strings.TrimSpace(strings.Join(lines[1:], "\n"))
		if description != "" {
			input["description"] = description
		}
	}

	return input, nil
}
