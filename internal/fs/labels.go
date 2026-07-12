package fs

import (
	"context"
	"errors"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// LabelsNode represents the /teams/{KEY}/labels/ directory
type LabelsNode struct {
	attrNode
	teamID string
}

var _ fs.NodeReaddirer = (*LabelsNode)(nil)
var _ fs.NodeLookuper = (*LabelsNode)(nil)
var _ fs.NodeGetattrer = (*LabelsNode)(nil)
var _ fs.NodeCreater = (*LabelsNode)(nil)
var _ fs.NodeUnlinker = (*LabelsNode)(nil)
var _ fs.NodeRenamer = (*LabelsNode)(nil)

func (n *LabelsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return n.collection().readdir(ctx)
}

// collection is the item-file surface (Readdir/Lookup/Unlink) for labels/.
// api.Label carries no timestamps, so metaTimes is zero — an honest "unknown"
// (see the renderFile rule), never a fabricated now().
func (n *LabelsNode) collection() collectionDir[api.Label] {
	return collectionDir[api.Label]{
		parent:       n,
		lfs:          n.lfs,
		trio:         n.trio(),
		noun:         "label",
		fetch:        func(ctx context.Context) ([]api.Label, error) { return n.lfs.repo.GetTeamLabels(ctx, n.teamID) },
		listing:      func(items []api.Label) collectionListing[api.Label] { return n.listing(items) },
		idOf:         func(l api.Label) string { return l.ID },
		buildFile:    n.newLabelInode,
		metaMarshal:  marshal.LabelMetaToMarkdown,
		metaTimes:    func(api.Label) (time.Time, time.Time) { return time.Time{}, time.Time{} },
		metaIno:      func(l api.Label) uint64 { return labelMetaIno(l.ID) },
		deleteMutate: func(ctx context.Context, l *api.Label) error { return n.lfs.mutator().DeleteLabel(ctx, l.ID) },
		deleteForget: func(ctx context.Context, l *api.Label) error { return n.lfs.store.Queries().DeleteLabel(ctx, l.ID) },
	}
}

// trio declares the labels collection's writable surfaces.
func (n *LabelsNode) trio() collectionTrio {
	return collectionTrio{kind: "labels", parentID: n.teamID, onFlush: n.createLabel}
}

// listing declares the labels collection's item files: one per label, named by
// labelFilename. Backs Readdir/Lookup/Unlink/Rename/Create-overwrite so they
// derive and match names through one place. See namedListing.
func (n *LabelsNode) listing(labels []api.Label) namedListing[api.Label] {
	return namedListing[api.Label]{items: labels, nameOf: labelFilename}
}

func (n *LabelsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return n.collection().lookup(ctx, name, out)
}

// newLabelInode builds the read/write LabelFileNode inode for an existing label,
// populated with its current content. Shared by Lookup and Create.
func (n *LabelsNode) newLabelInode(ctx context.Context, name string, label api.Label, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	content, err := marshal.LabelToMarkdown(&label)
	if err != nil {
		log.Printf("Failed to marshal label: %v", err)
		return nil, syscall.EIO
	}
	node := &LabelFileNode{
		BaseNode:   BaseNode{lfs: n.lfs},
		label:      label,
		teamID:     n.teamID,
		editBuffer: editBuffer{content: content},
	}
	now := time.Now()
	out.Attr.Mode = 0644 | syscall.S_IFREG
	out.Attr.Uid = n.lfs.uid
	out.Attr.Gid = n.lfs.gid
	out.Attr.Size = uint64(len(content))
	out.Attr.SetTimes(&now, &now, &now)
	out.SetAttrTimeout(5 * time.Second)  // Shorter timeout for writable files
	out.SetEntryTimeout(5 * time.Second) // Shorter timeout for writable files
	// The bridge dedups AFTER this handler returns: push the fresh
	// label/content into the node it will keep (see refresh.go).
	refreshExisting(n, name, node)
	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  labelIno(label.ID),
	}), 0
}

func (n *LabelsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.collection().unlink(ctx, name)
}

