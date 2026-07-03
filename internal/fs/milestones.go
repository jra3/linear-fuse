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
		// On error, return just _create and .error
		return fs.NewListDirStream([]fuse.DirEntry{
			{Name: "_create", Mode: syscall.S_IFREG},
			{Name: ".error", Mode: syscall.S_IFREG},
		}), 0
	}

	// Always include _create for creating milestones and .error for feedback
	entries := []fuse.DirEntry{
		{Name: "_create", Mode: syscall.S_IFREG},
		{Name: ".error", Mode: syscall.S_IFREG},
		{Name: ".last", Mode: syscall.S_IFREG},
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
		node := newCreateFile(n.lfs, n.createMilestone)
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

	// Handle .error feedback file (last failed milestone write in this dir)
	if name == ".error" {
		return n.lfs.lookupErrorFile(ctx, n, collectionErrorKey("milestones", n.projectID), out), 0
	}
	// Handle .last feedback file (recent successful milestone creations)
	if name == ".last" {
		return n.lfs.lookupSuccessFile(ctx, n, collectionSuccessKey("milestones", n.projectID), out), 0
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

	return commitDelete(ctx, n.lfs, deleteSpec[api.ProjectMilestone]{
		op:  `delete milestone "` + name + `"`,
		key: collectionErrorKey("milestones", n.projectID),
		find: func(ctx context.Context) (*api.ProjectMilestone, error) {
			milestones, err := n.lfs.GetProjectMilestones(ctx, n.projectID)
			if err != nil {
				return nil, err
			}
			for _, m := range milestones {
				if milestoneFilename(m) == name {
					return &m, nil
				}
			}
			return nil, nil
		},
		mutate: func(ctx context.Context, m *api.ProjectMilestone) error {
			return n.lfs.mutator().DeleteProjectMilestone(ctx, m.ID)
		},
		forget: func(ctx context.Context, m *api.ProjectMilestone) error {
			return n.lfs.store.Queries().DeleteProjectMilestone(ctx, m.ID)
		},
		dir:  milestonesDirIno(n.projectID),
		name: name,
	})
}

// milestoneFilename returns the filename for a milestone
func milestoneFilename(m api.ProjectMilestone) string {
	// Sanitize name for filename
	name := strings.ReplaceAll(m.Name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
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

	milestoneErrKey := collectionErrorKey("milestones", n.projectID)

	// Parse the markdown and get update fields
	input, err := marshal.MarkdownToMilestoneUpdate(n.content, &n.milestone)
	if err != nil {
		log.Printf("Failed to parse milestone: %v", err)
		n.lfs.SetWriteError(milestoneErrKey, "Operation: update milestone "+milestoneFilename(n.milestone)+"\nParse error: "+err.Error())
		return syscall.EINVAL
	}

	// Validate input
	if err := marshal.ValidateMilestoneUpdate(input); err != nil {
		log.Printf("Milestone validation failed: %v", err)
		n.lfs.SetWriteError(milestoneErrKey, "Operation: update milestone "+milestoneFilename(n.milestone)+"\nValidation error: "+err.Error())
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
		n.lfs.SetWriteError(milestoneErrKey, "Operation: update milestone "+milestoneFilename(n.milestone)+"\nError: "+err.Error())
		return syscall.EIO
	}
	// Edit-commit tail. LinearFS.UpdateProjectMilestone already upserted to SQLite
	// (after routing through the mutation seam), so persist is nil; verify
	// read-your-writes against the API's echoed response (milestones have no
	// single-entity getter) and surface divergence via .error.
	fresh, errno := commitWriteBack(ctx, n.lfs, writeBackSpec[api.ProjectMilestone]{
		errKey:  milestoneErrKey,
		fetch:   func(ctx context.Context) (*api.ProjectMilestone, error) { return updated, nil },
		persist: nil,
		compare: func(fresh *api.ProjectMilestone) []writeBackResult {
			var results []writeBackResult
			if input.Name != nil {
				results = append(results, writeBackDivergence("name", *input.Name, fresh.Name, n.milestone.Name))
			}
			if input.Description != nil {
				results = append(results, writeBackDivergence("description", *input.Description, fresh.Description, n.milestone.Description))
			}
			return results
		},
	})

	// Update local data with the fresh value (reflects reality, even on divergence)
	if fresh != nil {
		n.milestone = *fresh
		if newContent, err := marshal.MilestoneToMarkdown(fresh); err == nil {
			n.content = newContent
		}
	}

	// Invalidate kernel cache for this milestone file
	n.lfs.InvalidateUpdated(milestoneIno(n.milestone.ID))

	n.dirty = false
	return errno
}

func (n *MilestoneFileNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	return 0
}

// createMilestone is the milestones create surface's onFlush: parse the
// frontmatter and run the create tail.
func (n *MilestonesNode) createMilestone(ctx context.Context, content []byte) syscall.Errno {
	_, errno := commitCreate(ctx, n.lfs, createSpec[api.ProjectMilestone]{
		op:  "create milestone",
		key: collectionErrorKey("milestones", n.projectID),
		mutate: func(ctx context.Context) (*api.ProjectMilestone, error) {
			name, description := marshal.ParseNewMilestone(content)
			if name == "" {
				return nil, &FieldError{Field: "name", Message: "milestone has no name. Add a 'name:' field to the frontmatter."}
			}
			return n.lfs.mutator().CreateProjectMilestone(ctx, n.projectID, name, description)
		},
		result: func(m *api.ProjectMilestone) WriteResult {
			return WriteResult{
				Path:  milestoneFilename(*m),
				Title: m.Name,
			}
		},
		persist: func(ctx context.Context, m *api.ProjectMilestone) error {
			return n.lfs.UpsertProjectMilestone(ctx, n.projectID, *m)
		},
		dir:       milestonesDirIno(n.projectID),
		entryName: func(m *api.ProjectMilestone) string { return milestoneFilename(*m) },
	})
	return errno
}
