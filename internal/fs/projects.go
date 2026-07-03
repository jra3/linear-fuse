package fs

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
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
	"github.com/jra3/linear-fuse/internal/marshal"
)

// projectsDirIno generates a stable inode number for a projects directory
func projectsDirIno(teamID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("projects:" + teamID))
	return h.Sum64()
}

// projectInfoIno generates a stable inode number for a project info file
func projectInfoIno(projectID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("project-info:" + projectID))
	return h.Sum64()
}

// updatesDirIno generates a stable inode number for a project updates directory
func updatesDirIno(projectID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("updates:" + projectID))
	return h.Sum64()
}

// ProjectsNode represents the /teams/{KEY}/projects directory
type ProjectsNode struct {
	BaseNode
	team api.Team
}

var _ fs.NodeReaddirer = (*ProjectsNode)(nil)
var _ fs.NodeLookuper = (*ProjectsNode)(nil)
var _ fs.NodeMkdirer = (*ProjectsNode)(nil)
var _ fs.NodeRmdirer = (*ProjectsNode)(nil)
var _ fs.NodeGetattrer = (*ProjectsNode)(nil)

func (p *ProjectsNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	p.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (p *ProjectsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	projects, err := p.lfs.GetTeamProjects(ctx, p.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Projects are created by mkdir, so the collection has no _create; the
	// trio degrades to .error/.last (#149).
	entries := p.trio().entries()
	for _, project := range projects {
		entries = append(entries, fuse.DirEntry{
			Name: projectDirName(project),
			Mode: syscall.S_IFDIR,
		})
	}

	return fs.NewListDirStream(entries), 0
}

// trio declares the projects collection's feedback surfaces. Projects are
// created by mkdir rather than a _create trigger, so onFlush stays nil.
func (p *ProjectsNode) trio() collectionTrio {
	return collectionTrio{kind: "projects", parentID: p.team.ID}
}

func (p *ProjectsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := p.lfs.lookupCollectionTrio(ctx, p, p.trio(), name, out); ok {
		return inode, 0
	}

	projects, err := p.lfs.GetTeamProjects(ctx, p.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, project := range projects {
		if projectDirName(project) == name {
			out.Attr.Mode = 0755 | syscall.S_IFDIR
			out.Attr.Uid = p.lfs.uid
			out.Attr.Gid = p.lfs.gid
			out.Attr.SetTimes(&project.UpdatedAt, &project.UpdatedAt, &project.CreatedAt)
			node := &ProjectNode{BaseNode: BaseNode{lfs: p.lfs}, team: p.team, project: project}
			return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	return nil, syscall.ENOENT
}

// Mkdir creates a new project
func (p *ProjectsNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if p.lfs.debug {
		log.Printf("Mkdir: creating project %s in team %s", name, p.team.Key)
	}

	project, errno := commitCreate(ctx, p.lfs, createSpec[api.Project]{
		op:  `create project "` + name + `"`,
		key: collectionErrorKey("projects", p.team.ID),
		mutate: func(ctx context.Context) (*api.Project, error) {
			return p.lfs.mutator().CreateProject(ctx, map[string]any{
				"name":    name,
				"teamIds": []string{p.team.ID},
			})
		},
		result: func(pr *api.Project) WriteResult {
			return WriteResult{
				Identifier: pr.Slug,
				URL:        pr.URL,
				Path:       projectDirName(*pr),
				Title:      pr.Name,
			}
		},
		persist: func(ctx context.Context, pr *api.Project) error {
			return p.lfs.UpsertProject(ctx, p.team.ID, *pr)
		},
		dir:       projectsDirIno(p.team.ID),
		entryName: func(pr *api.Project) string { return projectDirName(*pr) },
	})
	if errno != 0 {
		return nil, errno
	}

	node := &ProjectNode{
		BaseNode: BaseNode{lfs: p.lfs},
		team:     p.team,
		project:  *project,
	}

	out.Attr.Mode = 0755 | syscall.S_IFDIR
	out.SetAttrTimeout(30 * time.Second)
	out.SetEntryTimeout(30 * time.Second)

	return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// Rmdir archives a project (soft delete)
func (p *ProjectsNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if p.lfs.debug {
		log.Printf("Rmdir: archiving project %s in team %s", name, p.team.Key)
	}

	return commitDelete(ctx, p.lfs, deleteSpec[api.Project]{
		op:  `archive project "` + name + `"`,
		key: collectionErrorKey("projects", p.team.ID),
		find: func(ctx context.Context) (*api.Project, error) {
			projects, err := p.lfs.GetTeamProjects(ctx, p.team.ID)
			if err != nil {
				return nil, err
			}
			for _, project := range projects {
				if projectDirName(project) == name {
					return &project, nil
				}
			}
			return nil, nil
		},
		mutate: func(ctx context.Context, pr *api.Project) error {
			return p.lfs.mutator().ArchiveProject(ctx, pr.ID)
		},
		// The store forget was missing here: the archived project's row stayed
		// in SQLite (the listing source of truth), so it resurrected on the
		// next readdir until the sync worker reconciled.
		forget: func(ctx context.Context, pr *api.Project) error {
			return p.lfs.store.Queries().DeleteProject(ctx, pr.ID)
		},
		dir:  projectsDirIno(p.team.ID),
		name: name,
	})
}

// projectDirName returns a safe directory name for a project
func projectDirName(project api.Project) string {
	// Sanitize name: lowercase, replace spaces with hyphens, remove special chars
	name := strings.ToLower(project.Name)
	name = strings.ReplaceAll(name, " ", "-")
	// Remove any characters that aren't alphanumeric or hyphen
	reg := regexp.MustCompile(`[^a-z0-9-]`)
	name = reg.ReplaceAllString(name, "")
	if name != "" {
		return name
	}
	// Fallback to slug if name sanitizes to empty
	return project.Slug
}

// ProjectNode represents a single project directory
type ProjectNode struct {
	BaseNode
	team    api.Team
	project api.Project
}

var _ fs.NodeReaddirer = (*ProjectNode)(nil)
var _ fs.NodeLookuper = (*ProjectNode)(nil)
var _ fs.NodeGetattrer = (*ProjectNode)(nil)
var _ fs.NodeCreater = (*ProjectNode)(nil)
var _ fs.NodeRenamer = (*ProjectNode)(nil)
var _ fs.NodeUnlinker = (*ProjectNode)(nil)

func (p *ProjectNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	p.SetOwner(out)
	out.SetTimes(&p.project.UpdatedAt, &p.project.UpdatedAt, &p.project.CreatedAt)
	return 0
}

func (p *ProjectNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := p.lfs.GetProjectIssues(ctx, p.project.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// +6 for project.md, project.meta, .error, docs/, updates/, and milestones/
	entries := make([]fuse.DirEntry, len(issues)+6)
	entries[0] = fuse.DirEntry{
		Name: "project.md",
		Mode: syscall.S_IFREG,
	}
	entries[1] = fuse.DirEntry{
		Name: "project.meta",
		Mode: syscall.S_IFREG,
	}
	entries[2] = fuse.DirEntry{
		Name: ".error",
		Mode: syscall.S_IFREG,
	}
	entries[3] = fuse.DirEntry{
		Name: "docs",
		Mode: syscall.S_IFDIR,
	}
	entries[4] = fuse.DirEntry{
		Name: "updates",
		Mode: syscall.S_IFDIR,
	}
	entries[5] = fuse.DirEntry{
		Name: "milestones",
		Mode: syscall.S_IFDIR,
	}
	for i, issue := range issues {
		entries[i+6] = fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFLNK, // Symlink to issue directory
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (p *ProjectNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle project.md metadata file
	if name == "project.md" {
		node := &ProjectInfoNode{BaseNode: BaseNode{lfs: p.lfs}, team: p.team, project: p.project}
		content := node.generateContent()
		out.Attr.Mode = 0644 | syscall.S_IFREG
		out.Attr.Uid = p.lfs.uid
		out.Attr.Gid = p.lfs.gid
		out.Attr.Size = uint64(len(content))
		out.Attr.SetTimes(&p.project.UpdatedAt, &p.project.UpdatedAt, &p.project.CreatedAt)
		return p.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  projectInfoIno(p.project.ID),
		}), 0
	}

	// Handle project.meta (read-only server-managed fields), rendered read-through
	// from the freshest project so an edit to project.md is reflected here.
	if name == "project.meta" {
		lfs := p.lfs
		team := p.team
		snapshot := p.project
		render := func() ([]byte, time.Time, time.Time) {
			proj := snapshot
			if projs, err := lfs.GetTeamProjects(context.Background(), team.ID); err == nil {
				for _, pr := range projs {
					if pr.ID == snapshot.ID {
						proj = pr
						break
					}
				}
			}
			node := &ProjectInfoNode{BaseNode: BaseNode{lfs: lfs}, team: team, project: proj}
			return node.metaContent(), proj.UpdatedAt, proj.CreatedAt
		}
		return p.lfs.lookupMetaFile(ctx, p, p.project.ID, render, out), 0
	}

	// Handle .error feedback file (last failed write to project.md)
	if name == ".error" {
		return p.lfs.lookupErrorFile(ctx, p, p.project.ID, out), 0
	}

	// Handle docs/ directory
	if name == "docs" {
		node := &DocsNode{BaseNode: BaseNode{lfs: p.lfs}, projectID: p.project.ID}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = p.lfs.uid
		out.Attr.Gid = p.lfs.gid
		out.Attr.SetTimes(&p.project.UpdatedAt, &p.project.UpdatedAt, &p.project.CreatedAt)
		return p.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  docsDirIno(p.project.ID),
		}), 0
	}

	// Handle updates/ directory
	if name == "updates" {
		node := &UpdatesNode{BaseNode: BaseNode{lfs: p.lfs}, projectID: p.project.ID, projectUpdatedAt: p.project.UpdatedAt}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = p.lfs.uid
		out.Attr.Gid = p.lfs.gid
		out.Attr.SetTimes(&p.project.UpdatedAt, &p.project.UpdatedAt, &p.project.CreatedAt)
		return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	// Handle milestones/ directory
	if name == "milestones" {
		node := &MilestonesNode{BaseNode: BaseNode{lfs: p.lfs}, projectID: p.project.ID}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = p.lfs.uid
		out.Attr.Gid = p.lfs.gid
		out.Attr.SetTimes(&p.project.UpdatedAt, &p.project.UpdatedAt, &p.project.CreatedAt)
		return p.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  milestonesDirIno(p.project.ID),
		}), 0
	}

	issues, err := p.lfs.GetProjectIssues(ctx, p.project.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			node := &ProjectIssueSymlink{
				BaseNode:   BaseNode{lfs: p.lfs},
				identifier: issue.Identifier,
				createdAt:  issue.CreatedAt,
				updatedAt:  issue.UpdatedAt,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			out.Attr.Uid = p.lfs.uid
			out.Attr.Gid = p.lfs.gid
			out.Attr.SetTimes(&issue.UpdatedAt, &issue.UpdatedAt, &issue.CreatedAt)
			return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}

	return nil, syscall.ENOENT
}

// Create accepts an editor's atomic-save temp file (e.g. project.md.tmp.<pid>.<rand>)
// as an in-memory scratch buffer so Rename can route its bytes into project.md's
// write path. Without it, go-fuse rejects the temp-file create with a misleading
// EROFS on the rw mount (#145).
func (p *ProjectNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if p.lfs.debug {
		log.Printf("Create scratch file in project %s: %s", p.project.Name, name)
	}
	return newScratchInode(ctx, &p.BaseNode, p.EmbeddedInode().StableAttr().Ino, name, out)
}

// Rename persists an editor's atomic save: a scratch temp file renamed onto
// project.md is written through project.md's normal Flush path. project.md is the
// only writable file here, so other rename targets are rejected.
func (p *ProjectNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if p.lfs.debug {
		log.Printf("Rename in project %s: %s -> %s", p.project.Name, name, newName)
	}

	dirIno := p.EmbeddedInode().StableAttr().Ino
	if newParent.EmbeddedInode().StableAttr().Ino != dirIno {
		return syscall.EXDEV
	}

	content, ok := scratchRenameBytes(p, name)
	if !ok {
		return syscall.ENOTSUP
	}

	if newName != "project.md" {
		p.lfs.SetWriteError(p.project.ID, fmt.Sprintf("Operation: rename %s -> %s\nError: only project.md is writable in this directory; save your changes onto project.md (atomic save-via-rename onto project.md is supported).", name, newName))
		return syscall.ENOTSUP
	}

	fileNode := &ProjectInfoNode{
		BaseNode:     BaseNode{lfs: p.lfs},
		team:         p.team,
		project:      p.project,
		content:      content,
		contentReady: true,
		dirty:        true,
	}
	errno := fileNode.Flush(ctx, nil)

	if errno == 0 || errno == syscall.EIO {
		p.project = fileNode.project
		p.lfs.InvalidateRenamed(dirIno, name, newName, projectInfoIno(p.project.ID))
	}

	return errno
}

// Unlink lets editors clean up an abandoned atomic-save temp file. Only scratch
// files we created are removable; the canonical entries are not.
func (p *ProjectNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if _, ok := scratchRenameBytes(p, name); ok {
		return 0
	}
	return syscall.EPERM
}

// ProjectIssueSymlink is a symlink pointing to an issue directory
type ProjectIssueSymlink struct {
	BaseNode
	identifier string
	createdAt  time.Time
	updatedAt  time.Time
}

var _ fs.NodeReadlinker = (*ProjectIssueSymlink)(nil)
var _ fs.NodeGetattrer = (*ProjectIssueSymlink)(nil)

func (s *ProjectIssueSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	target := fmt.Sprintf("../../issues/%s", s.identifier)
	return []byte(target), 0
}

func (s *ProjectIssueSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	target := fmt.Sprintf("../../issues/%s", s.identifier)
	out.Mode = 0777 | syscall.S_IFLNK
	s.SetOwner(out)
	out.Size = uint64(len(target))
	out.SetTimes(&s.updatedAt, &s.updatedAt, &s.createdAt)
	return 0
}

// ProjectInfoNode is a virtual file containing project metadata
type ProjectInfoNode struct {
	BaseNode
	team         api.Team
	project      api.Project
	mu           sync.Mutex
	content      []byte
	contentReady bool
	dirty        bool
}

var _ fs.NodeGetattrer = (*ProjectInfoNode)(nil)
var _ fs.NodeOpener = (*ProjectInfoNode)(nil)
var _ fs.NodeReader = (*ProjectInfoNode)(nil)
var _ fs.NodeWriter = (*ProjectInfoNode)(nil)
var _ fs.NodeFlusher = (*ProjectInfoNode)(nil)
var _ fs.NodeFsyncer = (*ProjectInfoNode)(nil)
var _ fs.NodeSetattrer = (*ProjectInfoNode)(nil)

// generateContent renders the editable-only project.md: name, initiatives, and
// the description body. Server-managed fields live in project.meta (#150), so a
// successful write never rewrites the bytes the writer wrote.
func (p *ProjectInfoNode) generateContent() []byte {
	fm := map[string]any{"name": p.project.Name}

	if p.project.Initiatives != nil && len(p.project.Initiatives.Nodes) > 0 {
		names := make([]string, len(p.project.Initiatives.Nodes))
		for i, init := range p.project.Initiatives.Nodes {
			names[i] = init.Name
		}
		fm["initiatives"] = names
	}

	out, err := marshal.Render(&marshal.Document{Frontmatter: fm, Body: p.project.Description})
	if err != nil {
		return []byte{}
	}
	return out
}

// metaContent renders the read-only project.meta: server-managed identity,
// status, lead, dates, and timestamps as a frontmatter-only block.
func (p *ProjectInfoNode) metaContent() []byte {
	status := "unknown"
	if p.project.Status != nil {
		status = p.project.Status.Name
	}
	fm := map[string]any{
		"id":      p.project.ID,
		"slug":    p.project.Slug,
		"url":     p.project.URL,
		"status":  status,
		"created": p.project.CreatedAt.Format(time.RFC3339),
		"updated": p.project.UpdatedAt.Format(time.RFC3339),
	}
	if p.project.Lead != nil {
		fm["lead"] = map[string]any{
			"id":    p.project.Lead.ID,
			"name":  p.project.Lead.Name,
			"email": p.project.Lead.Email,
		}
	}
	if p.project.StartDate != nil {
		fm["startDate"] = *p.project.StartDate
	}
	if p.project.TargetDate != nil {
		fm["targetDate"] = *p.project.TargetDate
	}
	out, err := marshal.Render(&marshal.Document{Frontmatter: fm})
	if err != nil {
		return []byte{}
	}
	return out
}

func (p *ProjectInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	p.mu.Lock()
	defer p.mu.Unlock()

	var size int
	if p.contentReady && p.content != nil {
		size = len(p.content)
	} else {
		size = len(p.generateContent())
	}
	out.Mode = 0644 | syscall.S_IFREG
	p.SetOwner(out)
	out.Size = uint64(size)
	out.Attr.SetTimes(&p.project.UpdatedAt, &p.project.UpdatedAt, &p.project.CreatedAt)
	return 0
}

func (p *ProjectInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (p *ProjectInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.contentReady {
		p.content = p.generateContent()
		p.contentReady = true
	}

	if off >= int64(len(p.content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(p.content)) {
		end = int64(len(p.content))
	}
	return fuse.ReadResultData(p.content[off:end]), 0
}

func (p *ProjectInfoNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Initialize content if not ready
	if !p.contentReady {
		p.content = p.generateContent()
		p.contentReady = true
	}

	// Expand buffer if needed
	end := off + int64(len(data))
	if end > int64(len(p.content)) {
		newContent := make([]byte, end)
		copy(newContent, p.content)
		p.content = newContent
	}

	copy(p.content[off:], data)
	p.dirty = true
	return uint32(len(data)), 0
}

func (p *ProjectInfoNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if !p.contentReady {
			p.content = p.generateContent()
			p.contentReady = true
		}
		if int(sz) < len(p.content) {
			p.content = p.content[:sz]
			p.dirty = true
		}
	}

	var size int
	if p.contentReady && p.content != nil {
		size = len(p.content)
	} else {
		size = len(p.generateContent())
	}
	out.Mode = 0644 | syscall.S_IFREG
	out.Size = uint64(size)
	return 0
}

// Fsync is a no-op; actual persistence happens in Flush. It must be
// implemented (not return ENOTSUP) so editors that write-then-fsync
// (e.g. Claude Code's Edit tool, vim, VS Code) can save project.md.
func (p *ProjectInfoNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

func (p *ProjectInfoNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.dirty || p.content == nil {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if p.lfs.debug {
		log.Printf("Flush: project %s (saving changes)", p.project.Name)
	}

	// Parse the modified content
	doc, err := marshal.Parse(p.content)
	if err != nil {
		log.Printf("Failed to parse project changes for %s: %v", p.project.Name, err)
		p.lfs.SetWriteError(p.project.ID, "Parse error: "+err.Error())
		return syscall.EINVAL
	}

	// Extract initiatives from frontmatter
	var newInitiatives []string
	if initiativesRaw, ok := doc.Frontmatter["initiatives"]; ok {
		switch v := initiativesRaw.(type) {
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					newInitiatives = append(newInitiatives, s)
				}
			}
		case []string:
			newInitiatives = v
		}
	}

	// Get current initiatives
	var currentInitiatives []string
	if p.project.Initiatives != nil {
		for _, init := range p.project.Initiatives.Nodes {
			currentInitiatives = append(currentInitiatives, init.Name)
		}
	}

	// Reconcile the project's initiative links (front half of the edit). The
	// link/unlink closures own the API mutation and the immediate junction-row
	// write; reconcileLinks owns the diff and the resolve-error classification.
	if err := reconcileLinks(ctx, linkReconcileSpec{
		current: currentInitiatives,
		desired: newInitiatives,
		resolve: p.lfs.ResolveInitiativeID,
		link: func(ctx context.Context, initiativeID string) error {
			if err := p.lfs.mutator().AddProjectToInitiative(ctx, p.project.ID, initiativeID); err != nil {
				return err
			}
			p.lfs.persistInitiativeProjectLink(ctx, initiativeID, p.project.ID, true)
			return nil
		},
		unlink: func(ctx context.Context, initiativeID string) error {
			if err := p.lfs.mutator().RemoveProjectFromInitiative(ctx, p.project.ID, initiativeID); err != nil {
				return err
			}
			p.lfs.persistInitiativeProjectLink(ctx, initiativeID, p.project.ID, false)
			return nil
		},
		field: "initiatives",
		hint:  ". See initiatives/ for valid initiative names.",
	}); err != nil {
		msg, errno := classifyMutationErr("update project initiatives", err)
		p.lfs.SetWriteError(p.project.ID, msg)
		return errno
	}

	// Persist editable scalar fields (name in frontmatter, description in body).
	// The body maps to the project's description, matching generateContent().
	var projectInput api.ProjectUpdateInput
	fieldChanged := false
	if newDesc := strings.TrimSpace(doc.Body); newDesc != strings.TrimSpace(p.project.Description) {
		projectInput.Description = &newDesc
		fieldChanged = true
	}
	if name, ok := doc.Frontmatter["name"].(string); ok && name != "" && name != p.project.Name {
		nameCopy := name
		projectInput.Name = &nameCopy
		fieldChanged = true
	}
	if fieldChanged {
		if err := p.lfs.mutator().UpdateProject(ctx, p.project.ID, projectInput); err != nil {
			log.Printf("Failed to update project %s: %v", p.project.Name, err)
			p.lfs.SetWriteError(p.project.ID, "Field: name/description\nError: failed to update project: "+err.Error())
			return syscall.EIO
		}
		if p.lfs.debug {
			log.Printf("Updated project %s scalar fields", p.project.Name)
		}
	}

	// Edit-commit tail: re-fetch the project, verify read-your-writes against the
	// pre-write values still on p.project, upsert, and surface divergence via
	// .error. The initiative-link side-work (above and below) stays in the handler.
	fresh, errno := commitWriteBack(ctx, p.lfs, writeBackSpec[api.Project]{
		errKey: p.project.ID,
		fetch:  func(ctx context.Context) (*api.Project, error) { return p.lfs.verify().GetProject(ctx, p.project.ID) },
		persist: func(ctx context.Context, fresh *api.Project) error {
			return p.lfs.UpsertProject(ctx, p.team.ID, *fresh)
		},
		compare: func(fresh *api.Project) []writeBackResult {
			var results []writeBackResult
			if projectInput.Name != nil {
				results = append(results, writeBackDivergence("name", *projectInput.Name, fresh.Name, p.project.Name))
			}
			if projectInput.Description != nil {
				results = append(results, writeBackDivergence("description (body)", *projectInput.Description, fresh.Description, p.project.Description))
			}
			return results
		},
	})
	if fresh != nil {
		p.project = *fresh
	}

	if p.lfs.debug {
		log.Printf("Flush: project %s updated successfully", p.project.Name)
	}

	// Invalidate kernel inode cache
	p.lfs.InvalidateUpdated(projectInfoIno(p.project.ID))
	p.lfs.InvalidateUpdated(metaIno(p.project.ID)) // project.meta reflects the edit

	p.dirty = false
	p.contentReady = false // Force re-generate on next read
	return errno
}

// UpdatesNode represents /teams/{KEY}/projects/{slug}/updates/
type UpdatesNode struct {
	BaseNode
	projectID        string
	projectUpdatedAt time.Time
}

var _ fs.NodeReaddirer = (*UpdatesNode)(nil)
var _ fs.NodeLookuper = (*UpdatesNode)(nil)
var _ fs.NodeCreater = (*UpdatesNode)(nil)
var _ fs.NodeGetattrer = (*UpdatesNode)(nil)

func (n *UpdatesNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&n.projectUpdatedAt, &n.projectUpdatedAt, &n.projectUpdatedAt)
	return 0
}

func (n *UpdatesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	updates, err := n.lfs.GetProjectUpdates(ctx, n.projectID)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := n.trio().entries()

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

// trio declares the project-updates collection's writable surfaces.
func (n *UpdatesNode) trio() collectionTrio {
	return collectionTrio{kind: "updates", parentID: n.projectID, onFlush: n.createUpdate}
}

func (n *UpdatesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
	}

	updates, err := n.lfs.GetProjectUpdates(ctx, n.projectID)
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
			content := updateToMarkdown(&update)
			node := &UpdateNode{
				update:  update,
				content: content,
			}
			out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
			out.Attr.Size = uint64(len(content))
			out.SetAttrTimeout(30 * time.Second)
			out.SetEntryTimeout(30 * time.Second)
			out.Attr.SetTimes(&update.UpdatedAt, &update.UpdatedAt, &update.CreatedAt)
			return n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
		}
	}

	return nil, syscall.ENOENT
}

func (n *UpdatesNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create update file: %s", name)
	}

	// Only allow creating .md files
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}

	node := newCreateFile(n.lfs, n.createUpdate)
	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})
	return inode, &createFileHandle{}, fuse.FOPEN_DIRECT_IO, 0
}