func (n *LabelsNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Rename label: %s -> %s (newParent type: %T)", name, newName, newParent)
	}

	// Don't allow renaming _create
	if name == "_create" {
		return syscall.EPERM
	}

	// The .meta sidecar is read-only; its name follows the .md's.
	if _, isMeta := metaSidecarSource(name); isMeta {
		return syscall.EPERM
	}

	// For same-directory rename, newParent should be the same inode as us
	// Compare by inode number
	if newParent.EmbeddedInode().StableAttr().Ino != n.EmbeddedInode().StableAttr().Ino {
		if n.lfs.debug {
			log.Printf("Rename: cross-directory not allowed")
		}
		return syscall.EXDEV
	}

	// Extract new label name from filename (remove .md suffix, convert dashes to spaces)
	if !strings.HasSuffix(newName, ".md") {
		return syscall.EINVAL
	}
	newLabelName := strings.TrimSuffix(newName, ".md")
	newLabelName = strings.ReplaceAll(newLabelName, "-", " ")

	labels, err := n.lfs.repo.GetTeamLabels(ctx, n.teamID)
	if err != nil {
		return syscall.EIO
	}

	label, ok := n.listing(labels).find(name)
	if !ok {
		return syscall.ENOENT
	}

	// Update label name
	updatedLabel, err := n.lfs.UpdateLabel(ctx, label.ID, map[string]any{"name": newLabelName}, n.teamID)
	if err != nil {
		log.Printf("Failed to rename label: %v", err)
		msg, errno := classifyMutationErr("rename label "+name+" -> "+newName, err)
		n.lfs.SetWriteError(collectionErrorKey("labels", n.teamID), msg)
		return errno
	}
	// Upsert to SQLite so it's immediately visible
	if err := n.lfs.UpsertLabel(ctx, n.teamID, *updatedLabel); err != nil {
		log.Printf("Warning: failed to upsert label to SQLite: %v", err)
	}
	if n.lfs.debug {
		log.Printf("Label renamed successfully: %s -> %s", label.Name, newLabelName)
	}
	// Invalidate kernel cache for old and new names — the .meta sidecar's
	// name follows the .md's, so both pairs move.
	n.lfs.InvalidateRenamed(labelsDirIno(n.teamID), name, newName, 0)
	n.lfs.InvalidateRenamed(labelsDirIno(n.teamID), metaSidecarName(name), metaSidecarName(newName), 0)
	return 0
}

func (n *LabelsNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create label file: %s", name)
	}

	// Only allow creating .md files
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}

	// If a label already exists with this name, return its read/write node so an
	// overwrite (mv/cp/editor save-over) updates it in place instead of binding a
	// write-only _create node to the name and corrupting it (#137).
	if labels, err := n.lfs.repo.GetTeamLabels(ctx, n.teamID); err == nil {
		if label, ok := n.listing(labels).find(name); ok {
			inode, errno := n.newLabelInode(ctx, name, label, out)
			if errno != 0 {
				return nil, nil, 0, errno
			}
			return inode, nil, 0, 0
		}
	}

	node := newCreateFile(n.lfs, n.createLabel)
	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})

	return inode, &createFileHandle{}, fuse.FOPEN_DIRECT_IO, 0
}

// labelFilename returns the filename for a label
func labelFilename(label api.Label) string {
	// Sanitize name for filename
	name := strings.ReplaceAll(label.Name, " ", "-")
	name = strings.ReplaceAll(name, "/", "-")
	return name + ".md"
}

// LabelFileNode represents a single label file (read-write)
type LabelFileNode struct {
	BaseNode
	editBuffer
	label  api.Label
	teamID string
}

var _ fs.NodeGetattrer = (*LabelFileNode)(nil)
var _ fs.NodeOpener = (*LabelFileNode)(nil)
var _ fs.NodeReader = (*LabelFileNode)(nil)
var _ fs.NodeWriter = (*LabelFileNode)(nil)
var _ fs.NodeFlusher = (*LabelFileNode)(nil)
var _ fs.NodeFsyncer = (*LabelFileNode)(nil)
var _ fs.NodeSetattrer = (*LabelFileNode)(nil)

func (n *LabelFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	// api.Label carries no timestamps, so there is nothing to report but now().
	now := time.Now()
	fileAttr(n.size(), now, now).fill(&out.Attr, &n.BaseNode)
	return 0
}

