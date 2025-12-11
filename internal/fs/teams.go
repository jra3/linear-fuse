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

// TeamsNode represents the /teams directory
type TeamsNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*TeamsNode)(nil)
var _ fs.NodeLookuper = (*TeamsNode)(nil)

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
var _ fs.NodeCreater = (*TeamNode)(nil)

func (t *TeamNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := t.lfs.GetTeamIssues(ctx, t.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// +5 for .team.md, .states.md, .labels.md, cycles/, projects/
	entries := make([]fuse.DirEntry, len(issues)+5)
	entries[0] = fuse.DirEntry{
		Name: ".team.md",
		Mode: syscall.S_IFREG,
	}
	entries[1] = fuse.DirEntry{
		Name: ".states.md",
		Mode: syscall.S_IFREG,
	}
	entries[2] = fuse.DirEntry{
		Name: ".labels.md",
		Mode: syscall.S_IFREG,
	}
	entries[3] = fuse.DirEntry{
		Name: "cycles",
		Mode: syscall.S_IFDIR,
	}
	entries[4] = fuse.DirEntry{
		Name: "projects",
		Mode: syscall.S_IFDIR,
	}
	for i, issue := range issues {
		entries[i+5] = fuse.DirEntry{
			Name: issue.Identifier + ".md",
			Mode: syscall.S_IFREG,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (t *TeamNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle .team.md metadata file
	if name == ".team.md" {
		node := &TeamInfoNode{team: t.team}
		content := node.generateContent()
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		out.Attr.SetTimes(&t.team.UpdatedAt, &t.team.UpdatedAt, &t.team.CreatedAt)
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

	// Handle .states.md metadata file
	if name == ".states.md" {
		node := &StatesInfoNode{lfs: t.lfs, team: t.team}
		content := node.generateContent(context.Background())
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

	// Handle .labels.md metadata file
	if name == ".labels.md" {
		node := &LabelsInfoNode{lfs: t.lfs, team: t.team}
		content := node.generateContent(context.Background())
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

	// Handle cycles directory
	if name == "cycles" {
		node := &CyclesNode{lfs: t.lfs, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	// Handle projects directory
	if name == "projects" {
		node := &ProjectsNode{lfs: t.lfs, team: t.team}
		return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	issues, err := t.lfs.GetTeamIssues(ctx, t.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier+".md" == name {
			// Pre-generate content so Getattr returns correct size
			content, err := marshal.IssueToMarkdown(&issue)
			if err != nil {
				return nil, syscall.EIO
			}
			node := &IssueNode{
				lfs:          t.lfs,
				issue:        issue,
				content:      content,
				contentReady: true,
			}
			// Set attributes on EntryOut so ls shows correct size/times
			out.Attr.Mode = 0644 | syscall.S_IFREG
			out.Attr.Size = uint64(len(content))
			out.SetAttrTimeout(30 * time.Second)
			out.SetEntryTimeout(30 * time.Second)
			out.Attr.SetTimes(&issue.UpdatedAt, &issue.UpdatedAt, &issue.CreatedAt)
			return t.NewInode(ctx, node, fs.StableAttr{
				Mode: syscall.S_IFREG,
				Ino:  issueIno(issue.ID),
			}), 0
		}
	}

	return nil, syscall.ENOENT
}

// Create creates a new issue file
func (t *TeamNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if t.lfs.debug {
		log.Printf("Create: %s in team %s", name, t.team.Key)
	}

	// Extract title from filename (remove .md extension)
	title := strings.TrimSuffix(name, ".md")
	if title == name {
		// No .md extension, add it
		name = name + ".md"
	}

	// Create a new issue node that will be written to
	node := &NewIssueNode{
		lfs:    t.lfs,
		teamID: t.team.ID,
		title:  title,
	}

	inode := t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})

	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
}

// TeamInfoNode is a virtual file containing team metadata
type TeamInfoNode struct {
	fs.Inode
	team api.Team
}

var _ fs.NodeGetattrer = (*TeamInfoNode)(nil)
var _ fs.NodeOpener = (*TeamInfoNode)(nil)
var _ fs.NodeReader = (*TeamInfoNode)(nil)

func (t *TeamInfoNode) generateContent() []byte {
	content := fmt.Sprintf(`---
id: %s
key: %s
name: %s
icon: %s
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
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeGetattrer = (*StatesInfoNode)(nil)
var _ fs.NodeOpener = (*StatesInfoNode)(nil)
var _ fs.NodeReader = (*StatesInfoNode)(nil)

func (s *StatesInfoNode) generateContent(ctx context.Context) []byte {
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

	content := fmt.Sprintf(`---
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
	)
	return []byte(content)
}

func (s *StatesInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := s.generateContent(ctx)
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(content))
	return 0
}

func (s *StatesInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (s *StatesInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := s.generateContent(ctx)
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
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeGetattrer = (*LabelsInfoNode)(nil)
var _ fs.NodeOpener = (*LabelsInfoNode)(nil)
var _ fs.NodeReader = (*LabelsInfoNode)(nil)

func (l *LabelsInfoNode) generateContent(ctx context.Context) []byte {
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

	content := fmt.Sprintf(`---
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
	)
	return []byte(content)
}

func (l *LabelsInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := l.generateContent(ctx)
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(content))
	return 0
}

func (l *LabelsInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (l *LabelsInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := l.generateContent(ctx)
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}
