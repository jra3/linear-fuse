package fs

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// issueIno generates a stable inode number from an issue ID
func issueIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(issueID))
	return h.Sum64()
}

// issuesDirIno generates a stable inode number for a team's issues directory
func issuesDirIno(teamID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("issues:" + teamID))
	return h.Sum64()
}

// issueDirIno generates a stable inode number for an issue directory
func issueDirIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("dir:" + issueID))
	return h.Sum64()
}

// childrenDirIno generates a stable inode number for a children directory
func childrenDirIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("children:" + issueID))
	return h.Sum64()
}

// historyIno generates a stable inode number for an issue's history.md file
func historyIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("history:" + issueID))
	return h.Sum64()
}

// errorIno generates a stable inode number for an issue's .error file
func errorIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("error:" + issueID))
	return h.Sum64()
}

// IssuesNode represents the /teams/{KEY}/issues directory
type IssuesNode struct {
	BaseNode
	team api.Team
}

var _ fs.NodeReaddirer = (*IssuesNode)(nil)
var _ fs.NodeLookuper = (*IssuesNode)(nil)
var _ fs.NodeMkdirer = (*IssuesNode)(nil)
var _ fs.NodeRmdirer = (*IssuesNode)(nil)
var _ fs.NodeGetattrer = (*IssuesNode)(nil)

func (n *IssuesNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *IssuesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := n.lfs.GetTeamIssues(ctx, n.team.ID)
	if err != nil {
		return nil, syscall.EIO
	}

	entries := make([]fuse.DirEntry, len(issues))
	for i, issue := range issues {
		entries[i] = fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFDIR,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (n *IssuesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Check if name looks like a valid issue identifier (e.g., "ENG-123")
	// to avoid unnecessary API calls for invalid names
	if !looksLikeIdentifier(name) {
		return nil, syscall.ENOENT
	}

	// Use FetchIssueByIdentifier which checks: cache -> SQLite -> direct API
	// This avoids loading ALL team issues just to access a single issue
	issue, err := n.lfs.FetchIssueByIdentifier(ctx, name)
	if err != nil {
		// If API returns not found, return ENOENT
		return nil, syscall.ENOENT
	}

	node := &IssueDirectoryNode{
		BaseNode: BaseNode{lfs: n.lfs},
		issue:    *issue,
	}
	out.Attr.Mode = 0755 | syscall.S_IFDIR
	out.Attr.Uid = n.lfs.uid
	out.Attr.Gid = n.lfs.gid
	out.SetAttrTimeout(30 * time.Second)
	out.SetEntryTimeout(30 * time.Second)
	out.Attr.SetTimes(&issue.UpdatedAt, &issue.UpdatedAt, &issue.CreatedAt)
	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFDIR,
		Ino:  issueDirIno(issue.ID),
	}), 0
}

// looksLikeIdentifier checks if a name looks like a Linear issue identifier
// Valid formats: "ABC-123", "AB-1", etc. (1-5 uppercase letters, dash, 1+ digits)
func looksLikeIdentifier(name string) bool {
	dashIdx := -1
	for i, c := range name {
		if c == '-' {
			dashIdx = i
			break
		}
		// Before dash: must be uppercase letter
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	// Must have dash with 1-5 chars before it
	if dashIdx < 1 || dashIdx > 5 {
		return false
	}
	// After dash: must be digits
	if dashIdx >= len(name)-1 {
		return false
	}
	for _, c := range name[dashIdx+1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Mkdir creates a new issue from a directory name
func (n *IssuesNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Mkdir: %s in team %s (creating issue)", name, n.team.Key)
	}

	// Create a new issue with the directory name as title
	input := map[string]any{
		"teamId": n.team.ID,
		"title":  name,
	}

	issue, err := n.lfs.client.CreateIssue(ctx, input)
	if err != nil {
		log.Printf("Failed to create issue: %v", err)
		return nil, syscall.EIO
	}

	// Upsert to SQLite so it's immediately visible
	if err := n.lfs.UpsertIssue(ctx, *issue); err != nil {
		log.Printf("Warning: failed to upsert issue to SQLite: %v", err)
		// Don't fail - the issue was created in Linear, sync will eventually pick it up
	}

	// Invalidate caches
	n.lfs.InvalidateTeamIssues(n.team.ID)
	n.lfs.InvalidateMyIssues()
	n.lfs.InvalidateFilteredIssues(n.team.ID)
	n.lfs.InvalidateKernelEntry(issuesDirIno(n.team.ID), issue.Identifier)

	node := &IssueDirectoryNode{
		BaseNode: BaseNode{lfs: n.lfs},
		issue:    *issue,
	}

	out.Attr.Mode = 0755 | syscall.S_IFDIR
	out.SetAttrTimeout(30 * time.Second)
	out.SetEntryTimeout(30 * time.Second)
	out.Attr.SetTimes(&issue.UpdatedAt, &issue.UpdatedAt, &issue.CreatedAt)

	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFDIR,
		Ino:  issueDirIno(issue.ID),
	}), 0
}

