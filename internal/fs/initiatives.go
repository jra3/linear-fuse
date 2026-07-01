package fs

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/marshal"
)

func initiativeInfoIno(initiativeID string) uint64 { return ino("initiative-info", initiativeID) }

func initiativeProjectsIno(initiativeID string) uint64 {
	return ino("initiative-projects", initiativeID)
}

func initiativeUpdatesDirIno(initiativeID string) uint64 {
	return ino("initiative-updates", initiativeID)
}

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
	reg := regexp.MustCompile(`[^a-z0-9-]`)
	name = reg.ReplaceAllString(name, "")
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
		node.content = contentBuffer{buf: content, loaded: true, load: node.initiativeLoader()}
		out.Attr.Mode = 0644 | syscall.S_IFREG
		out.Attr.Uid = i.lfs.uid
		out.Attr.Gid = i.lfs.gid
		out.Attr.Size = uint64(len(content))
		out.Attr.SetTimes(&i.initiative.UpdatedAt, &i.initiative.UpdatedAt, &i.initiative.CreatedAt)
		return i.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  initiativeInfoIno(i.initiative.ID),
		}), 0

	case ".error":
		return i.lfs.lookupErrorFile(ctx, i, i.initiative.ID, out), 0

	case "docs":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = i.lfs.uid
		out.Attr.Gid = i.lfs.gid
		out.Attr.SetTimes(&i.initiative.UpdatedAt, &i.initiative.UpdatedAt, &i.initiative.CreatedAt)
		node := &DocsNode{BaseNode: BaseNode{lfs: i.lfs}, initiativeID: i.initiative.ID}
		return i.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR, Ino: docsDirIno(i.initiative.ID)}), 0

	case "projects":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = i.lfs.uid
		out.Attr.Gid = i.lfs.gid
		out.Attr.SetTimes(&i.initiative.UpdatedAt, &i.initiative.UpdatedAt, &i.initiative.CreatedAt)
		node := &InitiativeProjectsNode{BaseNode: BaseNode{lfs: i.lfs}, initiative: i.initiative}
		return i.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  initiativeProjectsIno(i.initiative.ID),
		}), 0

	case "updates":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = i.lfs.uid
		out.Attr.Gid = i.lfs.gid
		out.Attr.SetTimes(&i.initiative.UpdatedAt, &i.initiative.UpdatedAt, &i.initiative.CreatedAt)
		node := &InitiativeUpdatesNode{BaseNode: BaseNode{lfs: i.lfs}, initiativeID: i.initiative.ID, initiativeUpdatedAt: i.initiative.UpdatedAt}
		return i.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
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
	}
	fileNode.content = contentBuffer{buf: content, loaded: true, dirty: true, load: fileNode.initiativeLoader()}
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

	// Write buffer and cached content. mu guards content and initiative together:
	// content's loader reads initiative, which Flush also mutates.
	mu      sync.Mutex
	content contentBuffer
}

var _ fs.NodeGetattrer = (*InitiativeInfoNode)(nil)
var _ fs.NodeOpener = (*InitiativeInfoNode)(nil)
var _ fs.NodeReader = (*InitiativeInfoNode)(nil)
var _ fs.NodeWriter = (*InitiativeInfoNode)(nil)
var _ fs.NodeFlusher = (*InitiativeInfoNode)(nil)
var _ fs.NodeFsyncer = (*InitiativeInfoNode)(nil)
var _ fs.NodeSetattrer = (*InitiativeInfoNode)(nil)

func (i *InitiativeInfoNode) generateContent() []byte {
	var ownerYAML string
	if i.initiative.Owner != nil {
		ownerYAML = fmt.Sprintf(`owner:
  id: %s
  name: %q
  email: %s
`, i.initiative.Owner.ID, i.initiative.Owner.Name, i.initiative.Owner.Email)
	}

	var targetDate string
	if i.initiative.TargetDate != nil {
		targetDate = fmt.Sprintf("targetDate: %q\n", *i.initiative.TargetDate)
	}

	// Build project list (editable - use slugs like how projects use initiative names)
	var projectsYAML string
	if len(i.initiative.Projects.Nodes) > 0 {
		projectsYAML = "projects:\n"
		for _, p := range i.initiative.Projects.Nodes {
			projectsYAML += fmt.Sprintf("  - %q\n", p.Slug)
		}
	}

	content := fmt.Sprintf(`---
id: %s
name: %q
slug: %s
url: %s
status: %s
color: %q
icon: %q
%s%s%screated: %q
updated: %q
---

%s
`,
		i.initiative.ID,
		i.initiative.Name,
		i.initiative.Slug,
		i.initiative.URL,
		i.initiative.Status,
		i.initiative.Color,
		i.initiative.Icon,
		ownerYAML,
		targetDate,
		projectsYAML,
		i.initiative.CreatedAt.Format(time.RFC3339),
		i.initiative.UpdatedAt.Format(time.RFC3339),
		i.initiative.Description,
	)
	return []byte(content)
}

