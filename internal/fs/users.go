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

// UsersNode represents the /users directory. Stateless container: zero times
// (honest unknown), no refresh needs; Getattr comes from the attrNode mixin.
type UsersNode struct {
	attrNode
}

var _ fs.NodeReaddirer = (*UsersNode)(nil)
var _ fs.NodeLookuper = (*UsersNode)(nil)
var _ fs.NodeGetattrer = (*UsersNode)(nil)

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
			// api.User carries no time fields; the dir honestly reports zero
			// (unknown) rather than a fabricated now().
			node := &UserNode{attrNode: attrNode{BaseNode: BaseNode{lfs: u.lfs}}, user: user}
			return u.newDirInode(ctx, out, name, node, dirAttr(time.Time{}, time.Time{}), userDirIno(user.ID), inheritTimeout), 0
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

// UserNode represents a single user's directory (e.g., /users/alice).
// api.User has no time fields, so the dir reports zero times, but it still
// carries a user snapshot (user.md renders from it) — so it implements the
// nodeRefresher seam like the other snapshot carriers.
type UserNode struct {
	attrNode
	user api.User
}

var _ fs.NodeReaddirer = (*UserNode)(nil)
var _ fs.NodeLookuper = (*UserNode)(nil)
var _ fs.NodeGetattrer = (*UserNode)(nil)

// entity/setEntity snapshot and swap the directory's user under the node's
// volatile-state lock; setEntity is written by the nodeRefresher seam
// (refresh.go).
func (u *UserNode) entity() api.User {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	return u.user
}

func (u *UserNode) setEntity(user api.User) {
	u.stateMu.Lock()
	u.user = user
	u.stateMu.Unlock()
}

func (u *UserNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*UserNode); ok {
		u.setEntity(f.user)
	}
}

func (u *UserNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := u.lfs.GetUserIssues(ctx, u.entity().ID)
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
	user := u.entity() // snapshot captured by the closures below
	// Handle user.md metadata file. api.User carries no created/updated times,
	// so the file honestly reports zero (unknown) rather than a fabricated now().
	if name == "user.md" {
		return u.lookupRenderFile(ctx, out, "user.md", func(context.Context) ([]byte, time.Time, time.Time) {
			return userMarkdown(user), time.Time{}, time.Time{}
		}, 0, inheritTimeout), 0
	}

	issues, err := u.lfs.GetUserIssues(ctx, user.ID)
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

// userMarkdown renders the user.md content for a user. Frontmatter goes
// through renderWithFrontmatter so hostile display names stay valid YAML.
func userMarkdown(user api.User) []byte {
	status := "active"
	if !user.Active {
		status = "inactive"
	}

	fm := map[string]any{
		"id":          user.ID,
		"name":        user.Name,
		"email":       user.Email,
		"displayName": user.DisplayName,
		"status":      status,
	}
	body := fmt.Sprintf(`
# %s

- **Email:** %s
- **ID:** %s
- **Status:** %s
`, user.Name, user.Email, user.ID, status)
	return renderWithFrontmatter(fm, body)
}
