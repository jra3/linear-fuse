package fs

import (
	"bytes"
	"context"
	"fmt"
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

// InitiativesNode represents the /initiatives directory
type InitiativesNode struct {
	BaseNode
}

var _ fs.NodeReaddirer = (*InitiativesNode)(nil)
var _ fs.NodeLookuper = (*InitiativesNode)(nil)
var _ fs.NodeGetattrer = (*InitiativesNode)(nil)

func (i *InitiativesNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	i.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (i *InitiativesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	initiatives, err := i.lfs.GetInitiatives(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(initiatives))
	for idx, init := range initiatives {
		entries[idx] = fuse.DirEntry{
			Name: initiativeDirName(init),
			Mode: syscall.S_IFDIR,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (i *InitiativesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	initiatives, err := i.lfs.GetInitiatives(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, init := range initiatives {
		if initiativeDirName(init) == name {
			out.Attr.Mode = 0755 | syscall.S_IFDIR
			out.Attr.Uid = i.lfs.uid
			out.Attr.Gid = i.lfs.gid
			out.Attr.SetTimes(&init.UpdatedAt, &init.UpdatedAt, &init.CreatedAt)
			node := &InitiativeNode{BaseNode: BaseNode{lfs: i.lfs}, initiative: init}
			return i.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	return nil, syscall.ENOENT
}

// initiativeDirName returns a safe directory name for an initiative
func initiativeDirName(init api.Initiative) string {
	// Always derive from name (Linear's slugId for initiatives is not human-readable)
	name := strings.ToLower(init.Name)
	name = strings.ReplaceAll(name, " ", "-")
	name = dirNameUnsafe.ReplaceAllString(name, "")
	if name != "" {
		return name
	}
	// Fallback to ID only if name is empty
	return init.ID
}

// InitiativeNode represents a single initiative directory
type InitiativeNode struct {
	BaseNode
	initiative api.Initiative
}

var _ fs.NodeReaddirer = (*InitiativeNode)(nil)
var _ fs.NodeLookuper = (*InitiativeNode)(nil)
var _ fs.NodeGetattrer = (*InitiativeNode)(nil)
var _ fs.NodeCreater = (*InitiativeNode)(nil)
var _ fs.NodeRenamer = (*InitiativeNode)(nil)
var _ fs.NodeUnlinker = (*InitiativeNode)(nil)

func (i *InitiativeNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	i.SetOwner(out)
	out.SetTimes(&i.initiative.UpdatedAt, &i.initiative.UpdatedAt, &i.initiative.CreatedAt)
	return 0
}

func (i *InitiativeNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "initiative.md", Mode: syscall.S_IFREG},
		{Name: "initiative.meta", Mode: syscall.S_IFREG},
		{Name: ".error", Mode: syscall.S_IFREG},
		{Name: "docs", Mode: syscall.S_IFDIR},
		{Name: "projects", Mode: syscall.S_IFDIR},
		{Name: "updates", Mode: syscall.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (i *InitiativeNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "initiative.md":
		node := &InitiativeInfoNode{BaseNode: BaseNode{lfs: i.lfs}, initiative: i.initiative, initiativeID: i.initiative.ID}
		content := node.generateContent()
		return i.newFileInode(ctx, out, node, fileAttr(len(content), i.initiative.CreatedAt, i.initiative.UpdatedAt), initiativeInfoIno(i.initiative.ID), 0), 0

	case "initiative.meta":
		// Read-through from the freshest initiative so an edit to initiative.md is
		// reflected here (go-fuse reuses this node across lookups).
		lfs := i.lfs
		snapshot := i.initiative
		render := func() ([]byte, time.Time, time.Time) {
			init := snapshot
			if inits, err := lfs.GetInitiatives(context.Background()); err == nil {
				for _, it := range inits {
					if it.ID == snapshot.ID {
						init = it
						break
					}
				}
			}
			node := &InitiativeInfoNode{BaseNode: BaseNode{lfs: lfs}, initiative: init, initiativeID: init.ID}
			return node.metaContent(), init.UpdatedAt, init.CreatedAt
		}
		return i.lfs.lookupMetaFile(ctx, i, i.initiative.ID, render, out), 0

	case ".error":
		return i.lfs.lookupErrorFile(ctx, i, i.initiative.ID, out), 0

	case "docs":
		node := &DocsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: i.lfs}}, initiativeID: i.initiative.ID}
		return i.newDirInode(ctx, out, node, dirAttr(i.initiative.CreatedAt, i.initiative.UpdatedAt), docsDirIno(i.initiative.ID), 0), 0

	case "projects":
		node := &InitiativeProjectsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: i.lfs}}, initiative: i.initiative}
		return i.newDirInode(ctx, out, node, dirAttr(i.initiative.CreatedAt, i.initiative.UpdatedAt), initiativeProjectsIno(i.initiative.ID), 0), 0

	case "updates":
		node := &InitiativeUpdatesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: i.lfs}}, initiativeID: i.initiative.ID}
		return i.newDirInode(ctx, out, node, dirAttr(i.initiative.CreatedAt, i.initiative.UpdatedAt), initiativeUpdatesDirIno(i.initiative.ID), 0), 0
	}

	return nil, syscall.ENOENT
}