// initiativeLoader generates initiative.md markdown; the contentBuffer's lazy
// loader, used to re-materialize content after invalidate().
func (i *InitiativeInfoNode) initiativeLoader() func() ([]byte, error) {
	return func() ([]byte, error) { return i.generateContent(), nil }
}

func (i *InitiativeInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	sz, err := i.content.size()
	if err != nil {
		return syscall.EIO
	}
	out.Mode = 0644 | syscall.S_IFREG
	i.SetOwner(out)
	out.Size = uint64(sz)
	out.Attr.SetTimes(&i.initiative.UpdatedAt, &i.initiative.UpdatedAt, &i.initiative.CreatedAt)
	return 0
}

func (i *InitiativeInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (i *InitiativeInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	i.mu.Lock()
	defer i.mu.Unlock()

	b, err := i.content.bytes()
	if err != nil {
		return nil, syscall.EIO
	}

	if off >= int64(len(b)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(b)) {
		end = int64(len(b))
	}
	return fuse.ReadResultData(b[off:end]), 0
}

func (i *InitiativeInfoNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	i.mu.Lock()
	defer i.mu.Unlock()

	w, err := i.content.writeAt(off, data)
	if err != nil {
		return 0, syscall.EIO
	}
	return uint32(w), 0
}

func (i *InitiativeInfoNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if err := i.content.truncate(int64(sz)); err != nil {
			return syscall.EIO
		}
	}

	sz, err := i.content.size()
	if err != nil {
		return syscall.EIO
	}
	out.Mode = 0644 | syscall.S_IFREG
	out.Size = uint64(sz)
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

	if !i.content.isDirty() {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if i.lfs.debug {
		log.Printf("Flush: initiative %s (saving changes)", i.initiative.Name)
	}

	content, err := i.content.bytes()
	if err != nil {
		return syscall.EIO
	}

	// Parse the modified content
	doc, err := marshal.Parse(content)
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

	// Build sets for comparison
	currentSet := make(map[string]bool)
	for _, slug := range currentProjectSlugs {
		currentSet[slug] = true
	}
	newSet := make(map[string]bool)
	for _, slug := range newProjectSlugs {
		newSet[slug] = true
	}

	// Track resolved project IDs for SQLite sync
	addedProjectIDs := make(map[string]string)   // slug -> ID
	removedProjectIDs := make(map[string]string) // slug -> ID

	// Find projects to add (in new but not in current)
	for _, slug := range newProjectSlugs {
		if !currentSet[slug] {
			projectID, err := i.lfs.ResolveProjectSlugToID(ctx, slug)
			if err != nil {
				log.Printf("Failed to resolve project slug '%s': %v", slug, err)
				i.lfs.SetWriteError(i.initiativeID, "Field: projects\nValue: \""+slug+"\"\nError: "+err.Error()+". Use a project slug from teams/<KEY>/projects/.")
				return syscall.EINVAL
			}
			if err := i.lfs.client.AddProjectToInitiative(ctx, projectID, i.initiativeID); err != nil {
				log.Printf("Failed to add project to initiative '%s': %v", slug, err)
				i.lfs.SetWriteError(i.initiativeID, "Field: projects\nValue: \""+slug+"\"\nError: failed to link project to initiative: "+err.Error())
				return syscall.EIO
			}
			addedProjectIDs[slug] = projectID
			if i.lfs.debug {
				log.Printf("Added project %s to initiative %s", slug, i.initiative.Name)
			}
		}
	}

	// Find projects to remove (in current but not in new)
	for _, slug := range currentProjectSlugs {
		if !newSet[slug] {
			projectID, err := i.lfs.ResolveProjectSlugToID(ctx, slug)
			if err != nil {
				log.Printf("Failed to resolve project slug '%s' for removal: %v", slug, err)
				i.lfs.SetWriteError(i.initiativeID, "Field: projects\nValue: \""+slug+"\"\nError: cannot resolve project to remove: "+err.Error())
				return syscall.EINVAL
			}
			if err := i.lfs.client.RemoveProjectFromInitiative(ctx, projectID, i.initiativeID); err != nil {
				log.Printf("Failed to remove project from initiative '%s': %v", slug, err)
				i.lfs.SetWriteError(i.initiativeID, "Field: projects\nValue: \""+slug+"\"\nError: failed to unlink project from initiative: "+err.Error())
				return syscall.EIO
			}
			removedProjectIDs[slug] = projectID
			if i.lfs.debug {
				log.Printf("Removed project %s from initiative %s", slug, i.initiative.Name)
			}
		}
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
		if err := i.lfs.client.UpdateInitiative(ctx, i.initiativeID, initiativeInput); err != nil {
			log.Printf("Failed to update initiative %s: %v", i.initiative.Name, err)
			i.lfs.SetWriteError(i.initiativeID, "Field: name/description\nError: failed to update initiative: "+err.Error())
			return syscall.EIO
		}
		if i.lfs.debug {
			log.Printf("Updated initiative %s scalar fields", i.initiative.Name)
		}
	}

	// Fetch fresh initiative from API and upsert to SQLite for immediate visibility
	freshInitiative, err := i.lfs.client.GetInitiative(ctx, i.initiativeID)
	var divergence string
	var fatal bool
	if err != nil {
		log.Printf("Warning: failed to fetch fresh initiative after update: %v", err)
	} else {
		// Read-your-writes verification on the free-text fields we sent, using
		// the pre-write values (i.initiative) to classify the divergence.
		var results []writeBackResult
		if initiativeInput.Name != nil {
			results = append(results, writeBackDivergence("name", *initiativeInput.Name, freshInitiative.Name, i.initiative.Name))
		}
		if initiativeInput.Description != nil {
			results = append(results, writeBackDivergence("description (body)", *initiativeInput.Description, freshInitiative.Description, i.initiative.Description))
		}
		divergence, fatal = writeBackError(results...)
		i.initiative = *freshInitiative
		if err := i.lfs.UpsertInitiative(ctx, *freshInitiative); err != nil {
			log.Printf("Warning: failed to upsert initiative to SQLite: %v", err)
		}
	}

	// Sync initiative-project associations to SQLite
	if i.lfs.store != nil {
		for _, projID := range addedProjectIDs {
			if err := i.lfs.store.Queries().UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
				InitiativeID: i.initiativeID,
				ProjectID:    projID,
				SyncedAt:     db.Now(),
			}); err != nil {
				log.Printf("Warning: failed to upsert initiative-project to SQLite: %v", err)
			}
		}
		for _, projID := range removedProjectIDs {
			if err := i.lfs.store.Queries().DeleteInitiativeProject(ctx, db.DeleteInitiativeProjectParams{
				InitiativeID: i.initiativeID,
				ProjectID:    projID,
			}); err != nil {
				log.Printf("Warning: failed to delete initiative-project from SQLite: %v", err)
			}
		}
	}

	if i.lfs.debug {
		log.Printf("Flush: initiative %s updated successfully", i.initiative.Name)
	}

	// Invalidate caches
	i.lfs.InvalidateInitiatives()

	// Invalidate kernel inode cache (initiative.md and the projects/ listing)
	i.lfs.InvalidateUpdated(initiativeInfoIno(i.initiativeID))
	i.lfs.InvalidateUpdated(initiativeProjectsIno(i.initiativeID))

	// Drop the buffer so the next read re-generates from the fresh initiative.
	i.content.invalidate()

	if divergence != "" {
		log.Printf("Read-your-writes %s on initiative %s:\n%s", writeBackKind(fatal), i.initiative.Name, divergence)
		i.lfs.SetWriteError(i.initiativeID, divergence)
		if fatal {
			return syscall.EIO
		}
		return 0
	}

	i.lfs.ClearWriteError(i.initiativeID)
	return 0
}

