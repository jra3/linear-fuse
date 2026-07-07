package fs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// AttachmentsNode represents the /teams/{KEY}/issues/{ID}/attachments directory
type AttachmentsNode struct {
	attrNode
	issueID string
}

var _ fs.NodeReaddirer = (*AttachmentsNode)(nil)
var _ fs.NodeLookuper = (*AttachmentsNode)(nil)
var _ fs.NodeGetattrer = (*AttachmentsNode)(nil)

func (n *AttachmentsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Trigger background refresh of sub-resources if stale
	n.lfs.MaybeRefreshIssueDetails(n.issueID)

	entries := n.trio().entries()

	// Listing is best-effort: a failed fetch lists that family as empty
	// rather than failing the whole directory.
	listing := n.listing(ctx, nil)
	for _, e := range listing.entries() {
		entries = append(entries, fuse.DirEntry{
			Name: e.name,
			Mode: syscall.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), 0
}

// listing fetches both item families and builds the name-derivation module.
// A failed fetch leaves that family empty; when fetchErr is non-nil it also
// records the first error there (Lookup distinguishes "not found" from
// "couldn't look").
func (n *AttachmentsNode) listing(ctx context.Context, fetchErr *error) attachmentListing {
	files, ferr := n.lfs.GetIssueEmbeddedFiles(ctx, n.issueID)
	attachments, aerr := n.lfs.GetIssueAttachments(ctx, n.issueID)
	if fetchErr != nil {
		if ferr != nil {
			*fetchErr = ferr
		} else if aerr != nil {
			*fetchErr = aerr
		}
	}
	return attachmentListing{embedded: files, external: attachments}
}

// trio declares the attachments collection's writable surfaces.
func (n *AttachmentsNode) trio() collectionTrio {
	return collectionTrio{kind: "attachments", parentID: n.issueID, onFlush: n.createAttachment}
}

func (n *AttachmentsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
	}

	var fetchErr error
	entry, ok := n.listing(ctx, &fetchErr).find(name)
	if !ok {
		if fetchErr != nil {
			return nil, syscall.EIO
		}
		return nil, syscall.ENOENT
	}

	if entry.external != nil {
		return n.createExternalAttachmentNode(ctx, *entry.external, out)
	}

	file := *entry.embedded
	node := &EmbeddedFileNode{
		BaseNode: BaseNode{lfs: n.lfs},
		file:     file,
	}

	// Set initial attributes
	out.Attr.Mode = 0444 | syscall.S_IFREG
	out.Attr.Uid = n.lfs.uid
	out.Attr.Gid = n.lfs.gid
	if file.FileSize > 0 {
		out.Attr.Size = uint64(file.FileSize)
	} else {
		out.Attr.Size = 1024 * 1024 // Placeholder for lazy-fetch
	}
	out.SetAttrTimeout(30 * time.Second)
	out.SetEntryTimeout(30 * time.Second)

	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  embeddedFileIno(file.ID),
	}), 0
}

func (n *AttachmentsNode) createExternalAttachmentNode(ctx context.Context, att api.Attachment, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	node := &ExternalAttachmentNode{
		renderFile: renderFile{
			BaseNode: BaseNode{lfs: n.lfs},
			render: func() ([]byte, time.Time, time.Time) {
				return []byte(externalAttachmentContent(att)), att.UpdatedAt, att.CreatedAt
			},
		},
		attachment: att,
		issueID:    n.issueID,
	}
	return n.newRenderInode(ctx, out, node, externalAttachmentIno(att.ID), 30*time.Second), 0
}

// EmbeddedFileNode represents a file in the /attachments/ directory
// Files are lazily fetched from Linear's CDN on first read
type EmbeddedFileNode struct {
	BaseNode
	file api.EmbeddedFile
}

var _ fs.NodeGetattrer = (*EmbeddedFileNode)(nil)
var _ fs.NodeOpener = (*EmbeddedFileNode)(nil)
var _ fs.NodeReader = (*EmbeddedFileNode)(nil)

func (n *EmbeddedFileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0444 // Read-only
	n.SetOwner(out)
	if n.file.FileSize > 0 {
		out.Size = uint64(n.file.FileSize)
	} else {
		// Report a placeholder size so tools will attempt to read the file.
		// Lazy-fetch happens during Read(), which will return actual content.
		// Use 1MB as a reasonable placeholder for images.
		out.Size = 1024 * 1024
	}
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *EmbeddedFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Don't use kernel caching since file might be lazily downloaded
	return nil, 0, 0
}

func (n *EmbeddedFileNode) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// Lazy fetch: download file from Linear CDN if not cached
	content, err := n.lfs.FetchEmbeddedFile(ctx, n.file)
	if err != nil {
		return nil, syscall.EIO
	}

	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}

	return fuse.ReadResultData(content[off:end]), 0
}

// ExternalAttachmentNode represents a .link file for an external attachment
// (GitHub PR, URL, etc.). It embeds renderFile for Open/Read/Getattr and keeps
// only its Unlink.
type ExternalAttachmentNode struct {
	renderFile
	attachment api.Attachment
	issueID    string
}

var _ fs.NodeUnlinker = (*ExternalAttachmentNode)(nil)

