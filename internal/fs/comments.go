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

func commentsDirIno(issueID string) uint64 { return ino("comments", issueID) }

func commentIno(commentID string) uint64 { return ino("comment", commentID) }

// CommentsNode represents /teams/{KEY}/issues/{ID}/comments/
type CommentsNode struct {
	BaseNode
	issueID        string
	teamID         string
	issueUpdatedAt time.Time
}

var _ fs.NodeReaddirer = (*CommentsNode)(nil)
var _ fs.NodeLookuper = (*CommentsNode)(nil)
var _ fs.NodeCreater = (*CommentsNode)(nil)
var _ fs.NodeUnlinker = (*CommentsNode)(nil)
var _ fs.NodeGetattrer = (*CommentsNode)(nil)

func (n *CommentsNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&n.issueUpdatedAt, &n.issueUpdatedAt, &n.issueUpdatedAt)
	return 0
}

func (n *CommentsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Trigger background refresh of sub-resources if stale
	n.lfs.MaybeRefreshIssueDetails(n.issueID)

	// Fetch comments (uses cache if available)
	comments, err := n.lfs.GetIssueComments(ctx, n.issueID)
	if err != nil {
		// On error, return just _create and .error
		return fs.NewListDirStream([]fuse.DirEntry{
			{Name: "_create", Mode: syscall.S_IFREG},
			{Name: ".error", Mode: syscall.S_IFREG},
		}), 0
	}

	// Always include _create for creating comments and .error for feedback
	entries := []fuse.DirEntry{
		{Name: "_create", Mode: syscall.S_IFREG},
		{Name: ".error", Mode: syscall.S_IFREG},
	}

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

func (n *CommentsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle _create for creating comments
	if name == "_create" {
		now := time.Now()
		node := &NewCommentNode{
			BaseNode: BaseNode{lfs: n.lfs},
			issueID:  n.issueID,
			teamID:   n.teamID,
		}
		out.Attr.Mode = 0200 | syscall.S_IFREG
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = 0
		out.Attr.SetTimes(&now, &now, &now)
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
		}), 0
	}

	// Handle .error feedback file (last failed comment write in this dir)
	if name == ".error" {
		return n.lfs.lookupErrorFile(ctx, n, collectionErrorKey("comments", n.issueID), out), 0
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
				BaseNode: BaseNode{lfs: n.lfs},
				issueID:  n.issueID,
				comment:  comment,
				content:  contentBuffer{buf: content, loaded: true},
			}
			out.Attr.Mode = 0644 | syscall.S_IFREG // Read-write
			out.Attr.Uid = n.lfs.uid
			out.Attr.Gid = n.lfs.gid
			out.Attr.Size = uint64(len(content))
			out.SetAttrTimeout(5 * time.Second)  // Shorter timeout for writable files
			out.SetEntryTimeout(5 * time.Second) // Shorter timeout for writable files
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

	// Don't allow deleting _create
	if name == "_create" {
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
		expectedName := fmt.Sprintf("%04d-%s.md", i+1, timestamp)
		if expectedName == name {
			commentID := comment.ID
			return commitMutation(ctx, n.lfs, mutationSpec{
				errKey:     collectionErrorKey("comments", n.issueID),
				op:         "delete comment " + name,
				persist:    func(ctx context.Context) error { return n.lfs.store.Queries().DeleteComment(ctx, commentID) },
				invalidate: func() { n.lfs.InvalidateDeleted(commentsDirIno(n.issueID), name) },
			}, n.lfs.DeleteComment(ctx, n.issueID, comment.ID))
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
		BaseNode: BaseNode{lfs: n.lfs},
		issueID:  n.issueID,
		teamID:   n.teamID,
	}

	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})

	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
}

// CommentNode represents a single comment file (read-write)
type CommentNode struct {
	BaseNode
	issueID string
	comment api.Comment

	mu      sync.Mutex
	content contentBuffer
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

	sz, err := n.content.size()
	if err != nil {
		return syscall.EIO
	}
	out.Mode = 0644
	n.SetOwner(out)
	out.Size = uint64(sz)
	out.SetTimes(&n.comment.UpdatedAt, &n.comment.UpdatedAt, &n.comment.CreatedAt)
	return 0
}

func (n *CommentNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *CommentNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
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

func (n *CommentNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	w, err := n.content.writeAt(off, data)
	if err != nil {
		return 0, syscall.EIO
	}
	return uint32(w), 0
}

func (n *CommentNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
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
// (e.g. Claude Code's Edit tool, vim, VS Code) can save comment edits.
func (n *CommentNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

func (n *CommentNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.content.isDirty() {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	content, err := n.content.bytes()
	if err != nil {
		return syscall.EIO
	}

	// Extract body from the markdown (skip frontmatter)
	body := extractCommentBody(content)
	if body == "" {
		if n.lfs.debug {
			log.Printf("Flush comment %s: empty body, skipping", n.comment.ID)
		}
		n.content.markClean()
		return 0
	}

	// Only update if body changed
	if body == n.comment.Body {
		if n.lfs.debug {
			log.Printf("Flush comment %s: no changes", n.comment.ID)
		}
		n.content.markClean()
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
	// Eager node with no loader: keep the edited buffer, just clear dirty.
	n.content.markClean()
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

// NewCommentNode handles creating new comments
type NewCommentNode struct {
	BaseNode
	issueID string
	teamID  string

	mu      sync.Mutex
	content contentBuffer
	created bool
}

var _ fs.NodeGetattrer = (*NewCommentNode)(nil)
var _ fs.NodeOpener = (*NewCommentNode)(nil)
var _ fs.NodeReader = (*NewCommentNode)(nil)
var _ fs.NodeWriter = (*NewCommentNode)(nil)
var _ fs.NodeFlusher = (*NewCommentNode)(nil)
var _ fs.NodeFsyncer = (*NewCommentNode)(nil)
var _ fs.NodeSetattrer = (*NewCommentNode)(nil)

func (n *NewCommentNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	sz, err := n.content.size()
	if err != nil {
		return syscall.EIO
	}
	out.Mode = 0200
	n.SetOwner(out)
	out.Size = uint64(sz)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *NewCommentNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewCommentNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// _create is write-only - return permission denied
	return nil, syscall.EACCES
}

func (n *NewCommentNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	w, err := n.content.writeAt(off, data)
	if err != nil {
		return 0, syscall.EIO
	}
	return uint32(w), 0
}

func (n *NewCommentNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if err := n.content.truncate(int64(sz)); err != nil {
			return syscall.EIO
		}
	}

	out.Mode = 0200
	sz, err := n.content.size()
	if err != nil {
		return syscall.EIO
	}
	out.Size = uint64(sz)
	return 0
}

func (n *NewCommentNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created {
		return 0
	}

	b, err := n.content.bytes()
	if err != nil {
		return syscall.EIO
	}
	body := strings.TrimSpace(string(b))
	if body == "" {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if n.lfs.debug {
		log.Printf("Creating comment on issue %s: %s", n.issueID, body)
	}

	comment, err := n.lfs.CreateComment(ctx, n.issueID, body)
	errno := commitMutation(ctx, n.lfs, mutationSpec{
		errKey:     collectionErrorKey("comments", n.issueID),
		op:         "create comment",
		persist:    func(ctx context.Context) error { return n.lfs.UpsertComment(ctx, n.issueID, *comment) },
		invalidate: func() { n.lfs.InvalidateCreated(commentsDirIno(n.issueID), "") },
	}, err)
	if errno != 0 {
		return errno
	}
	n.created = true
	return 0
}

func (n *NewCommentNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	// Fsync is a no-op; actual persistence happens in Flush
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
