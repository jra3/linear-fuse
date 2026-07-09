package fs

import (
	"context"
	"fmt"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
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

	items := n.listing(comments).entries()
	entries := append(n.trio().entries(), items...)
	entries = append(entries, metaSidecarEntries(items)...)
	return fs.NewListDirStream(entries), 0
}

// trio declares the comments collection's writable surfaces.
func (n *CommentsNode) trio() collectionTrio {
	return collectionTrio{kind: "comments", parentID: n.issueID, onFlush: n.createComment}
}

// listing declares how comment files are named — <NNNN>-<date-time>.md by
// creation order — so Readdir, Lookup, and Unlink derive identical names.
func (n *CommentsNode) listing(comments []api.Comment) indexedListing[api.Comment] {
	return indexedListing[api.Comment]{
		items:   comments,
		lessKey: func(c api.Comment) time.Time { return c.CreatedAt },
		nameOf: func(i int, c api.Comment) string {
			return fmt.Sprintf("%04d-%s.md", i+1, c.CreatedAt.Format("2006-01-02T15-04"))
		},
	}
}

func (n *CommentsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
	}

	comments, err := n.lfs.GetIssueComments(ctx, n.issueID)
	if err != nil {
		return nil, syscall.EIO
	}

	// "{base}.meta" shadows "{base}.md": the read-only server-managed sidecar.
	if mdName, ok := metaSidecarSource(name); ok {
		comment, found := n.listing(comments).find(mdName)
		if !found {
			return nil, syscall.ENOENT
		}
		return n.lfs.mountRenderFile(ctx, n, name, n.commentMetaRender(comment), commentMetaIno(comment.ID), 0, out), 0
	}

	comment, ok := n.listing(comments).find(name)
	if !ok {
		return nil, syscall.ENOENT
	}
	content := marshal.CommentToMarkdown(&comment)
	node := &CommentNode{
		BaseNode:   BaseNode{lfs: n.lfs},
		issueID:    n.issueID,
		comment:    comment,
		editBuffer: editBuffer{content: content},
	}
	// Shorter timeout for writable files.
	return n.newFileInode(ctx, out, name, node, fileAttr(len(content), comment.CreatedAt, comment.UpdatedAt), commentIno(comment.ID), 5*time.Second), 0
}

// commentMetaRender returns the render closure behind a comment's .meta
// sidecar: re-derive the freshest comment on every read (renderFile is
// DIRECT_IO, so baked bytes would go stale for the life of the mount) and
// report its real times.
func (n *CommentsNode) commentMetaRender(comment api.Comment) renderFunc {
	lfs, issueID := n.lfs, n.issueID
	return func(ctx context.Context) ([]byte, time.Time, time.Time) {
		cur := comment
		if comments, err := lfs.GetIssueComments(ctx, issueID); err == nil {
			for _, c := range comments {
				if c.ID == comment.ID {
					cur = c
					break
				}
			}
		}
		b, err := marshal.CommentMetaToMarkdown(&cur)
		if err != nil {
			return nil, cur.UpdatedAt, cur.CreatedAt
		}
		return b, cur.UpdatedAt, cur.CreatedAt
	}
}

func (n *CommentsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Unlink comment: %s", name)
	}

	// Don't allow deleting _create
	if name == "_create" {
		return syscall.EPERM
	}

	// The .meta sidecar is a read-only virtual file; it vanishes with its
	// entity (rm the .md), never on its own.
	if _, isMeta := metaSidecarSource(name); isMeta {
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
			if c, ok := n.listing(comments).find(name); ok {
				return &c, nil
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
		// The .meta sidecar renders from the deleted entity: drop its entry too.
		invalidateExtra: func(c *api.Comment) {
			n.lfs.InvalidateDeleted(commentsDirIno(n.issueID), metaSidecarName(name))
		},
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
	editBuffer
	issueID string
	comment api.Comment
}

var _ fs.NodeGetattrer = (*CommentNode)(nil)
var _ fs.NodeOpener = (*CommentNode)(nil)
var _ fs.NodeReader = (*CommentNode)(nil)
var _ fs.NodeWriter = (*CommentNode)(nil)
var _ fs.NodeFlusher = (*CommentNode)(nil)
var _ fs.NodeFsyncer = (*CommentNode)(nil)
var _ fs.NodeSetattrer = (*CommentNode)(nil)

func (n *CommentNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	// One lock for size + times: a concurrent refresh (refresh.go) swaps
	// content and entity atomically, so the read must snapshot both together.
	n.mu.Lock()
	size := len(n.content)
	created, updated := n.comment.CreatedAt, n.comment.UpdatedAt
	n.mu.Unlock()
	fileAttr(size, created, updated).fill(&out.Attr, &n.BaseNode)
	return 0
}

// refreshFrom adopts a fresh twin's comment and rendered content unless an
// edit is in flight — the dirty buffer always wins (refresh.go).
func (n *CommentNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*CommentNode); ok {
		n.refresh(f.content, func() { n.comment, n.issueID = f.comment, f.issueID })
	}
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
		msg, errno := classifyMutationErr("update comment", err)
		n.lfs.SetWriteError(commentErrKey, msg)
		return errno
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

	// Invalidate kernel cache for this comment file and its .meta sidecar
	// (updated/edited reflect the edit)
	n.lfs.InvalidateUpdated(commentIno(n.comment.ID))
	n.lfs.InvalidateUpdated(commentMetaIno(n.comment.ID))

	if fresh != nil {
		n.comment = *fresh
	}
	n.dirty = false
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
