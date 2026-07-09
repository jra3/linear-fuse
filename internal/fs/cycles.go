package fs

import (
	"context"
	"fmt"
	"math"
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

// CyclesNode represents the /teams/{KEY}/cycles directory. It holds a team
// snapshot and reports the team's times; Getattr comes from the attrNode mixin.
type CyclesNode struct {
	attrNode
	team api.Team
}

var _ fs.NodeReaddirer = (*CyclesNode)(nil)
var _ fs.NodeLookuper = (*CyclesNode)(nil)
var _ fs.NodeGetattrer = (*CyclesNode)(nil)

// entity/setEntity snapshot and swap the directory's team under the node's
// volatile-state lock; setEntity is written by the nodeRefresher seam
// (refresh.go).
func (c *CyclesNode) entity() api.Team {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.team
}

func (c *CyclesNode) setEntity(team api.Team) {
	c.stateMu.Lock()
	c.team = team
	c.stateMu.Unlock()
}

func (c *CyclesNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*CyclesNode); ok {
		c.setEntity(f.team)
	}
}

func (c *CyclesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	cycles, err := c.lfs.repo.GetTeamCycles(ctx, c.entity().ID)
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
	team := c.entity()
	cycles, err := c.lfs.repo.GetTeamCycles(ctx, team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Handle "current" symlink
	if name == "current" {
		for _, cycle := range cycles {
			if isCurrent(cycle) {
				// atime=EndsAt matches the target CycleDirNode's convention.
				return c.newSymlinkInodeAtime(ctx, out, cycleDirName(cycle), cycle.StartsAt, cycle.StartsAt, cycle.EndsAt), 0
			}
		}
		return nil, syscall.ENOENT
	}

	// Match by cycle directory name
	for _, cycle := range cycles {
		if cycleDirName(cycle) == name {
			node := &CycleDirNode{attrNode: attrNode{BaseNode: BaseNode{lfs: c.lfs}}, team: team, cycle: cycle}
			// api.Cycle has no created/updated fields; the cycle tier's
			// convention is mtime/ctime=StartsAt with atime=EndsAt (which the
			// "current" symlink mirrors) — never now().
			na := nodeAttr{mode: 0755 | syscall.S_IFDIR, created: cycle.StartsAt, updated: cycle.StartsAt, atime: cycle.EndsAt}
			return c.newDirInode(ctx, out, name, node, na, cycleDirIno(cycle.ID), inheritTimeout), 0
		}
	}

	return nil, syscall.ENOENT
}

// CycleDirNode represents a cycle directory (e.g., /teams/ENG/cycles/71/)
type CycleDirNode struct {
	attrNode
	team  api.Team
	cycle api.Cycle
}

var _ fs.NodeReaddirer = (*CycleDirNode)(nil)
var _ fs.NodeLookuper = (*CycleDirNode)(nil)
var _ fs.NodeGetattrer = (*CycleDirNode)(nil)

// entity/setEntity snapshot and swap the directory's team+cycle under the
// node's volatile-state lock; setEntity is written by the nodeRefresher seam
// (refresh.go).
func (c *CycleDirNode) entity() (api.Team, api.Cycle) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.team, c.cycle
}

func (c *CycleDirNode) setEntity(team api.Team, cycle api.Cycle) {
	c.stateMu.Lock()
	c.team, c.cycle = team, cycle
	c.stateMu.Unlock()
}

func (c *CycleDirNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*CycleDirNode); ok {
		c.setEntity(f.team, f.cycle)
	}
}

func (c *CycleDirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	_, cycle := c.entity()
	issues, err := c.lfs.GetCycleIssues(ctx, cycle.ID)
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
	team, cycle := c.entity()
	// Handle cycle.md. A cycle has no updatedAt; report StartsAt as both mtime
	// and ctime (preserving the previous sort order), never now().
	if name == "cycle.md" {
		return c.lookupRenderFile(ctx, out, "cycle.md", func(context.Context) ([]byte, time.Time, time.Time) {
			return cycleMarkdown(team, cycle), cycle.StartsAt, cycle.StartsAt
		}, 0, inheritTimeout), 0
	}

	// Handle issue symlinks (e.g., "ENG-123")
	issues, err := c.lfs.GetCycleIssues(ctx, cycle.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			// Path from /teams/ENG/cycles/Cycle-22/ENG-123 to /teams/ENG/issues/ENG-123/
			target := fmt.Sprintf("../../issues/%s", issue.Identifier)
			return c.newSymlinkInode(ctx, out, target, issue.CreatedAt, issue.UpdatedAt), 0
		}
	}

	return nil, syscall.ENOENT
}

// cycleMarkdown renders the cycle.md content for a cycle. Status/progress are
// computed at render time, so a read reflects the cycle's live state.
func cycleMarkdown(team api.Team, cycle api.Cycle) []byte {
	now := time.Now()
	isCurrent := now.After(cycle.StartsAt) && now.Before(cycle.EndsAt)

	// Calculate progress from history arrays
	var completed, total int
	if len(cycle.CompletedIssueCountHistory) > 0 {
		completed = cycle.CompletedIssueCountHistory[len(cycle.CompletedIssueCountHistory)-1]
	}
	if len(cycle.IssueCountHistory) > 0 {
		total = cycle.IssueCountHistory[len(cycle.IssueCountHistory)-1]
	}

	var percentage float64
	if total > 0 {
		percentage = float64(completed) / float64(total) * 100
	}

	status := "upcoming"
	if isCurrent {
		status = "current"
	} else if now.After(cycle.EndsAt) {
		status = "completed"
	}

	cycleName := cycle.Name
	if cycleName == "" {
		cycleName = fmt.Sprintf("Cycle %d", cycle.Number)
	}

	// Frontmatter goes through renderWithFrontmatter so a hostile cycle name
	// stays valid YAML. percentage keeps the historical one-decimal rounding
	// (yaml.v3 renders an integral float as a bare integer — YAML-equivalent).
	fm := map[string]any{
		"id":       cycle.ID,
		"number":   cycle.Number,
		"name":     cycleName,
		"team":     team.Key,
		"startsAt": cycle.StartsAt.Format(time.RFC3339),
		"endsAt":   cycle.EndsAt.Format(time.RFC3339),
		"status":   status,
		"progress": map[string]any{
			"completed":  completed,
			"total":      total,
			"percentage": math.Round(percentage*10) / 10,
		},
	}
	body := fmt.Sprintf(`
# %s

- **Duration:** %s - %s
- **Progress:** %d/%d issues (%.1f%%)
- **Status:** %s
`,
		cycleName,
		cycle.StartsAt.Format("Jan 2, 2006"),
		cycle.EndsAt.Format("Jan 2, 2006"),
		completed,
		total,
		percentage,
		status,
	)
	return renderWithFrontmatter(fm, body)
}

// isCurrent checks if a cycle is the current active cycle
func isCurrent(cycle api.Cycle) bool {
	now := time.Now()
	return now.After(cycle.StartsAt) && now.Before(cycle.EndsAt)
}
