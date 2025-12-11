package fs

import (
	"context"
	"fmt"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// UsersNode represents the /users directory
type UsersNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*UsersNode)(nil)
var _ fs.NodeLookuper = (*UsersNode)(nil)

func (u *UsersNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	users, err := u.lfs.GetUsers(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(users))
	for i, user := range users {
		entries[i] = fuse.DirEntry{
			Name: userDirName(user),
			Mode: syscall.S_IFDIR,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (u *UsersNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	users, err := u.lfs.GetUsers(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, user := range users {
		if userDirName(user) == name {
			node := &UserNode{lfs: u.lfs, user: user}
			return u.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	return nil, syscall.ENOENT
}

// userDirName returns the directory name for a user (email without domain)
func userDirName(user api.User) string {
	// Use email local part as directory name for uniqueness
	email := user.Email
	for i, c := range email {
		if c == '@' {
			return email[:i]
		}
	}
	// Fallback to full email if no @ found
	return email
}

// UserNode represents a single user's directory (e.g., /users/alice)
type UserNode struct {
	fs.Inode
	lfs  *LinearFS
	user api.User
}

var _ fs.NodeReaddirer = (*UserNode)(nil)
var _ fs.NodeLookuper = (*UserNode)(nil)

func (u *UserNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := u.lfs.GetUserIssues(ctx, u.user.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(issues))
	for i, issue := range issues {
		entries[i] = fuse.DirEntry{
			Name: issue.Identifier + ".md",
			Mode: syscall.S_IFLNK, // Symlink
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (u *UserNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	issues, err := u.lfs.GetUserIssues(ctx, u.user.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier+".md" == name {
			// Create symlink to team directory
			teamKey := ""
			if issue.Team != nil {
				teamKey = issue.Team.Key
			}
			node := &IssueSymlink{
				teamKey:    teamKey,
				identifier: issue.Identifier,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			return u.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}

	return nil, syscall.ENOENT
}

// IssueSymlink is a symlink pointing to an issue in /teams/<KEY>/<identifier>.md
type IssueSymlink struct {
	fs.Inode
	teamKey    string
	identifier string
}

var _ fs.NodeReadlinker = (*IssueSymlink)(nil)
var _ fs.NodeGetattrer = (*IssueSymlink)(nil)

func (s *IssueSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	// Return relative path to team directory
	target := fmt.Sprintf("../../teams/%s/%s.md", s.teamKey, s.identifier)
	return []byte(target), 0
}

func (s *IssueSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0777 | syscall.S_IFLNK
	target := fmt.Sprintf("../../teams/%s/%s.md", s.teamKey, s.identifier)
	out.Size = uint64(len(target))
	return 0
}
