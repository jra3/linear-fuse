package fuse

import (
	"context"
	"fmt"
	"log"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/cache"
	"github.com/jra3/linear-fuse/pkg/linear"
)

// StateDirectoryNode represents a directory for a specific issue state
type StateDirectoryNode struct {
	fs.Inode
	state  string
	client *linear.Client
	cache  *cache.Cache
	debug  bool
}

// Ensure StateDirectoryNode implements necessary interfaces
var _ = (fs.NodeReaddirer)((*StateDirectoryNode)(nil))
var _ = (fs.NodeLookuper)((*StateDirectoryNode)(nil))

// Readdir reads the directory contents (issues in this state)
func (n *StateDirectoryNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if n.debug {
		log.Printf("Readdir called on state directory: %s", n.state)
	}

	// Get all issues
	issueIDs, cached := n.cache.GetList()
	var issues []linear.Issue

	if !cached {
		var err error
		issues, err = n.client.ListIssues()
		if err != nil {
			log.Printf("Failed to list issues: %v", err)
			return nil, syscall.EIO
		}
		n.cache.SetList(issues)
	} else {
		issues = make([]linear.Issue, 0, len(issueIDs))
		for _, id := range issueIDs {
			if issue, ok := n.cache.Get(id); ok {
				issues = append(issues, *issue)
			}
		}
	}

	// Filter by state
	var filteredIssues []linear.Issue
	for _, issue := range issues {
		if issue.State.Name == n.state || (n.state == "all" || n.state == "") {
			filteredIssues = append(filteredIssues, issue)
		}
	}

	// Create directory entries
	entries := make([]fuse.DirEntry, 0, len(filteredIssues))
	for _, issue := range filteredIssues {
		filename := fmt.Sprintf("%s.md", issue.Identifier)
		entries = append(entries, fuse.DirEntry{
			Name: filename,
			Mode: fuse.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), fs.OK
}

// Lookup looks up a file in the state directory
func (n *StateDirectoryNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.debug {
		log.Printf("Lookup called in state directory %s for: %s", n.state, name)
	}

	identifier := parseFilename(name)
	if identifier == "" {
		return nil, syscall.ENOENT
	}

	// Get all issues
	issueIDs, cached := n.cache.GetList()
	if !cached {
		issues, err := n.client.ListIssues()
		if err != nil {
			log.Printf("Failed to list issues: %v", err)
			return nil, syscall.EIO
		}
		n.cache.SetList(issues)
	}

	// Find the issue by identifier
	var issue *linear.Issue
	if cached {
		for _, id := range issueIDs {
			if cachedIssue, ok := n.cache.Get(id); ok {
				if cachedIssue.Identifier == identifier {
					// Check if it matches the state filter
					if n.state == "all" || n.state == "" || cachedIssue.State.Name == n.state {
						issue = cachedIssue
						break
					}
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
		client: n.client,
		cache:  n.cache,
		debug:  n.debug,
	}

	child := n.NewInode(ctx, node, fs.StableAttr{
		Mode: fuse.S_IFREG,
	})

	return child, fs.OK
}

// TeamDirectoryNode represents a directory for a specific team
type TeamDirectoryNode struct {
	fs.Inode
	team   string
	client *linear.Client
	cache  *cache.Cache
	debug  bool
}

// Ensure TeamDirectoryNode implements necessary interfaces
var _ = (fs.NodeReaddirer)((*TeamDirectoryNode)(nil))
var _ = (fs.NodeLookuper)((*TeamDirectoryNode)(nil))

// Readdir reads the directory contents (issues for this team)
func (n *TeamDirectoryNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if n.debug {
		log.Printf("Readdir called on team directory: %s", n.team)
	}

	// Get all issues
	issueIDs, cached := n.cache.GetList()
	var issues []linear.Issue

	if !cached {
		var err error
		issues, err = n.client.ListIssues()
		if err != nil {
			log.Printf("Failed to list issues: %v", err)
			return nil, syscall.EIO
		}
		n.cache.SetList(issues)
	} else {
		issues = make([]linear.Issue, 0, len(issueIDs))
		for _, id := range issueIDs {
			if issue, ok := n.cache.Get(id); ok {
				issues = append(issues, *issue)
			}
		}
	}

	// Filter by team
	var filteredIssues []linear.Issue
	for _, issue := range issues {
		if issue.Team.Key == n.team || issue.Team.Name == n.team {
			filteredIssues = append(filteredIssues, issue)
		}
	}

	// Create directory entries
	entries := make([]fuse.DirEntry, 0, len(filteredIssues))
	for _, issue := range filteredIssues {
		filename := fmt.Sprintf("%s.md", issue.Identifier)
		entries = append(entries, fuse.DirEntry{
			Name: filename,
			Mode: fuse.S_IFREG,
		})
	}

	return fs.NewListDirStream(entries), fs.OK
}

// Lookup looks up a file in the team directory
func (n *TeamDirectoryNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.debug {
		log.Printf("Lookup called in team directory %s for: %s", n.team, name)
	}

	identifier := parseFilename(name)
	if identifier == "" {
		return nil, syscall.ENOENT
	}

	// Get all issues
	issueIDs, cached := n.cache.GetList()
	if !cached {
		issues, err := n.client.ListIssues()
		if err != nil {
			log.Printf("Failed to list issues: %v", err)
			return nil, syscall.EIO
		}
		n.cache.SetList(issues)
	}

	// Find the issue
	var issue *linear.Issue
	if cached {
		for _, id := range issueIDs {
			if cachedIssue, ok := n.cache.Get(id); ok {
				if cachedIssue.Identifier == identifier {
					// Check if it matches the team filter
					if cachedIssue.Team.Key == n.team || cachedIssue.Team.Name == n.team {
						issue = cachedIssue
						break
					}
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
		client: n.client,
		cache:  n.cache,
		debug:  n.debug,
	}

	child := n.NewInode(ctx, node, fs.StableAttr{
		Mode: fuse.S_IFREG,
	})

	return child, fs.OK
}
