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
	return n.collection().readdir(ctx)
}

// collection is the item-file surface (Readdir/Lookup/Unlink) for comments/.
func (n *CommentsNode) collection() collectionDir[api.Comment] {
	return collectionDir[api.Comment]{
		parent:       n,
		lfs:          n.lfs,
		trio:         n.trio(),
		noun:         "comment",
		refresh:      func(ctx context.Context) { n.lfs.repo.MaybeRefreshIssueDetails(n.issueID) },
		fetch:        func(ctx context.Context) ([]api.Comment, error) { return n.lfs.repo.GetIssueComments(ctx, n.issueID) },
		listing:      func(items []api.Comment) collectionListing[api.Comment] { return n.listing(items) },
		idOf:         func(c api.Comment) string { return c.ID },
		buildFile:    n.buildComment,
		metaMarshal:  marshal.CommentMetaToMarkdown,
		metaTimes:    func(c api.Comment) (time.Time, time.Time) { return c.UpdatedAt, c.CreatedAt },
		metaIno:      func(c api.Comment) uint64 { return commentMetaIno(c.ID) },
		deleteMutate: func(ctx context.Context, c *api.Comment) error { return n.lfs.mutator().DeleteComment(ctx, c.ID) },
		deleteForget: func(ctx context.Context, c *api.Comment) error { return n.lfs.store.Queries().DeleteComment(ctx, c.ID) },
	}
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
	return n.collection().lookup(ctx, name, out)
}

// buildComment mounts the read/write CommentNode for an existing comment.
func (n *CommentsNode) buildComment(ctx context.Context, name string, comment api.Comment, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
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

func (n *CommentsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.collection().unlink(ctx, name)
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
	commentErrKey := collectionErrorKey("comments", n.issueID)
	// body + updatedComment bridge the front half to the commit tail (mutate runs
	// first, then commitWriteBack fetches the echoed response and compares body).
	var body string
	var updatedComment *api.Comment
	return editFlush(ctx, n.lfs, &n.editBuffer, editFlushSpec[api.Comment]{
		mutate: func(ctx context.Context) (bool, syscall.Errno) {
			// Extract body from the markdown (skip frontmatter).
			body = extractCommentBody(n.content)
			if body == "" {
				if n.lfs.debug {
					log.Printf("Flush comment %s: empty body, skipping", n.comment.ID)
				}
				return false, 0
			}
			if body == n.comment.Body {
				if n.lfs.debug {
					log.Printf("Flush comment %s: no changes", n.comment.ID)
				}
				return false, 0
			}
			if n.lfs.debug {
				log.Printf("Updating comment %s", n.comment.ID)
			}
			var err error
			updatedComment, err = n.lfs.UpdateComment(ctx, n.issueID, n.comment.ID, body)
			if err != nil {
				log.Printf("Failed to update comment: %v", err)
				msg, errno := classifyMutationErr("update comment", err)
				n.lfs.SetWriteError(commentErrKey, msg)
				return false, errno
			}
			return true, 0
		},
		// Edit-commit tail: verify read-your-writes against the API's echoed
		// response, persist, and surface divergence via .error.
		writeBack: writeBackSpec[api.Comment]{
			errKey: commentErrKey,
			fetch:  func(ctx context.Context) (*api.Comment, error) { return updatedComment, nil },
			persist: func(ctx context.Context, fresh *api.Comment) error {
				return n.lfs.UpsertComment(ctx, n.issueID, *fresh)
			},
			compare: func(fresh *api.Comment) []writeBackResult {
				return []writeBackResult{writeBackDivergence("comment body", body, fresh.Body, n.comment.Body)}
			},
		},
		adopt:     func(fresh *api.Comment) { n.comment = *fresh },
		coherence: []uint64{commentIno(n.comment.ID), commentMetaIno(n.comment.ID)},
	})
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
