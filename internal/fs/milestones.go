package fs

import (
	"context"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// MilestonesNode represents a milestones/ directory within a project
type MilestonesNode struct {
	attrNode
	projectID string
}

var _ fs.NodeReaddirer = (*MilestonesNode)(nil)
var _ fs.NodeLookuper = (*MilestonesNode)(nil)
var _ fs.NodeUnlinker = (*MilestonesNode)(nil)
var _ fs.NodeGetattrer = (*MilestonesNode)(nil)

func (n *MilestonesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	milestones, err := n.lfs.GetProjectMilestones(ctx, n.projectID)
	if err != nil {
		// On error, still serve the trio
		return fs.NewListDirStream(n.trio().entries()), 0
	}

	entries := append(n.trio().entries(), n.listing(milestones).entries()...)
	return fs.NewListDirStream(entries), 0
}

// trio declares the milestones collection's writable surfaces.
func (n *MilestonesNode) trio() collectionTrio {
	return collectionTrio{kind: "milestones", parentID: n.projectID, onFlush: n.createMilestone}
}

// listing declares the milestones collection's item files: one per milestone,
// named by milestoneFilename. Backs Readdir/Lookup/Unlink so they derive and
// match names through one place. See namedListing.
func (n *MilestonesNode) listing(ms []api.ProjectMilestone) namedListing[api.ProjectMilestone] {
	return namedListing[api.ProjectMilestone]{items: ms, nameOf: milestoneFilename}
}

func (n *MilestonesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
	}

	milestones, err := n.lfs.GetProjectMilestones(ctx, n.projectID)
	if err != nil {
		return nil, syscall.EIO
	}

	m, ok := n.listing(milestones).find(name)
	if !ok {
		return nil, syscall.ENOENT
	}

	content, err := marshal.MilestoneToMarkdown(&m)
	if err != nil {
		log.Printf("Failed to marshal milestone: %v", err)
		return nil, syscall.EIO
	}
	node := &MilestoneFileNode{
		BaseNode:   BaseNode{lfs: n.lfs},
		milestone:  m,
		projectID:  n.projectID,
		editBuffer: editBuffer{content: content},
	}
	now := time.Now()
	out.Attr.Mode = 0644 | syscall.S_IFREG
	out.Attr.Uid = n.lfs.uid
	out.Attr.Gid = n.lfs.gid
	out.Attr.Size = uint64(len(content))
	out.SetAttrTimeout(5 * time.Second)
	out.SetEntryTimeout(5 * time.Second)
	out.Attr.SetTimes(&now, &now, &now)
	// The bridge dedups AFTER this handler returns: push the fresh
	// milestone/content into the node it will keep (see refresh.go).
	refreshExisting(n, name, node)
	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  milestoneIno(m.ID),
	}), 0
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
			if m, ok := n.listing(milestones).find(name); ok {
				return &m, nil
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
	editBuffer
	milestone api.ProjectMilestone
	projectID string
}

var _ fs.NodeGetattrer = (*MilestoneFileNode)(nil)
var _ fs.NodeOpener = (*MilestoneFileNode)(nil)
var _ fs.NodeReader = (*MilestoneFileNode)(nil)
var _ fs.NodeWriter = (*MilestoneFileNode)(nil)
var _ fs.NodeFlusher = (*MilestoneFileNode)(nil)
var _ fs.NodeFsyncer = (*MilestoneFileNode)(nil)
var _ fs.NodeSetattrer = (*MilestoneFileNode)(nil)

func (n *MilestoneFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	// api.ProjectMilestone carries no timestamps, so there is nothing but now().
	now := time.Now()
	fileAttr(n.size(), now, now).fill(&out.Attr, &n.BaseNode)
	return 0
}

// refreshFrom adopts a fresh twin's milestone and rendered content unless an
// edit is in flight — the dirty buffer always wins (refresh.go).
func (n *MilestoneFileNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*MilestoneFileNode); ok {
		n.refresh(f.content, func() { n.milestone, n.projectID = f.milestone, f.projectID })
	}
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
		msg, errno := classifyMutationErr("update milestone "+milestoneFilename(n.milestone), err)
		n.lfs.SetWriteError(milestoneErrKey, msg)
		return errno
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
