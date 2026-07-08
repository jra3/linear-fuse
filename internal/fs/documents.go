package fs

import (
	"context"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// DocsNode represents a docs/ directory within issues, teams, projects, or initiatives
type DocsNode struct {
	attrNode
	issueID      string // Set if issue docs
	teamID       string // Set if team docs
	projectID    string // Set if project docs
	initiativeID string // Set if initiative docs
}

var _ fs.NodeReaddirer = (*DocsNode)(nil)
var _ fs.NodeLookuper = (*DocsNode)(nil)
var _ fs.NodeCreater = (*DocsNode)(nil)
var _ fs.NodeUnlinker = (*DocsNode)(nil)
var _ fs.NodeRenamer = (*DocsNode)(nil)
var _ fs.NodeGetattrer = (*DocsNode)(nil)

func (n *DocsNode) getDocuments(ctx context.Context) ([]api.Document, error) {
	if n.issueID != "" {
		// Trigger background refresh of sub-resources if stale
		n.lfs.MaybeRefreshIssueDetails(n.issueID)
		return n.lfs.GetIssueDocuments(ctx, n.issueID)
	}
	if n.teamID != "" {
		return n.lfs.GetTeamDocuments(ctx, n.teamID)
	}
	if n.projectID != "" {
		return n.lfs.GetProjectDocuments(ctx, n.projectID)
	}
	if n.initiativeID != "" {
		return n.lfs.GetInitiativeDocuments(ctx, n.initiativeID)
	}
	return nil, nil
}

func (n *DocsNode) parentID() string {
	return docParentID(n.issueID, n.teamID, n.projectID, n.initiativeID)
}

// docParentID returns the single non-empty parent ID for a docs surface, in
// precedence order (issue, team, project, initiative). Used both for kernel
// cache inodes and as the parent for the docs/ .error key.
func docParentID(issueID, teamID, projectID, initiativeID string) string {
	switch {
	case issueID != "":
		return issueID
	case teamID != "":
		return teamID
	case projectID != "":
		return projectID
	default:
		return initiativeID
	}
}

func (n *DocsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Fetch documents (uses cache if available)
	docs, err := n.getDocuments(ctx)
	if err != nil {
		// On error, still serve the trio
		return fs.NewListDirStream(n.trio().entries()), 0
	}

	entries := append(n.trio().entries(), n.listing(docs).entries()...)
	return fs.NewListDirStream(entries), 0
}

// trio declares the docs collection's writable surfaces. The _create trigger
// has no user-chosen filename, so the title must come from the content.
func (n *DocsNode) trio() collectionTrio {
	return collectionTrio{kind: "docs", parentID: n.parentID(), onFlush: n.createDocument("")}
}

// listing declares the docs collection's item files: one per document, named by
// documentFilename. Backs Readdir/Lookup/Unlink/Rename/Create-overwrite so they
// derive and match names through one place. See namedListing.
func (n *DocsNode) listing(docs []api.Document) namedListing[api.Document] {
	return namedListing[api.Document]{items: docs, nameOf: documentFilename}
}

func (n *DocsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
	}

	docs, err := n.getDocuments(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	if doc, ok := n.listing(docs).find(name); ok {
		return n.newDocumentInode(ctx, name, doc, out)
	}

	return nil, syscall.ENOENT
}

func (n *DocsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Unlink document: %s", name)
	}

	// Don't allow deleting _create
	if name == "_create" {
		return syscall.EPERM
	}

	return commitDelete(ctx, n.lfs, deleteSpec[api.Document]{
		op:  `delete document "` + name + `"`,
		key: collectionErrorKey("docs", n.parentID()),
		find: func(ctx context.Context) (*api.Document, error) {
			docs, err := n.getDocuments(ctx)
			if err != nil {
				return nil, err
			}
			if doc, ok := n.listing(docs).find(name); ok {
				return &doc, nil
			}
			return nil, nil
		},
		mutate: func(ctx context.Context, d *api.Document) error {
			return n.lfs.mutator().DeleteDocument(ctx, d.ID)
		},
		forget: func(ctx context.Context, d *api.Document) error {
			return n.lfs.store.Queries().DeleteDocument(ctx, d.ID)
		},
		dir:  docsDirIno(n.parentID()),
		name: name,
	})
}

