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
	"github.com/jra3/linear-fuse/internal/marshal"
)

// InitiativesNode represents the /initiatives directory. Stateless container:
// zero times (honest unknown); Getattr comes from the attrNode mixin.
type InitiativesNode struct {
	attrNode
}

var _ fs.NodeReaddirer = (*InitiativesNode)(nil)
var _ fs.NodeLookuper = (*InitiativesNode)(nil)
var _ fs.NodeGetattrer = (*InitiativesNode)(nil)

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
			node := &InitiativeNode{attrNode: attrNode{BaseNode: BaseNode{lfs: i.lfs}}, initiative: init}
			return i.newDirInode(ctx, out, name, node, dirAttr(init.CreatedAt, init.UpdatedAt), initiativeDirIno(init.ID), 30*time.Second), 0
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
	attrNode
	initiative api.Initiative
}

var _ fs.NodeReaddirer = (*InitiativeNode)(nil)
var _ fs.NodeLookuper = (*InitiativeNode)(nil)
var _ fs.NodeCreater = (*InitiativeNode)(nil)
var _ fs.NodeRenamer = (*InitiativeNode)(nil)
var _ fs.NodeUnlinker = (*InitiativeNode)(nil)

func (i *InitiativeNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return fs.NewListDirStream(i.manifest().entries()), 0
}

func (i *InitiativeNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if child, ok := i.manifest().find(name); ok {
		return child.build(ctx, out)
	}
	return nil, syscall.ENOENT
}

// manifest declares an initiative directory's static children: the editable
// initiative.md, the read-through initiative.meta, the .error sidecar, and the
// docs/projects/updates subdirs. Initiative children have no dynamic tail and a
// 0 timeout.
// entity/setEntity snapshot and swap the directory's initiative under the
// node's volatile-state lock: setEntity is written by the Rename write-back
// and the nodeRefresher seam (refresh.go).
func (i *InitiativeNode) entity() api.Initiative {
	i.stateMu.Lock()
	defer i.stateMu.Unlock()
	return i.initiative
}

func (i *InitiativeNode) setEntity(init api.Initiative) {
	i.stateMu.Lock()
	i.initiative = init
	i.stateMu.Unlock()
}

func (i *InitiativeNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*InitiativeNode); ok {
		i.setEntity(f.initiative)
	}
}

func (i *InitiativeNode) manifest() *dirManifest {
	initiative := i.entity() // snapshot captured by the build closures
	lfs := i.lfs
	m := newDirManifest(&i.BaseNode, initiative.ID, initiative.CreatedAt, initiative.UpdatedAt, 0)

	// initiative.md is editable-only; identity/status/owner live in initiative.meta.
	m.file("initiative.md", initiativeInfoIno(initiative.ID), func(ctx context.Context) (fs.InodeEmbedder, []byte, syscall.Errno) {
		node := &InitiativeInfoNode{BaseNode: BaseNode{lfs: lfs}, initiative: initiative, initiativeID: initiative.ID}
		content := node.generateContent()
		node.content = content
		return node, content, 0
	})

	// initiative.meta: read-through from the freshest initiative so an edit to
	// initiative.md is reflected here.
	m.metaFile("initiative.meta", func(ctx context.Context) ([]byte, time.Time, time.Time) {
		init := initiative
		if inits, err := lfs.GetInitiatives(ctx); err == nil {
			init = freshestByID(inits, initiative.ID, func(i api.Initiative) string { return i.ID }, initiative)
		}
		node := &InitiativeInfoNode{BaseNode: BaseNode{lfs: lfs}, initiative: init, initiativeID: init.ID}
		return node.metaContent(), init.UpdatedAt, init.CreatedAt
	})

	m.errorFile(".error")

	m.subdir("docs", docsDirIno(initiative.ID), func() dirChild {
		return &DocsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, initiativeID: initiative.ID}
	})
	m.subdir("projects", initiativeProjectsIno(initiative.ID), func() dirChild {
		return &InitiativeProjectsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, initiative: initiative}
	})
	m.subdir("updates", initiativeUpdatesDirIno(initiative.ID), func() dirChild {
		return &InitiativeUpdatesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, initiativeID: initiative.ID}
	})

	return m
}

// Create accepts an editor's atomic-save temp file (e.g. initiative.md.tmp.<pid>.<rand>)
// as an in-memory scratch buffer so Rename can route its bytes into
// initiative.md's write path. Without it, go-fuse rejects the temp-file create
// with a misleading EROFS on the rw mount (#145).
func (i *InitiativeNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if i.lfs.debug {
		log.Printf("Create scratch file in initiative %s: %s", i.entity().Name, name)
	}
	return newScratchInode(ctx, &i.BaseNode, i.EmbeddedInode().StableAttr().Ino, name, out)
}