// Rmdir archives an issue (soft delete)
func (n *IssuesNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Rmdir: %s in team %s (archiving issue)", name, n.team.Key)
	}

	issues, err := n.lfs.GetTeamIssues(ctx, n.team.ID)
	if err != nil {
		return syscall.EIO
	}

	for _, issue := range issues {
		if issue.Identifier == name {
			assigneeID := ""
			if issue.Assignee != nil {
				assigneeID = issue.Assignee.ID
			}
			err := n.lfs.ArchiveIssue(ctx, issue.ID, n.team.ID, assigneeID)
			if err != nil {
				log.Printf("Failed to archive issue %s: %v", name, err)
				return syscall.EIO
			}

			// Additional cache invalidations
			n.lfs.InvalidateFilteredIssues(n.team.ID)
			n.lfs.InvalidateIssueById(issue.Identifier)
			if issue.Project != nil {
				n.lfs.InvalidateProjectIssues(issue.Project.ID)
			}
			n.lfs.InvalidateKernelEntry(issuesDirIno(n.team.ID), name)

			if n.lfs.debug {
				log.Printf("Issue %s archived successfully", name)
			}
			return 0
		}
	}

	return syscall.ENOENT
}

// IssueDirectoryNode represents /teams/{KEY}/issues/{ID}/ directory
type IssueDirectoryNode struct {
	BaseNode
	issue api.Issue
}

var _ fs.NodeReaddirer = (*IssueDirectoryNode)(nil)
var _ fs.NodeLookuper = (*IssueDirectoryNode)(nil)
var _ fs.NodeGetattrer = (*IssueDirectoryNode)(nil)

func (n *IssueDirectoryNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
	return 0
}

