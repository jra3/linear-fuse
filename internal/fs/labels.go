package fs

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// labelsDirIno generates a stable inode number for a labels directory
func labelsDirIno(teamID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("labels:" + teamID))
	return h.Sum64()
}

// labelIno generates a stable inode number for a label
func labelIno(labelID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("label:" + labelID))
	return h.Sum64()
}

// LabelsNode represents the /teams/{KEY}/labels/ directory
type LabelsNode struct {
	fs.Inode
	lfs    *LinearFS
	teamID string
}

var _ fs.NodeReaddirer = (*LabelsNode)(nil)
var _ fs.NodeLookuper = (*LabelsNode)(nil)
var _ fs.NodeCreater = (*LabelsNode)(nil)
var _ fs.NodeUnlinker = (*LabelsNode)(nil)
var _ fs.NodeRenamer = (*LabelsNode)(nil)

func (n *LabelsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	labels, err := n.lfs.GetTeamLabels(ctx, n.teamID)
	if err != nil {
		log.Printf("Failed to get labels: %v", err)
		return nil, syscall.EIO
	}

	// +1 for new.md
	entries := make([]fuse.DirEntry, len(labels)+1)

	// Always include new.md for creating labels
	entries[0] = fuse.DirEntry{
		Name: "new.md",
		Mode: syscall.S_IFREG,
	}

	for i, label := range labels {
		entries[i+1] = fuse.DirEntry{
			Name: labelFilename(label),
			Mode: syscall.S_IFREG,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (n *LabelsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle new.md for creating labels
	if name == "new.md" {
		now := time.Now()
		node := &NewLabelNode{
			lfs:    n.lfs,
			teamID: n.teamID,
		}
		out.Attr.Mode = 0200 | syscall.S_IFREG
		out.Attr.Size = 0
		out.Attr.SetTimes(&now, &now, &now)
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
		}), 0
	}

	labels, err := n.lfs.GetTeamLabels(ctx, n.teamID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Match by filename
	for _, label := range labels {
		if labelFilename(label) == name {
			content := labelToMarkdown(&label)
			node := &LabelFileNode{
				lfs:          n.lfs,
				label:        label,
				teamID:       n.teamID,
				content:      content,
				contentReady: true,
			}
			out.Attr.Mode = 0644 | syscall.S_IFREG
			out.Attr.Size = uint64(len(content))
			out.SetAttrTimeout(30 * time.Second)
			out.SetEntryTimeout(30 * time.Second)
			return n.NewInode(ctx, node, fs.StableAttr{
				Mode: syscall.S_IFREG,
				Ino:  labelIno(label.ID),
			}), 0
		}
	}

	return nil, syscall.ENOENT
}

func (n *LabelsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Unlink label: %s", name)
	}

	// Don't allow deleting new.md
	if name == "new.md" {
		return syscall.EPERM
	}

	labels, err := n.lfs.GetTeamLabels(ctx, n.teamID)
	if err != nil {
		return syscall.EIO
	}

	// Find the label by filename
	for _, label := range labels {
		if labelFilename(label) == name {
			err := n.lfs.DeleteLabel(ctx, label.ID, n.teamID)
			if err != nil {
				log.Printf("Failed to delete label: %v", err)
				return syscall.EIO
			}
			if n.lfs.debug {
				log.Printf("Label deleted successfully")
			}
			// Invalidate kernel cache entry
			n.lfs.InvalidateKernelEntry(labelsDirIno(n.teamID), name)
			return 0
		}
	}

	return syscall.ENOENT
}

func (n *LabelsNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Rename label: %s -> %s (newParent type: %T)", name, newName, newParent)
	}

	// Don't allow renaming new.md
	if name == "new.md" {
		return syscall.EPERM
	}

	// For same-directory rename, newParent should be the same inode as us
	// Compare by inode number
	if newParent.EmbeddedInode().StableAttr().Ino != n.EmbeddedInode().StableAttr().Ino {
		if n.lfs.debug {
			log.Printf("Rename: cross-directory not allowed")
		}
		return syscall.EXDEV
	}

	// Extract new label name from filename (remove .md suffix, convert dashes to spaces)
	if !strings.HasSuffix(newName, ".md") {
		return syscall.EINVAL
	}
	newLabelName := strings.TrimSuffix(newName, ".md")
	newLabelName = strings.ReplaceAll(newLabelName, "-", " ")

	labels, err := n.lfs.GetTeamLabels(ctx, n.teamID)
	if err != nil {
		return syscall.EIO
	}

	// Find the label by old filename
	for _, label := range labels {
		if labelFilename(label) == name {
			// Update label name
			err := n.lfs.UpdateLabel(ctx, label.ID, map[string]any{"name": newLabelName}, n.teamID)
			if err != nil {
				log.Printf("Failed to rename label: %v", err)
				return syscall.EIO
			}
			if n.lfs.debug {
				log.Printf("Label renamed successfully: %s -> %s", label.Name, newLabelName)
			}
			// Invalidate kernel cache for old and new names
			n.lfs.InvalidateKernelEntry(labelsDirIno(n.teamID), name)
			n.lfs.InvalidateKernelEntry(labelsDirIno(n.teamID), newName)
			return 0
		}
	}

	return syscall.ENOENT
}

