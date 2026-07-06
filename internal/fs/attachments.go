package fs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// attachmentsDirIno generates a stable inode for an issue's attachments directory
func attachmentsDirIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("attachments:" + issueID))
	return h.Sum64()
}

// embeddedFileIno generates a stable inode for an embedded file
func embeddedFileIno(fileID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("file:" + fileID))
	return h.Sum64()
}

// externalAttachmentIno generates a stable inode for an external attachment (.link file)
func externalAttachmentIno(attachmentID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("extatt:" + attachmentID))
	return h.Sum64()
}

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

	// Add embedded files (images, etc.)
	files, err := n.lfs.GetIssueEmbeddedFiles(ctx, n.issueID)
	if err == nil {
		nameCount := make(map[string]int)
		for _, file := range files {
			name := deduplicateFilename(file.Filename, nameCount)
			entries = append(entries, fuse.DirEntry{
				Name: name,
				Mode: syscall.S_IFREG,
			})
		}
	}

	// Add external attachments (.link files)
	attachments, err := n.lfs.GetIssueAttachments(ctx, n.issueID)
	if err == nil {
		for _, att := range attachments {
			name := sanitizeFilename(att.Title) + ".link"
			entries = append(entries, fuse.DirEntry{
				Name: name,
				Mode: syscall.S_IFREG,
			})
		}
	}

	return fs.NewListDirStream(entries), 0
}

// trio declares the attachments collection's writable surfaces.
func (n *AttachmentsNode) trio() collectionTrio {
	return collectionTrio{kind: "attachments", parentID: n.issueID, onFlush: n.createAttachment}
}

func (n *AttachmentsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
	}

	// Handle .link files (external attachments)
	if strings.HasSuffix(name, ".link") {
		attachments, err := n.lfs.GetIssueAttachments(ctx, n.issueID)
		if err != nil {
			return nil, syscall.EIO
		}
		baseName := strings.TrimSuffix(name, ".link")
		for _, att := range attachments {
			if sanitizeFilename(att.Title) == baseName {
				return n.createExternalAttachmentNode(ctx, att, out)
			}
		}
		return nil, syscall.ENOENT
	}

	// Handle embedded files
	files, err := n.lfs.GetIssueEmbeddedFiles(ctx, n.issueID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Build deduplicated name mapping (same logic as Readdir)
	nameCount := make(map[string]int)
	fileByDeduplicatedName := make(map[string]api.EmbeddedFile)
	for _, file := range files {
		deduped := deduplicateFilename(file.Filename, nameCount)
		fileByDeduplicatedName[deduped] = file
	}

	file, ok := fileByDeduplicatedName[name]
	if !ok {
		return nil, syscall.ENOENT
	}

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
		BaseNode:   BaseNode{lfs: n.lfs},
		attachment: att,
		issueID:    n.issueID,
	}
	content := node.generateContent()
	now := time.Now()
	out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
	out.Attr.Uid = n.lfs.uid
	out.Attr.Gid = n.lfs.gid
	out.Attr.Size = uint64(len(content))
	out.SetAttrTimeout(30 * time.Second)
	out.SetEntryTimeout(30 * time.Second)
	out.Attr.SetTimes(&now, &now, &now)
	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  externalAttachmentIno(att.ID),
	}), 0
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

// deduplicateFilename returns a unique filename by appending (2), (3), etc. for duplicates.
// The nameCount map tracks how many times each base name has been seen.
func deduplicateFilename(name string, nameCount map[string]int) string {
	nameCount[name]++
	count := nameCount[name]
	if count == 1 {
		return name
	}

	// Insert counter before extension: image.png -> image (2).png
	ext := ""
	base := name
	if dot := strings.LastIndex(name, "."); dot > 0 {
		ext = name[dot:]
		base = name[:dot]
	}
	return fmt.Sprintf("%s (%d)%s", base, count, ext)
}

// sanitizeFilename converts a string to a safe filename by replacing problematic characters
func sanitizeFilename(s string) string {
	// Replace path separators and null bytes
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, "\x00", "")
	// Trim spaces and dots from ends
	s = strings.Trim(s, " .")
	if s == "" {
		return "untitled"
	}
	return s
}

// ExternalAttachmentNode represents a .link file for an external attachment (GitHub PR, URL, etc.)
type ExternalAttachmentNode struct {
	BaseNode
	attachment api.Attachment
	issueID    string
}

var _ fs.NodeGetattrer = (*ExternalAttachmentNode)(nil)
var _ fs.NodeOpener = (*ExternalAttachmentNode)(nil)
var _ fs.NodeReader = (*ExternalAttachmentNode)(nil)
var _ fs.NodeUnlinker = (*ExternalAttachmentNode)(nil)

func (n *ExternalAttachmentNode) generateContent() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("title: %s\n", n.attachment.Title))
	sb.WriteString(fmt.Sprintf("url: %s\n", n.attachment.URL))
	if n.attachment.Subtitle != "" {
		sb.WriteString(fmt.Sprintf("subtitle: %s\n", n.attachment.Subtitle))
	}
	if n.attachment.SourceType != "" {
		sb.WriteString(fmt.Sprintf("source: %s\n", n.attachment.SourceType))
	}
	return sb.String()
}

func (n *ExternalAttachmentNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := n.generateContent()
	now := time.Now()
	out.Mode = 0444 // Read-only
	n.SetOwner(out)
	out.Size = uint64(len(content))
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *ExternalAttachmentNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *ExternalAttachmentNode) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := []byte(n.generateContent())
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
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
				Path:  sanitizeFilename(a.Title) + ".link",
				Title: a.Title,
			}
		},
		persist: func(ctx context.Context, a *api.Attachment) error {
			n.upsertAttachment(ctx, *a)
			return nil
		},
		dir:       attachmentsDirIno(n.issueID),
		entryName: func(a *api.Attachment) string { return sanitizeFilename(a.Title) + ".link" },
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