func (n *IssueDirectoryNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "issue.md", Mode: syscall.S_IFREG},
		{Name: "history.md", Mode: syscall.S_IFREG},
		{Name: ".error", Mode: syscall.S_IFREG},
		{Name: "comments", Mode: syscall.S_IFDIR},
		{Name: "docs", Mode: syscall.S_IFDIR},
		{Name: "children", Mode: syscall.S_IFDIR},
		{Name: "attachments", Mode: syscall.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (n *IssueDirectoryNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "issue.md":
		// Fetch attachments for the issue
		attachments, _ := n.lfs.GetIssueAttachments(ctx, n.issue.ID)
		content, err := marshal.IssueToMarkdown(&n.issue, attachments...)
		if err != nil {
			return nil, syscall.EIO
		}
		node := &IssueFileNode{
			BaseNode:     BaseNode{lfs: n.lfs},
			issue:        n.issue,
			content:      content,
			contentReady: true,
		}
		out.Attr.Mode = 0644 | syscall.S_IFREG
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = uint64(len(content))
		out.SetAttrTimeout(5 * time.Second)  // Shorter timeout for writable files
		out.SetEntryTimeout(5 * time.Second) // Shorter timeout for writable files
		out.Attr.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  issueIno(n.issue.ID),
		}), 0

	case "history.md":
		// Fetch history content during Lookup so we can set the actual size
		// (kernel won't issue READ if size is 0)
		entries, err := n.lfs.client.GetIssueHistory(ctx, n.issue.ID)
		if err != nil {
			log.Printf("Failed to fetch history for %s: %v", n.issue.Identifier, err)
			return nil, syscall.EIO
		}
		content := marshal.HistoryToMarkdown(n.issue.Identifier, entries)

		node := &HistoryFileNode{
			BaseNode:     BaseNode{lfs: n.lfs},
			issueID:      n.issue.ID,
			identifier:   n.issue.Identifier,
			content:      content,
			contentReady: true,
		}
		out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = uint64(len(content))
		out.SetAttrTimeout(30 * time.Second)
		out.SetEntryTimeout(30 * time.Second)
		out.Attr.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  historyIno(n.issue.ID),
		}), 0

	case ".error":
		node := &ErrorFileNode{
			BaseNode: BaseNode{lfs: n.lfs},
			issueID:  n.issue.ID,
		}
		// Get error to determine size
		issueErr := n.lfs.GetIssueError(n.issue.ID)
		size := uint64(0)
		if issueErr != nil {
			size = uint64(len(issueErr.Message) + 1) // +1 for newline
		}
		now := time.Now()
		out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = size
		out.SetAttrTimeout(1 * time.Second)  // Short timeout - errors change on writes
		out.SetEntryTimeout(1 * time.Second)
		out.Attr.SetTimes(&now, &now, &now)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  errorIno(n.issue.ID),
		}), 0

	case "comments":
		teamID := ""
		if n.issue.Team != nil {
			teamID = n.issue.Team.ID
		}
		node := &CommentsNode{
			BaseNode:       BaseNode{lfs: n.lfs},
			issueID:        n.issue.ID,
			teamID:         teamID,
			issueUpdatedAt: n.issue.UpdatedAt,
		}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
		out.SetAttrTimeout(30 * time.Second)
		out.SetEntryTimeout(30 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  commentsDirIno(n.issue.ID),
		}), 0

	case "docs":
		node := &DocsNode{
			BaseNode: BaseNode{lfs: n.lfs},
			issueID:  n.issue.ID,
		}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
		out.SetAttrTimeout(30 * time.Second)
		out.SetEntryTimeout(30 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  docsDirIno(n.issue.ID),
		}), 0

	case "children":
		node := &ChildrenNode{
			BaseNode: BaseNode{lfs: n.lfs},
			issue:    n.issue,
		}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
		out.SetAttrTimeout(30 * time.Second)
		out.SetEntryTimeout(30 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  childrenDirIno(n.issue.ID),
		}), 0

	case "attachments":
		node := &AttachmentsNode{
			BaseNode: BaseNode{lfs: n.lfs},
			issueID:  n.issue.ID,
		}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
		out.SetAttrTimeout(30 * time.Second)
		out.SetEntryTimeout(30 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  attachmentsDirIno(n.issue.ID),
		}), 0
	}

	return nil, syscall.ENOENT
}

// IssueFileNode represents an issue.md file inside /teams/{KEY}/issues/{ID}/
type IssueFileNode struct {
	BaseNode
	issue api.Issue

	// Write buffer and cached content
	mu           sync.Mutex
	content      []byte
	contentReady bool
	dirty        bool
}

var _ fs.NodeGetattrer = (*IssueFileNode)(nil)
var _ fs.NodeOpener = (*IssueFileNode)(nil)
var _ fs.NodeReader = (*IssueFileNode)(nil)
var _ fs.NodeWriter = (*IssueFileNode)(nil)
var _ fs.NodeFlusher = (*IssueFileNode)(nil)
var _ fs.NodeFsyncer = (*IssueFileNode)(nil)
var _ fs.NodeSetattrer = (*IssueFileNode)(nil)

// ensureContent generates markdown content if not already cached
func (i *IssueFileNode) ensureContent() error {
	if i.contentReady {
		return nil
	}
	// Fetch attachments for the issue
	attachments, _ := i.lfs.GetIssueAttachments(context.Background(), i.issue.ID)
	content, err := marshal.IssueToMarkdown(&i.issue, attachments...)
	if err != nil {
		return err
	}
	i.content = content
	i.contentReady = true
	return nil
}

func (i *IssueFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := i.ensureContent(); err != nil {
		return syscall.EIO
	}

	out.Mode = 0644
	i.SetOwner(out)
	out.Size = uint64(len(i.content))
	out.SetTimes(nil, &i.issue.UpdatedAt, &i.issue.CreatedAt)

	return 0
}

