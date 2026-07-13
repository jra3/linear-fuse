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
	"github.com/jra3/linear-fuse/internal/db"
)

// LinksNode represents a links/ directory on a project or initiative — the
// "Links / Resources" surface backed by Linear's EntityExternalLink entity
// (distinct from issue attachments, which live in AttachmentsNode). Exactly one
// of projectID/initiativeID is set, dispatched like DocsNode.
type LinksNode struct {
	attrNode
	projectID    string
	initiativeID string
}

var _ fs.NodeReaddirer = (*LinksNode)(nil)
var _ fs.NodeLookuper = (*LinksNode)(nil)
var _ fs.NodeGetattrer = (*LinksNode)(nil)

// parentID returns the single non-empty parent ID (project or initiative). Used
// for the .error/.last key, the kernel-cache inode, and the create input.
func (n *LinksNode) parentID() string {
	if n.projectID != "" {
		return n.projectID
	}
	return n.initiativeID
}

// getLinks fetches the external links for whichever parent is set.
func (n *LinksNode) getLinks(ctx context.Context) ([]api.EntityExternalLink, error) {
	if n.projectID != "" {
		return n.lfs.repo.GetProjectLinks(ctx, n.projectID)
	}
	return n.lfs.repo.GetInitiativeLinks(ctx, n.initiativeID)
}

// liveLinks fetches the parent's links straight from the API, promoted to
// interactive so a tight detail budget can't stall a user's blocking write.
func (n *LinksNode) liveLinks(ctx context.Context) ([]api.EntityExternalLink, error) {
	ctx = api.WithInteractive(ctx)
	if n.projectID != "" {
		return n.lfs.client.GetProjectLinks(ctx, n.projectID)
	}
	return n.lfs.client.GetInitiativeLinks(ctx, n.initiativeID)
}

// dir constructs the read-only listing head. Listing is best-effort: a failed
// fetch lists empty rather than failing the whole directory
// (failReaddirOnError=false).
func (n *LinksNode) dir() listingDir[linkEntry] {
	return listingDir[linkEntry]{
		parent:  n,
		lfs:     n.lfs,
		trio:    n.trio(),
		listing: func(ctx context.Context, fetchErr *error) infoListing[linkEntry] { return n.listing(ctx, fetchErr) },
		nameOf:  func(e linkEntry) string { return e.name },
		build: func(ctx context.Context, name string, e linkEntry, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
			return n.createExternalLinkNode(ctx, name, *e.link, out), 0
		},
	}
}

func (n *LinksNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return n.dir().readdir(ctx)
}

// listing fetches the links and builds the name-derivation module. A failed
// fetch leaves it empty; when fetchErr is non-nil it also records the error so
// Lookup distinguishes "not found" from "couldn't look".
func (n *LinksNode) listing(ctx context.Context, fetchErr *error) linkListing {
	links, err := n.getLinks(ctx)
	if fetchErr != nil && err != nil {
		*fetchErr = err
	}
	return linkListing{links: links}
}

// trio declares the links collection's writable surfaces.
func (n *LinksNode) trio() collectionTrio {
	return collectionTrio{kind: "links", parentID: n.parentID(), onFlush: n.createLink}
}

func (n *LinksNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return n.dir().lookup(ctx, name, out)
}

func (n *LinksNode) createExternalLinkNode(ctx context.Context, name string, link api.EntityExternalLink, out *fuse.EntryOut) *fs.Inode {
	node := &ExternalLinkNode{
		renderFile: renderFile{
			BaseNode: BaseNode{lfs: n.lfs},
			render: func(context.Context) ([]byte, time.Time, time.Time) {
				return []byte(externalLinkContent(link)), link.UpdatedAt, link.CreatedAt
			},
		},
		link:         link,
		projectID:    n.projectID,
		initiativeID: n.initiativeID,
	}
	return n.newRenderInode(ctx, out, name, node, externalLinkIno(link.ID), 30*time.Second)
}

// ExternalLinkNode represents a .link file for a project/initiative external
// link. It embeds renderFile for Open/Read/Getattr and keeps only its Unlink.
type ExternalLinkNode struct {
	renderFile
	link         api.EntityExternalLink
	projectID    string
	initiativeID string
}

var _ fs.NodeUnlinker = (*ExternalLinkNode)(nil)

// refreshFrom adopts a fresh twin's link and render closure (refresh.go);
// renderMu doubles as the entity-field lock.
func (n *ExternalLinkNode) refreshFrom(fresh fs.InodeEmbedder) {
	f, ok := fresh.(*ExternalLinkNode)
	if !ok {
		return
	}
	n.renderMu.Lock()
	n.render = f.render
	n.link, n.projectID, n.initiativeID = f.link, f.projectID, f.initiativeID
	n.renderMu.Unlock()
}