func (n *LabelsNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create label file: %s", name)
	}

	// Only allow creating .md files
	if !strings.HasSuffix(name, ".md") {
		return nil, nil, 0, syscall.EINVAL
	}

	node := &NewLabelNode{
		lfs:    n.lfs,
		teamID: n.teamID,
	}

	inode := n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})

	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
}

// labelFilename returns the filename for a label
func labelFilename(label api.Label) string {
	// Sanitize name for filename
	name := strings.ReplaceAll(label.Name, " ", "-")
	name = strings.ReplaceAll(name, "/", "-")
	return name + ".md"
}

// labelToMarkdown converts a label to markdown with YAML frontmatter
func labelToMarkdown(label *api.Label) []byte {
	content := fmt.Sprintf(`---
id: %s
name: %q
color: %q
description: %q
---

# %s

- **Color:** %s
- **ID:** %s
`,
		label.ID,
		label.Name,
		label.Color,
		label.Description,
		label.Name,
		label.Color,
		label.ID,
	)
	if label.Description != "" {
		content += fmt.Sprintf("\n%s\n", label.Description)
	}
	return []byte(content)
}

// LabelFileNode represents a single label file (read-write)
type LabelFileNode struct {
	fs.Inode
	lfs    *LinearFS
	label  api.Label
	teamID string

	mu           sync.Mutex
	content      []byte
	contentReady bool
	dirty        bool
}

var _ fs.NodeGetattrer = (*LabelFileNode)(nil)
var _ fs.NodeOpener = (*LabelFileNode)(nil)
var _ fs.NodeReader = (*LabelFileNode)(nil)
var _ fs.NodeWriter = (*LabelFileNode)(nil)
var _ fs.NodeFlusher = (*LabelFileNode)(nil)
var _ fs.NodeFsyncer = (*LabelFileNode)(nil)
var _ fs.NodeSetattrer = (*LabelFileNode)(nil)

func (n *LabelFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	out.Mode = 0644
	out.Size = uint64(len(n.content))
	return 0
}

func (n *LabelFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *LabelFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if off >= int64(len(n.content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(n.content)) {
		end = int64(len(n.content))
	}

	return fuse.ReadResultData(n.content[off:end]), 0
}

func (n *LabelFileNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write label %s: offset=%d len=%d", n.label.ID, off, len(data))
	}

	// Expand buffer if needed
	newLen := int(off) + len(data)
	if newLen > len(n.content) {
		newContent := make([]byte, newLen)
		copy(newContent, n.content)
		n.content = newContent
	}

	copy(n.content[off:], data)
	n.dirty = true

	return uint32(len(data)), 0
}

func (n *LabelFileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if n.lfs.debug {
			log.Printf("Setattr truncate label %s: size=%d", n.label.ID, sz)
		}
		if int(sz) < len(n.content) {
			n.content = n.content[:sz]
		} else if int(sz) > len(n.content) {
			newContent := make([]byte, sz)
			copy(newContent, n.content)
			n.content = newContent
		}
		n.dirty = true
	}

	out.Mode = 0644
	out.Size = uint64(len(n.content))
	return 0
}

func (n *LabelFileNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.dirty || n.content == nil {
		return 0
	}

	// Parse the markdown and get update fields
	update, err := parseLabelMarkdown(n.content, &n.label)
	if err != nil {
		log.Printf("Failed to parse label: %v", err)
		return syscall.EIO
	}

	if len(update) == 0 {
		if n.lfs.debug {
			log.Printf("Flush label %s: no changes", n.label.ID)
		}
		n.dirty = false
		return 0
	}

	if n.lfs.debug {
		log.Printf("Updating label %s", n.label.ID)
	}

	err = n.lfs.UpdateLabel(ctx, n.label.ID, update, n.teamID)
	if err != nil {
		log.Printf("Failed to update label: %v", err)
		return syscall.EIO
	}

	n.dirty = false
	n.contentReady = false // Force regenerate on next read

	// Invalidate kernel inode cache
	n.lfs.InvalidateKernelInode(labelIno(n.label.ID))

	if n.lfs.debug {
		log.Printf("Label updated successfully")
	}

	return 0
}

