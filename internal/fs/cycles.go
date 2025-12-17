package fs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// cycleDirName returns the directory name for a cycle (name with spaces as hyphens)
func cycleDirName(cycle api.Cycle) string {
	name := cycle.Name
	if name == "" {
		name = fmt.Sprintf("Cycle %d", cycle.Number)
	}
	return strings.ReplaceAll(name, " ", "-")
}

// CyclesNode represents the /teams/{KEY}/cycles directory
type CyclesNode struct {
	fs.Inode
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeReaddirer = (*CyclesNode)(nil)
var _ fs.NodeLookuper = (*CyclesNode)(nil)
var _ fs.NodeGetattrer = (*CyclesNode)(nil)

func (c *CyclesNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	return 0
}

func (c *CyclesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	cycles, err := c.lfs.GetTeamCycles(ctx, c.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Start with cycle directories
	entries := make([]fuse.DirEntry, 0, len(cycles)+1)
	var hasCurrent bool
	for _, cycle := range cycles {
		entries = append(entries, fuse.DirEntry{
			Name: cycleDirName(cycle),
			Mode: syscall.S_IFDIR,
		})
		if isCurrent(cycle) {
			hasCurrent = true
		}
	}

	// Add "current" symlink if there's an active cycle
	if hasCurrent {
		entries = append(entries, fuse.DirEntry{
			Name: "current",
			Mode: syscall.S_IFLNK,
		})
	}

	return fs.NewListDirStream(entries), 0
}

func (c *CyclesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	cycles, err := c.lfs.GetTeamCycles(ctx, c.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Handle "current" symlink
	if name == "current" {
		for _, cycle := range cycles {
			if isCurrent(cycle) {
				target := cycleDirName(cycle)
				node := &CurrentCycleSymlink{target: target}
				out.Attr.Mode = 0777 | syscall.S_IFLNK
				out.Attr.Size = uint64(len(target))
				return c.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
			}
		}
		return nil, syscall.ENOENT
	}

	// Match by cycle directory name
	for _, cycle := range cycles {
		if cycleDirName(cycle) == name {
			node := &CycleDirNode{lfs: c.lfs, team: c.team, cycle: cycle}
			out.Attr.Mode = 0755 | syscall.S_IFDIR
			out.Attr.SetTimes(&cycle.EndsAt, &cycle.StartsAt, &cycle.StartsAt)
			return c.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	return nil, syscall.ENOENT
}

// CurrentCycleSymlink represents the /teams/{KEY}/cycles/current symlink
type CurrentCycleSymlink struct {
	fs.Inode
	target string
}

var _ fs.NodeReadlinker = (*CurrentCycleSymlink)(nil)
var _ fs.NodeGetattrer = (*CurrentCycleSymlink)(nil)

func (s *CurrentCycleSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	return []byte(s.target), 0
}

func (s *CurrentCycleSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0777 | syscall.S_IFLNK
	out.Size = uint64(len(s.target))
	return 0
}

// CycleDirNode represents a cycle directory (e.g., /teams/ENG/cycles/71/)
type CycleDirNode struct {
	fs.Inode
	lfs   *LinearFS
	team  api.Team
	cycle api.Cycle
}

var _ fs.NodeReaddirer = (*CycleDirNode)(nil)
var _ fs.NodeLookuper = (*CycleDirNode)(nil)
var _ fs.NodeGetattrer = (*CycleDirNode)(nil)

func (c *CycleDirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	out.Attr.SetTimes(&c.cycle.EndsAt, &c.cycle.StartsAt, &c.cycle.StartsAt)
	return 0
}

func (c *CycleDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := c.lfs.GetCycleIssues(ctx, c.cycle.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// cycle.md + issue symlinks
	entries := make([]fuse.DirEntry, 0, len(issues)+1)
	entries = append(entries, fuse.DirEntry{
		Name: "cycle.md",
		Mode: syscall.S_IFREG,
	})

	for _, issue := range issues {
		entries = append(entries, fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFLNK, // Symlink to issue directory
		})
	}

	return fs.NewListDirStream(entries), 0
}

func (c *CycleDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle cycle.md
	if name == "cycle.md" {
		node := &CycleFileNode{team: c.team, cycle: c.cycle}
		content := node.generateContent()
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		out.Attr.SetTimes(&c.cycle.EndsAt, &c.cycle.StartsAt, &c.cycle.StartsAt)
		return c.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

	// Handle issue symlinks (e.g., "ENG-123")
	issues, err := c.lfs.GetCycleIssues(ctx, c.cycle.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			node := &CycleIssueSymlink{
				identifier: issue.Identifier,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			return c.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}

	return nil, syscall.ENOENT
}

// CycleIssueSymlink represents a symlink from cycle to issue directory
type CycleIssueSymlink struct {
	fs.Inode
	identifier string
}

var _ fs.NodeReadlinker = (*CycleIssueSymlink)(nil)
var _ fs.NodeGetattrer = (*CycleIssueSymlink)(nil)

func (s *CycleIssueSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	// Path from /teams/ENG/cycles/Cycle-22/ENG-123 to /teams/ENG/issues/ENG-123/
	target := fmt.Sprintf("../../issues/%s", s.identifier)
	return []byte(target), 0
}

func (s *CycleIssueSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	target := fmt.Sprintf("../../issues/%s", s.identifier)
	out.Mode = 0777 | syscall.S_IFLNK
	out.Size = uint64(len(target))
	return 0
}

// CycleFileNode represents the cycle.md file inside a cycle directory
type CycleFileNode struct {
	fs.Inode
	team  api.Team
	cycle api.Cycle
}

var _ fs.NodeGetattrer = (*CycleFileNode)(nil)
var _ fs.NodeOpener = (*CycleFileNode)(nil)
var _ fs.NodeReader = (*CycleFileNode)(nil)

func (c *CycleFileNode) generateContent() []byte {
	now := time.Now()
	isCurrent := now.After(c.cycle.StartsAt) && now.Before(c.cycle.EndsAt)

	// Calculate progress from history arrays
	var completed, total int
	if len(c.cycle.CompletedIssueCountHistory) > 0 {
		completed = c.cycle.CompletedIssueCountHistory[len(c.cycle.CompletedIssueCountHistory)-1]
	}
	if len(c.cycle.IssueCountHistory) > 0 {
		total = c.cycle.IssueCountHistory[len(c.cycle.IssueCountHistory)-1]
	}

	var percentage float64
	if total > 0 {
		percentage = float64(completed) / float64(total) * 100
	}

	status := "upcoming"
	if isCurrent {
		status = "current"
	} else if now.After(c.cycle.EndsAt) {
		status = "completed"
	}

	cycleName := c.cycle.Name
	if cycleName == "" {
		cycleName = fmt.Sprintf("Cycle %d", c.cycle.Number)
	}

	content := fmt.Sprintf(`---
id: %s
number: %d
name: %s
team: %s
startsAt: %q
endsAt: %q
status: %s
progress:
  completed: %d
  total: %d
  percentage: %.1f
---

# %s

- **Duration:** %s - %s
- **Progress:** %d/%d issues (%.1f%%)
- **Status:** %s
`,
		c.cycle.ID,
		c.cycle.Number,
		cycleName,
		c.team.Key,
		c.cycle.StartsAt.Format(time.RFC3339),
		c.cycle.EndsAt.Format(time.RFC3339),
		status,
		completed,
		total,
		percentage,
		cycleName,
		c.cycle.StartsAt.Format("Jan 2, 2006"),
		c.cycle.EndsAt.Format("Jan 2, 2006"),
		completed,
		total,
		percentage,
		status,
	)
	return []byte(content)
}

func (c *CycleFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := c.generateContent()
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(content))
	out.Attr.SetTimes(&c.cycle.EndsAt, &c.cycle.StartsAt, &c.cycle.StartsAt)
	return 0
}

func (c *CycleFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (c *CycleFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := c.generateContent()
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}

// isCurrent checks if a cycle is the current active cycle
func isCurrent(cycle api.Cycle) bool {
	now := time.Now()
	return now.After(cycle.StartsAt) && now.Before(cycle.EndsAt)
}

// sortCyclesByNumber returns cycles sorted by number descending (most recent first)
func sortCyclesByNumber(cycles []api.Cycle) []api.Cycle {
	sorted := make([]api.Cycle, len(cycles))
	copy(sorted, cycles)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Number > sorted[j].Number
	})
	return sorted
}