// Rename persists an editor's atomic save: a scratch temp file renamed onto
// initiative.md is written through initiative.md's normal Flush path. The tail
// (EXDEV / target guard / flush / adopt-on-{0,EIO} / invalidate) is the shared
// renameSave module.
func (i *InitiativeNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	initiative := i.entity()
	if i.lfs.debug {
		log.Printf("Rename in initiative %s: %s -> %s", initiative.Name, name, newName)
	}

	var fileNode *InitiativeInfoNode
	return renameSave(ctx, i.lfs, name, newParent, newName, renameSaveSpec{
		targetName: "initiative.md",
		errKey:     initiative.ID,
		dirIno:     i.EmbeddedInode().StableAttr().Ino,
		fileIno:    initiativeInfoIno(initiative.ID),
		scratch:    func(oldName string) ([]byte, bool) { return scratchRenameBytes(i, oldName) },
		flush: func(ctx context.Context, content []byte) syscall.Errno {
			fileNode = &InitiativeInfoNode{
				BaseNode:     BaseNode{lfs: i.lfs},
				initiative:   initiative,
				initiativeID: initiative.ID,
				editBuffer:   editBuffer{content: content, dirty: true},
			}
			return fileNode.Flush(ctx, nil)
		},
		adopt: func() { i.setEntity(fileNode.initiative) },
	})
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
	editBuffer
	initiative   api.Initiative
	initiativeID string

	// Write buffer and cached content
}

var _ fs.NodeGetattrer = (*InitiativeInfoNode)(nil)
var _ fs.NodeOpener = (*InitiativeInfoNode)(nil)
var _ fs.NodeReader = (*InitiativeInfoNode)(nil)
var _ fs.NodeWriter = (*InitiativeInfoNode)(nil)
var _ fs.NodeFlusher = (*InitiativeInfoNode)(nil)
var _ fs.NodeFsyncer = (*InitiativeInfoNode)(nil)
var _ fs.NodeSetattrer = (*InitiativeInfoNode)(nil)

// generateContent renders the editable-only initiative.md via
// marshal.InitiativeToMarkdown; a render failure serves an empty file rather
// than failing the node.
func (i *InitiativeInfoNode) generateContent() []byte {
	out, err := marshal.InitiativeToMarkdown(&i.initiative)
	if err != nil {
		return []byte{}
	}
	return out
}

// metaContent renders the read-only initiative.meta via
// marshal.InitiativeMetaToMarkdown.
func (i *InitiativeInfoNode) metaContent() []byte {
	out, err := marshal.InitiativeMetaToMarkdown(&i.initiative)
	if err != nil {
		return []byte{}
	}
	return out
}

func (i *InitiativeInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	// One lock for size + times: a concurrent refresh (refresh.go) swaps
	// content and entity atomically, so the read must snapshot both together.
	i.mu.Lock()
	size := len(i.content)
	created, updated := i.initiative.CreatedAt, i.initiative.UpdatedAt
	i.mu.Unlock()
	fileAttr(size, created, updated).fill(&out.Attr, &i.BaseNode)
	return 0
}

// refreshFrom adopts a fresh twin's initiative and rendered content unless an
// edit is in flight — the dirty buffer always wins (refresh.go).
func (i *InitiativeInfoNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*InitiativeInfoNode); ok {
		i.refresh(f.content, func() { i.initiative, i.initiativeID = f.initiative, f.initiativeID })
	}
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

	// Extract projects from frontmatter (coerced via the shared marshal helper)
	newProjectSlugs := marshal.StringSliceFromYAML(doc.Frontmatter["projects"])

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
	// scalarEdit owns the diff, name coercion, and the divergence compare below.
	edit := newScalarEdit(doc, i.initiative.Name, i.initiative.Description)
	initiativeInput := api.InitiativeUpdateInput{Name: edit.name, Description: edit.desc}
	if edit.changed() {
		if err := i.lfs.mutator().UpdateInitiative(ctx, i.initiativeID, initiativeInput); err != nil {
			msg, errno := classifyMutationErr("update initiative", err)
			i.lfs.SetWriteError(i.initiativeID, msg)
			return errno
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
			return edit.divergences(fresh.Name, fresh.Description)
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
	if !ok {
		return nil, syscall.ENOENT
	}
	return n.lookupUpdateFile(ctx, out, name, update.ID, update.Health, update.CreatedAt, update.UpdatedAt,
		update.User, update.Body, initiativeUpdateIno(update.ID)), 0
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
