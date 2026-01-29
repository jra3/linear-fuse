package fs

import (
	"context"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// milestonesDirIno generates a stable inode for a milestones directory
func milestonesDirIno(projectID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("milestones:" + projectID))
	return h.Sum64()
}

// milestoneIno generates a stable inode for a milestone file
func milestoneIno(milestoneID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("milestone:" + milestoneID))
	return h.Sum64()
}

// milestonesCreateIno generates a stable inode for the _create trigger file
func milestonesCreateIno(projectID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("milestones-create:" + projectID))
	return h.Sum64()
}

// MilestonesNode represents a milestones/ directory within a project
type MilestonesNode struct {
	BaseNode
	projectID string
}

var _ fs.NodeReaddirer = (*MilestonesNode)(nil)
var _ fs.NodeLookuper = (*MilestonesNode)(nil)
var _ fs.NodeUnlinker = (*MilestonesNode)(nil)
var _ fs.NodeGetattrer = (*MilestonesNode)(nil)

func (n *MilestonesNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *MilestonesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	milestones, err := n.lfs.GetProjectMilestones(ctx, n.projectID)
	if err != nil {
		// On error, return just _create
		return fs.NewListDirStream([]fuse.DirEntry{
			{Name: "_create", Mode: syscall.S_IFREG},
		}), 0
	}

	// Always include _create for creating milestones
	entries := []fuse.DirEntry{
		{Name: "_create", Mode: syscall.S_IFREG},
	}

	for _, m := range milestones {
		entries = append(entries, fuse.DirEntry{
			Name: milestoneFilename(m),
			Mode: syscall.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), 0
}

func (n *MilestonesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle _create for creating milestones
	if name == "_create" {
		now := time.Now()
		node := &NewMilestoneNode{
			BaseNode:  BaseNode{lfs: n.lfs},
			projectID: n.projectID,
		}
		out.Attr.Mode = 0200 | syscall.S_IFREG
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = 0
		out.Attr.SetTimes(&now, &now, &now)
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  milestonesCreateIno(n.projectID),
		}), 0
	}

	milestones, err := n.lfs.GetProjectMilestones(ctx, n.projectID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Match by filename
	for _, m := range milestones {
		if milestoneFilename(m) == name {
			content, err := marshal.MilestoneToMarkdown(&m)
			if err != nil {
				log.Printf("Failed to marshal milestone: %v", err)
				return nil, syscall.EIO
			}
			node := &MilestoneFileNode{
				BaseNode:     BaseNode{lfs: n.lfs},
				milestone:    m,
				projectID:    n.projectID,
				content:      content,
				contentReady: true,
			}
			now := time.Now()
			out.Attr.Mode = 0644 | syscall.S_IFREG
			out.Attr.Uid = n.lfs.uid
			out.Attr.Gid = n.lfs.gid
			out.Attr.Size = uint64(len(content))
			out.SetAttrTimeout(5 * time.Second)
			out.SetEntryTimeout(5 * time.Second)
			out.Attr.SetTimes(&now, &now, &now)
			return n.NewInode(ctx, node, fs.StableAttr{
				Mode: syscall.S_IFREG,
				Ino:  milestoneIno(m.ID),
			}), 0
		}
	}

	return nil, syscall.ENOENT
}

func (n *MilestonesNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Unlink milestone: %s", name)
	}

	// Don't allow deleting _create
	if name == "_create" {
		return syscall.EPERM
	}

	milestones, err := n.lfs.GetProjectMilestones(ctx, n.projectID)
	if err != nil {
		return syscall.EIO
	}

	// Find the milestone by filename
	for _, m := range milestones {
		if milestoneFilename(m) == name {
			err := n.lfs.DeleteProjectMilestone(ctx, m.ID)
			if err != nil {
				log.Printf("Failed to delete milestone: %v", err)
				return syscall.EIO
			}
			// Invalidate kernel cache
			n.lfs.InvalidateKernelInode(milestonesDirIno(n.projectID))
			n.lfs.InvalidateKernelEntry(milestonesDirIno(n.projectID), name)
			if n.lfs.debug {
				log.Printf("Milestone deleted successfully")
			}
			return 0
		}
	}

	return syscall.ENOENT
}

// milestoneFilename returns the filename for a milestone
func milestoneFilename(m api.ProjectMilestone) string {
	// Sanitize name for filename
	name := strings.ReplaceAll(m.Name, "/", "-")
	name = strings.ReplaceAll(m.Name, "\\", "-")
	return name + ".md"
}

// MilestoneFileNode represents a single milestone file (read-write)
type MilestoneFileNode struct {
	BaseNode
	milestone api.ProjectMilestone
	projectID string

	mu           sync.Mutex
	content      []byte
	contentReady bool
	dirty        bool
}