// UpdateNode represents a single project update file (read-only)
type UpdateNode struct {
	fs.Inode
	update  api.ProjectUpdate
	content []byte
}

var _ fs.NodeGetattrer = (*UpdateNode)(nil)
var _ fs.NodeOpener = (*UpdateNode)(nil)
var _ fs.NodeReader = (*UpdateNode)(nil)

func (n *UpdateNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(n.content))
	out.SetTimes(&n.update.UpdatedAt, &n.update.UpdatedAt, &n.update.CreatedAt)
	return 0
}

func (n *UpdateNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *UpdateNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(n.content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(n.content)) {
		end = int64(len(n.content))
	}

	return fuse.ReadResultData(n.content[off:end]), 0
}

// createUpdate is the project-updates create surface's onFlush: parse the
// content and run the create tail. A parse error goes through the tail so it
// lands in .error; only whitespace-with-no-frontmatter is flush noise and
// no-ops.
func (n *UpdatesNode) createUpdate(ctx context.Context, content []byte) syscall.Errno {
	body, health, perr := parseUpdateContent(content)
	if perr == nil && body == "" {
		return 0
	}

	_, errno := commitCreate(ctx, n.lfs, createSpec[api.ProjectUpdate]{
		op:  "create project update",
		key: collectionErrorKey("updates", n.projectID),
		mutate: func(ctx context.Context) (*api.ProjectUpdate, error) {
			if perr != nil {
				return nil, perr
			}
			return n.lfs.mutator().CreateProjectUpdate(ctx, n.projectID, body, health)
		},
		// Updates are addressed by an index-derived filename (not knowable
		// without re-listing), so .last reports the update id + health and
		// entryName stays unknowable.
		result: func(u *api.ProjectUpdate) WriteResult {
			return WriteResult{
				Identifier: u.ID,
				Title:      firstLine(u.Body),
				Status:     u.Health,
			}
		},
		persist: func(ctx context.Context, u *api.ProjectUpdate) error {
			return n.lfs.UpsertProjectUpdate(ctx, n.projectID, *u)
		},
		dir: updatesDirIno(n.projectID),
	})
	return errno
}

