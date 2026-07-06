package fs

import (
	"context"
	"fmt"
	"sort"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// recentLimit caps how many issues the recent/ view exposes.
const recentLimit = 50

// RecentNode is teams/{KEY}/recent/: a read-only view listing the team's issues
// as symlinks, newest-first by updatedAt, capped to recentLimit. It gives an
// agent a shell-flag-independent "what changed lately" (ls recent/ | head) that
// doesn't depend on `ls -t` (which failed under eza in the #148 retro).
type RecentNode struct {
	BaseNode
	team api.Team
}

var _ fs.NodeReaddirer = (*RecentNode)(nil)
var _ fs.NodeLookuper = (*RecentNode)(nil)
var _ fs.NodeGetattrer = (*RecentNode)(nil)

func (n *RecentNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0555 | syscall.S_IFDIR // read-only dir
	n.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

// recentIssues returns the team's issues sorted newest-first and capped. SQL
// ORDER BY does not survive as a contract to the fs layer, so we sort here
// explicitly — in one place used by both Readdir and Lookup so `ls` and
// `stat recent/X` agree on membership.
func (n *RecentNode) recentIssues(ctx context.Context) ([]api.Issue, error) {
	issues, err := n.lfs.GetTeamIssues(ctx, n.team.ID)
	if err != nil {
		return nil, err
	}
	// Stable sort with an Identifier tiebreaker: equal updatedAt (common under
	// batch syncs / the test's fixed clock) must not reorder nondeterministically
	// at the recentLimit cutoff, or `ls` and the cap would disagree run-to-run.
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].UpdatedAt.Equal(issues[j].UpdatedAt) {
			return issues[i].Identifier > issues[j].Identifier
		}
		return issues[i].UpdatedAt.After(issues[j].UpdatedAt)
	})
	if len(issues) > recentLimit {
		issues = issues[:recentLimit]
	}
	return issues, nil
}

func (n *RecentNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := n.recentIssues(ctx)
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, len(issues))
	for i, issue := range issues {
		entries[i] = fuse.DirEntry{Name: issue.Identifier, Mode: syscall.S_IFLNK}
	}
	return fs.NewListDirStream(entries), 0
}

func (n *RecentNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Resolve against ALL team issues, not just the capped window: lookup must be
	// a superset of readdir so a name that appeared in `ls recent/` never fails
	// its per-entry stat (the safe direction; the cap lives only in Readdir).
	issues, err := n.lfs.GetTeamIssues(ctx, n.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}
	for _, issue := range issues {
		if issue.Identifier == name {
			target := fmt.Sprintf("../issues/%s", issue.Identifier)
			return n.newSymlinkInode(ctx, out, target, issue.CreatedAt, issue.UpdatedAt), 0
		}
	}
	return nil, syscall.ENOENT
}
