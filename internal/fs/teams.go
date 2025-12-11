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

// TeamsNode represents the /teams directory
type TeamsNode struct {
	fs.Inode
	lfs *LinearFS
}

var _ fs.NodeReaddirer = (*TeamsNode)(nil)
var _ fs.NodeLookuper = (*TeamsNode)(nil)

func (t *TeamsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	teams, err := t.lfs.GetTeams(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(teams))
	for i, team := range teams {
		entries[i] = fuse.DirEntry{
			Name: team.Key,
			Mode: syscall.S_IFDIR,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (t *TeamsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	teams, err := t.lfs.GetTeams(ctx)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, team := range teams {
		if team.Key == name {
			node := &TeamNode{lfs: t.lfs, team: team}
			return t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	return nil, syscall.ENOENT
}

// TeamNode represents a single team directory (e.g., /teams/ENG)
type TeamNode struct {
	fs.Inode
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeReaddirer = (*TeamNode)(nil)
var _ fs.NodeLookuper = (*TeamNode)(nil)
var _ fs.NodeCreater = (*TeamNode)(nil)

func (t *TeamNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := t.lfs.GetTeamIssues(ctx, t.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(issues))
	for i, issue := range issues {
		entries[i] = fuse.DirEntry{
			Name: issue.Identifier + ".md",
			Mode: syscall.S_IFREG,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (t *TeamNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	issues, err := t.lfs.GetTeamIssues(ctx, t.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier+".md" == name {
			// Pre-generate content so Getattr returns correct size
			content, err := marshal.IssueToMarkdown(&issue)
			if err != nil {
				return nil, syscall.EIO
			}
			node := &IssueNode{
				lfs:          t.lfs,
				issue:        issue,
				content:      content,
				contentReady: true,
			}
			// Set attributes on EntryOut so ls shows correct size/times
			out.Attr.Mode = 0644 | syscall.S_IFREG
			out.Attr.Size = uint64(len(content))
			out.SetAttrTimeout(30 * time.Second)
			out.SetEntryTimeout(30 * time.Second)
			out.Attr.SetTimes(&issue.UpdatedAt, &issue.UpdatedAt, &issue.CreatedAt)
			return t.NewInode(ctx, node, fs.StableAttr{
				Mode: syscall.S_IFREG,
				Ino:  issueIno(issue.ID),
			}), 0
		}
	}

	return nil, syscall.ENOENT
}

// Create creates a new issue file
func (t *TeamNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if t.lfs.debug {
		log.Printf("Create: %s in team %s", name, t.team.Key)
	}

	// Extract title from filename (remove .md extension)
	title := strings.TrimSuffix(name, ".md")
	if title == name {
		// No .md extension, add it
		name = name + ".md"
	}

	// Create a new issue node that will be written to
	node := &NewIssueNode{
		lfs:    t.lfs,
		teamID: t.team.ID,
		title:  title,
	}

	inode := t.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG})

	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
}
