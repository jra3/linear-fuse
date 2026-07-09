package marshal

import (
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// CommentToMarkdown renders the editable-only comment .md: the body alone, no
// frontmatter at all. Every frontmatter field a comment file used to carry
// (id, created, updated, edited, author, authorName) is server-managed — the
// flush path discarded edits to them wholesale, a silent no-op — so they live
// in the sibling .meta (see CommentMetaToMarkdown) and the .md is pure body.
// The parse side (fs's extractCommentBody) stays lenient and strips a leading
// frontmatter block, so an agent pasting old-format content still works.
func CommentToMarkdown(comment *api.Comment) []byte {
	return []byte(comment.Body + "\n")
}

// CommentMetaToMarkdown renders the read-only comment .meta sidecar:
// server-managed identity, timestamps, and authorship as a frontmatter-only
// block (empties omitted).
func CommentMetaToMarkdown(comment *api.Comment) ([]byte, error) {
	fm := map[string]any{
		"id":      comment.ID,
		"created": comment.CreatedAt.Format(time.RFC3339),
		"updated": comment.UpdatedAt.Format(time.RFC3339),
	}
	if comment.EditedAt != nil {
		fm["edited"] = comment.EditedAt.Format(time.RFC3339)
	}
	if comment.User != nil {
		fm["author"] = comment.User.Email
		fm["authorName"] = comment.User.Name
	}
	return Render(&Document{Frontmatter: fm})
}
