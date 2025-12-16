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
)

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

	// +2 for .project.md and docs/
	entries := make([]fuse.DirEntry, len(issues)+2)
	entries[0] = fuse.DirEntry{
		Name: ".project.md",
		Mode: syscall.S_IFREG,
	}
	entries[1] = fuse.DirEntry{
		Name: "docs",
		Mode: syscall.S_IFDIR,
	}
	for i, issue := range issues {
		entries[i+2] = fuse.DirEntry{
			Name: issue.Identifier + ".md",
			Mode: syscall.S_IFLNK, // Symlink to team issue
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (p *ProjectNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle .project.md metadata file
	if name == ".project.md" {
		node := &ProjectInfoNode{project: p.project}
		content := node.generateContent()
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		out.Attr.SetTimes(&p.project.UpdatedAt, &p.project.UpdatedAt, &p.project.CreatedAt)
		return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

	// Handle docs/ directory
	if name == "docs" {
		node := &DocsNode{lfs: p.lfs, projectID: p.project.ID}
		return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	issues, err := p.lfs.GetProjectIssues(ctx, p.project.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier+".md" == name {
			// Create symlink to team directory
			teamKey := ""
			if issue.Team != nil {
				teamKey = issue.Team.Key
			}
			node := &ProjectIssueSymlink{
				teamKey:    teamKey,
				identifier: issue.Identifier,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			return p.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}

	return nil, syscall.ENOENT
}

// ProjectIssueSymlink is a symlink pointing to an issue in /teams/<KEY>/issues/<identifier>.md
type ProjectIssueSymlink struct {
	fs.Inode
	teamKey    string
	identifier string
}

var _ fs.NodeReadlinker = (*ProjectIssueSymlink)(nil)
var _ fs.NodeGetattrer = (*ProjectIssueSymlink)(nil)

func (s *ProjectIssueSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	// Return relative path to issues directory: ../../issues/<identifier>/issue.md
	target := fmt.Sprintf("../../issues/%s/issue.md", s.identifier)
	return []byte(target), 0
}

func (s *ProjectIssueSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0777 | syscall.S_IFLNK
	target := fmt.Sprintf("../../issues/%s/issue.md", s.identifier)
	out.Size = uint64(len(target))
	return 0
}

// ProjectInfoNode is a virtual file containing project metadata
type ProjectInfoNode struct {
	fs.Inode
	project api.Project
}

var _ fs.NodeGetattrer = (*ProjectInfoNode)(nil)
var _ fs.NodeOpener = (*ProjectInfoNode)(nil)
var _ fs.NodeReader = (*ProjectInfoNode)(nil)

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

	content := fmt.Sprintf(`---
id: %s
name: %s
slug: %s
url: %s
status: %s
%s%s%screated: %q
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
		p.project.CreatedAt.Format(time.RFC3339),
		p.project.UpdatedAt.Format(time.RFC3339),
		p.project.Description,
	)
	return []byte(content)
}

func (p *ProjectInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := p.generateContent()
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(content))
	out.Attr.SetTimes(&p.project.UpdatedAt, &p.project.UpdatedAt, &p.project.CreatedAt)
	return 0
}

func (p *ProjectInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (p *ProjectInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := p.generateContent()
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}
