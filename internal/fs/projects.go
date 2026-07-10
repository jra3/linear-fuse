package fs

import (
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

// ProjectsNode represents the /teams/{KEY}/projects directory. It holds a team
// snapshot and reports the team's times; Getattr comes from the attrNode mixin.
type ProjectsNode struct {
	attrNode
	team api.Team
}

var _ fs.NodeReaddirer = (*ProjectsNode)(nil)
var _ fs.NodeLookuper = (*ProjectsNode)(nil)
var _ fs.NodeMkdirer = (*ProjectsNode)(nil)
var _ fs.NodeRmdirer = (*ProjectsNode)(nil)
var _ fs.NodeGetattrer = (*ProjectsNode)(nil)

// entity/setEntity snapshot and swap the directory's team under the node's
// volatile-state lock; setEntity is written by the nodeRefresher seam
// (refresh.go).
func (p *ProjectsNode) entity() api.Team {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.team
}

func (p *ProjectsNode) setEntity(team api.Team) {
	p.stateMu.Lock()
	p.team = team
	p.stateMu.Unlock()
}

func (p *ProjectsNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*ProjectsNode); ok {
		p.setEntity(f.team)
	}
}

func (p *ProjectsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	projects, err := p.lfs.repo.GetTeamProjects(ctx, p.entity().ID)
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
	return collectionTrio{kind: "projects", parentID: p.entity().ID}
}