// InitiativeProjectsNode represents the projects/ directory within an initiative
type InitiativeProjectsNode struct {
	BaseNode
	initiative api.Initiative
}

var _ fs.NodeReaddirer = (*InitiativeProjectsNode)(nil)
var _ fs.NodeLookuper = (*InitiativeProjectsNode)(nil)
var _ fs.NodeGetattrer = (*InitiativeProjectsNode)(nil)

func (p *InitiativeProjectsNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	p.SetOwner(out)
	out.SetTimes(&p.initiative.UpdatedAt, &p.initiative.UpdatedAt, &p.initiative.CreatedAt)
	return 0
}

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
			// We need to find which team this project belongs to
			// For now, create a symlink that requires resolving the team
			node := &InitiativeProjectSymlink{
				BaseNode: BaseNode{lfs: p.lfs},
				project:  proj,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			out.Attr.Uid = p.lfs.uid
			out.Attr.Gid = p.lfs.gid
			return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}
	return nil, syscall.ENOENT
}

// initiativeProjectDirName returns a safe directory name for an initiative project
func initiativeProjectDirName(proj api.InitiativeProject) string {
	// Derive from name (not slugId, which is an opaque hash in Linear)
	name := strings.ToLower(proj.Name)
	name = strings.ReplaceAll(name, " ", "-")
	reg := regexp.MustCompile(`[^a-z0-9-]`)
	name = reg.ReplaceAllString(name, "")
	if name != "" {
		return name
	}
	// Fallback to slug/ID only if name sanitizes to empty
	if proj.Slug != "" {
		return proj.Slug
	}
	return proj.ID
}

