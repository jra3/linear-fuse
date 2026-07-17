package fs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
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
var _ fs.NodeUnlinker = (*AttachmentsNode)(nil)

// dir constructs the read-only listing head. Readdir refreshes stale
// sub-resources first, then lists best-effort: a failed fetch lists that family
// as empty rather than failing the whole directory (failReaddirOnError=false).
// build dispatches the two item families — embedded CDN files vs external
// .link attachments — since the heterogeneity lives entirely inside the entry.
func (n *AttachmentsNode) dir() listingDir[attachmentEntry] {
	return listingDir[attachmentEntry]{
		parent:  n,
		lfs:     n.lfs,
		trio:    n.trio(),
		refresh: func(context.Context) { n.lfs.repo.MaybeRefreshIssueDetails(n.issueID) },
		listing: func(ctx context.Context, fetchErr *error) infoListing[attachmentEntry] {
			return n.listing(ctx, fetchErr)
		},
		nameOf:      func(e attachmentEntry) string { return e.name },
		build:       n.buildAttachment,
		unlinkEntry: n.deleteAttachment,
	}
}

func (n *AttachmentsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return n.dir().readdir(ctx)
}

func (n *AttachmentsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.dir().unlink(ctx, name)
}

// deleteAttachment is the attachments unlink tail (listingDir.unlinkEntry). Only
// external attachments (*.link) are deletable: an embedded file is CDN-backed
// bytes referenced from the issue's markdown, with no attachment entity to
// delete, so rm on one is EPERM. The resolved entry already holds the entity.
func (n *AttachmentsNode) deleteAttachment(ctx context.Context, name string, e attachmentEntry) syscall.Errno {
	if e.external == nil {
		return syscall.EPERM
	}
	att := *e.external
	return commitDelete(ctx, n.lfs, deleteSpec[api.Attachment]{
		op:  `delete attachment "` + name + `"`,
		key: collectionErrorKey("attachments", n.issueID),
		find: func(context.Context) (*api.Attachment, error) {
			return &att, nil
		},
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

// listing fetches both item families and builds the name-derivation module.
// A failed fetch leaves that family empty; when fetchErr is non-nil it also
// records the first error there (Lookup distinguishes "not found" from
// "couldn't look").
func (n *AttachmentsNode) listing(ctx context.Context, fetchErr *error) attachmentListing {
	files, ferr := n.lfs.repo.GetIssueEmbeddedFiles(ctx, n.issueID)
	attachments, aerr := n.lfs.repo.GetIssueAttachments(ctx, n.issueID)
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
	return n.dir().lookup(ctx, name, out)
}

// buildAttachment mounts the read-only node for a resolved entry: an external
// attachment renders a .link file, an embedded file mounts the lazily-fetched
// CDN-backed node.
func (n *AttachmentsNode) buildAttachment(ctx context.Context, name string, entry attachmentEntry, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if entry.external != nil {
		return n.createExternalAttachmentNode(ctx, name, *entry.external, out)
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

	// The bridge dedups AFTER this handler returns: push the fresh file
	// metadata into the node it will keep (see refresh.go).
	refreshExisting(n, name, node)
	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  embeddedFileIno(file.ID),
	}), 0
}

func (n *AttachmentsNode) createExternalAttachmentNode(ctx context.Context, name string, att api.Attachment, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	node := &ExternalAttachmentNode{
		renderFile: renderFile{
			BaseNode: BaseNode{lfs: n.lfs},
			render: func(context.Context) ([]byte, time.Time, time.Time) {
				return []byte(externalAttachmentContent(att)), att.UpdatedAt, att.CreatedAt
			},
		},
		attachment: att,
		issueID:    n.issueID,
	}
	return n.newRenderInode(ctx, out, name, node, externalAttachmentIno(att.ID), 30*time.Second), 0
}

// EmbeddedFileNode represents a file in the /attachments/ directory
// Files are lazily fetched from Linear's CDN on first read
type EmbeddedFileNode struct {
	BaseNode
	mu   sync.Mutex // guards file: swapped by the nodeRefresher seam (refresh.go)
	file api.EmbeddedFile
}

// fileSnapshot reads the entity under the lock.
func (n *EmbeddedFileNode) fileSnapshot() api.EmbeddedFile {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.file
}

// refreshFrom adopts a fresh twin's file metadata (refresh.go).
func (n *EmbeddedFileNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*EmbeddedFileNode); ok {
		n.mu.Lock()
		n.file = f.file
		n.mu.Unlock()
	}
}

var _ fs.NodeGetattrer = (*EmbeddedFileNode)(nil)
var _ fs.NodeOpener = (*EmbeddedFileNode)(nil)
var _ fs.NodeReader = (*EmbeddedFileNode)(nil)

func (n *EmbeddedFileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	file := n.fileSnapshot()
	out.Mode = 0444 // Read-only
	n.SetOwner(out)
	if file.FileSize > 0 {
		out.Size = uint64(file.FileSize)
	} else {
		// Report a placeholder size so tools will attempt to read the file.
		// Lazy-fetch happens during Read(), which will return actual content.
		// Use 1MB as a reasonable placeholder for images.
		out.Size = 1024 * 1024
	}
	// Real times from the row (this was the package's last fabricated now(),
	// reshuffling attachments/ on every ls -lt): ctime = when the file was
	// first extracted, mtime/atime = when its metadata last synced. A zero
	// time reports unset.
	mtime := file.SyncedAt
	if mtime.IsZero() {
		mtime = file.CreatedAt
	}
	out.SetTimes(nonZeroTime(mtime), nonZeroTime(mtime), nonZeroTime(file.CreatedAt))
	return 0
}

func (n *EmbeddedFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Don't use kernel caching since file might be lazily downloaded
	return nil, 0, 0
}

func (n *EmbeddedFileNode) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// Lazy fetch: download file from Linear CDN if not cached
	content, err := n.lfs.FetchEmbeddedFile(ctx, n.fileSnapshot())
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
// (GitHub PR, URL, etc.). Deletion is the parent AttachmentsNode's Unlink, so
// this node embeds renderFile for Open/Read/Getattr only.
type ExternalAttachmentNode struct {
	renderFile
	attachment api.Attachment
	issueID    string
}

// refreshFrom adopts a fresh twin's attachment and render closure
// (refresh.go); renderMu doubles as the entity-field lock.
func (n *ExternalAttachmentNode) refreshFrom(fresh fs.InodeEmbedder) {
	f, ok := fresh.(*ExternalAttachmentNode)
	if !ok {
		return
	}
	n.renderMu.Lock()
	n.render = f.render
	n.attachment, n.issueID = f.attachment, f.issueID
	n.renderMu.Unlock()
}

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
		if existing, err := n.lfs.repo.GetIssueAttachments(ctx, n.issueID); err == nil {
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
			// Synchronous API read inside a user-blocking flush: promote it so
			// a tight detail budget can't stall the user's write verdict.
			if live, lerr := n.lfs.client.GetIssueAttachments(api.WithInteractive(ctx), n.issueID); lerr == nil {
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
		SyncedAt:   db.Now(),
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