func (i *IssueFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Use kernel caching for better performance
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (i *IssueFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := i.ensureContent(); err != nil {
		return nil, syscall.EIO
	}

	if off >= int64(len(i.content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(i.content)) {
		end = int64(len(i.content))
	}

	return fuse.ReadResultData(i.content[off:end]), 0
}

func (i *IssueFileNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.lfs.debug {
		log.Printf("Write: %s offset=%d len=%d", i.issue.Identifier, off, len(data))
	}

	// Initialize content buffer if needed
	if err := i.ensureContent(); err != nil {
		return 0, syscall.EIO
	}

	// Expand buffer if needed
	newLen := int(off) + len(data)
	if newLen > len(i.content) {
		newContent := make([]byte, newLen)
		copy(newContent, i.content)
		i.content = newContent
	}

	// Write data at offset
	copy(i.content[off:], data)
	i.dirty = true

	return uint32(len(data)), 0
}

func (i *IssueFileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Handle truncate
	if sz, ok := in.GetSize(); ok {
		if i.lfs.debug {
			log.Printf("Setattr truncate: %s size=%d", i.issue.Identifier, sz)
		}

		if err := i.ensureContent(); err != nil {
			return syscall.EIO
		}

		if int(sz) < len(i.content) {
			i.content = i.content[:sz]
		} else if int(sz) > len(i.content) {
			newContent := make([]byte, sz)
			copy(newContent, i.content)
			i.content = newContent
		}
		i.dirty = true
	}

	out.Mode = 0644
	if i.content != nil {
		out.Size = uint64(len(i.content))
	}

	return 0
}

func (i *IssueFileNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.dirty || i.content == nil {
		return 0
	}

	// Add timeout for API operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if i.lfs.debug {
		log.Printf("Flush: %s (saving changes)", i.issue.Identifier)
	}

	// Parse the modified content and compute updates
	updates, err := marshal.MarkdownToIssueUpdate(i.content, &i.issue)
	if err != nil {
		log.Printf("Failed to parse changes for %s: %v", i.issue.Identifier, err)
		i.lfs.SetIssueError(i.issue.ID, "Parse error: "+err.Error())
		return syscall.EINVAL
	}

	if len(updates) == 0 {
		if i.lfs.debug {
			log.Printf("Flush: %s no changes detected", i.issue.Identifier)
		}
		i.dirty = false
		return 0
	}

	// Track state name for updating i.issue after API call
	var newStateName string

	// Resolve status name to state ID if needed
	if stateName, ok := updates["stateId"].(string); ok {
		if i.issue.Team == nil {
			log.Printf("Cannot resolve state '%s': issue has no team", stateName)
			i.lfs.SetIssueError(i.issue.ID, "Field: status\nValue: \""+stateName+"\"\nError: Cannot resolve state - issue has no team")
			return syscall.EINVAL
		}
		stateID, err := i.lfs.ResolveStateID(ctx, i.issue.Team.ID, stateName)
		if err != nil {
			log.Printf("Failed to resolve state '%s': %v", stateName, err)
			i.lfs.SetIssueError(i.issue.ID, "Field: status\nValue: \""+stateName+"\"\nError: "+err.Error()+". See states.md for valid workflow states.")
			return syscall.EINVAL
		}
		newStateName = stateName // Capture name before replacing with ID
		updates["stateId"] = stateID
	}

	// Resolve assignee email/name to user ID if needed
	if assigneeID, ok := updates["assigneeId"].(string); ok {
		userID, err := i.lfs.ResolveUserID(ctx, assigneeID)
		if err != nil {
			log.Printf("Failed to resolve assignee '%s': %v", assigneeID, err)
			i.lfs.SetIssueError(i.issue.ID, "Field: assignee\nValue: \""+assigneeID+"\"\nError: "+err.Error()+". Use email address or display name.")
			return syscall.EINVAL
		}
		updates["assigneeId"] = userID
	}

	// Resolve label names to IDs if needed
	if labelNames, ok := updates["labelIds"].([]string); ok {
		if len(labelNames) == 0 {
			// Clearing all labels - use removedLabelIds instead of empty labelIds
			// (Linear API rejects labelIds: [])
			delete(updates, "labelIds")
			if len(i.issue.Labels.Nodes) > 0 {
				removedIDs := make([]string, len(i.issue.Labels.Nodes))
				for idx, l := range i.issue.Labels.Nodes {
					removedIDs[idx] = l.ID
				}
				updates["removedLabelIds"] = removedIDs
			}
		} else {
			if i.issue.Team == nil {
				log.Printf("Cannot resolve labels: issue has no team")
				i.lfs.SetIssueError(i.issue.ID, "Field: labels\nError: Cannot resolve labels - issue has no team")
				return syscall.EINVAL
			}
			labelIDs, notFound, err := i.lfs.ResolveLabelIDs(ctx, i.issue.Team.ID, labelNames)
			if err != nil {
				log.Printf("Failed to resolve labels: %v", err)
				i.lfs.SetIssueError(i.issue.ID, "Field: labels\nError: "+err.Error())
				return syscall.EINVAL
			}
			if len(notFound) > 0 {
				log.Printf("Unknown labels: %v (see labels.md for valid labels)", notFound)
				i.lfs.SetIssueError(i.issue.ID, "Field: labels\nValue: "+fmt.Sprintf("%v", notFound)+"\nError: Unknown labels. See labels.md for valid labels.")
				return syscall.EINVAL
			}
			updates["labelIds"] = labelIDs
		}
	}

	// Resolve parent issue identifier to ID if needed
	if parentID, ok := updates["parentId"].(string); ok {
		issueID, err := i.lfs.ResolveIssueID(ctx, parentID)
		if err != nil {
			log.Printf("Failed to resolve parent '%s': %v", parentID, err)
			i.lfs.SetIssueError(i.issue.ID, "Field: parent\nValue: \""+parentID+"\"\nError: "+err.Error())
			return syscall.EINVAL
		}
		updates["parentId"] = issueID
	}

	// Resolve project name to ID if needed
	if projectName, ok := updates["projectId"].(string); ok {
		if i.issue.Team == nil {
			log.Printf("Cannot resolve project '%s': issue has no team", projectName)
			i.lfs.SetIssueError(i.issue.ID, "Field: project\nValue: \""+projectName+"\"\nError: Cannot resolve project - issue has no team")
			return syscall.EINVAL
		}
		projectID, err := i.lfs.ResolveProjectID(ctx, i.issue.Team.ID, projectName)
		if err != nil {
			log.Printf("Failed to resolve project '%s': %v", projectName, err)
			i.lfs.SetIssueError(i.issue.ID, "Field: project\nValue: \""+projectName+"\"\nError: "+err.Error())
			return syscall.EINVAL
		}
		updates["projectId"] = projectID
	}

	// Resolve milestone name to ID if needed
	if milestoneName, ok := updates["projectMilestoneId"].(string); ok {
		// Get project ID - prefer newly set project, fallback to existing
		var projectID string
		if newProjectID, ok := updates["projectId"].(string); ok {
			projectID = newProjectID
		} else if i.issue.Project != nil {
			projectID = i.issue.Project.ID
		} else {
			log.Printf("Cannot resolve milestone '%s': issue has no project", milestoneName)
			i.lfs.SetIssueError(i.issue.ID, "Field: milestone\nValue: \""+milestoneName+"\"\nError: Cannot resolve milestone - issue has no project. Set project first.")
			return syscall.EINVAL
		}
		milestoneID, err := i.lfs.ResolveMilestoneID(ctx, projectID, milestoneName)
		if err != nil {
			log.Printf("Failed to resolve milestone '%s': %v", milestoneName, err)
			i.lfs.SetIssueError(i.issue.ID, "Field: milestone\nValue: \""+milestoneName+"\"\nError: "+err.Error())
			return syscall.EINVAL
		}
		updates["projectMilestoneId"] = milestoneID
	}

	// Resolve cycle name to ID if needed
	if cycleName, ok := updates["cycleId"].(string); ok {
		if i.issue.Team == nil {
			log.Printf("Cannot resolve cycle '%s': issue has no team", cycleName)
			i.lfs.SetIssueError(i.issue.ID, "Field: cycle\nValue: \""+cycleName+"\"\nError: Cannot resolve cycle - issue has no team")
			return syscall.EINVAL
		}
		cycleID, err := i.lfs.ResolveCycleID(ctx, i.issue.Team.ID, cycleName)
		if err != nil {
			log.Printf("Failed to resolve cycle '%s': %v", cycleName, err)
			i.lfs.SetIssueError(i.issue.ID, "Field: cycle\nValue: \""+cycleName+"\"\nError: "+err.Error())
			return syscall.EINVAL
		}
		updates["cycleId"] = cycleID
	}

	// Call Linear API to update
	if err := i.lfs.client.UpdateIssue(ctx, i.issue.ID, updates); err != nil {
		log.Printf("Failed to update issue %s: %v", i.issue.Identifier, err)
		i.lfs.SetIssueError(i.issue.ID, "API error: "+err.Error())
		return syscall.EIO
	}

	// Clear any previous error on successful write
	i.lfs.ClearIssueError(i.issue.ID)

	if i.lfs.debug {
		log.Printf("Flush: %s updated successfully", i.issue.Identifier)
	}

	// Invalidate caches so next read gets fresh data
	if i.issue.Team != nil {
		i.lfs.InvalidateTeamIssues(i.issue.Team.ID)
		i.lfs.InvalidateFilteredIssues(i.issue.Team.ID)
	}
	i.lfs.InvalidateMyIssues()
	i.lfs.InvalidateIssueById(i.issue.Identifier)

	// Invalidate user caches for old and new assignee
	if i.issue.Assignee != nil {
		i.lfs.InvalidateUserIssues(i.issue.Assignee.ID)
	}
	if newAssigneeID, ok := updates["assigneeId"].(string); ok {
		// Also invalidate new assignee's cache if different from old
		if i.issue.Assignee == nil || newAssigneeID != i.issue.Assignee.ID {
			i.lfs.InvalidateUserIssues(newAssigneeID)
		}
	}

	// Invalidate project caches if project changed
	if _, hasProjectUpdate := updates["projectId"]; hasProjectUpdate {
		// Invalidate old project (if issue was in one)
		if i.issue.Project != nil {
			i.lfs.InvalidateProjectIssues(i.issue.Project.ID)
		}
		// Invalidate new project (if being assigned to one)
		if newProjectID, ok := updates["projectId"].(string); ok {
			i.lfs.InvalidateProjectIssues(newProjectID)
		}
	}

	// Invalidate kernel cache for this file
	i.lfs.InvalidateKernelInode(issueIno(i.issue.ID))

	// Update i.issue with the new values so next read sees them
	// (otherwise generateContent would serialize the old i.issue data)
	if newStateName != "" {
		i.issue.State.Name = newStateName
	}
	if title, ok := updates["title"].(string); ok {
		i.issue.Title = title
	}
	if desc, ok := updates["description"].(string); ok {
		i.issue.Description = desc
	}
	if priority, ok := updates["priority"].(int); ok {
		i.issue.Priority = priority
	}
	if dueDate, ok := updates["dueDate"].(string); ok {
		i.issue.DueDate = &dueDate
	} else if updates["dueDate"] == nil {
		i.issue.DueDate = nil
	}
	if estimate, ok := updates["estimate"].(int); ok {
		est := float64(estimate)
		i.issue.Estimate = &est
	} else if updates["estimate"] == nil {
		i.issue.Estimate = nil
	}

	// Upsert the updated issue to SQLite so it's immediately visible
	if err := i.lfs.UpsertIssue(ctx, i.issue); err != nil {
		log.Printf("Warning: failed to upsert issue to SQLite: %v", err)
		// Don't fail - the issue was updated in Linear, sync will eventually pick it up
	}

	i.dirty = false
	i.contentReady = false // Force re-generate on next read

	return 0
}

func (i *IssueFileNode) Fsync(ctx context.Context, f fs.FileHandle, flags uint32) syscall.Errno {
	// Fsync is a no-op; actual persistence happens in Flush
	return 0
}

// ChildrenNode represents the /teams/{KEY}/issues/{ID}/children/ directory
type ChildrenNode struct {
	BaseNode
	issue api.Issue
}

var _ fs.NodeReaddirer = (*ChildrenNode)(nil)
var _ fs.NodeLookuper = (*ChildrenNode)(nil)
var _ fs.NodeGetattrer = (*ChildrenNode)(nil)

func (n *ChildrenNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
	return 0
}

func (n *ChildrenNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Query children from database by parent_id
	children, err := n.lfs.GetIssueChildren(ctx, n.issue.ID)
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, len(children))
	for i, child := range children {
		entries[i] = fuse.DirEntry{
			Name: child.Identifier,
			Mode: syscall.S_IFLNK,
		}
	}
	return fs.NewListDirStream(entries), 0
}