func (n *DocsNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Rename document: %s -> %s", name, newName)
	}

	// Don't allow renaming _create
	if name == "_create" {
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

	doc, ok := n.listing(docs).find(name)
	if !ok {
		return syscall.ENOENT
	}

	// Update document title
	updatedDoc, err := n.lfs.UpdateDocument(ctx, doc.ID, map[string]any{"title": newTitle}, n.issueID, n.teamID, n.projectID)
	if err != nil {
		log.Printf("Failed to rename document: %v", err)
		msg, errno := classifyMutationErr("rename document "+name+" -> "+newName, err)
		n.lfs.SetWriteError(collectionErrorKey("docs", n.parentID()), msg)
		return errno
	}
	// Upsert to SQLite so it's immediately visible
	if err := n.lfs.UpsertDocument(ctx, *updatedDoc); err != nil {
		log.Printf("Warning: failed to upsert document to SQLite: %v", err)
	}
	if n.lfs.debug {
		log.Printf("Document renamed successfully: %s -> %s", doc.Title, newTitle)
	}
	// Invalidate kernel cache for old and new names
	n.lfs.InvalidateRenamed(docsDirIno(n.parentID()), name, newName, 0)
	return 0
}

// newDocumentInode builds the read/write DocumentFileNode inode for an existing
// document, populated with its current content. Shared by Lookup and Create.
func (n *DocsNode) newDocumentInode(ctx context.Context, name string, doc api.Document, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
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
		initiativeID: n.initiativeID,
		editBuffer:   editBuffer{content: content},
	}
	// Shorter timeout for writable files.
	return n.newFileInode(ctx, out, name, node, fileAttr(len(content), doc.CreatedAt, doc.UpdatedAt), documentIno(doc.ID), 5*time.Second), 0
}

func (n *DocsNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create document file: %s", name)
	}

	// Only allow creating .md files
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}

	// If a document already exists with this name, return its read/write node so
	// an overwrite (mv tmp doc.md, cp, editor save-over) updates it in place via
	// the normal truncate+write+flush path. Previously Create always bound a
	// write-only _create node to the name, leaving the file unreadable and
	// unwritable (#137).
	if docs, err := n.getDocuments(ctx); err == nil {
		if doc, ok := n.listing(docs).find(name); ok {
			inode, errno := n.newDocumentInode(ctx, name, doc, out)
			if errno != 0 {
				return nil, nil, 0, errno
			}
			return inode, nil, 0, 0
		}
	}

	// The user-chosen filename feeds the title fallback.
	node := newCreateFile(n.lfs, n.createDocument(name))
	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})

	return inode, &createFileHandle{}, fuse.FOPEN_DIRECT_IO, 0
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
	editBuffer
	document     api.Document
	issueID      string
	teamID       string
	projectID    string
	initiativeID string
}

var _ fs.NodeGetattrer = (*DocumentFileNode)(nil)
var _ fs.NodeOpener = (*DocumentFileNode)(nil)
var _ fs.NodeReader = (*DocumentFileNode)(nil)
var _ fs.NodeWriter = (*DocumentFileNode)(nil)
var _ fs.NodeFlusher = (*DocumentFileNode)(nil)
var _ fs.NodeFsyncer = (*DocumentFileNode)(nil)
var _ fs.NodeSetattrer = (*DocumentFileNode)(nil)

func (n *DocumentFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	// One lock for size + times: a concurrent refresh (refresh.go) swaps
	// content and entity atomically, so the read must snapshot both together.
	n.mu.Lock()
	size := len(n.content)
	created, updated := n.document.CreatedAt, n.document.UpdatedAt
	n.mu.Unlock()
	fileAttr(size, created, updated).fill(&out.Attr, &n.BaseNode)
	return 0
}