// Create accepts an editor's atomic-save temp file (e.g. initiative.md.tmp.<pid>.<rand>)
// as an in-memory scratch buffer so Rename can route its bytes into
// initiative.md's write path. Without it, go-fuse rejects the temp-file create
// with a misleading EROFS on the rw mount (#145).
func (i *InitiativeNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if i.lfs.debug {
		log.Printf("Create scratch file in initiative %s: %s", i.initiative.Name, name)
	}
	return newScratchInode(ctx, &i.BaseNode, i.EmbeddedInode().StableAttr().Ino, name, out)
}

// Rename persists an editor's atomic save: a scratch temp file renamed onto
// initiative.md is written through initiative.md's normal Flush path.
// initiative.md is the only writable file here, so other targets are rejected.
func (i *InitiativeNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if i.lfs.debug {
		log.Printf("Rename in initiative %s: %s -> %s", i.initiative.Name, name, newName)
	}

	dirIno := i.EmbeddedInode().StableAttr().Ino
	if newParent.EmbeddedInode().StableAttr().Ino != dirIno {
		return syscall.EXDEV
	}

	content, ok := scratchRenameBytes(i, name)
	if !ok {
		return syscall.ENOTSUP
	}

	if newName != "initiative.md" {
		i.lfs.SetWriteError(i.initiative.ID, fmt.Sprintf("Operation: rename %s -> %s\nError: only initiative.md is writable in this directory; save your changes onto initiative.md (atomic save-via-rename onto initiative.md is supported).", name, newName))
		return syscall.ENOTSUP
	}

	fileNode := &InitiativeInfoNode{
		BaseNode:     BaseNode{lfs: i.lfs},
		initiative:   i.initiative,
		initiativeID: i.initiative.ID,
		content:      content,
		contentReady: true,
		dirty:        true,
	}
	errno := fileNode.Flush(ctx, nil)

	if errno == 0 || errno == syscall.EIO {
		i.initiative = fileNode.initiative
		i.lfs.InvalidateRenamed(dirIno, name, newName, initiativeInfoIno(i.initiative.ID))
	}

	return errno
}

// Unlink lets editors clean up an abandoned atomic-save temp file. Only scratch
// files we created are removable; the canonical entries are not.
func (i *InitiativeNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if _, ok := scratchRenameBytes(i, name); ok {
		return 0
	}
	return syscall.EPERM
}

// InitiativeInfoNode is a virtual file containing initiative metadata
type InitiativeInfoNode struct {
	BaseNode
	initiative   api.Initiative
	initiativeID string

	// Write buffer and cached content
	mu           sync.Mutex
	content      []byte
	contentReady bool
	dirty        bool
}

var _ fs.NodeGetattrer = (*InitiativeInfoNode)(nil)
var _ fs.NodeOpener = (*InitiativeInfoNode)(nil)
var _ fs.NodeReader = (*InitiativeInfoNode)(nil)
var _ fs.NodeWriter = (*InitiativeInfoNode)(nil)
var _ fs.NodeFlusher = (*InitiativeInfoNode)(nil)
var _ fs.NodeFsyncer = (*InitiativeInfoNode)(nil)
var _ fs.NodeSetattrer = (*InitiativeInfoNode)(nil)

// generateContent renders the editable-only initiative.md: name, linked project
// slugs, and the description body. Server-managed fields live in initiative.meta
// (#150) so a successful write never rewrites the bytes the writer wrote.
func (i *InitiativeInfoNode) generateContent() []byte {
	fm := map[string]any{"name": i.initiative.Name}

	if len(i.initiative.Projects.Nodes) > 0 {
		slugs := make([]string, len(i.initiative.Projects.Nodes))
		for j, p := range i.initiative.Projects.Nodes {
			slugs[j] = p.Slug
		}
		fm["projects"] = slugs
	}

	out, err := marshal.Render(&marshal.Document{Frontmatter: fm, Body: i.initiative.Description})
	if err != nil {
		return []byte{}
	}
	return out
}

