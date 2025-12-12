package fs

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"gopkg.in/yaml.v3"
)

// commentsDirIno generates a stable inode number for a comments directory
func commentsDirIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("comments:" + issueID))
	return h.Sum64()
}

// commentIno generates a stable inode number for a comment
func commentIno(commentID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("comment:" + commentID))
	return h.Sum64()
}

// CommentsNode represents /teams/{KEY}/issues/{ID}/comments/
type CommentsNode struct {
	fs.Inode
	lfs     *LinearFS
	issueID string
	teamID  string
}

var _ fs.NodeReaddirer = (*CommentsNode)(nil)
var _ fs.NodeLookuper = (*CommentsNode)(nil)
var _ fs.NodeCreater = (*CommentsNode)(nil)
var _ fs.NodeUnlinker = (*CommentsNode)(nil)

func (n *CommentsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	comments, err := n.lfs.GetIssueComments(ctx, n.issueID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Sort comments by creation time
	sort.Slice(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})

	entries := make([]fuse.DirEntry, len(comments))
	for i, comment := range comments {
		// Format: 001-2025-01-10T14:30.md
		timestamp := comment.CreatedAt.Format("2006-01-02T15-04")
		entries[i] = fuse.DirEntry{
			Name: fmt.Sprintf("%03d-%s.md", i+1, timestamp),
			Mode: syscall.S_IFREG,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (n *CommentsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle new.md for creating comments
	if name == "new.md" {
		node := &NewCommentNode{
			lfs:     n.lfs,
			issueID: n.issueID,
			teamID:  n.teamID,
		}
		out.Attr.Mode = 0644 | syscall.S_IFREG
		out.Attr.Size = 0
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
		}), 0
	}

	comments, err := n.lfs.GetIssueComments(ctx, n.issueID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Sort comments by creation time
	sort.Slice(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})

	// Match by filename pattern
	for i, comment := range comments {
		timestamp := comment.CreatedAt.Format("2006-01-02T15-04")
		expectedName := fmt.Sprintf("%03d-%s.md", i+1, timestamp)
		if expectedName == name {
			content := commentToMarkdown(&comment)
			node := &CommentNode{
				lfs:          n.lfs,
				issueID:      n.issueID,
				comment:      comment,
				content:      content,
				contentReady: true,
			}
			out.Attr.Mode = 0644 | syscall.S_IFREG // Read-write
			out.Attr.Size = uint64(len(content))
			out.SetAttrTimeout(30 * time.Second)
			out.SetEntryTimeout(30 * time.Second)
			// Use updatedAt for mtime (comments can be edited)
			out.Attr.SetTimes(&comment.UpdatedAt, &comment.UpdatedAt, &comment.CreatedAt)
			return n.NewInode(ctx, node, fs.StableAttr{
				Mode: syscall.S_IFREG,
				Ino:  commentIno(comment.ID),
			}), 0
		}
	}

	return nil, syscall.ENOENT
}

func (n *CommentsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Unlink comment: %s", name)
	}

	// Don't allow deleting new.md
	if name == "new.md" {
		return syscall.EPERM
	}

	comments, err := n.lfs.GetIssueComments(ctx, n.issueID)
	if err != nil {
		return syscall.EIO
	}

	// Sort comments by creation time
	sort.Slice(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})

	// Find the comment by filename
	for i, comment := range comments {
		timestamp := comment.CreatedAt.Format("2006-01-02T15-04")
		expectedName := fmt.Sprintf("%03d-%s.md", i+1, timestamp)
		if expectedName == name {
			err := n.lfs.DeleteComment(ctx, n.issueID, comment.ID)
			if err != nil {
				log.Printf("Failed to delete comment: %v", err)
				return syscall.EIO
			}
			if n.lfs.debug {
				log.Printf("Comment deleted successfully")
			}
			return 0
		}
	}

	return syscall.ENOENT
}

func (n *CommentsNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create comment file: %s", name)
	}

	// Only allow creating .md files
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}

	node := &NewCommentNode{
		lfs:     n.lfs,
		issueID: n.issueID,
		teamID:  n.teamID,
	}

	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})

	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
}

// CommentNode represents a single comment file (read-write)
type CommentNode struct {
	fs.Inode
	lfs     *LinearFS
	issueID string
	comment api.Comment

	mu           sync.Mutex
	content      []byte
	contentReady bool
	dirty        bool
}

var _ fs.NodeGetattrer = (*CommentNode)(nil)
var _ fs.NodeOpener = (*CommentNode)(nil)
var _ fs.NodeReader = (*CommentNode)(nil)
var _ fs.NodeWriter = (*CommentNode)(nil)
var _ fs.NodeFlusher = (*CommentNode)(nil)
var _ fs.NodeSetattrer = (*CommentNode)(nil)

func (n *CommentNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	out.Mode = 0644
	out.Size = uint64(len(n.content))
	out.SetTimes(&n.comment.UpdatedAt, &n.comment.UpdatedAt, &n.comment.CreatedAt)
	return 0
}