// refreshFrom adopts a fresh twin's label and rendered content unless an edit
// is in flight — the dirty buffer always wins (refresh.go).
func (n *LabelFileNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*LabelFileNode); ok {
		n.refresh(f.content, func() { n.label, n.teamID = f.label, f.teamID })
	}
}

func (n *LabelFileNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.dirty || n.content == nil {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Parse the markdown and get update fields
	update, err := marshal.MarkdownToLabelUpdate(n.content, &n.label)
	if err != nil {
		log.Printf("Failed to parse label: %v", err)
		n.lfs.SetWriteError(collectionErrorKey("labels", n.teamID), "Operation: update label "+labelFilename(n.label)+"\nParse error: "+err.Error())
		return syscall.EINVAL
	}

	if len(update) == 0 {
		if n.lfs.debug {
			log.Printf("Flush label %s: no changes", n.label.ID)
		}
		n.dirty = false
		return 0
	}

	if n.lfs.debug {
		log.Printf("Updating label %s", n.label.ID)
	}

	updatedLabel, err := n.lfs.UpdateLabel(ctx, n.label.ID, update, n.teamID)
	if err != nil {
		log.Printf("Failed to update label: %v", err)
		msg, errno := classifyMutationErr("update label "+labelFilename(n.label), err)
		n.lfs.SetWriteError(collectionErrorKey("labels", n.teamID), msg)
		return errno
	}
	// Edit-commit tail: persist the label, verify read-your-writes against the
	// API's echoed response (labels have no single-entity getter), and surface
	// divergence via .error — verification this handler previously skipped.
	labelErrKey := collectionErrorKey("labels", n.teamID)
	fresh, errno := commitWriteBack(ctx, n.lfs, writeBackSpec[api.Label]{
		errKey:  labelErrKey,
		fetch:   func(ctx context.Context) (*api.Label, error) { return updatedLabel, nil },
		persist: func(ctx context.Context, fresh *api.Label) error { return n.lfs.UpsertLabel(ctx, n.teamID, *fresh) },
		compare: func(fresh *api.Label) []writeBackResult {
			var results []writeBackResult
			if want, ok := update["name"].(string); ok {
				results = append(results, writeBackDivergence("name", want, fresh.Name, n.label.Name))
			}
			if want, ok := update["description"].(string); ok {
				results = append(results, writeBackDivergence("description", want, fresh.Description, n.label.Description))
			}
			return results
		},
	})
	if fresh != nil {
		n.label = *fresh
	}

	n.dirty = false

	// Invalidate kernel inode cache (the .meta sidecar renders from the label)
	n.lfs.InvalidateUpdated(labelIno(n.label.ID))
	n.lfs.InvalidateUpdated(labelMetaIno(n.label.ID))

	return errno
}

// createLabel is the labels create surface's onFlush: parse the frontmatter
// and run the create tail.
func (n *LabelsNode) createLabel(ctx context.Context, content []byte) syscall.Errno {
	_, errno := commitCreate(ctx, n.lfs, createSpec[api.Label]{
		op:  "create label",
		key: collectionErrorKey("labels", n.teamID),
		mutate: func(ctx context.Context) (*api.Label, error) {
			name, color, description, err := marshal.ParseNewLabel(content)
			if err != nil {
				// A *FieldError (e.g. the unquoted-color guard) already names
				// the field; only wrap the shapeless parse failures.
				var ferr *FieldError
				if errors.As(err, &ferr) {
					return nil, ferr
				}
				return nil, &FieldError{Field: "content", Message: "parse error: " + err.Error()}
			}
			if name == "" {
				return nil, &FieldError{Field: "name", Message: "label has no name. Add a 'name:' field to the frontmatter."}
			}
			input := map[string]any{
				"teamId": n.teamID,
				"name":   name,
			}
			if color != "" {
				input["color"] = color
			}
			if description != "" {
				input["description"] = description
			}
			return n.lfs.mutator().CreateLabel(ctx, input)
		},
		result: func(l *api.Label) WriteResult {
			return WriteResult{
				Path:  labelFilename(*l),
				Title: l.Name,
			}
		},
		persist: func(ctx context.Context, l *api.Label) error {
			return n.lfs.UpsertLabel(ctx, n.teamID, *l)
		},
		dir:       labelsDirIno(n.teamID),
		entryName: func(l *api.Label) string { return labelFilename(*l) },
	})
	return errno
}