// metaContent renders the read-only initiative.meta: server-managed identity,
// status, owner, appearance, and timestamps as a frontmatter-only block.
func (i *InitiativeInfoNode) metaContent() []byte {
	fm := map[string]any{
		"id":      i.initiative.ID,
		"slug":    i.initiative.Slug,
		"url":     i.initiative.URL,
		"status":  i.initiative.Status,
		"created": i.initiative.CreatedAt.Format(time.RFC3339),
		"updated": i.initiative.UpdatedAt.Format(time.RFC3339),
	}
	if i.initiative.Color != "" {
		fm["color"] = i.initiative.Color
	}
	if i.initiative.Icon != "" {
		fm["icon"] = i.initiative.Icon
	}
	if i.initiative.Owner != nil {
		fm["owner"] = map[string]any{
			"id":    i.initiative.Owner.ID,
			"name":  i.initiative.Owner.Name,
			"email": i.initiative.Owner.Email,
		}
	}
	if i.initiative.TargetDate != nil {
		fm["targetDate"] = *i.initiative.TargetDate
	}
	out, err := marshal.Render(&marshal.Document{Frontmatter: fm})
	if err != nil {
		return []byte{}
	}
	return out
}

func (i *InitiativeInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	var size int
	if i.contentReady && i.content != nil {
		size = len(i.content)
	} else {
		size = len(i.generateContent())
	}
	out.Mode = 0644 | syscall.S_IFREG
	i.SetOwner(out)
	out.Size = uint64(size)
	out.Attr.SetTimes(&i.initiative.UpdatedAt, &i.initiative.UpdatedAt, &i.initiative.CreatedAt)
	return 0
}

