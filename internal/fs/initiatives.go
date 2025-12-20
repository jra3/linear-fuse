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
)

// initiativeUpdatesDirIno generates a stable inode number for an initiative updates directory
func initiativeUpdatesDirIno(initiativeID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("initiative-updates:" + initiativeID))
	return h.Sum64()
}

// InitiativesNode represents the /initiatives directory
type InitiativesNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*InitiativesNode)(nil)
var _ fs.NodeLookuper = (*InitiativesNode)(nil)

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
			node := &InitiativeNode{lfs: i.lfs, initiative: init}
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
	fs.Inode
	lfs        *LinearFS
	initiative api.Initiative
}

var _ fs.NodeReaddirer = (*InitiativeNode)(nil)
var _ fs.NodeLookuper = (*InitiativeNode)(nil)

func (i *InitiativeNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "initiative.md", Mode: syscall.S_IFREG},
		{Name: "projects", Mode: syscall.S_IFDIR},
		{Name: "updates", Mode: syscall.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (i *InitiativeNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "initiative.md":
		node := &InitiativeInfoNode{initiative: i.initiative}
		content := node.generateContent()
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		out.Attr.SetTimes(&i.initiative.UpdatedAt, &i.initiative.UpdatedAt, &i.initiative.CreatedAt)
		return i.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0

	case "projects":
		node := &InitiativeProjectsNode{lfs: i.lfs, initiative: i.initiative}
		return i.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "updates":
		node := &InitiativeUpdatesNode{lfs: i.lfs, initiativeID: i.initiative.ID, initiativeUpdatedAt: i.initiative.UpdatedAt}
		return i.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	return nil, syscall.ENOENT
}

// InitiativeInfoNode is a virtual file containing initiative metadata
type InitiativeInfoNode struct {
	fs.Inode
	initiative api.Initiative
}

var _ fs.NodeGetattrer = (*InitiativeInfoNode)(nil)
var _ fs.NodeOpener = (*InitiativeInfoNode)(nil)
var _ fs.NodeReader = (*InitiativeInfoNode)(nil)

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

	// Build project list
	var projectsYAML string
	if len(i.initiative.Projects.Nodes) > 0 {
		projectsYAML = "projects:\n"
		for _, p := range i.initiative.Projects.Nodes {
			projectsYAML += fmt.Sprintf("  - id: %s\n    name: %q\n    slug: %s\n", p.ID, p.Name, p.Slug)
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

# %s

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
		i.initiative.Name,
		i.initiative.Description,
	)
	return []byte(content)
}

func (i *InitiativeInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := i.generateContent()
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(content))
	out.Attr.SetTimes(&i.initiative.UpdatedAt, &i.initiative.UpdatedAt, &i.initiative.CreatedAt)
	return 0
}

func (i *InitiativeInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (i *InitiativeInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := i.generateContent()
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}

// InitiativeProjectsNode represents the projects/ directory within an initiative
type InitiativeProjectsNode struct {
	fs.Inode
	lfs        *LinearFS
	initiative api.Initiative
}

var _ fs.NodeReaddirer = (*InitiativeProjectsNode)(nil)
var _ fs.NodeLookuper = (*InitiativeProjectsNode)(nil)

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
				lfs:     p.lfs,
				project: proj,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
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
	fs.Inode
	lfs     *LinearFS
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
	// Use a reasonable estimate for symlink size
	out.Mode = 0777 | syscall.S_IFLNK
	out.Size = 64
	return 0
}

// InitiativeUpdatesNode represents /initiatives/{slug}/updates/
type InitiativeUpdatesNode struct {
	fs.Inode
	lfs                 *LinearFS
	initiativeID        string
	initiativeUpdatedAt time.Time
}

var _ fs.NodeReaddirer = (*InitiativeUpdatesNode)(nil)
var _ fs.NodeLookuper = (*InitiativeUpdatesNode)(nil)
var _ fs.NodeCreater = (*InitiativeUpdatesNode)(nil)
var _ fs.NodeGetattrer = (*InitiativeUpdatesNode)(nil)

func (n *InitiativeUpdatesNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&n.initiativeUpdatedAt, &n.initiativeUpdatedAt, &n.initiativeUpdatedAt)
	return 0
}

func (n *InitiativeUpdatesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	updates, err := n.lfs.GetInitiativeUpdates(ctx, n.initiativeID)
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
			Name: fmt.Sprintf("%04d-%s-%s.md", i+1, timestamp, healthSuffix),
			Mode: syscall.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), 0
}

func (n *InitiativeUpdatesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle new.md for creating updates
	if name == "new.md" {
		now := time.Now()
		node := &NewInitiativeUpdateNode{
			lfs:          n.lfs,
			initiativeID: n.initiativeID,
		}
		out.Attr.Mode = 0200 | syscall.S_IFREG
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

func (n *InitiativeUpdatesNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create initiative update file: %s", name)
	}

	// Only allow creating .md files
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}

	node := &NewInitiativeUpdateNode{
		lfs:          n.lfs,
		initiativeID: n.initiativeID,
	}

	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})
	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
}

// InitiativeUpdateNode represents a single initiative update file (read-only)
type InitiativeUpdateNode struct {
	fs.Inode
	update  api.InitiativeUpdate
	content []byte
}

var _ fs.NodeGetattrer = (*InitiativeUpdateNode)(nil)
var _ fs.NodeOpener = (*InitiativeUpdateNode)(nil)
var _ fs.NodeReader = (*InitiativeUpdateNode)(nil)

func (n *InitiativeUpdateNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0444 | syscall.S_IFREG
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
	fs.Inode
	lfs          *LinearFS
	initiativeID string

	mu      sync.Mutex
	content []byte
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
	out.Mode = 0200
	out.Size = uint64(len(n.content))
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *NewInitiativeUpdateNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewInitiativeUpdateNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// new.md is write-only - return permission denied
	return nil, syscall.EACCES
}

func (n *NewInitiativeUpdateNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write new initiative update: offset=%d len=%d", off, len(data))
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

func (n *NewInitiativeUpdateNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
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

func (n *NewInitiativeUpdateNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created || len(n.content) == 0 {
		return 0
	}

	// Parse the content - could be plain text or markdown with frontmatter
	body, health := parseInitiativeUpdateContent(n.content)
	if body == "" {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if n.lfs.debug {
		log.Printf("Creating initiative update: health=%s body=%s", health, body[:min(50, len(body))])
	}

	_, err := n.lfs.CreateInitiativeUpdate(ctx, n.initiativeID, body, health)
	if err != nil {
		log.Printf("Failed to create initiative update: %v", err)
		return syscall.EIO
	}

	n.created = true

	// Invalidate kernel cache entry for updates directory
	n.lfs.InvalidateKernelEntry(initiativeUpdatesDirIno(n.initiativeID), "new.md")

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