// externalAttachmentContent renders a .link file's YAML body.
func externalAttachmentContent(att api.Attachment) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("title: %s\n", att.Title))
	sb.WriteString(fmt.Sprintf("url: %s\n", att.URL))
	if att.Subtitle != "" {
		sb.WriteString(fmt.Sprintf("subtitle: %s\n", att.Subtitle))
	}
	if att.SourceType != "" {
		sb.WriteString(fmt.Sprintf("source: %s\n", att.SourceType))
	}
	return sb.String()
}

func (n *ExternalAttachmentNode) Unlink(ctx context.Context, name string) syscall.Errno {
	// The file node already holds its entity, so find just hands it over.
	return commitDelete(ctx, n.lfs, deleteSpec[api.Attachment]{
		op:   `delete attachment "` + name + `"`,
		key:  collectionErrorKey("attachments", n.issueID),
		find: func(context.Context) (*api.Attachment, error) { return &n.attachment, nil },
		mutate: func(ctx context.Context, a *api.Attachment) error {
			return n.lfs.mutator().DeleteAttachment(ctx, a.ID)
		},
		forget: func(ctx context.Context, a *api.Attachment) error {
			return n.lfs.store.Queries().DeleteAttachment(ctx, a.ID)
		},
		dir:  attachmentsDirIno(n.issueID),
		name: name,
	})
}

// createAttachment is the attachments create surface's onFlush: parse
// "url [title]" and run the create tail.
func (n *AttachmentsNode) createAttachment(ctx context.Context, raw []byte) syscall.Errno {
	content := strings.TrimSpace(string(raw))

	// Idempotency (#146): if the URL is already attached, linking it again is a
	// success, not a failure. Linear rejects the duplicate with an opaque
	// "Unable to create issue attachment", indistinguishable from a genuine
	// failure (auth, bad URL, outage) — the common case being Linear's GitHub
	// integration having already auto-linked a branch-named PR. The cheap
	// cache pre-check returns 0 without a .last entry (nothing was created);
	// the authoritative post-failure re-check inside mutate treats a stale-cache
	// miss as the created attachment.
	if url := strings.SplitN(content, " ", 2)[0]; url != "" {
		if existing, err := n.lfs.GetIssueAttachments(ctx, n.issueID); err == nil {
			for _, att := range existing {
				if attachmentURLsEqual(att.URL, url) {
					n.lfs.ClearWriteError(collectionErrorKey("attachments", n.issueID))
					return 0
				}
			}
		}
	}

	_, errno := commitCreate(ctx, n.lfs, createSpec[api.Attachment]{
		op:  "create attachment",
		key: collectionErrorKey("attachments", n.issueID),
		mutate: func(ctx context.Context) (*api.Attachment, error) {
			if content == "" {
				return nil, &FieldError{Field: "content", Message: `empty content. Write "<url> [title]".`}
			}
			// Parse the content: "url [title]" or just "url" (title defaults
			// to the URL).
			parts := strings.SplitN(content, " ", 2)
			url := parts[0]
			title := url
			if len(parts) > 1 {
				title = parts[1]
			}

			att, err := n.lfs.mutator().LinkURL(ctx, n.issueID, url, title)
			if err == nil {
				return att, nil
			}
			// The local cache may be stale relative to Linear (e.g. an auto-link
			// that hasn't synced yet), so the pre-check above can miss a
			// duplicate. On failure, re-check authoritatively against the API:
			// if the URL is in fact already attached, treat it as the idempotent
			// success it is rather than surfacing the raw GraphQL rejection.
			if live, lerr := n.lfs.client.GetIssueAttachments(ctx, n.issueID); lerr == nil {
				for _, ex := range live {
					if attachmentURLsEqual(ex.URL, url) {
						return &ex, nil
					}
				}
			}
			return nil, err
		},
		result: func(a *api.Attachment) WriteResult {
			return WriteResult{
				URL:   a.URL,
				Path:  linkName(*a),
				Title: a.Title,
			}
		},
		persist: func(ctx context.Context, a *api.Attachment) error {
			n.upsertAttachment(ctx, *a)
			return nil
		},
		dir:       attachmentsDirIno(n.issueID),
		entryName: func(a *api.Attachment) string { return linkName(*a) },
	})
	return errno
}

// upsertAttachment writes an attachment to SQLite for immediate visibility.
// Failures are logged, not fatal: the sync worker will reconcile.
func (n *AttachmentsNode) upsertAttachment(ctx context.Context, att api.Attachment) {
	data, _ := json.Marshal(att)
	if err := n.lfs.store.Queries().UpsertAttachment(ctx, db.UpsertAttachmentParams{
		ID:         att.ID,
		IssueID:    n.issueID,
		Title:      att.Title,
		Subtitle:   sql.NullString{String: att.Subtitle, Valid: att.Subtitle != ""},
		Url:        att.URL,
		SourceType: sql.NullString{String: att.SourceType, Valid: att.SourceType != ""},
		Metadata:   json.RawMessage("{}"),
		SyncedAt:   time.Now(),
		Data:       data,
	}); err != nil {
		log.Printf("[attachments] upsert to DB failed: %v", err)
	}
}

// attachmentURLsEqual reports whether two attachment URLs refer to the same
// target, ignoring surrounding whitespace and trailing slashes. Linear stores
// auto-linked URLs verbatim, so a trailing-slash-tolerant exact match is enough
// to recognize a duplicate without false positives.
func attachmentURLsEqual(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}
