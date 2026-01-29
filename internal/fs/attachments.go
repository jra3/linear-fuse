package fs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
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

// attachmentsCreateIno generates a stable inode for the _create trigger file
func attachmentsCreateIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("attachments-create:" + issueID))
	return h.Sum64()
}

// AttachmentsNode represents the /teams/{KEY}/issues/{ID}/attachments directory
type AttachmentsNode struct {
	BaseNode
	issueID string
}

var _ fs.NodeReaddirer = (*AttachmentsNode)(nil)
var _ fs.NodeLookuper = (*AttachmentsNode)(nil)
var _ fs.NodeGetattrer = (*AttachmentsNode)(nil)

func (n *AttachmentsNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *AttachmentsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Start with _create entry
	entries := []fuse.DirEntry{
		{Name: "_create", Mode: syscall.S_IFREG},
	}

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

func (n *AttachmentsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle _create trigger file
	if name == "_create" {
		node := &NewAttachmentNode{
			BaseNode: BaseNode{lfs: n.lfs},
			issueID:  n.issueID,
		}
		now := time.Now()
		out.Attr.Mode = 0200 | syscall.S_IFREG // Write-only
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = 0
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		out.Attr.SetTimes(&now, &now, &now)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  attachmentsCreateIno(n.issueID),
		}), 0
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
	// Delete via API
	if err := n.lfs.client.DeleteAttachment(ctx, n.attachment.ID); err != nil {
		return syscall.EIO
	}

	// Delete from local DB
	n.lfs.store.Queries().DeleteAttachment(ctx, n.attachment.ID)

	// Invalidate caches
	n.lfs.InvalidateKernelInode(attachmentsDirIno(n.issueID))

	return 0
}

// NewAttachmentNode represents the _create file for creating new attachments
type NewAttachmentNode struct {
	BaseNode
	issueID string
}

var _ fs.NodeGetattrer = (*NewAttachmentNode)(nil)
var _ fs.NodeSetattrer = (*NewAttachmentNode)(nil)
var _ fs.NodeOpener = (*NewAttachmentNode)(nil)
var _ fs.NodeWriter = (*NewAttachmentNode)(nil)
var _ fs.NodeFlusher = (*NewAttachmentNode)(nil)

func (n *NewAttachmentNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0200 // Write-only
	n.SetOwner(out)
	out.Size = 0
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *NewAttachmentNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	// Allow truncation (for > redirect) - just return success
	out.Mode = 0200
	n.SetOwner(out)
	out.Size = 0
	return 0
}

func (n *NewAttachmentNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return &attachmentCreateHandle{}, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewAttachmentNode) Write(ctx context.Context, fh fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	handle, ok := fh.(*attachmentCreateHandle)
	if !ok {
		return 0, syscall.EIO
	}
	handle.buffer = append(handle.buffer, data...)
	return uint32(len(data)), 0
}

func (n *NewAttachmentNode) Flush(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	handle, ok := fh.(*attachmentCreateHandle)
	if !ok || len(handle.buffer) == 0 {
		return 0
	}

	// Parse the content: "url [title]" or just "url"
	content := strings.TrimSpace(string(handle.buffer))
	handle.buffer = nil

	if content == "" {
		return syscall.EINVAL
	}

	// Parse URL and optional title
	parts := strings.SplitN(content, " ", 2)
	url := parts[0]
	title := url // Default title is the URL
	if len(parts) > 1 {
		title = parts[1]
	}

	// Create the attachment via API (LinkURL for external links)
	att, err := n.lfs.client.LinkURL(ctx, n.issueID, url, title)
	if err != nil {
		return syscall.EIO
	}

	// Upsert to SQLite for immediate visibility
	now := time.Now()
	data, _ := json.Marshal(att)
	n.lfs.store.Queries().UpsertAttachment(ctx, db.UpsertAttachmentParams{
		ID:         att.ID,
		IssueID:    n.issueID,
		Title:      att.Title,
		Subtitle:   sql.NullString{String: att.Subtitle, Valid: att.Subtitle != ""},
		Url:        att.URL,
		SourceType: sql.NullString{String: att.SourceType, Valid: att.SourceType != ""},
		Metadata:   json.RawMessage("{}"),
		SyncedAt:   now,
		Data:       data,
	})

	// Invalidate cache
	n.lfs.InvalidateKernelInode(attachmentsDirIno(n.issueID))

	return 0
}

type attachmentCreateHandle struct {
	buffer []byte
}
