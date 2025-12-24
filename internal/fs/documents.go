package fs

import (
	"context"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// docsDirIno generates a stable inode number for a docs directory
func docsDirIno(parentID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("docs:" + parentID))
	return h.Sum64()
}

// documentIno generates a stable inode number for a document
func documentIno(docID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("doc:" + docID))
	return h.Sum64()
}

// DocsNode represents a docs/ directory within issues, teams, or projects
type DocsNode struct {
	BaseNode
	issueID   string // Set if issue docs
	teamID    string // Set if team docs
	projectID string // Set if project docs
}

var _ fs.NodeReaddirer = (*DocsNode)(nil)
var _ fs.NodeLookuper = (*DocsNode)(nil)
var _ fs.NodeCreater = (*DocsNode)(nil)
var _ fs.NodeUnlinker = (*DocsNode)(nil)
var _ fs.NodeRenamer = (*DocsNode)(nil)
var _ fs.NodeGetattrer = (*DocsNode)(nil)

func (n *DocsNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	return 0
}

func (n *DocsNode) getDocuments(ctx context.Context) ([]api.Document, error) {
	if n.issueID != "" {
		return n.lfs.GetIssueDocuments(ctx, n.issueID)
	}
	if n.teamID != "" {
		return n.lfs.GetTeamDocuments(ctx, n.teamID)
	}
	if n.projectID != "" {
		return n.lfs.GetProjectDocuments(ctx, n.projectID)
	}
	return nil, nil
}

func (n *DocsNode) parentID() string {
	if n.issueID != "" {
		return n.issueID
	}
	if n.teamID != "" {
		return n.teamID
	}
	return n.projectID
}

func (n *DocsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Fetch documents (uses cache if available)
	docs, err := n.getDocuments(ctx)
	if err != nil {
		// On error, return just new.md
		return fs.NewListDirStream([]fuse.DirEntry{
			{Name: "new.md", Mode: syscall.S_IFREG},
		}), 0
	}

	// Always include new.md for creating documents
	entries := []fuse.DirEntry{
		{Name: "new.md", Mode: syscall.S_IFREG},
	}

	for _, doc := range docs {
		entries = append(entries, fuse.DirEntry{
			Name: documentFilename(doc),
			Mode: syscall.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), 0
}

func (n *DocsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle new.md for creating documents
	if name == "new.md" {
		now := time.Now()
		node := &NewDocumentNode{
			BaseNode:  BaseNode{lfs: n.lfs},
			issueID:   n.issueID,
			teamID:    n.teamID,
			projectID: n.projectID,
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

	docs, err := n.getDocuments(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	// Match by filename
	for _, doc := range docs {
		if documentFilename(doc) == name {
			content, err := marshal.DocumentToMarkdown(&doc)
			if err != nil {
				log.Printf("Failed to marshal document: %v", err)
				return nil, syscall.EIO
			}
			node := &DocumentFileNode{
				BaseNode:     BaseNode{lfs: n.lfs},
				document:     doc,
				issueID:      n.issueID,
				teamID:       n.teamID,
				projectID:    n.projectID,
				content:      content,
				contentReady: true,
			}
			out.Attr.Mode = 0644 | syscall.S_IFREG
			out.Attr.Uid = n.lfs.uid
			out.Attr.Gid = n.lfs.gid
			out.Attr.Size = uint64(len(content))
			out.SetAttrTimeout(5 * time.Second)  // Shorter timeout for writable files
			out.SetEntryTimeout(5 * time.Second) // Shorter timeout for writable files
			out.Attr.SetTimes(&doc.UpdatedAt, &doc.UpdatedAt, &doc.CreatedAt)
			return n.NewInode(ctx, node, fs.StableAttr{
				Mode: syscall.S_IFREG,
				Ino:  documentIno(doc.ID),
			}), 0
		}
	}

	return nil, syscall.ENOENT
}

func (n *DocsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Unlink document: %s", name)
	}

	// Don't allow deleting new.md
	if name == "new.md" {
		return syscall.EPERM
	}

	docs, err := n.getDocuments(ctx)
	if err != nil {
		return syscall.EIO
	}

	// Find the document by filename
	for _, doc := range docs {
		if documentFilename(doc) == name {
			err := n.lfs.DeleteDocument(ctx, doc.ID, n.issueID, n.teamID, n.projectID)
			if err != nil {
				log.Printf("Failed to delete document: %v", err)
				return syscall.EIO
			}
			// Invalidate kernel cache - both directory inode and entry
			n.lfs.InvalidateKernelInode(docsDirIno(n.parentID()))
			n.lfs.InvalidateKernelEntry(docsDirIno(n.parentID()), name)
			if n.lfs.debug {
				log.Printf("Document deleted successfully")
			}
			return 0
		}
	}

	return syscall.ENOENT
}

func (n *DocsNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Rename document: %s -> %s", name, newName)
	}

	// Don't allow renaming new.md
	if name == "new.md" {
		return syscall.EPERM
	}

	// For same-directory rename, compare inode numbers
	if newParent.EmbeddedInode().StableAttr().Ino != n.EmbeddedInode().StableAttr().Ino {
		if n.lfs.debug {
			log.Printf("Rename: cross-directory not allowed")
		}
		return syscall.EXDEV
	}

	// Extract new title from filename (remove .md suffix, convert dashes to spaces)
	if !strings.HasSuffix(newName, ".md") {
		return syscall.EINVAL
	}
	newTitle := strings.TrimSuffix(newName, ".md")
	newTitle = strings.ReplaceAll(newTitle, "-", " ")

	docs, err := n.getDocuments(ctx)
	if err != nil {
		return syscall.EIO
	}

	// Find the document by old filename
	for _, doc := range docs {
		if documentFilename(doc) == name {
			// Update document title
			updatedDoc, err := n.lfs.UpdateDocument(ctx, doc.ID, map[string]any{"title": newTitle}, n.issueID, n.teamID, n.projectID)
			if err != nil {
				log.Printf("Failed to rename document: %v", err)
				return syscall.EIO
			}
			// Upsert to SQLite so it's immediately visible
			if err := n.lfs.UpsertDocument(ctx, *updatedDoc); err != nil {
				log.Printf("Warning: failed to upsert document to SQLite: %v", err)
			}
			if n.lfs.debug {
				log.Printf("Document renamed successfully: %s -> %s", doc.Title, newTitle)
			}
			// Invalidate kernel cache for old and new names
			n.lfs.InvalidateKernelEntry(docsDirIno(n.parentID()), name)
			n.lfs.InvalidateKernelEntry(docsDirIno(n.parentID()), newName)
			return 0
		}
	}

	return syscall.ENOENT
}

func (n *DocsNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create document file: %s", name)
	}

	// Only allow creating .md files
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}

	node := &NewDocumentNode{
		BaseNode:  BaseNode{lfs: n.lfs},
		issueID:   n.issueID,
		teamID:    n.teamID,
		projectID: n.projectID,
		filename:  name, // Store filename for use as title
	}

	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})

	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
}