func (p *ProjectsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := p.lfs.lookupCollectionTrio(ctx, p, p.trio(), name, out); ok {
		return inode, 0
	}

	team := p.entity()
	projects, err := p.lfs.repo.GetTeamProjects(ctx, team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, project := range projects {
		if projectDirName(project) == name {
			node := &ProjectNode{attrNode: attrNode{BaseNode: BaseNode{lfs: p.lfs}}, team: team, project: project}
			return p.newDirInode(ctx, out, name, node, dirAttr(project.CreatedAt, project.UpdatedAt), projectDirIno(project.ID), 30*time.Second), 0
		}
	}

	return nil, syscall.ENOENT
}

// Mkdir creates a new project
func (p *ProjectsNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	team := p.entity()
	if p.lfs.debug {
		log.Printf("Mkdir: creating project %s in team %s", name, team.Key)
	}

	project, errno := commitCreate(ctx, p.lfs, createSpec[api.Project]{
		op:  `create project "` + name + `"`,
		key: collectionErrorKey("projects", team.ID),
		mutate: func(ctx context.Context) (*api.Project, error) {
			return p.lfs.mutator().CreateProject(ctx, map[string]any{
				"name":    name,
				"teamIds": []string{team.ID},
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
			return p.lfs.UpsertProject(ctx, team.ID, *pr)
		},
		dir:       projectsDirIno(team.ID),
		entryName: func(pr *api.Project) string { return projectDirName(*pr) },
	})
	if errno != 0 {
		return nil, errno
	}

	node := &ProjectNode{attrNode: attrNode{BaseNode: BaseNode{lfs: p.lfs}}, team: team, project: *project}
	return p.newDirInode(ctx, out, projectDirName(*project), node, dirAttr(project.CreatedAt, project.UpdatedAt), projectDirIno(project.ID), 30*time.Second), 0
}

// Rmdir archives a project (soft delete)
func (p *ProjectsNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	team := p.entity()
	if p.lfs.debug {
		log.Printf("Rmdir: archiving project %s in team %s", name, team.Key)
	}

	return commitDelete(ctx, p.lfs, deleteSpec[api.Project]{
		op:  `archive project "` + name + `"`,
		key: collectionErrorKey("projects", team.ID),
		find: func(ctx context.Context) (*api.Project, error) {
			projects, err := p.lfs.repo.GetTeamProjects(ctx, team.ID)
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
		dir:  projectsDirIno(team.ID),
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

// entity/setEntity snapshot and swap the directory's project (+team) under
// the node's volatile-state lock: setEntity is written by the Rename
// write-back and the nodeRefresher seam (refresh.go).
func (p *ProjectNode) entity() (api.Team, api.Project) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.team, p.project
}

func (p *ProjectNode) setEntity(project api.Project) {
	p.stateMu.Lock()
	p.project = project
	p.stateMu.Unlock()
}

func (p *ProjectNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*ProjectNode); ok {
		p.stateMu.Lock()
		p.team, p.project = f.team, f.project
		p.stateMu.Unlock()
	}
}

func (p *ProjectNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	_, project := p.entity()
	entries := p.manifest().entries()
	// Dynamic tail: issue symlinks.
	issues, err := p.lfs.GetProjectIssues(ctx, project.ID)
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
	_, project := p.entity()
	issues, err := p.lfs.GetProjectIssues(ctx, project.ID)
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
	team, project := p.entity() // snapshot captured by the build closures
	lfs := p.lfs
	m := newDirManifest(&p.BaseNode, project.ID, project.CreatedAt, project.UpdatedAt, 0)

	// project.md is editable-only; identity/status/dates live in project.meta.
	m.file("project.md", projectInfoIno(project.ID), func(ctx context.Context) (fs.InodeEmbedder, []byte, syscall.Errno) {
		node := &ProjectInfoNode{BaseNode: BaseNode{lfs: lfs}, team: team, project: project}
		content := node.generateContent(ctx)
		node.content = content
		return node, content, 0
	})

	// project.meta: read-through from the freshest project so an edit to
	// project.md is reflected here.
	m.metaFile("project.meta", func(ctx context.Context) ([]byte, time.Time, time.Time) {
		proj := project
		if projs, err := lfs.repo.GetTeamProjects(ctx, team.ID); err == nil {
			proj = freshestByID(projs, project.ID, func(p api.Project) string { return p.ID }, project)
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
	m.subdir("links", linksDirIno(project.ID), func() dirChild {
		return &LinksNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: project.ID}
	})

	return m
}

// Create accepts an editor's atomic-save temp file (e.g. project.md.tmp.<pid>.<rand>)
// as an in-memory scratch buffer so Rename can route its bytes into project.md's
// write path. Without it, go-fuse rejects the temp-file create with a misleading
// EROFS on the rw mount (#145).
func (p *ProjectNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if p.lfs.debug {
		_, project := p.entity()
		log.Printf("Create scratch file in project %s: %s", project.Name, name)
	}
	return newScratchInode(ctx, &p.BaseNode, p.EmbeddedInode().StableAttr().Ino, name, out)
}

// Rename persists an editor's atomic save: a scratch temp file renamed onto
// project.md is written through project.md's normal Flush path. The tail (EXDEV /
// target guard / flush / adopt-on-{0,EIO} / invalidate) is the shared
// renameSave module.
func (p *ProjectNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	team, project := p.entity()
	if p.lfs.debug {
		log.Printf("Rename in project %s: %s -> %s", project.Name, name, newName)
	}

	var fileNode *ProjectInfoNode
	return renameSave(ctx, p.lfs, name, newParent, newName, renameSaveSpec{
		targetName: "project.md",
		errKey:     project.ID,
		dirIno:     p.EmbeddedInode().StableAttr().Ino,
		fileIno:    projectInfoIno(project.ID),
		scratch:    func(oldName string) ([]byte, bool) { return scratchRenameBytes(p, oldName) },
		flush: func(ctx context.Context, content []byte) syscall.Errno {
			fileNode = &ProjectInfoNode{
				BaseNode:   BaseNode{lfs: p.lfs},
				team:       team,
				project:    project,
				editBuffer: editBuffer{content: content, dirty: true},
			}
			return fileNode.Flush(ctx, nil)
		},
		adopt: func() { p.setEntity(fileNode.project) },
	})
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

// generateContent renders the editable-only project.md via
// marshal.ProjectToMarkdown; a render failure serves an empty file rather
// than failing the node. Label IDs render as catalog names; an ID the catalog
// does not know renders verbatim (round-trip invariant — see projectLabelNames).
func (p *ProjectInfoNode) generateContent(ctx context.Context) []byte {
	labelNames := p.lfs.projectLabelNames(ctx, p.project.LabelIds)
	out, err := marshal.ProjectToMarkdown(&p.project, labelNames)
	if err != nil {
		return []byte{}
	}
	return out
}

// metaContent renders the read-only project.meta via
// marshal.ProjectMetaToMarkdown.
func (p *ProjectInfoNode) metaContent() []byte {
	out, err := marshal.ProjectMetaToMarkdown(&p.project)
	if err != nil {
		return []byte{}
	}
	return out
}

func (p *ProjectInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	// One lock for size + times: a concurrent refresh (refresh.go) swaps
	// content and entity atomically, so the read must snapshot both together.
	p.mu.Lock()
	size := len(p.content)
	created, updated := p.project.CreatedAt, p.project.UpdatedAt
	p.mu.Unlock()
	fileAttr(size, created, updated).fill(&out.Attr, &p.BaseNode)
	return 0
}

// refreshFrom adopts a fresh twin's project and rendered content unless an
// edit is in flight — the dirty buffer always wins (refresh.go).
func (p *ProjectInfoNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*ProjectInfoNode); ok {
		p.refresh(f.content, func() { p.team, p.project = f.team, f.project })
	}
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

	// Parse the modified content: extraction/coercion only, into the editable
	// field set. The diffs below own change detection.
	parsed, err := marshal.MarkdownToProjectEdit(p.content)
	if err != nil {
		log.Printf("Failed to parse project changes for %s: %v", p.project.Name, err)
		p.lfs.SetWriteError(p.project.ID, "Parse error: "+err.Error())
		return syscall.EINVAL
	}

	// Labels front half: labelsEdit (sibling of scalarEdit) owns the label
	// coercion, the stale-blob clobber guard, resolve + validate, and the one
	// change decision — hoisted before any mutation so a validation failure
	// cannot leave the initiatives reconcile partially applied. The refresh
	// closure is interactive-promoted because it is a synchronous read inside a
	// user-blocking flush; labelsEdit decides when it fires.
	labels, ferr := newLabelsEdit(ctx, parsed.LabelsRaw, parsed.LabelsPresent, p.project.LabelIds,
		p.lfs.repo.GetProjectLabels,
		func(ctx context.Context) []string {
			if fresh, err := p.lfs.verify().GetProject(api.WithInteractive(ctx), p.project.ID); err == nil && fresh != nil {
				p.project.LabelIds = fresh.LabelIds
			}
			return p.project.LabelIds
		})
	if ferr != nil {
		p.lfs.SetWriteError(p.project.ID, ferr.Detail())
		return syscall.EINVAL
	}

	// Desired initiatives, already coerced by the parse (absent ⇒ empty ⇒ unlink all)
	newInitiatives := parsed.Initiatives

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

	// Persist editable scalar fields (name in frontmatter, content in the
	// body) plus the label set, in ONE UpdateProject call. The body maps to
	// Linear's uncapped `content`, not the ≤255 `description` (see #5). Each
	// edit module maps itself onto the input pointer-or-omit; labelsEdit.applyTo
	// owns the full-set labels semantics (see projectlabels.go).
	edit := newScalarEdit(parsed.Name, parsed.Body, p.project.Name, p.project.Content)
	projectInput := api.ProjectUpdateInput{Name: edit.name, Content: edit.desc}
	labels.applyTo(&projectInput)
	if edit.changed() || labels.changed() {
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
			return append(edit.divergences(fresh.Name, fresh.Content), labels.divergences(fresh.LabelIds)...)
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
	updates, err := n.lfs.repo.GetProjectUpdates(ctx, n.projectID)
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

	updates, err := n.lfs.repo.GetProjectUpdates(ctx, n.projectID)
	if err != nil {
		return nil, syscall.EIO
	}

	update, ok := n.listing(updates).find(name)
	if !ok {
		return nil, syscall.ENOENT
	}
	return n.lookupUpdateFile(ctx, out, name, update.ID, update.Health, update.CreatedAt, update.UpdatedAt,
		update.User, update.Body, projectUpdateIno(update.ID)), 0
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

// createUpdate is the project-updates create surface's onFlush: parse the
// content and run the create tail. A parse error goes through the tail so it
// lands in .error; only whitespace-with-no-frontmatter is flush noise and
// no-ops.
func (n *UpdatesNode) createUpdate(ctx context.Context, content []byte) syscall.Errno {
	body, health, perr := marshal.MarkdownToStatusUpdate(content)
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