func (n *LabelFileNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

// NewLabelNode handles creating new labels
type NewLabelNode struct {
	fs.Inode
	lfs    *LinearFS
	teamID string

	mu      sync.Mutex
	content []byte
	created bool
}

var _ fs.NodeGetattrer = (*NewLabelNode)(nil)
var _ fs.NodeOpener = (*NewLabelNode)(nil)
var _ fs.NodeReader = (*NewLabelNode)(nil)
var _ fs.NodeWriter = (*NewLabelNode)(nil)
var _ fs.NodeFlusher = (*NewLabelNode)(nil)
var _ fs.NodeFsyncer = (*NewLabelNode)(nil)
var _ fs.NodeSetattrer = (*NewLabelNode)(nil)

func (n *NewLabelNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	out.Mode = 0200
	out.Size = uint64(len(n.content))
	return 0
}

func (n *NewLabelNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewLabelNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// new.md is write-only - return permission denied
	return nil, syscall.EACCES
}

func (n *NewLabelNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write new label: offset=%d len=%d", off, len(data))
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

func (n *NewLabelNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
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

func (n *NewLabelNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created || len(n.content) == 0 {
		return 0
	}

	// Parse the new label content
	name, color, description, err := parseNewLabelMarkdown(n.content)
	if err != nil {
		log.Printf("Failed to parse new label: %v", err)
		return syscall.EIO
	}

	if name == "" {
		log.Printf("New label has no name")
		return syscall.EINVAL
	}

	if n.lfs.debug {
		log.Printf("Creating label: name=%s color=%s", name, color)
	}

	// Build create input
	input := map[string]any{
		"teamId": n.teamID,
		"name":   name,
	}
	if color != "" {
		input["color"] = color
	}
	if description != "" {
		input["description"] = description
	}

	_, err = n.lfs.CreateLabel(ctx, input)
	if err != nil {
		log.Printf("Failed to create label: %v", err)
		return syscall.EIO
	}

	n.created = true

	// Invalidate kernel cache entry for labels directory
	n.lfs.InvalidateKernelEntry(labelsDirIno(n.teamID), "new.md")

	if n.lfs.debug {
		log.Printf("Label created successfully")
	}

	return 0
}

func (n *NewLabelNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

// parseLabelMarkdown parses markdown and returns fields that changed
func parseLabelMarkdown(content []byte, original *api.Label) (map[string]any, error) {
	// Use the marshal package to parse YAML frontmatter
	doc, err := parseYAMLFrontmatter(content)
	if err != nil {
		return nil, err
	}

	update := make(map[string]any)

	// Check name
	if name, ok := doc["name"].(string); ok && name != original.Name {
		update["name"] = name
	}

	// Check color
	if color, ok := doc["color"].(string); ok && color != original.Color {
		update["color"] = color
	}

	// Check description
	if desc, ok := doc["description"].(string); ok && desc != original.Description {
		update["description"] = desc
	}

	return update, nil
}

// parseNewLabelMarkdown parses markdown for creating a new label
func parseNewLabelMarkdown(content []byte) (name, color, description string, err error) {
	doc, err := parseYAMLFrontmatter(content)
	if err != nil {
		return "", "", "", err
	}

	if n, ok := doc["name"].(string); ok {
		name = n
	}
	if c, ok := doc["color"].(string); ok {
		color = c
	}
	if d, ok := doc["description"].(string); ok {
		description = d
	}

	return name, color, description, nil
}

// parseYAMLFrontmatter extracts YAML frontmatter from markdown content
func parseYAMLFrontmatter(content []byte) (map[string]any, error) {
	s := string(content)

	// Check for YAML frontmatter delimiter
	if !strings.HasPrefix(s, "---") {
		return nil, fmt.Errorf("no YAML frontmatter found")
	}

	// Find end of frontmatter
	endIdx := strings.Index(s[3:], "---")
	if endIdx == -1 {
		return nil, fmt.Errorf("unterminated YAML frontmatter")
	}

	yamlContent := s[3 : endIdx+3]

	// Simple YAML parsing for our use case
	result := make(map[string]any)
	lines := strings.Split(yamlContent, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		// Remove quotes if present
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		result[key] = value
	}

	return result, nil
}