// documentFilename returns the filename for a document
func documentFilename(doc api.Document) string {
	// Use slugId if available, otherwise sanitize title
	if doc.SlugID != "" {
		return doc.SlugID + ".md"
	}
	// Sanitize title for filename
	name := strings.ToLower(doc.Title)
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "/", "-")
	return name + ".md"
}

// DocumentFileNode represents a single document file (read-write)
type DocumentFileNode struct {
	BaseNode
	document  api.Document
	issueID   string
	teamID    string
	projectID string

	mu           sync.Mutex
	content      []byte
	contentReady bool
	dirty        bool
}

var _ fs.NodeGetattrer = (*DocumentFileNode)(nil)
var _ fs.NodeOpener = (*DocumentFileNode)(nil)
var _ fs.NodeReader = (*DocumentFileNode)(nil)
var _ fs.NodeWriter = (*DocumentFileNode)(nil)
var _ fs.NodeFlusher = (*DocumentFileNode)(nil)
var _ fs.NodeFsyncer = (*DocumentFileNode)(nil)
var _ fs.NodeSetattrer = (*DocumentFileNode)(nil)

func (n *DocumentFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	out.Mode = 0644
	n.SetOwner(out)
	out.Size = uint64(len(n.content))
	out.SetTimes(&n.document.UpdatedAt, &n.document.UpdatedAt, &n.document.CreatedAt)
	return 0
}