var _ fs.NodeGetattrer = (*MilestoneFileNode)(nil)
var _ fs.NodeOpener = (*MilestoneFileNode)(nil)
var _ fs.NodeReader = (*MilestoneFileNode)(nil)
var _ fs.NodeWriter = (*MilestoneFileNode)(nil)
var _ fs.NodeFlusher = (*MilestoneFileNode)(nil)
var _ fs.NodeFsyncer = (*MilestoneFileNode)(nil)
var _ fs.NodeSetattrer = (*MilestoneFileNode)(nil)

func (n *MilestoneFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	out.Mode = 0644
	n.SetOwner(out)
	out.Size = uint64(len(n.content))
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *MilestoneFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *MilestoneFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
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

func (n *MilestoneFileNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write milestone %s: offset=%d len=%d", n.milestone.ID, off, len(data))
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

func (n *MilestoneFileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if sz, ok := in.GetSize(); ok {
		if n.lfs.debug {
			log.Printf("Setattr truncate milestone %s: size=%d", n.milestone.ID, sz)
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

func (n *MilestoneFileNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.dirty || n.content == nil {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Parse the markdown and get update fields
	input, err := marshal.MarkdownToMilestoneUpdate(n.content, &n.milestone)
	if err != nil {
		log.Printf("Failed to parse milestone: %v", err)
		return syscall.EIO
	}

	// Validate input
	if err := marshal.ValidateMilestoneUpdate(input); err != nil {
		log.Printf("Milestone validation failed: %v", err)
		return syscall.EINVAL
	}

	// Check if there are any changes
	if input.Name == nil && input.Description == nil && input.TargetDate == nil && input.SortOrder == nil {
		if n.lfs.debug {
			log.Printf("Flush milestone %s: no changes", n.milestone.ID)
		}
		n.dirty = false
		return 0
	}

	if n.lfs.debug {
		log.Printf("Updating milestone %s", n.milestone.ID)
	}

	updated, err := n.lfs.UpdateProjectMilestone(ctx, n.milestone.ID, input)
	if err != nil {
		log.Printf("Failed to update milestone: %v", err)
		return syscall.EIO
	}

	// Update local data with API response
	if updated != nil {
		n.milestone = *updated
		// Regenerate content from updated milestone
		newContent, err := marshal.MilestoneToMarkdown(updated)
		if err == nil {
			n.content = newContent
		}
	}

	// Invalidate kernel cache for this milestone file
	n.lfs.InvalidateKernelInode(milestoneIno(n.milestone.ID))

	n.dirty = false

	if n.lfs.debug {
		log.Printf("Milestone updated successfully")
	}

	return 0
}

func (n *MilestoneFileNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

// NewMilestoneNode handles creating new milestones
type NewMilestoneNode struct {
	BaseNode
	projectID string

	mu      sync.Mutex
	content []byte
	created bool
}

var _ fs.NodeGetattrer = (*NewMilestoneNode)(nil)
var _ fs.NodeOpener = (*NewMilestoneNode)(nil)
var _ fs.NodeReader = (*NewMilestoneNode)(nil)
var _ fs.NodeWriter = (*NewMilestoneNode)(nil)
var _ fs.NodeFlusher = (*NewMilestoneNode)(nil)
var _ fs.NodeFsyncer = (*NewMilestoneNode)(nil)
var _ fs.NodeSetattrer = (*NewMilestoneNode)(nil)

func (n *NewMilestoneNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	out.Mode = 0200
	n.SetOwner(out)
	out.Size = uint64(len(n.content))
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *NewMilestoneNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewMilestoneNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// _create is write-only
	return nil, syscall.EACCES
}

func (n *NewMilestoneNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.lfs.debug {
		log.Printf("Write new milestone: offset=%d len=%d", off, len(data))
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

func (n *NewMilestoneNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
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

func (n *NewMilestoneNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.created || len(n.content) == 0 {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Parse the new milestone content
	name, description := marshal.ParseNewMilestone(n.content)

	if name == "" {
		log.Printf("New milestone has no name")
		return syscall.EINVAL
	}

	if n.lfs.debug {
		log.Printf("Creating milestone: name=%s", name)
	}

	_, err := n.lfs.CreateProjectMilestone(ctx, n.projectID, name, description)
	if err != nil {
		log.Printf("Failed to create milestone: %v", err)
		return syscall.EIO
	}

	n.created = true

	// Invalidate kernel cache for milestones directory
	n.lfs.InvalidateKernelInode(milestonesDirIno(n.projectID))
	n.lfs.InvalidateKernelEntry(milestonesDirIno(n.projectID), "_create")

	if n.lfs.debug {
		log.Printf("Milestone created successfully")
	}

	return 0
}

func (n *NewMilestoneNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}
