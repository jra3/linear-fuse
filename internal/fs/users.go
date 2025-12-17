package fs

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"time"

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
			now := time.Now()
			out.Attr.Mode = 0755 | syscall.S_IFDIR
			out.Attr.SetTimes(&now, &now, &now)
			node := &UserNode{lfs: u.lfs, user: user}
			return u.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	return nil, syscall.ENOENT
}

// userDirName returns the directory name for a user (email without domain)
func userDirName(user api.User) string {
	// Use DisplayName as directory name (this is the user's handle)
	if user.DisplayName != "" {
		return user.DisplayName
	}
	// Fallback to email local part if DisplayName not set
	if idx := strings.Index(user.Email, "@"); idx != -1 {
		return user.Email[:idx]
	}
	return user.Email
}

// UserNode represents a single user's directory (e.g., /users/alice)
type UserNode struct {
	fs.Inode
	lfs  *LinearFS
	user api.User
}

var _ fs.NodeReaddirer = (*UserNode)(nil)
var _ fs.NodeLookuper = (*UserNode)(nil)
var _ fs.NodeGetattrer = (*UserNode)(nil)

func (u *UserNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	return 0
}

func (u *UserNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := u.lfs.GetUserIssues(ctx, u.user.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// +1 for .user.md
	entries := make([]fuse.DirEntry, len(issues)+1)
	entries[0] = fuse.DirEntry{
		Name: ".user.md",
		Mode: syscall.S_IFREG,
	}
	for i, issue := range issues {
		entries[i+1] = fuse.DirEntry{
			Name: issue.Identifier + ".md",
			Mode: syscall.S_IFLNK, // Symlink
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (u *UserNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle .user.md metadata file
	if name == ".user.md" {
		node := &UserInfoNode{user: u.user}
		content := node.generateContent()
		out.Attr.Mode = 0444 | syscall.S_IFREG
		out.Attr.Size = uint64(len(content))
		return u.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

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

// IssueSymlink is a symlink pointing to an issue in /teams/<KEY>/issues/<identifier>.md
type IssueSymlink struct {
	fs.Inode
	teamKey    string
	identifier string
}

var _ fs.NodeReadlinker = (*IssueSymlink)(nil)
var _ fs.NodeGetattrer = (*IssueSymlink)(nil)

func (s *IssueSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	// Return relative path to team issues directory
	target := fmt.Sprintf("../../teams/%s/issues/%s/issue.md", s.teamKey, s.identifier)
	return []byte(target), 0
}

func (s *IssueSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0777 | syscall.S_IFLNK
	target := fmt.Sprintf("../../teams/%s/issues/%s/issue.md", s.teamKey, s.identifier)
	out.Size = uint64(len(target))
	return 0
}

// IssueDirSymlink is a symlink pointing to an issue directory in /teams/<KEY>/issues/<identifier>/
type IssueDirSymlink struct {
	fs.Inode
	teamKey    string
	identifier string
}

var _ fs.NodeReadlinker = (*IssueDirSymlink)(nil)
var _ fs.NodeGetattrer = (*IssueDirSymlink)(nil)

func (s *IssueDirSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	target := fmt.Sprintf("../../teams/%s/issues/%s", s.teamKey, s.identifier)
	return []byte(target), 0
}

func (s *IssueDirSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0777 | syscall.S_IFLNK
	target := fmt.Sprintf("../../teams/%s/issues/%s", s.teamKey, s.identifier)
	out.Size = uint64(len(target))
	return 0
}

// UserInfoNode is a virtual file containing user metadata
type UserInfoNode struct {
	fs.Inode
	user api.User
}

var _ fs.NodeGetattrer = (*UserInfoNode)(nil)
var _ fs.NodeOpener = (*UserInfoNode)(nil)
var _ fs.NodeReader = (*UserInfoNode)(nil)

func (u *UserInfoNode) generateContent() []byte {
	status := "active"
	if !u.user.Active {
		status = "inactive"
	}

	content := fmt.Sprintf(`---
id: %s
name: %s
email: %s
displayName: %s
status: %s
---

# %s

- **Email:** %s
- **ID:** %s
- **Status:** %s
`,
		u.user.ID,
		u.user.Name,
		u.user.Email,
		u.user.DisplayName,
		status,
		u.user.Name,
		u.user.Email,
		u.user.ID,
		status,
	)
	return []byte(content)
}

func (u *UserInfoNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := u.generateContent()
	out.Mode = 0444 | syscall.S_IFREG
	out.Size = uint64(len(content))
	return 0
}

func (u *UserInfoNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (u *UserInfoNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := u.generateContent()
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}
