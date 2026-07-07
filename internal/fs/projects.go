package fs

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

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
			node := &ProjectNode{attrNode: attrNode{BaseNode: BaseNode{lfs: p.lfs}}, team: p.team, project: project}
			return p.newDirInode(ctx, out, node, dirAttr(project.CreatedAt, project.UpdatedAt), projectDirIno(project.ID), 30*time.Second), 0
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

	node := &ProjectNode{attrNode: attrNode{BaseNode: BaseNode{lfs: p.lfs}}, team: p.team, project: *project}
	return p.newDirInode(ctx, out, node, dirAttr(project.CreatedAt, project.UpdatedAt), projectDirIno(project.ID), 30*time.Second), 0
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

// dirNameUnsafe matches the characters stripped from name-derived directory
// names (projects, initiatives, initiative projects).
var dirNameUnsafe = regexp.MustCompile(`[^a-z0-9-]`)

// projectDirName returns a safe directory name for a project
func projectDirName(project api.Project) string {
	// Sanitize name: lowercase, replace spaces with hyphens, remove special chars
	name := strings.ToLower(project.Name)
	name = strings.ReplaceAll(name, " ", "-")
	name = dirNameUnsafe.ReplaceAllString(name, "")
	if name != "" {
		return name
	}
	// Fallback to slug if name sanitizes to empty
	return project.Slug
}

// ProjectNode represents a single project directory
type ProjectNode struct {
	attrNode
	team    api.Team
	project api.Project
}

var _ fs.NodeReaddirer = (*ProjectNode)(nil)
var _ fs.NodeLookuper = (*ProjectNode)(nil)
var _ fs.NodeCreater = (*ProjectNode)(nil)
var _ fs.NodeRenamer = (*ProjectNode)(nil)
var _ fs.NodeUnlinker = (*ProjectNode)(nil)

func (p *ProjectNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := p.manifest().entries()
	// Dynamic tail: issue symlinks.
	issues, err := p.lfs.GetProjectIssues(ctx, p.project.ID)
	if err != nil {
		return nil, syscall.EIO
	}
	for _, issue := range issues {
		entries = append(entries, fuse.DirEntry{Name: issue.Identifier, Mode: syscall.S_IFLNK})
	}
	return fs.NewListDirStream(entries), 0
}

func (p *ProjectNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if child, ok := p.manifest().find(name); ok {
		return child.build(ctx, out)
	}

	// Dynamic tail: an issue symlink, resolved only on a static-child miss.
	issues, err := p.lfs.GetProjectIssues(ctx, p.project.ID)
	if err != nil {
		return nil, syscall.EIO
	}
	for _, issue := range issues {
		if issue.Identifier == name {
			target := fmt.Sprintf("../../issues/%s", issue.Identifier)
			return p.newSymlinkInode(ctx, out, target, issue.CreatedAt, issue.UpdatedAt), 0
		}
	}

	return nil, syscall.ENOENT
}

