package fuse

import (
	"context"
	"fmt"
	"log"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/cache"
	"github.com/jra3/linear-fuse/pkg/linear"
)

// LinearFS represents the Linear FUSE filesystem
type LinearFS struct {
	fs.Inode
	client *linear.Client
	cache  *cache.Cache
	debug  bool
}

// NewLinearFS creates a new Linear FUSE filesystem
func NewLinearFS(client *linear.Client, debug bool) (*LinearFS, error) {
	return &LinearFS{
		client: client,
		cache:  cache.New(5 * time.Minute),
		debug:  debug,
	}, nil
}

// Mount mounts the filesystem at the specified mountpoint
func (lfs *LinearFS) Mount(mountpoint string) (*fuse.Server, error) {
	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			Name:   "linear-fuse",
			FsName: "linear",
			Debug:  lfs.debug,
		},
	}

	server, err := fs.Mount(mountpoint, lfs, opts)
	if err != nil {
		return nil, fmt.Errorf("mount failed: %w", err)
	}

	return server, nil
}

// Ensure LinearFS implements the NodeReaddirer interface
var _ = (fs.NodeReaddirer)((*LinearFS)(nil))

// Readdir reads the directory contents (list of issues)
func (lfs *LinearFS) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if lfs.debug {
		log.Printf("Readdir called on root")
	}

	// Try to get from cache
	issueIDs, cached := lfs.cache.GetList()
	var issues []linear.Issue

	if !cached {
		// Fetch from API
		var err error
		issues, err = lfs.client.ListIssues()
		if err != nil {
			log.Printf("Failed to list issues: %v", err)
			return nil, syscall.EIO
		}
		lfs.cache.SetList(issues)
	} else {
		// Get issues from cache
		issues = make([]linear.Issue, 0, len(issueIDs))
		for _, id := range issueIDs {
			if issue, ok := lfs.cache.Get(id); ok {
				issues = append(issues, *issue)
			}
		}
	}

	// Create directory entries
	entries := make([]fuse.DirEntry, 0, len(issues))
	for _, issue := range issues {
		filename := fmt.Sprintf("%s.md", issue.Identifier)
		entries = append(entries, fuse.DirEntry{
			Name: filename,
			Mode: fuse.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), fs.OK
}

// Ensure LinearFS implements the NodeLookuper interface
var _ = (fs.NodeLookuper)((*LinearFS)(nil))

// Lookup looks up a file in the directory
func (lfs *LinearFS) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if lfs.debug {
		log.Printf("Lookup called for: %s", name)
	}

	// Parse filename to get issue identifier
	identifier := parseFilename(name)
	if identifier == "" {
		return nil, syscall.ENOENT
	}

	// Get all issues to find the matching one
	issueIDs, cached := lfs.cache.GetList()
	if !cached {
		issues, err := lfs.client.ListIssues()
		if err != nil {
			log.Printf("Failed to list issues: %v", err)
			return nil, syscall.EIO
		}
		lfs.cache.SetList(issues)
	}

	// Find the issue by identifier
	var issue *linear.Issue
	if cached {
		for _, id := range issueIDs {
			if cachedIssue, ok := lfs.cache.Get(id); ok {
				if cachedIssue.Identifier == identifier {
					issue = cachedIssue
					break
				}
			}
		}
	}

	if issue == nil {
		return nil, syscall.ENOENT
	}

	// Create an inode for the file
	node := &IssueFileNode{
		issue:  issue,
		client: lfs.client,
		cache:  lfs.cache,
		debug:  lfs.debug,
	}

	child := lfs.NewInode(ctx, node, fs.StableAttr{
		Mode: fuse.S_IFREG,
	})

	return child, fs.OK
}

// parseFilename extracts the issue identifier from a filename
func parseFilename(filename string) string {
	// Remove .md extension
	if len(filename) > 3 && filename[len(filename)-3:] == ".md" {
		return filename[:len(filename)-3]
	}
	return ""
}