func (n *DocumentFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *DocumentFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
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

func (n *DocumentFileNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write document %s: offset=%d len=%d", n.document.ID, off, len(data))
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

func (n *DocumentFileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if n.lfs.debug {
			log.Printf("Setattr truncate document %s: size=%d", n.document.ID, sz)
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

func (n *DocumentFileNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.dirty || n.content == nil {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Parse the markdown and get update fields
	update, err := marshal.MarkdownToDocumentUpdate(n.content, &n.document)
	if err != nil {
		log.Printf("Failed to parse document: %v", err)
		return syscall.EIO
	}

	if len(update) == 0 {
		if n.lfs.debug {
			log.Printf("Flush document %s: no changes", n.document.ID)
		}
		n.dirty = false
		return 0
	}

	if n.lfs.debug {
		log.Printf("Updating document %s", n.document.ID)
	}

	updatedDoc, err := n.lfs.UpdateDocument(ctx, n.document.ID, update, n.issueID, n.teamID, n.projectID)
	if err != nil {
		log.Printf("Failed to update document: %v", err)
		return syscall.EIO
	}

	// Upsert to SQLite so it's immediately visible
	if err := n.lfs.UpsertDocument(ctx, *updatedDoc); err != nil {
		log.Printf("Warning: failed to upsert document to SQLite: %v", err)
	}

	// Invalidate kernel cache for this document file
	n.lfs.InvalidateKernelInode(documentIno(n.document.ID))

	n.dirty = false
	n.contentReady = false // Force regenerate on next read

	if n.lfs.debug {
		log.Printf("Document updated successfully")
	}

	return 0
}

func (n *DocumentFileNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	// Fsync is a no-op; actual persistence happens in Flush
	return 0
}

// NewDocumentNode handles creating new documents
type NewDocumentNode struct {
	BaseNode
	issueID   string
	teamID    string
	projectID string
	filename  string // Original filename (used as title if none in content)

	mu      sync.Mutex
	content []byte
	created bool
}

var _ fs.NodeGetattrer = (*NewDocumentNode)(nil)
var _ fs.NodeOpener = (*NewDocumentNode)(nil)
var _ fs.NodeReader = (*NewDocumentNode)(nil)
var _ fs.NodeWriter = (*NewDocumentNode)(nil)
var _ fs.NodeFlusher = (*NewDocumentNode)(nil)
var _ fs.NodeFsyncer = (*NewDocumentNode)(nil)
var _ fs.NodeSetattrer = (*NewDocumentNode)(nil)

func (n *NewDocumentNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	out.Mode = 0200
	n.SetOwner(out)
	out.Size = uint64(len(n.content))
	return 0
}

func (n *NewDocumentNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewDocumentNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// new.md is write-only - return permission denied
	return nil, syscall.EACCES
}

func (n *NewDocumentNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write new document: offset=%d len=%d", off, len(data))
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

func (n *NewDocumentNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
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

	out.Mode = 0200
	out.Size = uint64(len(n.content))
	return 0
}

func (n *NewDocumentNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created || len(n.content) == 0 {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Parse the new document content
	title, body, err := marshal.ParseNewDocument(n.content)
	if err != nil {
		log.Printf("Failed to parse new document: %v", err)
		return syscall.EIO
	}

	// If no title in content, use filename (unless it's new.md)
	if title == "" || title == "Untitled" {
		if n.filename != "" && n.filename != "new.md" {
			// Convert filename to title: remove .md, replace dashes with spaces
			title = strings.TrimSuffix(n.filename, ".md")
			title = strings.ReplaceAll(title, "-", " ")
		}
	}

	if title == "" {
		log.Printf("New document has no title")
		return syscall.EINVAL
	}

	if n.lfs.debug {
		log.Printf("Creating document: title=%s", title)
	}

	// Build create input
	input := map[string]any{
		"title":   title,
		"content": body,
	}

	// Set parent
	if n.issueID != "" {
		input["issueId"] = n.issueID
	}
	if n.teamID != "" {
		input["teamId"] = n.teamID
	}
	if n.projectID != "" {
		input["projectId"] = n.projectID
	}

	doc, err := n.lfs.CreateDocument(ctx, input)
	if err != nil {
		log.Printf("Failed to create document: %v", err)
		return syscall.EIO
	}

	// Upsert to SQLite so it's immediately visible
	if err := n.lfs.UpsertDocument(ctx, *doc); err != nil {
		log.Printf("Warning: failed to upsert document to SQLite: %v", err)
	}

	n.created = true

	// Invalidate kernel cache for docs directory
	parentID := n.issueID
	if parentID == "" {
		parentID = n.teamID
	}
	if parentID == "" {
		parentID = n.projectID
	}
	// Invalidate the directory inode so the kernel re-reads the listing
	n.lfs.InvalidateKernelInode(docsDirIno(parentID))
	// Also invalidate specific entries
	n.lfs.InvalidateKernelEntry(docsDirIno(parentID), "new.md")
	n.lfs.InvalidateKernelEntry(docsDirIno(parentID), documentFilename(*doc))

	if n.lfs.debug {
		log.Printf("Document created successfully")
	}

	return 0
}

func (n *NewDocumentNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	// Fsync is a no-op; actual persistence happens in Flush
	return 0
}
