package fs

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// CyclesNode represents the /teams/{KEY}/cycles directory
type CyclesNode struct {
	fs.Inode
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeReaddirer = (*CyclesNode)(nil)
var _ fs.NodeLookuper = (*CyclesNode)(nil)

func (c *CyclesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	cycles, err := c.lfs.GetTeamCycles(ctx, c.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(cycles))
	for i, cycle := range cycles {
		entries[i] = fuse.DirEntry{
			Name: fmt.Sprintf("%d.md", cycle.Number),
			Mode: syscall.S_IFREG,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (c *CyclesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Parse cycle number from filename (e.g., "70.md" -> 70)
	if !strings.HasSuffix(name, ".md") {
		return nil, syscall.ENOENT
	}
	numStr := strings.TrimSuffix(name, ".md")
	cycleNum, err := strconv.Atoi(numStr)
	if err != nil {
		return nil, syscall.ENOENT
	}

	cycles, err := c.lfs.GetTeamCycles(ctx, c.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, cycle := range cycles {
		if cycle.Number == cycleNum {
			node := &CycleNode{team: c.team, cycle: cycle}
			content := node.generateContent()
			out.Attr.Mode = 0444 | syscall.S_IFREG
			out.Attr.Size = uint64(len(content))
			out.Attr.SetTimes(&cycle.EndsAt, &cycle.StartsAt, &cycle.StartsAt)
			return c.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
		}
	}

	return nil, syscall.ENOENT
}

// CycleNode represents an individual cycle file (e.g., /teams/ENG/cycles/70.md)
type CycleNode struct {
	fs.Inode
	team  api.Team
	cycle api.Cycle
}

var _ fs.NodeGetattrer = (*CycleNode)(nil)
var _ fs.NodeOpener = (*CycleNode)(nil)
var _ fs.NodeReader = (*CycleNode)(nil)

func (c *CycleNode) generateContent() []byte {
	// Determine if current cycle
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

	// Build progress history as YAML array
	var progressHistory string
	histLen := len(c.cycle.CompletedIssueCountHistory)
	if histLen > 0 && histLen == len(c.cycle.IssueCountHistory) {
		progressHistory = "\nprogressHistory:\n"
		for i := 0; i < histLen; i++ {
			progressHistory += fmt.Sprintf("  - completed: %d\n    total: %d\n",
				c.cycle.CompletedIssueCountHistory[i],
				c.cycle.IssueCountHistory[i])
		}
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
  percentage: %.1f%s
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
		progressHistory,
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

func (c *CycleNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := c.generateContent()
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(content))
	out.Attr.SetTimes(&c.cycle.EndsAt, &c.cycle.StartsAt, &c.cycle.StartsAt)
	return 0
}

func (c *CycleNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (c *CycleNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
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