// parseUpdateContent extracts body and health from update content (shared by
// project and initiative updates). Supports plain text or markdown with YAML
// frontmatter containing a health field; plain text defaults health to onTrack.
// An explicitly written but unrecognized health value is a *FieldError
// (-> EINVAL), as is frontmatter whose body is empty — the writer expressed
// intent, so silently creating an onTrack update (or nothing) would swallow it.
// Only content with no frontmatter may parse to an empty body; the caller
// treats that as flush noise and no-ops.
func parseUpdateContent(content []byte) (body string, health string, err error) {
	s := string(content)
	health = "onTrack" // Default health

	// Check for frontmatter
	if !strings.HasPrefix(s, "---\n") {
		return strings.TrimSpace(s), health, nil
	}

	// Find end of frontmatter
	end := strings.Index(s[4:], "\n---")
	if end == -1 {
		return strings.TrimSpace(s), health, nil
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
			default:
				return "", "", &FieldError{Field: "health", Value: h,
					Message: "invalid health: must be onTrack, atRisk, or offTrack"}
			}
		}
	}

	// Return body after frontmatter
	body = strings.TrimSpace(s[4+end+4:])
	if body == "" {
		return "", "", &FieldError{Field: "body",
			Message: "update body is required: write the update text after the frontmatter"}
	}
	return body, health, nil
}

// updateToMarkdown converts a project update to markdown with YAML frontmatter
func updateToMarkdown(update *api.ProjectUpdate) []byte {
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