func (i *InitiativeInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (i *InitiativeInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.contentReady {
		i.content = i.generateContent()
		i.contentReady = true
	}

	if off >= int64(len(i.content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(i.content)) {
		end = int64(len(i.content))
	}
	return fuse.ReadResultData(i.content[off:end]), 0
}

func (i *InitiativeInfoNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Initialize content if not ready
	if !i.contentReady {
		i.content = i.generateContent()
		i.contentReady = true
	}

	// Expand buffer if needed
	end := off + int64(len(data))
	if end > int64(len(i.content)) {
		newContent := make([]byte, end)
		copy(newContent, i.content)
		i.content = newContent
	}

	copy(i.content[off:], data)
	i.dirty = true
	return uint32(len(data)), 0
}

func (i *InitiativeInfoNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if !i.contentReady {
			i.content = i.generateContent()
			i.contentReady = true
		}
		if int(sz) < len(i.content) {
			i.content = i.content[:sz]
			i.dirty = true
		}
	}

	var size int
	if i.contentReady && i.content != nil {
		size = len(i.content)
	} else {
		size = len(i.generateContent())
	}
	out.Mode = 0644 | syscall.S_IFREG
	out.Size = uint64(size)
	return 0
}

// Fsync is a no-op; actual persistence happens in Flush. It must be
// implemented (not return ENOTSUP) so editors that write-then-fsync
// (e.g. Claude Code's Edit tool, vim, VS Code) can save initiative.md.
func (i *InitiativeInfoNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

func (i *InitiativeInfoNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.dirty || i.content == nil {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if i.lfs.debug {
		log.Printf("Flush: initiative %s (saving changes)", i.initiative.Name)
	}

	// Parse the modified content
	doc, err := marshal.Parse(i.content)
	if err != nil {
		log.Printf("Failed to parse initiative changes for %s: %v", i.initiative.Name, err)
		i.lfs.SetWriteError(i.initiativeID, "Parse error: "+err.Error())
		return syscall.EINVAL
	}

	// Extract projects from frontmatter
	var newProjectSlugs []string
	if projectsRaw, ok := doc.Frontmatter["projects"]; ok {
		switch v := projectsRaw.(type) {
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					newProjectSlugs = append(newProjectSlugs, s)
				}
			}
		case []string:
			newProjectSlugs = v
		}
	}

	// Get current project slugs
	var currentProjectSlugs []string
	for _, proj := range i.initiative.Projects.Nodes {
		currentProjectSlugs = append(currentProjectSlugs, proj.Slug)
	}

	// Reconcile the initiative's project links (front half of the edit). The
	// link/unlink closures own the API mutation and the immediate junction-row
	// write; reconcileLinks owns the diff and the resolve-error classification.
	if err := reconcileLinks(ctx, linkReconcileSpec{
		current: currentProjectSlugs,
		desired: newProjectSlugs,
		resolve: i.lfs.ResolveProjectSlugToID,
		link: func(ctx context.Context, projectID string) error {
			if err := i.lfs.mutator().AddProjectToInitiative(ctx, projectID, i.initiativeID); err != nil {
				return err
			}
			i.lfs.persistInitiativeProjectLink(ctx, i.initiativeID, projectID, true)
			return nil
		},
		unlink: func(ctx context.Context, projectID string) error {
			if err := i.lfs.mutator().RemoveProjectFromInitiative(ctx, projectID, i.initiativeID); err != nil {
				return err
			}
			i.lfs.persistInitiativeProjectLink(ctx, i.initiativeID, projectID, false)
			return nil
		},
		field: "projects",
		hint:  ". Use a project slug from teams/<KEY>/projects/.",
	}); err != nil {
		msg, errno := classifyMutationErr("update initiative projects", err)
		i.lfs.SetWriteError(i.initiativeID, msg)
		return errno
	}

	// Persist editable scalar fields (name in frontmatter, description in body).
	// The body maps to the initiative's description, matching generateContent().
	var initiativeInput api.InitiativeUpdateInput
	fieldChanged := false
	if newDesc := strings.TrimSpace(doc.Body); newDesc != strings.TrimSpace(i.initiative.Description) {
		initiativeInput.Description = &newDesc
		fieldChanged = true
	}
	if name, ok := doc.Frontmatter["name"].(string); ok && name != "" && name != i.initiative.Name {
		nameCopy := name
		initiativeInput.Name = &nameCopy
		fieldChanged = true
	}
	if fieldChanged {
		if err := i.lfs.mutator().UpdateInitiative(ctx, i.initiativeID, initiativeInput); err != nil {
			log.Printf("Failed to update initiative %s: %v", i.initiative.Name, err)
			i.lfs.SetWriteError(i.initiativeID, "Field: name/description\nError: failed to update initiative: "+err.Error())
			return syscall.EIO
		}
		if i.lfs.debug {
			log.Printf("Updated initiative %s scalar fields", i.initiative.Name)
		}
	}

	// Edit-commit tail: re-fetch the initiative, verify read-your-writes against
	// the pre-write values still on i.initiative, upsert, and surface divergence
	// via .error. The project-link side-work (above) stays in the handler.
	fresh, errno := commitWriteBack(ctx, i.lfs, writeBackSpec[api.Initiative]{
		errKey: i.initiativeID,
		fetch: func(ctx context.Context) (*api.Initiative, error) {
			return i.lfs.verify().GetInitiative(ctx, i.initiativeID)
		},
		persist: func(ctx context.Context, fresh *api.Initiative) error {
			return i.lfs.UpsertInitiative(ctx, *fresh)
		},
		compare: func(fresh *api.Initiative) []writeBackResult {
			var results []writeBackResult
			if initiativeInput.Name != nil {
				results = append(results, writeBackDivergence("name", *initiativeInput.Name, fresh.Name, i.initiative.Name))
			}
			if initiativeInput.Description != nil {
				results = append(results, writeBackDivergence("description (body)", *initiativeInput.Description, fresh.Description, i.initiative.Description))
			}
			return results
		},
	})
	if fresh != nil {
		i.initiative = *fresh
	}

	if i.lfs.debug {
		log.Printf("Flush: initiative %s updated successfully", i.initiative.Name)
	}

	// Invalidate kernel inode cache (initiative.md, its meta, and projects/ listing)
	i.lfs.InvalidateUpdated(initiativeInfoIno(i.initiativeID))
	i.lfs.InvalidateUpdated(metaIno(i.initiativeID)) // initiative.meta reflects the edit
	i.lfs.InvalidateUpdated(initiativeProjectsIno(i.initiativeID))

	i.dirty = false
	i.contentReady = false // Force re-generate on next read
	return errno
}

// InitiativeProjectsNode represents the projects/ directory within an initiative
type InitiativeProjectsNode struct {
	attrNode
	initiative api.Initiative
}

var _ fs.NodeReaddirer = (*InitiativeProjectsNode)(nil)
var _ fs.NodeLookuper = (*InitiativeProjectsNode)(nil)
var _ fs.NodeGetattrer = (*InitiativeProjectsNode)(nil)

func (p *InitiativeProjectsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := make([]fuse.DirEntry, len(p.initiative.Projects.Nodes))
	for i, proj := range p.initiative.Projects.Nodes {
		entries[i] = fuse.DirEntry{
			Name: initiativeProjectDirName(proj),
			Mode: syscall.S_IFLNK,
		}
	}
	return fs.NewListDirStream(entries), 0
}

func (p *InitiativeProjectsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	for _, proj := range p.initiative.Projects.Nodes {
		if initiativeProjectDirName(proj) == name {
			target, createdAt, updatedAt, errno := p.resolveProjectTarget(ctx, proj.ID)
			if errno != 0 {
				return nil, errno
			}
			return p.newSymlinkInode(ctx, out, target, createdAt, updatedAt), 0
		}
	}
	return nil, syscall.ENOENT
}

// resolveProjectTarget resolves an initiative project's symlink target and
// timestamps. The initiative payload carries only ID/Name/Slug; the full
// project row supplies the team-side dir name and real timestamps, and
// GetProjectPrimaryTeamKey supplies the canonical team. Until sync has both
// the project and its team association, the name is a reference to something
// that doesn't exist yet -> ENOENT.
func (p *InitiativeProjectsNode) resolveProjectTarget(ctx context.Context, projectID string) (string, time.Time, time.Time, syscall.Errno) {
	full, err := p.lfs.repo.GetProjectByID(ctx, projectID)
	if err != nil {
		return "", time.Time{}, time.Time{}, syscall.EIO
	}
	if full == nil {
		return "", time.Time{}, time.Time{}, syscall.ENOENT
	}
	teamKey, err := p.lfs.repo.GetProjectPrimaryTeamKey(ctx, projectID)
	if err != nil {
		return "", time.Time{}, time.Time{}, syscall.EIO
	}
	if teamKey == "" {
		return "", time.Time{}, time.Time{}, syscall.ENOENT
	}
	// The symlink lives at initiatives/{slug}/projects/{name}, three levels
	// below the mount root.
	target := fmt.Sprintf("../../../teams/%s/projects/%s", teamKey, projectDirName(*full))
	return target, full.CreatedAt, full.UpdatedAt, 0
}

// initiativeProjectDirName returns a safe directory name for an initiative project
func initiativeProjectDirName(proj api.InitiativeProject) string {
	// Derive from name (not slugId, which is an opaque hash in Linear)
	name := strings.ToLower(proj.Name)
	name = strings.ReplaceAll(name, " ", "-")
	name = dirNameUnsafe.ReplaceAllString(name, "")
	if name != "" {
		return name
	}
	// Fallback to slug/ID only if name sanitizes to empty
	if proj.Slug != "" {
		return proj.Slug
	}
	return proj.ID
}

// InitiativeUpdatesNode represents /initiatives/{slug}/updates/
type InitiativeUpdatesNode struct {
	attrNode
	initiativeID string
}

var _ fs.NodeReaddirer = (*InitiativeUpdatesNode)(nil)
var _ fs.NodeLookuper = (*InitiativeUpdatesNode)(nil)
var _ fs.NodeCreater = (*InitiativeUpdatesNode)(nil)
var _ fs.NodeGetattrer = (*InitiativeUpdatesNode)(nil)

func (n *InitiativeUpdatesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	updates, err := n.lfs.GetInitiativeUpdates(ctx, n.initiativeID)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := append(n.trio().entries(), n.listing(updates).entries()...)
	return fs.NewListDirStream(entries), 0
}

// trio declares the initiative-updates collection's writable surfaces.
func (n *InitiativeUpdatesNode) trio() collectionTrio {
	return collectionTrio{kind: "updates", parentID: n.initiativeID, onFlush: n.createUpdate}
}

// listing declares how update files are named — <NNNN>-<date>-<health>.md by
// creation order — so Readdir and Lookup derive identical names.
func (n *InitiativeUpdatesNode) listing(updates []api.InitiativeUpdate) indexedListing[api.InitiativeUpdate] {
	return indexedListing[api.InitiativeUpdate]{
		items:   updates,
		lessKey: func(u api.InitiativeUpdate) time.Time { return u.CreatedAt },
		nameOf:  func(i int, u api.InitiativeUpdate) string { return updateEntryName(i, u.CreatedAt, u.Health) },
	}
}

func (n *InitiativeUpdatesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
	}

	updates, err := n.lfs.GetInitiativeUpdates(ctx, n.initiativeID)
	if err != nil {
		return nil, syscall.EIO
	}

	update, ok := n.listing(updates).find(name)
	if ok {
		content := initiativeUpdateToMarkdown(&update)
		node := &InitiativeUpdateNode{
			BaseNode: BaseNode{lfs: n.lfs},
			update:   update,
			content:  content,
		}
		out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = uint64(len(content))
		out.SetAttrTimeout(30 * time.Second)
		out.SetEntryTimeout(30 * time.Second)
		out.Attr.SetTimes(&update.UpdatedAt, &update.UpdatedAt, &update.CreatedAt)
		return n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

	return nil, syscall.ENOENT
}

func (n *InitiativeUpdatesNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create initiative update file: %s", name)
	}

	// Only allow creating .md files
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}

	node := newCreateFile(n.lfs, n.createUpdate)
	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})
	return inode, &createFileHandle{}, fuse.FOPEN_DIRECT_IO, 0
}

