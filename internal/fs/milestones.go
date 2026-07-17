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
	return n.collection().readdir(ctx)
}

// collection is the item-file surface (Readdir/Lookup/Unlink) for milestones/.
// api.ProjectMilestone carries no timestamps, so metaTimes is zero.
func (n *MilestonesNode) collection() collectionDir[api.ProjectMilestone] {
	return collectionDir[api.ProjectMilestone]{
		parent: n,
		lfs:    n.lfs,
		trio:   n.trio(),
		noun:   "milestone",
		fetch: func(ctx context.Context) ([]api.ProjectMilestone, error) {
			return n.lfs.repo.GetProjectMilestones(ctx, n.projectID)
		},
		listing:     func(items []api.ProjectMilestone) collectionListing[api.ProjectMilestone] { return n.listing(items) },
		idOf:        func(m api.ProjectMilestone) string { return m.ID },
		buildFile:   n.buildMilestone,
		metaMarshal: marshal.MilestoneMetaToMarkdown,
		metaTimes:   func(api.ProjectMilestone) (time.Time, time.Time) { return time.Time{}, time.Time{} },
		metaIno:     func(m api.ProjectMilestone) uint64 { return milestoneMetaIno(m.ID) },
		deleteMutate: func(ctx context.Context, m *api.ProjectMilestone) error {
			return n.lfs.mutator().DeleteProjectMilestone(ctx, m.ID)
		},
		deleteForget: func(ctx context.Context, m *api.ProjectMilestone) error {
			return n.lfs.store.Queries().DeleteProjectMilestone(ctx, m.ID)
		},
	}
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
	return n.collection().lookup(ctx, name, out)
}

// buildMilestone mounts the read/write MilestoneFileNode for an existing
// milestone.
func (n *MilestonesNode) buildMilestone(ctx context.Context, name string, m api.ProjectMilestone, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
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
	// api.ProjectMilestone carries no timestamps; use now() as the hand-rolled
	// path did. newFileInode owns the attr fill, timeouts, refresh dedup, and
	// the dirty-size clamp (shared with comments/docs).
	now := time.Now()
	return n.newFileInode(ctx, out, name, node, fileAttr(len(content), now, now), milestoneIno(m.ID), 5*time.Second), 0
}

func (n *MilestonesNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.collection().unlink(ctx, name)
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
	milestoneErrKey := collectionErrorKey("milestones", n.projectID)
	// input + updated bridge the front half to the commit tail.
	var input api.ProjectMilestoneUpdateInput
	var updated *api.ProjectMilestone
	return editFlush(ctx, n.lfs, &n.editBuffer, editFlushSpec[api.ProjectMilestone]{
		mutate: func(ctx context.Context) (bool, syscall.Errno) {
			var err error
			input, err = marshal.MarkdownToMilestoneUpdate(n.content, &n.milestone)
			if err != nil {
				log.Printf("Failed to parse milestone: %v", err)
				n.lfs.SetWriteError(milestoneErrKey, "Operation: update milestone "+milestoneFilename(n.milestone)+"\nParse error: "+err.Error())
				return false, syscall.EINVAL
			}
			if err := marshal.ValidateMilestoneUpdate(input); err != nil {
				log.Printf("Milestone validation failed: %v", err)
				n.lfs.SetWriteError(milestoneErrKey, "Operation: update milestone "+milestoneFilename(n.milestone)+"\nValidation error: "+err.Error())
				return false, syscall.EINVAL
			}
			if input.Name == nil && input.Description == nil && input.TargetDate == nil && input.SortOrder == nil {
				if n.lfs.debug {
					log.Printf("Flush milestone %s: no changes", n.milestone.ID)
				}
				return false, 0
			}
			if n.lfs.debug {
				log.Printf("Updating milestone %s", n.milestone.ID)
			}
			updated, err = n.lfs.mutator().UpdateProjectMilestone(ctx, n.milestone.ID, input)
			if err != nil {
				log.Printf("Failed to update milestone: %v", err)
				msg, errno := classifyMutationErr("update milestone "+milestoneFilename(n.milestone), err)
				n.lfs.SetWriteError(milestoneErrKey, msg)
				return false, errno
			}
			return true, 0
		},
		// Edit-commit tail. Verify read-your-writes against the API's echoed
		// response (milestones have no single-entity getter), then persist —
		// gated like every sibling so a wedged reflection fails loud (EIO) rather
		// than reverting silently (#285). The upsert carries the node's known
		// projectID so it never clobbers the association with a fallible lookup.
		writeBack: writeBackSpec[api.ProjectMilestone]{
			errKey: milestoneErrKey,
			fetch:  func(ctx context.Context) (*api.ProjectMilestone, error) { return updated, nil },
			persist: func(ctx context.Context, fresh *api.ProjectMilestone) error {
				return n.lfs.UpsertProjectMilestone(ctx, n.projectID, *fresh)
			},
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
		},
		// Adopt the fresh value AND re-render the buffer (reflects reality, even
		// on divergence).
		adopt: func(fresh *api.ProjectMilestone) {
			n.milestone = *fresh
			if newContent, err := marshal.MilestoneToMarkdown(fresh); err == nil {
				n.content = newContent
			}
		},
		coherence: []uint64{milestoneIno(n.milestone.ID), milestoneMetaIno(n.milestone.ID)},
	})
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