// InitiativeProjectSymlink is a symlink pointing to a project directory
type InitiativeProjectSymlink struct {
	BaseNode
	project api.InitiativeProject
}

var _ fs.NodeReadlinker = (*InitiativeProjectSymlink)(nil)
var _ fs.NodeGetattrer = (*InitiativeProjectSymlink)(nil)

func (s *InitiativeProjectSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	// Find the project's team by checking all teams
	teams, err := s.lfs.GetTeams(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, team := range teams {
		projects, err := s.lfs.GetTeamProjects(ctx, team.ID)
		if err != nil {
			continue
		}
		for _, proj := range projects {
			if proj.ID == s.project.ID {
				// Found the project - create relative symlink
				target := fmt.Sprintf("../../teams/%s/projects/%s", team.Key, projectDirName(proj))
				return []byte(target), 0
			}
		}
	}

	// Fallback: project not found in any team
	return []byte("broken-link"), syscall.ENOENT
}

func (s *InitiativeProjectSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	// Use a reasonable estimate for symlink size
	out.Mode = 0777 | syscall.S_IFLNK
	s.SetOwner(out)
	out.Size = 64
	out.SetTimes(&now, &now, &now)
	return 0
}

// InitiativeUpdatesNode represents /initiatives/{slug}/updates/
type InitiativeUpdatesNode struct {
	BaseNode
	initiativeID        string
	initiativeUpdatedAt time.Time
}

var _ fs.NodeReaddirer = (*InitiativeUpdatesNode)(nil)
var _ fs.NodeLookuper = (*InitiativeUpdatesNode)(nil)
var _ fs.NodeCreater = (*InitiativeUpdatesNode)(nil)
var _ fs.NodeGetattrer = (*InitiativeUpdatesNode)(nil)

func (n *InitiativeUpdatesNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&n.initiativeUpdatedAt, &n.initiativeUpdatedAt, &n.initiativeUpdatedAt)
	return 0
}

