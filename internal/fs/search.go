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

// SearchNode represents the /teams/{KEY}/search/ directory
// Lookups create dynamic SearchResultsNode for each query
type SearchNode struct {
	fs.Inode
	lfs  *LinearFS
	team api.Team
}

var _ fs.NodeReaddirer = (*SearchNode)(nil)
var _ fs.NodeLookuper = (*SearchNode)(nil)
var _ fs.NodeGetattrer = (*SearchNode)(nil)

func (n *SearchNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	return 0
}

// Readdir returns an empty directory - queries are created on-demand via Lookup
func (n *SearchNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Search directory is empty - queries are specified in the path
	return fs.NewListDirStream([]fuse.DirEntry{}), 0
}

// Lookup creates a SearchResultsNode for the given query
// Query format: spaces encoded as + (e.g., "login+error" → "login error")
func (n *SearchNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Decode query: replace + with space
	query := decodeSearchQuery(name)
	if query == "" {
		return nil, syscall.ENOENT
	}

	now := time.Now()
	out.Attr.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	// Short cache time for search results
	out.SetAttrTimeout(10 * time.Second)
	out.SetEntryTimeout(10 * time.Second)

	node := &SearchResultsNode{
		lfs:   n.lfs,
		team:  n.team,
		query: query,
	}
	return n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// SearchResultsNode represents search results for a query: /teams/{KEY}/search/{query}/
type SearchResultsNode struct {
	fs.Inode
	lfs   *LinearFS
	team  api.Team
	query string
}

var _ fs.NodeReaddirer = (*SearchResultsNode)(nil)
var _ fs.NodeLookuper = (*SearchResultsNode)(nil)
var _ fs.NodeGetattrer = (*SearchResultsNode)(nil)

func (n *SearchResultsNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	out.SetTimes(&now, &now, &now)
	return 0
}

// Readdir returns symlinks to matching issues
func (n *SearchResultsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := n.lfs.SearchTeamIssues(ctx, n.team.ID, n.query)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(issues))
	for i, issue := range issues {
		entries[i] = fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFLNK,
		}
	}
	return fs.NewListDirStream(entries), 0
}

// Lookup returns a symlink to the matching issue
func (n *SearchResultsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Validate it looks like an identifier
	if !looksLikeIdentifier(name) {
		return nil, syscall.ENOENT
	}

	// Verify issue exists and matches search
	issues, err := n.lfs.SearchTeamIssues(ctx, n.team.ID, n.query)
	if err != nil {
		return nil, syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			node := &SearchResultSymlink{
				identifier: issue.Identifier,
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			return n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}
	return nil, syscall.ENOENT
}

// SearchResultSymlink is a symlink pointing to an issue directory
// Path from search/{query}/ to issues/ is ../../issues/
type SearchResultSymlink struct {
	fs.Inode
	identifier string
}

var _ fs.NodeReadlinker = (*SearchResultSymlink)(nil)
var _ fs.NodeGetattrer = (*SearchResultSymlink)(nil)

func (s *SearchResultSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	// From search/{query}/ go up 2 levels to team dir, then into issues/
	target := fmt.Sprintf("../../issues/%s", s.identifier)
	return []byte(target), 0
}

func (s *SearchResultSymlink) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	target := fmt.Sprintf("../../issues/%s", s.identifier)
	out.Mode = 0777 | syscall.S_IFLNK
	out.Size = uint64(len(target))
	return 0
}

// decodeSearchQuery converts a URL-like encoded query to a search string
// + is replaced with space, allowing multi-word queries in directory names
func decodeSearchQuery(encoded string) string {
	if encoded == "" {
		return ""
	}
	// Replace + with space
	return strings.ReplaceAll(encoded, "+", " ")
}

// encodeSearchQuery converts a search string to a directory-safe name
// Spaces are replaced with +
func encodeSearchQuery(query string) string {
	return strings.ReplaceAll(query, " ", "+")
}
