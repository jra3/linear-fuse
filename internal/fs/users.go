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
	BaseNode
}

var _ fs.NodeReaddirer = (*UsersNode)(nil)
var _ fs.NodeLookuper = (*UsersNode)(nil)
var _ fs.NodeGetattrer = (*UsersNode)(nil)

func (u *UsersNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	u.SetOwner(out)
	return 0
}

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
			out.Attr.Uid = u.lfs.uid
			out.Attr.Gid = u.lfs.gid
			out.Attr.SetTimes(&now, &now, &now)
			node := &UserNode{BaseNode: BaseNode{lfs: u.lfs}, user: user}
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
	BaseNode
	user api.User
}

var _ fs.NodeReaddirer = (*UserNode)(nil)
var _ fs.NodeLookuper = (*UserNode)(nil)
var _ fs.NodeGetattrer = (*UserNode)(nil)

func (u *UserNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	u.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (u *UserNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := u.lfs.GetUserIssues(ctx, u.user.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// +1 for user.md
	entries := make([]fuse.DirEntry, len(issues)+1)
	entries[0] = fuse.DirEntry{
		Name: "user.md",
		Mode: syscall.S_IFREG,
	}
	for i, issue := range issues {
		entries[i+1] = fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFLNK, // Symlink to issue directory
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (u *UserNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle user.md metadata file. api.User carries no created/updated times,
	// so the file honestly reports zero (unknown) rather than a fabricated now().
	if name == "user.md" {
		user := u.user
		return u.lookupRenderFile(ctx, out, "user.md", func(context.Context) ([]byte, time.Time, time.Time) {
			return userMarkdown(user), time.Time{}, time.Time{}
		}, 0, inheritTimeout), 0
	}

	issues, err := u.lfs.GetUserIssues(ctx, u.user.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			target, errno := teamIssueTarget(issue)
			if errno != 0 {
				return nil, errno
			}
			return u.newSymlinkInode(ctx, out, target, issue.CreatedAt, issue.UpdatedAt), 0
		}
	}

	return nil, syscall.ENOENT
}

// userMarkdown renders the user.md content for a user.
func userMarkdown(user api.User) []byte {
	status := "active"
	if !user.Active {
		status = "inactive"
	}

	return []byte(fmt.Sprintf(`---
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
		user.ID,
		user.Name,
		user.Email,
		user.DisplayName,
		status,
		user.Name,
		user.Email,
		user.ID,
		status,
	))
}