func (n *InitiativeUpdatesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	updates, err := n.lfs.GetInitiativeUpdates(ctx, n.initiativeID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Always include _create for creating updates
	entries := []fuse.DirEntry{
		{Name: "_create", Mode: syscall.S_IFREG},
	}

	// Sort updates by creation time
	sort.Slice(updates, func(i, j int) bool {
		return updates[i].CreatedAt.Before(updates[j].CreatedAt)
	})

	for i, update := range updates {
		// Format: 001-2025-01-15-ontrack.md
		timestamp := update.CreatedAt.Format("2006-01-02")
		healthSuffix := strings.ToLower(update.Health)
		entries = append(entries, fuse.DirEntry{
			Name: fmt.Sprintf("%04d-%s-%s.md", i+1, timestamp, healthSuffix),
			Mode: syscall.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), 0
}

func (n *InitiativeUpdatesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle _create for creating updates
	if name == "_create" {
		now := time.Now()
		node := &NewInitiativeUpdateNode{
			BaseNode:     BaseNode{lfs: n.lfs},
			initiativeID: n.initiativeID,
		}
		out.Attr.Mode = 0200 | syscall.S_IFREG
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = 0
		out.Attr.SetTimes(&now, &now, &now)
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

	updates, err := n.lfs.GetInitiativeUpdates(ctx, n.initiativeID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Sort updates by creation time
	sort.Slice(updates, func(i, j int) bool {
		return updates[i].CreatedAt.Before(updates[j].CreatedAt)
	})

	// Match by filename pattern
	for i, update := range updates {
		timestamp := update.CreatedAt.Format("2006-01-02")
		healthSuffix := strings.ToLower(update.Health)
		expectedName := fmt.Sprintf("%04d-%s-%s.md", i+1, timestamp, healthSuffix)
		if expectedName == name {
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

	node := &NewInitiativeUpdateNode{
		BaseNode:     BaseNode{lfs: n.lfs},
		initiativeID: n.initiativeID,
	}

	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})
	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
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

// NewInitiativeUpdateNode handles creating new initiative updates
type NewInitiativeUpdateNode struct {
	BaseNode
	initiativeID string

	mu      sync.Mutex
	content contentBuffer
	created bool
}

var _ fs.NodeGetattrer = (*NewInitiativeUpdateNode)(nil)
var _ fs.NodeOpener = (*NewInitiativeUpdateNode)(nil)
var _ fs.NodeReader = (*NewInitiativeUpdateNode)(nil)
var _ fs.NodeWriter = (*NewInitiativeUpdateNode)(nil)
var _ fs.NodeFlusher = (*NewInitiativeUpdateNode)(nil)
var _ fs.NodeFsyncer = (*NewInitiativeUpdateNode)(nil)
var _ fs.NodeSetattrer = (*NewInitiativeUpdateNode)(nil)

func (n *NewInitiativeUpdateNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	sz, err := n.content.size()
	if err != nil {
		return syscall.EIO
	}
	out.Mode = 0200
	n.SetOwner(out)
	out.Size = uint64(sz)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *NewInitiativeUpdateNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewInitiativeUpdateNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// _create is write-only - return permission denied
	return nil, syscall.EACCES
}

func (n *NewInitiativeUpdateNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	w, err := n.content.writeAt(off, data)
	if err != nil {
		return 0, syscall.EIO
	}
	return uint32(w), 0
}

func (n *NewInitiativeUpdateNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if err := n.content.truncate(int64(sz)); err != nil {
			return syscall.EIO
		}
	}

	out.Mode = 0200
	sz, err := n.content.size()
	if err != nil {
		return syscall.EIO
	}
	out.Size = uint64(sz)
	return 0
}

func (n *NewInitiativeUpdateNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created {
		return 0
	}

	b, err := n.content.bytes()
	if err != nil {
		return syscall.EIO
	}

	// Parse the content - could be plain text or markdown with frontmatter
	body, health := parseInitiativeUpdateContent(b)
	if body == "" {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if n.lfs.debug {
		log.Printf("Creating initiative update: health=%s body=%s", health, body[:min(50, len(body))])
	}

	update, err := n.lfs.CreateInitiativeUpdate(ctx, n.initiativeID, body, health)
	if err != nil {
		log.Printf("Failed to create initiative update: %v", err)
		return syscall.EIO
	}

	// Upsert to SQLite so it's immediately visible
	if err := n.lfs.UpsertInitiativeUpdate(ctx, n.initiativeID, *update); err != nil {
		log.Printf("Warning: failed to upsert initiative update to SQLite: %v", err)
	}

	n.created = true

	// Invalidate kernel cache for the updates directory (previously skipped the
	// dir inode, so a newly-created update was missing from the listing).
	n.lfs.InvalidateCreated(initiativeUpdatesDirIno(n.initiativeID), "")

	if n.lfs.debug {
		log.Printf("Initiative update created successfully")
	}

	return 0
}

func (n *NewInitiativeUpdateNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

// parseInitiativeUpdateContent extracts body and health from update content
func parseInitiativeUpdateContent(content []byte) (body string, health string) {
	s := string(content)
	health = "onTrack" // Default health

	// Check for frontmatter
	if !strings.HasPrefix(s, "---\n") {
		return strings.TrimSpace(s), health
	}

	// Find end of frontmatter
	end := strings.Index(s[4:], "\n---")
	if end == -1 {
		return strings.TrimSpace(s), health
	}

	// Parse frontmatter for health field
	frontmatter := s[4 : 4+end]
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "health:") {
			h := strings.TrimSpace(strings.TrimPrefix(line, "health:"))
			h = strings.Trim(h, `"'`)
			// Normalize health value
			switch strings.ToLower(h) {
			case "ontrack", "on track", "on-track":
				health = "onTrack"
			case "atrisk", "at risk", "at-risk":
				health = "atRisk"
			case "offtrack", "off track", "off-track":
				health = "offTrack"
			}
		}
	}

	// Return body after frontmatter
	body = strings.TrimSpace(s[4+end+4:])
	return body, health
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