// manifest declares a project directory's static children: the editable
// project.md, the read-through project.meta, the .error sidecar, and the
// docs/updates/milestones subdirs. The dynamic tail (issue symlinks) is appended
// by Readdir/Lookup, not the manifest. Project children have a 0 timeout.
func (p *ProjectNode) manifest() *dirManifest {
	project := p.project // snapshot captured by the build closures
	team := p.team
	lfs := p.lfs
	m := newDirManifest(&p.BaseNode, project.ID, project.CreatedAt, project.UpdatedAt, 0)

	// project.md is editable-only; identity/status/dates live in project.meta.
	m.file("project.md", projectInfoIno(project.ID), func(ctx context.Context) (fs.InodeEmbedder, []byte, syscall.Errno) {
		node := &ProjectInfoNode{BaseNode: BaseNode{lfs: lfs}, team: team, project: project}
		content := node.generateContent()
		node.content = content
		return node, content, 0
	})

	// project.meta: read-through from the freshest project so an edit to
	// project.md is reflected here.
	m.metaFile("project.meta", func() ([]byte, time.Time, time.Time) {
		proj := project
		if projs, err := lfs.GetTeamProjects(context.Background(), team.ID); err == nil {
			for _, pr := range projs {
				if pr.ID == project.ID {
					proj = pr
					break
				}
			}
		}
		node := &ProjectInfoNode{BaseNode: BaseNode{lfs: lfs}, team: team, project: proj}
		return node.metaContent(), proj.UpdatedAt, proj.CreatedAt
	})

	m.errorFile(".error")

	m.subdir("docs", docsDirIno(project.ID), func() dirChild {
		return &DocsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: project.ID}
	})
	m.subdir("updates", updatesDirIno(project.ID), func() dirChild {
		return &UpdatesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: project.ID}
	})
	m.subdir("milestones", milestonesDirIno(project.ID), func() dirChild {
		return &MilestonesNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: project.ID}
	})

	return m
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
		BaseNode:   BaseNode{lfs: p.lfs},
		team:       p.team,
		project:    p.project,
		editBuffer: editBuffer{content: content, dirty: true},
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

// ProjectInfoNode is a virtual file containing project metadata
type ProjectInfoNode struct {
	BaseNode
	editBuffer
	team    api.Team
	project api.Project
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
	fileAttr(p.size(), p.project.CreatedAt, p.project.UpdatedAt).fill(&out.Attr, &p.BaseNode)
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

	// Extract initiatives from frontmatter (coerced via the shared marshal helper)
	newInitiatives := marshal.StringSliceFromYAML(doc.Frontmatter["initiatives"])

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
	// scalarEdit owns the diff, name coercion, and the divergence compare below.
	edit := newScalarEdit(doc, p.project.Name, p.project.Description)
	projectInput := api.ProjectUpdateInput{Name: edit.name, Description: edit.desc}
	if edit.changed() {
		if err := p.lfs.mutator().UpdateProject(ctx, p.project.ID, projectInput); err != nil {
			msg, errno := classifyMutationErr("update project", err)
			p.lfs.SetWriteError(p.project.ID, msg)
			return errno
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
			return edit.divergences(fresh.Name, fresh.Description)
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
	return errno
}

// UpdatesNode represents /teams/{KEY}/projects/{slug}/updates/
type UpdatesNode struct {
	attrNode
	projectID string
}

var _ fs.NodeReaddirer = (*UpdatesNode)(nil)
var _ fs.NodeLookuper = (*UpdatesNode)(nil)
var _ fs.NodeCreater = (*UpdatesNode)(nil)
var _ fs.NodeGetattrer = (*UpdatesNode)(nil)

func (n *UpdatesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	updates, err := n.lfs.GetProjectUpdates(ctx, n.projectID)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := append(n.trio().entries(), n.listing(updates).entries()...)
	return fs.NewListDirStream(entries), 0
}

// trio declares the project-updates collection's writable surfaces.
func (n *UpdatesNode) trio() collectionTrio {
	return collectionTrio{kind: "updates", parentID: n.projectID, onFlush: n.createUpdate}
}

// listing declares how update files are named — <NNNN>-<date>-<health>.md by
// creation order — so Readdir and Lookup derive identical names.
func (n *UpdatesNode) listing(updates []api.ProjectUpdate) indexedListing[api.ProjectUpdate] {
	return indexedListing[api.ProjectUpdate]{
		items:   updates,
		lessKey: func(u api.ProjectUpdate) time.Time { return u.CreatedAt },
		nameOf:  func(i int, u api.ProjectUpdate) string { return updateEntryName(i, u.CreatedAt, u.Health) },
	}
}

func (n *UpdatesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
	}

	updates, err := n.lfs.GetProjectUpdates(ctx, n.projectID)
	if err != nil {
		return nil, syscall.EIO
	}

	update, ok := n.listing(updates).find(name)
	if !ok {
		return nil, syscall.ENOENT
	}
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
