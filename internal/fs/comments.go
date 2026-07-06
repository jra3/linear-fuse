package fs

import (
	"bytes"
	"context"
	"fmt"
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

// CommentsNode represents /teams/{KEY}/issues/{ID}/comments/
type CommentsNode struct {
	attrNode
	issueID string
	teamID  string
}

var _ fs.NodeReaddirer = (*CommentsNode)(nil)
var _ fs.NodeLookuper = (*CommentsNode)(nil)
var _ fs.NodeCreater = (*CommentsNode)(nil)
var _ fs.NodeUnlinker = (*CommentsNode)(nil)
var _ fs.NodeGetattrer = (*CommentsNode)(nil)

func (n *CommentsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Trigger background refresh of sub-resources if stale
	n.lfs.MaybeRefreshIssueDetails(n.issueID)

	// Fetch comments (uses cache if available)
	comments, err := n.lfs.GetIssueComments(ctx, n.issueID)
	if err != nil {
		// On error, still serve the trio
		return fs.NewListDirStream(n.trio().entries()), 0
	}

	entries := n.trio().entries()

	// Sort comments by creation time
	sort.Slice(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})

	for i, comment := range comments {
		// Format: 001-2025-01-10T14:30.md
		timestamp := comment.CreatedAt.Format("2006-01-02T15-04")
		entries = append(entries, fuse.DirEntry{
			Name: fmt.Sprintf("%04d-%s.md", i+1, timestamp),
			Mode: syscall.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), 0
}

// trio declares the comments collection's writable surfaces.
func (n *CommentsNode) trio() collectionTrio {
	return collectionTrio{kind: "comments", parentID: n.issueID, onFlush: n.createComment}
}

func (n *CommentsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
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
		expectedName := fmt.Sprintf("%04d-%s.md", i+1, timestamp)
		if expectedName == name {
			content := commentToMarkdown(&comment)
			node := &CommentNode{
				BaseNode:     BaseNode{lfs: n.lfs},
				issueID:      n.issueID,
				comment:      comment,
				content:      content,
				contentReady: true,
			}
			// Shorter timeout for writable files.
			return n.newFileInode(ctx, out, node, fileAttr(len(content), comment.CreatedAt, comment.UpdatedAt), commentIno(comment.ID), 5*time.Second), 0
		}
	}

	return nil, syscall.ENOENT
}

func (n *CommentsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Unlink comment: %s", name)
	}

	// Don't allow deleting _create
	if name == "_create" {
		return syscall.EPERM
	}

	return commitDelete(ctx, n.lfs, deleteSpec[api.Comment]{
		op:  `delete comment "` + name + `"`,
		key: collectionErrorKey("comments", n.issueID),
		find: func(ctx context.Context) (*api.Comment, error) {
			comments, err := n.lfs.GetIssueComments(ctx, n.issueID)
			if err != nil {
				return nil, err
			}
			// Filenames are index-derived from creation order.
			sort.Slice(comments, func(i, j int) bool {
				return comments[i].CreatedAt.Before(comments[j].CreatedAt)
			})
			for i, comment := range comments {
				timestamp := comment.CreatedAt.Format("2006-01-02T15-04")
				if fmt.Sprintf("%04d-%s.md", i+1, timestamp) == name {
					return &comment, nil
				}
			}
			return nil, nil
		},
		mutate: func(ctx context.Context, c *api.Comment) error {
			return n.lfs.mutator().DeleteComment(ctx, c.ID)
		},
		forget: func(ctx context.Context, c *api.Comment) error {
			return n.lfs.store.Queries().DeleteComment(ctx, c.ID)
		},
		dir:  commentsDirIno(n.issueID),
		name: name,
	})
}

func (n *CommentsNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create comment file: %s", name)
	}

	// Only allow creating .md files
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}

	node := newCreateFile(n.lfs, n.createComment)
	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})

	return inode, &createFileHandle{}, fuse.FOPEN_DIRECT_IO, 0
}

// CommentNode represents a single comment file (read-write)
type CommentNode struct {
	BaseNode
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
var _ fs.NodeFsyncer = (*CommentNode)(nil)
var _ fs.NodeSetattrer = (*CommentNode)(nil)

func (n *CommentNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	out.Mode = 0644
	n.SetOwner(out)
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

// Fsync is a no-op; actual persistence happens in Flush. It must be
// implemented (not return ENOTSUP) so editors that write-then-fsync
// (e.g. Claude Code's Edit tool, vim, VS Code) can save comment edits.
func (n *CommentNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

func (n *CommentNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.dirty || n.content == nil {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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

	commentErrKey := collectionErrorKey("comments", n.issueID)
	updatedComment, err := n.lfs.UpdateComment(ctx, n.issueID, n.comment.ID, body)
	if err != nil {
		log.Printf("Failed to update comment: %v", err)
		n.lfs.SetWriteError(commentErrKey, "Operation: update comment\nError: "+err.Error())
		return syscall.EIO
	}

	// Edit-commit tail: verify read-your-writes against the API's echoed response,
	// persist, and surface divergence via .error.
	fresh, errno := commitWriteBack(ctx, n.lfs, writeBackSpec[api.Comment]{
		errKey: commentErrKey,
		fetch:  func(ctx context.Context) (*api.Comment, error) { return updatedComment, nil },
		persist: func(ctx context.Context, fresh *api.Comment) error {
			return n.lfs.UpsertComment(ctx, n.issueID, *fresh)
		},
		compare: func(fresh *api.Comment) []writeBackResult {
			return []writeBackResult{writeBackDivergence("comment body", body, fresh.Body, n.comment.Body)}
		},
	})

	// Invalidate kernel cache for this comment file
	n.lfs.InvalidateUpdated(commentIno(n.comment.ID))

	if fresh != nil {
		n.comment = *fresh
	}
	n.dirty = false
	n.contentReady = false // Force regenerate on next read
	return errno
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

// createComment is the comments create surface's onFlush: parse the body and
// run the create tail.
func (n *CommentsNode) createComment(ctx context.Context, content []byte) syscall.Errno {
	body := strings.TrimSpace(string(content))
	if body == "" {
		return 0
	}

	_, errno := commitCreate(ctx, n.lfs, createSpec[api.Comment]{
		op:  "create comment",
		key: collectionErrorKey("comments", n.issueID),
		mutate: func(ctx context.Context) (*api.Comment, error) {
			return n.lfs.mutator().CreateComment(ctx, n.issueID, body)
		},
		// Comments are addressed by an index-derived filename (not knowable
		// without re-listing), so .last reports the comment id + a body
		// snippet as the handle, and entryName stays unknowable.
		result: func(c *api.Comment) WriteResult {
			return WriteResult{
				Identifier: c.ID,
				Title:      firstLine(c.Body),
			}
		},
		persist: func(ctx context.Context, c *api.Comment) error {
			return n.lfs.UpsertComment(ctx, n.issueID, *c)
		},
		dir: commentsDirIno(n.issueID),
	})
	return errno
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