func (n *CommentNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *CommentNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
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

func (n *CommentNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write comment %s: offset=%d len=%d", n.comment.ID, off, len(data))
	}

	// Expand buffer if needed
	newLen := int(off) + len(data)
	if newLen > len(n.content) {
		newContent := make([]byte, newLen)
		copy(newContent, n.content)
		n.content = newContent
	}

	copy(n.content[off:], data)
	n.dirty = true

	return uint32(len(data)), 0
}

func (n *CommentNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if n.lfs.debug {
			log.Printf("Setattr truncate comment %s: size=%d", n.comment.ID, sz)
		}
		if int(sz) < len(n.content) {
			n.content = n.content[:sz]
		} else if int(sz) > len(n.content) {
			newContent := make([]byte, sz)
			copy(newContent, n.content)
			n.content = newContent
		}
		n.dirty = true
	}

	out.Mode = 0644
	out.Size = uint64(len(n.content))
	return 0
}

func (n *CommentNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.dirty || n.content == nil {
		return 0
	}

	// Extract body from the markdown (skip frontmatter)
	body := extractCommentBody(n.content)
	if body == "" {
		if n.lfs.debug {
			log.Printf("Flush comment %s: empty body, skipping", n.comment.ID)
		}
		n.dirty = false
		return 0
	}

	// Only update if body changed
	if body == n.comment.Body {
		if n.lfs.debug {
			log.Printf("Flush comment %s: no changes", n.comment.ID)
		}
		n.dirty = false
		return 0
	}

	if n.lfs.debug {
		log.Printf("Updating comment %s", n.comment.ID)
	}

	_, err := n.lfs.UpdateComment(ctx, n.issueID, n.comment.ID, body)
	if err != nil {
		log.Printf("Failed to update comment: %v", err)
		return syscall.EIO
	}

	n.dirty = false
	n.contentReady = false // Force regenerate on next read

	if n.lfs.debug {
		log.Printf("Comment updated successfully")
	}

	return 0
}

// extractCommentBody extracts the body from markdown with YAML frontmatter
func extractCommentBody(content []byte) string {
	s := string(content)

	// Check for frontmatter
	if !strings.HasPrefix(s, "---\n") {
		return strings.TrimSpace(s)
	}

	// Find end of frontmatter
	end := strings.Index(s[4:], "\n---")
	if end == -1 {
		return strings.TrimSpace(s)
	}

	// Return everything after frontmatter
	body := s[4+end+4:] // Skip "---\n", frontmatter, "\n---"
	return strings.TrimSpace(body)
}

// NewCommentNode handles creating new comments
type NewCommentNode struct {
	fs.Inode
	lfs     *LinearFS
	issueID string
	teamID  string

	mu      sync.Mutex
	content []byte
	created bool
}

var _ fs.NodeGetattrer = (*NewCommentNode)(nil)
var _ fs.NodeOpener = (*NewCommentNode)(nil)
var _ fs.NodeReader = (*NewCommentNode)(nil)
var _ fs.NodeWriter = (*NewCommentNode)(nil)
var _ fs.NodeFlusher = (*NewCommentNode)(nil)
var _ fs.NodeSetattrer = (*NewCommentNode)(nil)

func (n *NewCommentNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	out.Mode = 0644
	out.Size = uint64(len(n.content))
	return 0
}

func (n *NewCommentNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewCommentNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
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

func (n *NewCommentNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write new comment: offset=%d len=%d", off, len(data))
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

func (n *NewCommentNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
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

func (n *NewCommentNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created || len(n.content) == 0 {
		return 0
	}

	body := strings.TrimSpace(string(n.content))
	if body == "" {
		return 0
	}

	if n.lfs.debug {
		log.Printf("Creating comment on issue %s: %s", n.issueID, body)
	}

	_, err := n.lfs.CreateComment(ctx, n.issueID, body)
	if err != nil {
		log.Printf("Failed to create comment: %v", err)
		return syscall.EIO
	}

	n.created = true

	if n.lfs.debug {
		log.Printf("Comment created successfully")
	}

	return 0
}

// commentToMarkdown converts a comment to markdown with YAML frontmatter
func commentToMarkdown(comment *api.Comment) []byte {
	var buf bytes.Buffer

	// Build frontmatter
	frontmatter := map[string]any{
		"id":      comment.ID,
		"created": comment.CreatedAt.Format(time.RFC3339),
		"updated": comment.UpdatedAt.Format(time.RFC3339),
	}

	if comment.EditedAt != nil {
		frontmatter["edited"] = comment.EditedAt.Format(time.RFC3339)
	}

	if comment.User != nil {
		frontmatter["author"] = comment.User.Email
		frontmatter["authorName"] = comment.User.Name
	}

	buf.WriteString("---\n")
	yamlData, _ := yaml.Marshal(frontmatter)
	buf.Write(yamlData)
	buf.WriteString("---\n\n")
	buf.WriteString(comment.Body)
	buf.WriteString("\n")

	return buf.Bytes()
}