// externalLinkContent renders a .link file's YAML body.
func externalLinkContent(link api.EntityExternalLink) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("label: %s\n", link.Label))
	sb.WriteString(fmt.Sprintf("url: %s\n", link.URL))
	return sb.String()
}

func (n *ExternalLinkNode) parentID() string {
	if n.projectID != "" {
		return n.projectID
	}
	return n.initiativeID
}

func (n *ExternalLinkNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return commitDelete(ctx, n.lfs, deleteSpec[api.EntityExternalLink]{
		op:  `delete link "` + name + `"`,
		key: collectionErrorKey("links", n.parentID()),
		find: func(context.Context) (*api.EntityExternalLink, error) {
			// Snapshot: a concurrent refresh (refresh.go) may swap the field.
			n.renderMu.Lock()
			link := n.link
			n.renderMu.Unlock()
			return &link, nil
		},
		mutate: func(ctx context.Context, l *api.EntityExternalLink) error {
			return n.lfs.mutator().DeleteEntityExternalLink(ctx, l.ID)
		},
		forget: func(ctx context.Context, l *api.EntityExternalLink) error {
			return n.lfs.store.Queries().DeleteEntityExternalLink(ctx, l.ID)
		},
		dir:  linksDirIno(n.parentID()),
		name: name,
	})
}

// createLink is the links create surface's onFlush: parse "url [label]" and run
// the create tail.
func (n *LinksNode) createLink(ctx context.Context, raw []byte) syscall.Errno {
	content := strings.TrimSpace(string(raw))

	// Idempotency: if the URL is already linked, re-linking it is a success, not
	// a duplicate. Unlike issue attachments Linear does NOT dedup external links
	// by URL server-side, so the cache pre-check is the only guard against a
	// retry minting a second identical link. It returns 0 without a .last entry
	// (nothing was created).
	if url := strings.SplitN(content, " ", 2)[0]; url != "" {
		if existing, err := n.getLinks(ctx); err == nil {
			for _, l := range existing {
				if linkURLsEqual(l.URL, url) {
					n.lfs.ClearWriteError(collectionErrorKey("links", n.parentID()))
					return 0
				}
			}
		}
	}

	_, errno := commitCreate(ctx, n.lfs, createSpec[api.EntityExternalLink]{
		op:  "create link",
		key: collectionErrorKey("links", n.parentID()),
		mutate: func(ctx context.Context) (*api.EntityExternalLink, error) {
			if content == "" {
				return nil, &FieldError{Field: "content", Message: `empty content. Write "<url> [label]".`}
			}
			// Parse "url [label]" or just "url" (label defaults to the URL).
			parts := strings.SplitN(content, " ", 2)
			url := parts[0]
			label := url
			if len(parts) > 1 {
				label = parts[1]
			}

			input := map[string]any{"url": url, "label": label}
			if n.projectID != "" {
				input["projectId"] = n.projectID
			} else {
				input["initiativeId"] = n.initiativeID
			}

			link, err := n.lfs.mutator().CreateEntityExternalLink(ctx, input)
			if err == nil {
				return link, nil
			}
			// The mutation may have committed before the response was lost (a
			// network blip). Re-check authoritatively: if the URL is now linked,
			// treat it as the idempotent success it is.
			if live, lerr := n.liveLinks(ctx); lerr == nil {
				for _, ex := range live {
					if linkURLsEqual(ex.URL, url) {
						return &ex, nil
					}
				}
			}
			return nil, err
		},
		result: func(l *api.EntityExternalLink) WriteResult {
			return WriteResult{URL: l.URL, Path: externalLinkName(*l), Title: l.Label}
		},
		persist: func(ctx context.Context, l *api.EntityExternalLink) error {
			n.upsertLink(ctx, *l)
			return nil
		},
		dir:       linksDirIno(n.parentID()),
		entryName: func(l *api.EntityExternalLink) string { return externalLinkName(*l) },
	})
	return errno
}

// upsertLink writes a link to SQLite for immediate visibility. Failures are
// logged, not fatal: the SWR refresh will reconcile.
func (n *LinksNode) upsertLink(ctx context.Context, link api.EntityExternalLink) {
	params, err := db.APIEntityExternalLinkToDB(link, n.projectID, n.initiativeID)
	if err != nil {
		log.Printf("[links] convert for DB failed: %v", err)
		return
	}
	if err := n.lfs.store.Queries().UpsertEntityExternalLink(ctx, params); err != nil {
		log.Printf("[links] upsert to DB failed: %v", err)
	}
}

// linkURLsEqual reports whether two link URLs refer to the same target, ignoring
// surrounding whitespace and a trailing slash — the same tolerance
// attachmentURLsEqual applies.
func linkURLsEqual(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}
