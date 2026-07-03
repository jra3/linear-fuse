package fs

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// symlinkNode is the one symlink module behind every symlink view (issue
// symlinks under by/, cycles/, recent/, projects/, users/, my/; project
// symlinks under initiatives/; the cycles/current alias). The relative
// target and entity timestamps are fixed at construction — Readlink and
// Getattr only report them, so a view cannot grow per-call behaviour.
type symlinkNode struct {
	BaseNode
	target    string
	createdAt time.Time
	updatedAt time.Time
	// accessedAt covers the one view whose convention encodes a second date
	// in atime: cycle nodes report atime=EndsAt alongside mtime=StartsAt, and
	// the cycles/current alias must agree with its target directory.
	accessedAt time.Time
}

var _ fs.NodeReadlinker = (*symlinkNode)(nil)
var _ fs.NodeGetattrer = (*symlinkNode)(nil)

func (s *symlinkNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	return []byte(s.target), 0
}

func (s *symlinkNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	s.fillAttr(&out.Attr)
	return 0
}

func (s *symlinkNode) fillAttr(attr *fuse.Attr) {
	attr.Mode = 0777 | syscall.S_IFLNK
	s.setOwnerAttr(attr)
	attr.Size = uint64(len(s.target))
	// A zero time.Time must stay a zero attr (epoch): uint64(Unix()) on the
	// zero value wraps negative into a year-584-billion timestamp that would
	// sort first in `ls -lt`.
	attr.SetTimes(nonZeroTime(s.accessedAt), nonZeroTime(s.updatedAt), nonZeroTime(s.createdAt))
}

// nonZeroTime adapts a possibly-zero time for Attr.SetTimes, which skips nil
// fields.
func nonZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// newSymlinkInode builds the symlink child and fills out.Attr with exactly
// what symlinkNode.Getattr will later report, so a Lookup answer can never
// disagree with a subsequent stat.
func (b *BaseNode) newSymlinkInode(ctx context.Context, out *fuse.EntryOut, target string, createdAt, updatedAt time.Time) *fs.Inode {
	return b.newSymlinkInodeAtime(ctx, out, target, createdAt, updatedAt, updatedAt)
}

// newSymlinkInodeAtime is newSymlinkInode with an explicit atime, for the
// cycle views whose atime carries the cycle end date.
func (b *BaseNode) newSymlinkInodeAtime(ctx context.Context, out *fuse.EntryOut, target string, createdAt, updatedAt, accessedAt time.Time) *fs.Inode {
	node := &symlinkNode{
		BaseNode:   BaseNode{lfs: b.lfs},
		target:     target,
		createdAt:  createdAt,
		updatedAt:  updatedAt,
		accessedAt: accessedAt,
	}
	node.fillAttr(&out.Attr)
	return b.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK})
}

// teamIssueTarget is the relative target for an issue symlink two levels
// below the mount root (my/*, users/{name}). An issue whose team hasn't
// synced is a reference to something that doesn't exist yet -> ENOENT,
// never a dangling "teams//" placeholder.
func teamIssueTarget(issue api.Issue) (string, syscall.Errno) {
	if issue.Team == nil || issue.Team.Key == "" {
		return "", syscall.ENOENT
	}
	return fmt.Sprintf("../../teams/%s/issues/%s", issue.Team.Key, issue.Identifier), 0
}