func (n *ChildrenNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Query children from database by parent_id
	children, err := n.lfs.GetIssueChildren(ctx, n.issue.ID)
	if err != nil {
		return nil, syscall.EIO
	}
	for _, child := range children {
		if child.Identifier == name {
			node := &ChildSymlinkNode{
				child: api.ChildIssue{
					ID:         child.ID,
					Identifier: child.Identifier,
					Title:      child.Title,
					CreatedAt:  child.CreatedAt,
					UpdatedAt:  child.UpdatedAt,
				},
			}
			out.Attr.Mode = 0777 | syscall.S_IFLNK
			out.Attr.SetTimes(&child.UpdatedAt, &child.UpdatedAt, &child.CreatedAt)
			return n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK}), 0
		}
	}
	return nil, syscall.ENOENT
}

// ChildSymlinkNode is a symlink pointing to a child issue directory
type ChildSymlinkNode struct {
	fs.Inode
	child api.ChildIssue
}

var _ fs.NodeReadlinker = (*ChildSymlinkNode)(nil)
var _ fs.NodeGetattrer = (*ChildSymlinkNode)(nil)

func (s *ChildSymlinkNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	// Point to sibling issue directory: ../ENG-456
	target := "../" + s.child.Identifier
	return []byte(target), 0
}

