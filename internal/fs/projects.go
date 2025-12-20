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
	fs.Inode
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeReaddirer = (*ProjectsNode)(nil)
var _ fs.NodeLookuper = (*ProjectsNode)(nil)
var _ fs.NodeMkdirer = (*ProjectsNode)(nil)
var _ fs.NodeRmdirer = (*ProjectsNode)(nil)

func (p *ProjectsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	projects, err := p.lfs.GetTeamProjects(ctx, p.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(projects))
	for i, project := range projects {
		entries[i] = fuse.DirEntry{
			Name: projectDirName(project),
			Mode: syscall.S_IFDIR,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (p *ProjectsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	projects, err := p.lfs.GetTeamProjects(ctx, p.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, project := range projects {
		if projectDirName(project) == name {
			node := &ProjectNode{lfs: p.lfs, team: p.team, project: project}
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

	input := map[string]any{
		"name":    name,
		"teamIds": []string{p.team.ID},
	}

	project, err := p.lfs.CreateProject(ctx, input)
	if err != nil {
		log.Printf("Failed to create project: %v", err)
		return nil, syscall.EIO
	}

	// Invalidate cache
	p.lfs.InvalidateTeamProjects(p.team.ID)

	// Invalidate kernel cache entry for projects directory
	p.lfs.InvalidateKernelEntry(projectsDirIno(p.team.ID), name)

	node := &ProjectNode{
		lfs:     p.lfs,
		team:    p.team,
		project: *project,
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

	projects, err := p.lfs.GetTeamProjects(ctx, p.team.ID)
	if err != nil {
		return syscall.EIO
	}

	for _, project := range projects {
		if projectDirName(project) == name {
			err := p.lfs.ArchiveProject(ctx, project.ID, p.team.ID)
			if err != nil {
				log.Printf("Failed to archive project %s: %v", name, err)
				return syscall.EIO
			}
			if p.lfs.debug {
				log.Printf("Project %s archived successfully", name)
			}
			// Invalidate kernel cache entry for projects directory
			p.lfs.InvalidateKernelEntry(projectsDirIno(p.team.ID), name)
			return 0
		}
	}

	return syscall.ENOENT
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
	fs.Inode
	lfs     *LinearFS
	team    api.Team
	project api.Project
}

var _ fs.NodeReaddirer = (*ProjectNode)(nil)
var _ fs.NodeLookuper = (*ProjectNode)(nil)

func (p *ProjectNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := p.lfs.GetProjectIssues(ctx, p.project.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// +3 for project.md, docs/, and updates/
	entries := make([]fuse.DirEntry, len(issues)+3)
	entries[0] = fuse.DirEntry{
		Name: "project.md",
		Mode: syscall.S_IFREG,
	}
	entries[1] = fuse.DirEntry{
		Name: "docs",
		Mode: syscall.S_IFDIR,
	}
	entries[2] = fuse.DirEntry{
		Name: "updates",
		Mode: syscall.S_IFDIR,
	}
	for i, issue := range issues {
		entries[i+3] = fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFLNK, // Symlink to issue directory
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (p *ProjectNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle project.md metadata file
	if name == "project.md" {
		node := &ProjectInfoNode{lfs: p.lfs, team: p.team, project: p.project}
		content := node.generateContent()
		out.Attr.Mode = 0644 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		out.Attr.SetTimes(&p.project.UpdatedAt, &p.project.UpdatedAt, &p.project.CreatedAt)
		return p.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  projectInfoIno(p.project.ID),
		}), 0
	}

	// Handle docs/ directory
	if name == "docs" {
		node := &DocsNode{lfs: p.lfs, projectID: p.project.ID}
		return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	// Handle updates/ directory
	if name == "updates" {
		node := &UpdatesNode{lfs: p.lfs, projectID: p.project.ID, projectUpdatedAt: p.project.UpdatedAt}
		return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	issues, err := p.lfs.GetProjectIssues(ctx, p.project.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			node := &ProjectIssueSymlink{
				identifier: issue.Identifier,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}

	return nil, syscall.ENOENT
}

// ProjectIssueSymlink is a symlink pointing to an issue directory
type ProjectIssueSymlink struct {
	fs.Inode
	identifier string
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
	out.Size = uint64(len(target))
	return 0
}

// ProjectInfoNode is a virtual file containing project metadata
type ProjectInfoNode struct {
	fs.Inode
	lfs          *LinearFS
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
var _ fs.NodeSetattrer = (*ProjectInfoNode)(nil)

func (p *ProjectInfoNode) generateContent() []byte {
	status := "unknown"
	if p.project.Status != nil {
		status = p.project.Status.Name
	}

	var leadYAML string
	if p.project.Lead != nil {
		leadYAML = fmt.Sprintf(`lead:
  id: %s
  name: %s
  email: %s
`, p.project.Lead.ID, p.project.Lead.Name, p.project.Lead.Email)
	}

	var startDate, targetDate string
	if p.project.StartDate != nil {
		startDate = fmt.Sprintf("startDate: %q\n", *p.project.StartDate)
	}
	if p.project.TargetDate != nil {
		targetDate = fmt.Sprintf("targetDate: %q\n", *p.project.TargetDate)
	}

	// Build initiatives list (editable)
	var initiativesYAML string
	if p.project.Initiatives != nil && len(p.project.Initiatives.Nodes) > 0 {
		names := make([]string, len(p.project.Initiatives.Nodes))
		for i, init := range p.project.Initiatives.Nodes {
			names[i] = init.Name
		}
		initiativesYAML = "initiatives:\n"
		for _, name := range names {
			initiativesYAML += fmt.Sprintf("  - %q\n", name)
		}
	}

	content := fmt.Sprintf(`---
id: %s
name: %s
slug: %s
url: %s
status: %s
%s%s%s%screated: %q
updated: %q
---

%s`,
		p.project.ID,
		p.project.Name,
		p.project.Slug,
		p.project.URL,
		status,
		leadYAML,
		startDate,
		targetDate,
		initiativesYAML,
		p.project.CreatedAt.Format(time.RFC3339),
		p.project.UpdatedAt.Format(time.RFC3339),
		p.project.Description,
	)
	return []byte(content)
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
		return syscall.EIO
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

	// Build sets for comparison
	currentSet := make(map[string]bool)
	for _, name := range currentInitiatives {
		currentSet[name] = true
	}
	newSet := make(map[string]bool)
	for _, name := range newInitiatives {
		newSet[name] = true
	}

	// Find initiatives to add (in new but not in current)
	for _, name := range newInitiatives {
		if !currentSet[name] {
			initiativeID, err := p.lfs.ResolveInitiativeID(ctx, name)
			if err != nil {
				log.Printf("Failed to resolve initiative '%s': %v", name, err)
				return syscall.EIO
			}
			if err := p.lfs.client.AddProjectToInitiative(ctx, p.project.ID, initiativeID); err != nil {
				log.Printf("Failed to add project to initiative '%s': %v", name, err)
				return syscall.EIO
			}
			if p.lfs.debug {
				log.Printf("Added project %s to initiative %s", p.project.Name, name)
			}
		}
	}

	// Find initiatives to remove (in current but not in new)
	for _, name := range currentInitiatives {
		if !newSet[name] {
			initiativeID, err := p.lfs.ResolveInitiativeID(ctx, name)
			if err != nil {
				log.Printf("Failed to resolve initiative '%s' for removal: %v", name, err)
				return syscall.EIO
			}
			if err := p.lfs.client.RemoveProjectFromInitiative(ctx, p.project.ID, initiativeID); err != nil {
				log.Printf("Failed to remove project from initiative '%s': %v", name, err)
				return syscall.EIO
			}
			if p.lfs.debug {
				log.Printf("Removed project %s from initiative %s", p.project.Name, name)
			}
		}
	}

	if p.lfs.debug {
		log.Printf("Flush: project %s updated successfully", p.project.Name)
	}

	// Invalidate caches
	p.lfs.InvalidateTeamProjects(p.team.ID)
	p.lfs.initiativeCache.Delete("initiatives")

	// Invalidate kernel inode cache
	p.lfs.InvalidateKernelInode(projectInfoIno(p.project.ID))

	p.dirty = false
	p.contentReady = false // Force re-generate on next read

	return 0
}

// UpdatesNode represents /teams/{KEY}/projects/{slug}/updates/
type UpdatesNode struct {
	fs.Inode
	lfs              *LinearFS
	projectID        string
	projectUpdatedAt time.Time
}

var _ fs.NodeReaddirer = (*UpdatesNode)(nil)
var _ fs.NodeLookuper = (*UpdatesNode)(nil)
var _ fs.NodeCreater = (*UpdatesNode)(nil)
var _ fs.NodeGetattrer = (*UpdatesNode)(nil)

func (n *UpdatesNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&n.projectUpdatedAt, &n.projectUpdatedAt, &n.projectUpdatedAt)
	return 0
}

func (n *UpdatesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	updates, err := n.lfs.GetProjectUpdates(ctx, n.projectID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Always include new.md for creating updates
	entries := []fuse.DirEntry{
		{Name: "new.md", Mode: syscall.S_IFREG},
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
			Name: fmt.Sprintf("%03d-%s-%s.md", i+1, timestamp, healthSuffix),
			Mode: syscall.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), 0
}

func (n *UpdatesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle new.md for creating updates
	if name == "new.md" {
		now := time.Now()
		node := &NewUpdateNode{
			lfs:       n.lfs,
			projectID: n.projectID,
		}
		out.Attr.Mode = 0200 | syscall.S_IFREG
		out.Attr.Size = 0
		out.Attr.SetTimes(&now, &now, &now)
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
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
		expectedName := fmt.Sprintf("%03d-%s-%s.md", i+1, timestamp, healthSuffix)
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

	node := &NewUpdateNode{
		lfs:       n.lfs,
		projectID: n.projectID,
	}

	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})
	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
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

// NewUpdateNode handles creating new project updates
type NewUpdateNode struct {
	fs.Inode
	lfs       *LinearFS
	projectID string

	mu      sync.Mutex
	content []byte
	created bool
}

var _ fs.NodeGetattrer = (*NewUpdateNode)(nil)
var _ fs.NodeOpener = (*NewUpdateNode)(nil)
var _ fs.NodeReader = (*NewUpdateNode)(nil)
var _ fs.NodeWriter = (*NewUpdateNode)(nil)
var _ fs.NodeFlusher = (*NewUpdateNode)(nil)
var _ fs.NodeFsyncer = (*NewUpdateNode)(nil)
var _ fs.NodeSetattrer = (*NewUpdateNode)(nil)

func (n *NewUpdateNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	out.Mode = 0200
	out.Size = uint64(len(n.content))
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *NewUpdateNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewUpdateNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// new.md is write-only - return permission denied
	return nil, syscall.EACCES
}

func (n *NewUpdateNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write new update: offset=%d len=%d", off, len(data))
	}

	// Expand buffer if needed
	newLen := int(off) + len(data)
	if newLen > len(n.content) {
		newContent := make([]byte, newLen)
		copy(newContent, n.content)
		n.content = newContent
	}

	copy(n.content[off:], data)
	return uint32(len(data)), 0
}

func (n *NewUpdateNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if int(sz) < len(n.content) {
			n.content = n.content[:sz]
		} else if int(sz) > len(n.content) {
			newContent := make([]byte, sz)
			copy(newContent, n.content)
			n.content = newContent
		}
	}

	out.Mode = 0200
	out.Size = uint64(len(n.content))
	return 0
}

func (n *NewUpdateNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created || len(n.content) == 0 {
		return 0
	}

	// Parse the content - could be plain text or markdown with frontmatter
	body, health := parseUpdateContent(n.content)
	if body == "" {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if n.lfs.debug {
		log.Printf("Creating project update: health=%s body=%s", health, body[:min(50, len(body))])
	}

	_, err := n.lfs.CreateProjectUpdate(ctx, n.projectID, body, health)
	if err != nil {
		log.Printf("Failed to create project update: %v", err)
		return syscall.EIO
	}

	n.created = true

	// Invalidate kernel cache entry for updates directory
	n.lfs.InvalidateKernelEntry(updatesDirIno(n.projectID), "new.md")

	if n.lfs.debug {
		log.Printf("Project update created successfully")
	}

	return 0
}

func (n *NewUpdateNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

// parseUpdateContent extracts body and health from update content
// Supports plain text or markdown with YAML frontmatter containing health field
func parseUpdateContent(content []byte) (body string, health string) {
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
