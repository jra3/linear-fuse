package fs

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// TeamsNode represents the /teams directory
type TeamsNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*TeamsNode)(nil)
var _ fs.NodeLookuper = (*TeamsNode)(nil)
var _ fs.NodeGetattrer = (*TeamsNode)(nil)

func (t *TeamsNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	out.Uid = t.lfs.uid
	out.Gid = t.lfs.gid
	return 0
}

func (t *TeamsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	teams, err := t.lfs.GetTeams(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(teams))
	for i, team := range teams {
		entries[i] = fuse.DirEntry{
			Name: team.Key,
			Mode: syscall.S_IFDIR,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (t *TeamsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	teams, err := t.lfs.GetTeams(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, team := range teams {
		if team.Key == name {
			out.Attr.Mode = 0755 | syscall.S_IFDIR
			out.Attr.Uid = t.lfs.uid
			out.Attr.Gid = t.lfs.gid
			node := &TeamNode{lfs: t.lfs, team: team}
			return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	return nil, syscall.ENOENT
}

// TeamNode represents a single team directory (e.g., /teams/ENG)
type TeamNode struct {
	fs.Inode
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeReaddirer = (*TeamNode)(nil)
var _ fs.NodeLookuper = (*TeamNode)(nil)
var _ fs.NodeGetattrer = (*TeamNode)(nil)

func (t *TeamNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	out.Uid = t.lfs.uid
	out.Gid = t.lfs.gid
	return 0
}

func (t *TeamNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "team.md", Mode: syscall.S_IFREG},
		{Name: "states.md", Mode: syscall.S_IFREG},
		{Name: "labels.md", Mode: syscall.S_IFREG},
		{Name: "by", Mode: syscall.S_IFDIR},
		{Name: "cycles", Mode: syscall.S_IFDIR},
		{Name: "projects", Mode: syscall.S_IFDIR},
		{Name: "issues", Mode: syscall.S_IFDIR},
		{Name: "docs", Mode: syscall.S_IFDIR},
		{Name: "labels", Mode: syscall.S_IFDIR},
		{Name: "search", Mode: syscall.S_IFDIR},
	}

	return fs.NewListDirStream(entries), 0
}

func (t *TeamNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "team.md":
		node := &TeamInfoNode{lfs: t.lfs, team: t.team}
		content := node.generateContent()
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		out.Attr.Size = uint64(len(content))
		out.Attr.SetTimes(&t.team.UpdatedAt, &t.team.UpdatedAt, &t.team.CreatedAt)
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0

	case "states.md":
		node := &StatesInfoNode{lfs: t.lfs, team: t.team}
		content := node.getContent(ctx)
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		out.Attr.Size = uint64(len(content))
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0

	case "labels.md":
		node := &LabelsInfoNode{lfs: t.lfs, team: t.team}
		content := node.getContent(ctx)
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		out.Attr.Size = uint64(len(content))
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0

	case "by":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		node := &FilterRootNode{lfs: t.lfs, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "cycles":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		node := &CyclesNode{lfs: t.lfs, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "projects":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		node := &ProjectsNode{lfs: t.lfs, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "issues":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		node := &IssuesNode{lfs: t.lfs, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "docs":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		node := &DocsNode{lfs: t.lfs, teamID: t.team.ID}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0

	case "labels":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		node := &LabelsNode{lfs: t.lfs, teamID: t.team.ID}
		return t.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  labelsDirIno(t.team.ID),
		}), 0

	case "search":
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = t.lfs.uid
		out.Attr.Gid = t.lfs.gid
		node := &SearchNode{lfs: t.lfs, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	return nil, syscall.ENOENT
}

// TeamInfoNode is a virtual file containing team metadata
type TeamInfoNode struct {
	fs.Inode
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeGetattrer = (*TeamInfoNode)(nil)
var _ fs.NodeOpener = (*TeamInfoNode)(nil)
var _ fs.NodeReader = (*TeamInfoNode)(nil)

func (t *TeamInfoNode) generateContent() []byte {
	content := fmt.Sprintf(`---
id: %s
key: %s
name: %q
icon: %q
created: %q
updated: %q
---

# %s

- **Key:** %s
- **ID:** %s
`,
		t.team.ID,
		t.team.Key,
		t.team.Name,
		t.team.Icon,
		t.team.CreatedAt.Format(time.RFC3339),
		t.team.UpdatedAt.Format(time.RFC3339),
		t.team.Name,
		t.team.Key,
		t.team.ID,
	)
	return []byte(content)
}

func (t *TeamInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := t.generateContent()
	out.Mode = 0444 | syscall.S_IFREG
	if t.lfs != nil {
		out.Uid = t.lfs.uid
		out.Gid = t.lfs.gid
	}
	out.Size = uint64(len(content))
	out.Attr.SetTimes(&t.team.UpdatedAt, &t.team.UpdatedAt, &t.team.CreatedAt)
	return 0
}

func (t *TeamInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (t *TeamInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := t.generateContent()
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}

// StatesInfoNode is a virtual file containing workflow states metadata
type StatesInfoNode struct {
	fs.Inode
	lfs           *LinearFS
	team          api.Team
	cachedContent []byte
	cachedAt      time.Time
}

var _ fs.NodeGetattrer = (*StatesInfoNode)(nil)
var _ fs.NodeOpener = (*StatesInfoNode)(nil)
var _ fs.NodeReader = (*StatesInfoNode)(nil)

const metadataCacheTTL = 10 * time.Minute // Match state/label cache TTL

func (s *StatesInfoNode) getContent(ctx context.Context) []byte {
	// Return cached content if still valid
	if s.cachedContent != nil && time.Since(s.cachedAt) < metadataCacheTTL {
		return s.cachedContent
	}

	states, err := s.lfs.GetTeamStates(ctx, s.team.ID)
	if err != nil {
		return []byte("# Error loading states\n")
	}

	// Build YAML frontmatter
	var statesYAML string
	for _, state := range states {
		statesYAML += fmt.Sprintf("  - id: %s\n    name: %s\n    type: %s\n",
			state.ID, state.Name, state.Type)
	}

	// Build markdown table
	var table string
	for _, state := range states {
		table += fmt.Sprintf("| %s | %s | %s |\n", state.Name, state.Type, state.ID)
	}

	content := []byte(fmt.Sprintf(`---
team: %s
states:
%s---

# Workflow States for %s

| Name | Type | ID |
|------|------|-----|
%s`,
		s.team.Key,
		statesYAML,
		s.team.Key,
		table,
	))

	s.cachedContent = content
	s.cachedAt = time.Now()
	return content
}

func (s *StatesInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := s.getContent(ctx)
	out.Mode = 0444 | syscall.S_IFREG
	out.Uid = s.lfs.uid
	out.Gid = s.lfs.gid
	out.Size = uint64(len(content))
	return 0
}

func (s *StatesInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (s *StatesInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := s.getContent(ctx)
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}

// LabelsInfoNode is a virtual file containing labels metadata
type LabelsInfoNode struct {
	fs.Inode
	lfs           *LinearFS
	team          api.Team
	cachedContent []byte
	cachedAt      time.Time
}

var _ fs.NodeGetattrer = (*LabelsInfoNode)(nil)
var _ fs.NodeOpener = (*LabelsInfoNode)(nil)
var _ fs.NodeReader = (*LabelsInfoNode)(nil)

func (l *LabelsInfoNode) getContent(ctx context.Context) []byte {
	// Return cached content if still valid
	if l.cachedContent != nil && time.Since(l.cachedAt) < metadataCacheTTL {
		return l.cachedContent
	}

	labels, err := l.lfs.GetTeamLabels(ctx, l.team.ID)
	if err != nil {
		return []byte("# Error loading labels\n")
	}

	// Build YAML frontmatter
	var labelsYAML string
	for _, label := range labels {
		labelsYAML += fmt.Sprintf("  - id: %s\n    name: %s\n    color: %q\n",
			label.ID, label.Name, label.Color)
		if label.Description != "" {
			labelsYAML += fmt.Sprintf("    description: %q\n", label.Description)
		}
	}

	// Build markdown table
	var table string
	for _, label := range labels {
		table += fmt.Sprintf("| %s | %s | %s |\n", label.Name, label.Color, label.ID)
	}

	content := []byte(fmt.Sprintf(`---
team: %s
labels:
%s---

# Labels for %s

| Name | Color | ID |
|------|-------|-----|
%s`,
		l.team.Key,
		labelsYAML,
		l.team.Key,
		table,
	))

	l.cachedContent = content
	l.cachedAt = time.Now()
	return content
}

func (l *LabelsInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := l.getContent(ctx)
	out.Mode = 0444 | syscall.S_IFREG
	out.Uid = l.lfs.uid
	out.Gid = l.lfs.gid
	out.Size = uint64(len(content))
	return 0
}

func (l *LabelsInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (l *LabelsInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := l.getContent(ctx)
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}