func (s *ChildSymlinkNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0777 | syscall.S_IFLNK
	out.Size = uint64(len("../") + len(s.child.Identifier))
	out.SetTimes(&s.child.UpdatedAt, &s.child.UpdatedAt, &s.child.CreatedAt)
	return 0
}

// HistoryFileNode represents a history.md file inside /teams/{KEY}/issues/{ID}/
// It lazily fetches history from the Linear API on first read.
type HistoryFileNode struct {
	BaseNode
	issueID    string
	identifier string

	// Lazy-loaded content
	mu           sync.Mutex
	content      []byte
	contentReady bool
}

var _ fs.NodeGetattrer = (*HistoryFileNode)(nil)
var _ fs.NodeOpener = (*HistoryFileNode)(nil)
var _ fs.NodeReader = (*HistoryFileNode)(nil)

// ensureContent fetches and renders history content if not already cached
func (h *HistoryFileNode) ensureContent(ctx context.Context) error {
	if h.contentReady {
		return nil
	}

	if h.lfs.debug {
		log.Printf("HistoryFileNode: fetching history for %s (issueID=%s)", h.identifier, h.issueID)
	}

	// Fetch history from API (lazy fetch)
	entries, err := h.lfs.client.GetIssueHistory(ctx, h.issueID)
	if err != nil {
		log.Printf("Failed to fetch history for %s: %v", h.identifier, err)
		return err
	}

	if h.lfs.debug {
		log.Printf("HistoryFileNode: got %d history entries for %s", len(entries), h.identifier)
	}

	h.content = marshal.HistoryToMarkdown(h.identifier, entries)
	h.contentReady = true
	return nil
}