// InitiativeUpdateNode represents a single initiative update file (read-only)
type InitiativeUpdateNode struct {
	BaseNode
	update  api.InitiativeUpdate
	content []byte
}

var _ fs.NodeGetattrer = (*InitiativeUpdateNode)(nil)
var _ fs.NodeOpener = (*InitiativeUpdateNode)(nil)
var _ fs.NodeReader = (*InitiativeUpdateNode)(nil)

func (n *InitiativeUpdateNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0444 | syscall.S_IFREG
	n.SetOwner(out)
	out.Size = uint64(len(n.content))
	out.SetTimes(&n.update.UpdatedAt, &n.update.UpdatedAt, &n.update.CreatedAt)
	return 0
}

func (n *InitiativeUpdateNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *InitiativeUpdateNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(n.content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(n.content)) {
		end = int64(len(n.content))
	}

	return fuse.ReadResultData(n.content[off:end]), 0
}

// createUpdate is the initiative-updates create surface's onFlush: parse the
// content and run the create tail. A parse error goes through the tail so it
// lands in .error; only whitespace-with-no-frontmatter is flush noise and
// no-ops.
func (n *InitiativeUpdatesNode) createUpdate(ctx context.Context, content []byte) syscall.Errno {
	body, health, perr := parseUpdateContent(content)
	if perr == nil && body == "" {
		return 0
	}

	_, errno := commitCreate(ctx, n.lfs, createSpec[api.InitiativeUpdate]{
		op:  "create initiative update",
		key: collectionErrorKey("updates", n.initiativeID),
		mutate: func(ctx context.Context) (*api.InitiativeUpdate, error) {
			if perr != nil {
				return nil, perr
			}
			return n.lfs.mutator().CreateInitiativeUpdate(ctx, n.initiativeID, body, health)
		},
		// Updates are addressed by an index-derived filename (not knowable
		// without re-listing), so .last reports the update id + health and
		// entryName stays unknowable.
		result: func(u *api.InitiativeUpdate) WriteResult {
			return WriteResult{
				Identifier: u.ID,
				Title:      firstLine(u.Body),
				Status:     u.Health,
			}
		},
		persist: func(ctx context.Context, u *api.InitiativeUpdate) error {
			return n.lfs.UpsertInitiativeUpdate(ctx, n.initiativeID, *u)
		},
		dir: initiativeUpdatesDirIno(n.initiativeID),
	})
	return errno
}

// initiativeUpdateToMarkdown converts an initiative update to markdown with YAML frontmatter
func initiativeUpdateToMarkdown(update *api.InitiativeUpdate) []byte {
	var buf bytes.Buffer

	buf.WriteString("---\n")
	buf.WriteString(fmt.Sprintf("id: %s\n", update.ID))
	buf.WriteString(fmt.Sprintf("health: %s\n", update.Health))
	buf.WriteString(fmt.Sprintf("created: %q\n", update.CreatedAt.Format(time.RFC3339)))
	buf.WriteString(fmt.Sprintf("updated: %q\n", update.UpdatedAt.Format(time.RFC3339)))
	if update.User != nil {
		buf.WriteString(fmt.Sprintf("author: %s\n", update.User.Email))
		buf.WriteString(fmt.Sprintf("authorName: %s\n", update.User.Name))
	}
	buf.WriteString("---\n\n")
	buf.WriteString(update.Body)
	buf.WriteString("\n")

	return buf.Bytes()
}
