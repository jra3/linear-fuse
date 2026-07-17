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
		n.lfs.repo.MaybeRefreshIssueDetails(n.issueID)
		return n.lfs.repo.GetIssueDocuments(ctx, n.issueID)
	}
	if n.teamID != "" {
		return n.lfs.repo.GetTeamDocuments(ctx, n.teamID)
	}
	if n.projectID != "" {
		return n.lfs.repo.GetProjectDocuments(ctx, n.projectID)
	}
	if n.initiativeID != "" {
		return n.lfs.repo.GetInitiativeDocuments(ctx, n.initiativeID)
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
	return n.collection().readdir(ctx)
}

// collection is the item-file surface (Readdir/Lookup/Unlink) for docs/. The
// refresh is nil: getDocuments already triggers MaybeRefreshIssueDetails for
// issue docs internally.
func (n *DocsNode) collection() collectionDir[api.Document] {
	return collectionDir[api.Document]{
		parent:       n,
		lfs:          n.lfs,
		trio:         n.trio(),
		noun:         "document",
		fetch:        n.getDocuments,
		listing:      func(items []api.Document) collectionListing[api.Document] { return n.listing(items) },
		idOf:         func(d api.Document) string { return d.ID },
		buildFile:    n.newDocumentInode,
		metaMarshal:  marshal.DocumentMetaToMarkdown,
		metaTimes:    func(d api.Document) (time.Time, time.Time) { return d.UpdatedAt, d.CreatedAt },
		metaIno:      func(d api.Document) uint64 { return documentMetaIno(d.ID) },
		deleteMutate: func(ctx context.Context, d *api.Document) error { return n.lfs.mutator().DeleteDocument(ctx, d.ID) },
		deleteForget: func(ctx context.Context, d *api.Document) error {
			return n.lfs.store.Queries().DeleteDocument(ctx, d.ID)
		},
	}
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
	return n.collection().lookup(ctx, name, out)
}

func (n *DocsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.collection().unlink(ctx, name)
}

// Rename renames a document by changing its title on Linear. The whole rename
// tail — special-name/cross-dir guards, name parsing, find, mutate, persist gate,
// and kernel re-coherence of the .md and its .meta twin — lives in commitRename;
// this handler is the document-specific spec.
func (n *DocsNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	return commitRename(ctx, n.lfs, name, newParent, newName, renameSpec[api.Document]{
		kind:   "document",
		errKey: collectionErrorKey("docs", n.parentID()),
		dirIno: docsDirIno(n.parentID()),
		// Route through the same resolve Lookup/Unlink use so a rename can never
		// ENOENT a document those still resolve (#293).
		find: func(ctx context.Context) (*api.Document, error) { return n.collection().resolve(ctx, name) },
		mutate: func(ctx context.Context, target *api.Document, newName string) (*api.Document, error) {
			return n.lfs.UpdateDocument(ctx, target.ID, map[string]any{"title": newName}, n.issueID, n.teamID, n.projectID)
		},
		persist: func(ctx context.Context, fresh *api.Document) error {
			return n.lfs.UpsertDocument(ctx, *fresh)
		},
	})
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
	// The user-chosen filename feeds the title fallback (createDocument(name)).
	return n.collection().create(ctx, name, flags, out, n.createDocument(name))
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
	docErrKey := collectionErrorKey("docs", docParentID(n.issueID, n.teamID, n.projectID, n.initiativeID))
	// update + updatedDoc bridge the front half to the commit tail.
	var update map[string]any
	var updatedDoc *api.Document
	return editFlush(ctx, n.lfs, &n.editBuffer, editFlushSpec[api.Document]{
		mutate: func(ctx context.Context) (bool, syscall.Errno) {
			var err error
			update, err = marshal.MarkdownToDocumentUpdate(n.content, &n.document)
			if err != nil {
				log.Printf("Failed to parse document: %v", err)
				n.lfs.SetWriteError(docErrKey, "Operation: update document "+documentFilename(n.document)+"\nParse error: "+err.Error())
				return false, syscall.EINVAL
			}
			if len(update) == 0 {
				if n.lfs.debug {
					log.Printf("Flush document %s: no changes", n.document.ID)
				}
				return false, 0
			}
			if n.lfs.debug {
				log.Printf("Updating document %s", n.document.ID)
			}
			updatedDoc, err = n.lfs.UpdateDocument(ctx, n.document.ID, update, n.issueID, n.teamID, n.projectID)
			if err != nil {
				log.Printf("Failed to update document: %v", err)
				msg, errno := classifyMutationErr("update document "+documentFilename(n.document), err)
				n.lfs.SetWriteError(docErrKey, msg)
				return false, errno
			}
			return true, 0
		},
		// Edit-commit tail: verify read-your-writes against the API's echoed
		// response, persist, and surface divergence via .error.
		writeBack: writeBackSpec[api.Document]{
			errKey:  docErrKey,
			op:      "save document " + documentFilename(n.document),
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
		},
		adopt:     func(fresh *api.Document) { n.document = *fresh },
		coherence: []uint64{documentIno(n.document.ID), documentMetaIno(n.document.ID)},
	})
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