func (h *HistoryFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := h.ensureContent(ctx); err != nil {
		return syscall.EIO
	}

	out.Mode = 0444 // Read-only
	h.SetOwner(out)
	out.Size = uint64(len(h.content))

	return 0
}

func (h *HistoryFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (h *HistoryFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := h.ensureContent(ctx); err != nil {
		return nil, syscall.EIO
	}

	if off >= int64(len(h.content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(h.content)) {
		end = int64(len(h.content))
	}

	return fuse.ReadResultData(h.content[off:end]), 0
}

// ErrorFileNode represents a .error file inside /teams/{KEY}/issues/{ID}/
// It shows the last validation error from a failed write operation.
type ErrorFileNode struct {
	BaseNode
	issueID string
}

var _ fs.NodeGetattrer = (*ErrorFileNode)(nil)
var _ fs.NodeOpener = (*ErrorFileNode)(nil)
var _ fs.NodeReader = (*ErrorFileNode)(nil)

func (e *ErrorFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0444 // Read-only
	e.SetOwner(out)

	// Get current error to determine size
	issueErr := e.lfs.GetIssueError(e.issueID)
	if issueErr != nil {
		out.Size = uint64(len(issueErr.Message) + 1) // +1 for newline
	} else {
		out.Size = 0
	}

	return 0
}

func (e *ErrorFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (e *ErrorFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	issueErr := e.lfs.GetIssueError(e.issueID)
	if issueErr == nil {
		return fuse.ReadResultData(nil), 0
	}

	content := []byte(issueErr.Message + "\n")

	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}

	return fuse.ReadResultData(content[off:end]), 0
}