// refreshFrom adopts a fresh twin's document and rendered content unless an
// edit is in flight — the dirty buffer always wins (refresh.go).
func (n *DocumentFileNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*DocumentFileNode); ok {
		n.refresh(f.content, func() {
			n.document = f.document
			n.issueID, n.teamID, n.projectID, n.initiativeID = f.issueID, f.teamID, f.projectID, f.initiativeID
		})
	}
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

	docErrKey := collectionErrorKey("docs", docParentID(n.issueID, n.teamID, n.projectID, n.initiativeID))

	// Parse the markdown and get update fields
	update, err := marshal.MarkdownToDocumentUpdate(n.content, &n.document)
	if err != nil {
		log.Printf("Failed to parse document: %v", err)
		n.lfs.SetWriteError(docErrKey, "Operation: update document "+documentFilename(n.document)+"\nParse error: "+err.Error())
		return syscall.EINVAL
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
		msg, errno := classifyMutationErr("update document "+documentFilename(n.document), err)
		n.lfs.SetWriteError(docErrKey, msg)
		return errno
	}

	// Edit-commit tail: verify read-your-writes against the API's echoed response,
	// persist, and surface divergence via .error.
	fresh, errno := commitWriteBack(ctx, n.lfs, writeBackSpec[api.Document]{
		errKey:  docErrKey,
		fetch:   func(ctx context.Context) (*api.Document, error) { return updatedDoc, nil },
		persist: func(ctx context.Context, fresh *api.Document) error { return n.lfs.UpsertDocument(ctx, *fresh) },
		compare: func(fresh *api.Document) []writeBackResult {
			var results []writeBackResult
			if want, ok := update["title"].(string); ok {
				results = append(results, writeBackDivergence("title", want, fresh.Title, n.document.Title))
			}
			if want, ok := update["content"].(string); ok {
				results = append(results, writeBackDivergence("content (body)", want, fresh.Content, n.document.Content))
			}
			return results
		},
	})

	// Invalidate kernel cache for this document file
	n.lfs.InvalidateUpdated(documentIno(n.document.ID))

	if fresh != nil {
		n.document = *fresh
	}
	n.dirty = false
	return errno
}

// createDocument returns the docs create surface's onFlush for one write
// cycle. filename is the user-chosen name from a named Create ("" for the
// _create trigger); it becomes the title fallback when the content carries no
// '# Title' heading.
func (n *DocsNode) createDocument(filename string) func(ctx context.Context, content []byte) syscall.Errno {
	return func(ctx context.Context, content []byte) syscall.Errno {
		parentID := n.parentID()
		_, errno := commitCreate(ctx, n.lfs, createSpec[api.Document]{
			op:  "create document",
			key: collectionErrorKey("docs", parentID),
			mutate: func(ctx context.Context) (*api.Document, error) {
				title, body, err := marshal.ParseNewDocument(content)
				if err != nil {
					return nil, &FieldError{Field: "content", Message: "parse error: " + err.Error()}
				}
				// If no title in content, use filename: remove .md, replace
				// dashes with spaces.
				if title == "" || title == "Untitled" {
					if filename != "" {
						title = strings.TrimSuffix(filename, ".md")
						title = strings.ReplaceAll(title, "-", " ")
					}
				}
				if title == "" {
					return nil, &FieldError{Field: "title", Message: "document has no title. Add a '# Title' heading or name the file <title>.md."}
				}

				input := map[string]any{
					"title":   title,
					"content": body,
				}
				if n.issueID != "" {
					input["issueId"] = n.issueID
				}
				if n.teamID != "" {
					input["teamId"] = n.teamID
				}
				if n.projectID != "" {
					input["projectId"] = n.projectID
				}
				if n.initiativeID != "" {
					input["initiativeId"] = n.initiativeID
				}
				return n.lfs.mutator().CreateDocument(ctx, input)
			},
			result: func(d *api.Document) WriteResult {
				return WriteResult{
					URL:   d.URL,
					Path:  documentFilename(*d),
					Title: d.Title,
				}
			},
			persist: func(ctx context.Context, d *api.Document) error {
				return n.lfs.UpsertDocument(ctx, *d)
			},
			dir:       docsDirIno(parentID),
			entryName: func(d *api.Document) string { return documentFilename(*d) },
		})
		return errno
	}
}
