package fs

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
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

// issueWriteResult projects a freshly-created issue into a .last success entry.
// Path is the issue's identifier — the addressable on-disk directory name.
func issueWriteResult(issue *api.Issue) WriteResult {
	return WriteResult{
		Identifier: issue.Identifier,
		URL:        issue.URL,
		Path:       issue.Identifier,
		Title:      issue.Title,
		Status:     issue.State.Name,
	}
}

// createIssueFromSpec resolves a create spec's relational names to IDs and calls
// the create mutation. It is shared by IssuesNode.Mkdir (title-only spec) and the
// issues/_create trigger (full spec). An unresolvable field returns a *FieldError
// (commitCreate classifies it EINVAL); teamId and a title fallback are applied
// here.
func (lfs *LinearFS) createIssueFromSpec(ctx context.Context, team api.Team, spec map[string]any) (*api.Issue, error) {
	synthetic := api.Issue{Team: &team}
	if ferr := resolveIssueUpdate(ctx, lfs, &synthetic, spec); ferr != nil {
		return nil, ferr
	}
	spec["teamId"] = team.ID
	if t, ok := spec["title"].(string); !ok || t == "" {
		spec["title"] = "Untitled issue"
	}
	return lfs.mutator().CreateIssue(ctx, spec)
}

// issueCreateSpec assembles the createSpec shared by every issue-create surface
// (issues/ mkdir, issues/_create, children/ mkdir). key and dir vary by surface —
// a sub-issue reports to the parent issue's sidecars and the children/ dir —
// while the result projection, persist, and the views an issue create dirties
// (team/my/filtered caches, the issues/ listing, recent/) are invariant.
func (lfs *LinearFS) issueCreateSpec(teamID, op, key string, dir uint64, mutate func(context.Context) (*api.Issue, error)) createSpec[api.Issue] {
	return createSpec[api.Issue]{
		op:     op,
		key:    key,
		mutate: mutate,
		result: func(i *api.Issue) WriteResult { return issueWriteResult(i) },
		persist: func(ctx context.Context, i *api.Issue) error {
			return lfs.UpsertIssue(ctx, *i)
		},
		dir:       dir,
		entryName: func(i *api.Issue) string { return i.Identifier },
		invalidateExtra: func(i *api.Issue) {
			// A fresh issue must appear in recent/ immediately, not after the
			// dir cache TTL (the #148 design's known staleness bound).
			lfs.InvalidateCreated(recentDirIno(teamID), i.Identifier)
			// A sub-issue lands in children/ (spec.dir) and the team's issues/.
			if dir != issuesDirIno(teamID) {
				lfs.InvalidateCreated(issuesDirIno(teamID), i.Identifier)
			}
		},
	}
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

	// _create accepts a full issue spec; .error reports the last failed issue
	// creation, .last the identities of recent successful creations (#149/#151).
	entries := make([]fuse.DirEntry, 0, len(issues)+3)
	entries = append(entries, fuse.DirEntry{Name: "_create", Mode: syscall.S_IFREG})
	entries = append(entries, fuse.DirEntry{Name: ".error", Mode: syscall.S_IFREG})
	entries = append(entries, fuse.DirEntry{Name: ".last", Mode: syscall.S_IFREG})
	for _, issue := range issues {
		entries = append(entries, fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFDIR,
		})
	}

	return fs.NewListDirStream(entries), 0
}

func (n *IssuesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Handle _create trigger (full-object issue creation). The buffer lives on
	// the per-open handle, so kernel node reuse is harmless and the lookup can
	// use the standard cache timeouts.
	if name == "_create" {
		now := time.Now()
		node := newCreateFile(n.lfs, n.createIssue)
		out.Attr.Mode = 0200 | syscall.S_IFREG
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = 0
		out.Attr.SetTimes(&now, &now, &now)
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}
	// Handle .error feedback file (last failed issue creation in this team)
	if name == ".error" {
		return n.lfs.lookupErrorFile(ctx, n, collectionErrorKey("issues", n.team.ID), out), 0
	}
	// Handle .last feedback file (identities of recent successful creations)
	if name == ".last" {
		return n.lfs.lookupSuccessFile(ctx, n, collectionSuccessKey("issues", n.team.ID), out), 0
	}

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

// retryableCreateErr reports whether a create/mutation error is transient —
// rate limiting, a cancelled/timed-out rate-limit wait, or the connectivity
// circuit breaker — and therefore worth retrying, versus a permanent failure.
// Transient creation failures are surfaced as EAGAIN so the caller (or an LLM)
// knows to retry rather than treating it as a hard error.
func retryableCreateErr(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "rate limit") || strings.Contains(msg, "circuit breaker")
}

// Mkdir creates a new issue from a directory name
func (n *IssuesNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Mkdir: %s in team %s (creating issue)", name, n.team.Key)
	}

	// Quick path: title-only spec. Full-object creation goes through issues/_create.
	issue, errno := commitCreate(ctx, n.lfs, n.lfs.issueCreateSpec(
		n.team.ID,
		`create issue "`+name+`"`,
		collectionErrorKey("issues", n.team.ID),
		issuesDirIno(n.team.ID),
		func(ctx context.Context) (*api.Issue, error) {
			return n.lfs.createIssueFromSpec(ctx, n.team, map[string]any{"title": name})
		},
	))
	if errno != 0 {
		return nil, errno
	}

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

// createIssue is the issues/_create surface's onFlush: writing a full issue
// spec (frontmatter + body) creates one issue with all fields set at birth,
// resolving names to IDs and reporting the new identity to issues/.last (#151).
func (n *IssuesNode) createIssue(ctx context.Context, content []byte) syscall.Errno {
	_, errno := commitCreate(ctx, n.lfs, n.lfs.issueCreateSpec(
		n.team.ID,
		"create issue from spec",
		collectionErrorKey("issues", n.team.ID),
		issuesDirIno(n.team.ID),
		func(ctx context.Context) (*api.Issue, error) {
			spec, err := marshal.MarkdownToIssueCreate(content)
			if err != nil {
				// Normalize the marshal parse/validation error to the
				// Field/Value/Error shape so it matches the resolver's
				// EINVAL errors.
				field := "frontmatter"
				msg := err.Error()
				if strings.HasPrefix(msg, "priority:") {
					field = "priority"
					msg = strings.TrimSpace(strings.TrimPrefix(msg, "priority:"))
				}
				return nil, &FieldError{Field: field, Message: msg}
			}
			return n.lfs.createIssueFromSpec(ctx, n.team, spec)
		},
	))
	return errno
}

// Rmdir archives an issue (soft delete)
func (n *IssuesNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Rmdir: %s in team %s (archiving issue)", name, n.team.Key)
	}

	return commitDelete(ctx, n.lfs, deleteSpec[api.Issue]{
		op:  `archive issue "` + name + `"`,
		key: collectionErrorKey("issues", n.team.ID),
		find: func(ctx context.Context) (*api.Issue, error) {
			issues, err := n.lfs.GetTeamIssues(ctx, n.team.ID)
			if err != nil {
				return nil, err
			}
			for _, issue := range issues {
				if issue.Identifier == name {
					return &issue, nil
				}
			}
			return nil, nil
		},
		mutate: func(ctx context.Context, i *api.Issue) error {
			return n.lfs.mutator().ArchiveIssue(ctx, i.ID)
		},
		// The store forget was missing here: the archived issue's row stayed in
		// SQLite (the listing source of truth), so it resurrected on the next
		// readdir until the sync worker reconciled.
		forget: func(ctx context.Context, i *api.Issue) error {
			return n.lfs.store.Queries().DeleteIssue(ctx, i.ID)
		},
		dir:  issuesDirIno(n.team.ID),
		name: name,
		invalidateExtra: func(i *api.Issue) {
			// The archived issue must also vanish from recent/ immediately
			// (symmetric with the create tail's recent/ coherence).
			n.lfs.InvalidateDeleted(recentDirIno(n.team.ID), name)
		},
	})
}

// IssueDirectoryNode represents /teams/{KEY}/issues/{ID}/ directory
type IssueDirectoryNode struct {
	BaseNode
	issue api.Issue
}

var _ fs.NodeReaddirer = (*IssueDirectoryNode)(nil)
var _ fs.NodeLookuper = (*IssueDirectoryNode)(nil)
var _ fs.NodeGetattrer = (*IssueDirectoryNode)(nil)
var _ fs.NodeCreater = (*IssueDirectoryNode)(nil)
var _ fs.NodeRenamer = (*IssueDirectoryNode)(nil)
var _ fs.NodeUnlinker = (*IssueDirectoryNode)(nil)

func (n *IssueDirectoryNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
	return 0
}

func (n *IssueDirectoryNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "issue.md", Mode: syscall.S_IFREG},
		{Name: "issue.meta", Mode: syscall.S_IFREG},
		{Name: "history.md", Mode: syscall.S_IFREG},
		{Name: ".error", Mode: syscall.S_IFREG},
		{Name: ".last", Mode: syscall.S_IFREG},
		{Name: "comments", Mode: syscall.S_IFDIR},
		{Name: "docs", Mode: syscall.S_IFDIR},
		{Name: "children", Mode: syscall.S_IFDIR},
		{Name: "attachments", Mode: syscall.S_IFDIR},
		{Name: "relations", Mode: syscall.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (n *IssueDirectoryNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "issue.md":
		// issue.md is editable-only; identity/links/relations live in issue.meta.
		content, err := marshal.IssueToMarkdown(&n.issue)
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
		out.SetAttrTimeout(30 * time.Second)
		out.SetEntryTimeout(30 * time.Second)
		out.Attr.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  issueIno(n.issue.ID),
		}), 0

	case "issue.meta":
		// Read-only server-managed fields (identity, timestamps, links, relations),
		// rendered read-through from the freshest issue in the repo so an edit to
		// issue.md is reflected here (go-fuse reuses this node across lookups).
		lfs := n.lfs
		ident := n.issue.Identifier
		snapshot := n.issue
		render := func() ([]byte, time.Time, time.Time) {
			iss := &snapshot
			if fresh, err := lfs.FetchIssueByIdentifier(context.Background(), ident); err == nil && fresh != nil {
				iss = fresh
			}
			att, _ := lfs.GetIssueAttachments(context.Background(), iss.ID)
			b, err := marshal.IssueMetaToMarkdown(iss, att...)
			if err != nil {
				return nil, iss.UpdatedAt, iss.CreatedAt
			}
			return b, iss.UpdatedAt, iss.CreatedAt
		}
		return n.lfs.lookupMetaFile(ctx, n, n.issue.ID, render, out), 0

	case "history.md":
		// Fetch history content during Lookup so we can set the actual size
		// (kernel won't issue READ if size is 0)
		entries, err := n.lfs.GetIssueHistory(ctx, n.issue.ID)
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
		return n.lfs.lookupErrorFile(ctx, n, n.issue.ID, out), 0

	case ".last":
		// Successes of sub-issues created under this issue (via children/).
		return n.lfs.lookupSuccessFile(ctx, n, n.issue.ID, out), 0

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

	case "relations":
		teamID := ""
		if n.issue.Team != nil {
			teamID = n.issue.Team.ID
		}
		node := &RelationsNode{
			BaseNode: BaseNode{lfs: n.lfs},
			issueID:  n.issue.ID,
			teamID:   teamID,
		}
		out.Attr.Mode = 0755 | syscall.S_IFDIR
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.SetTimes(&n.issue.UpdatedAt, &n.issue.UpdatedAt, &n.issue.CreatedAt)
		out.SetAttrTimeout(30 * time.Second)
		out.SetEntryTimeout(30 * time.Second)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFDIR,
			Ino:  relationsDirIno(n.issue.ID),
		}), 0
	}

	return nil, syscall.ENOENT
}

// Create accepts an editor's atomic-save temp file (e.g. issue.md.tmp.<pid>.<rand>)
// as an in-memory scratch buffer. Rename then routes its bytes into issue.md's
// write path. Without this, go-fuse rejects the temp-file create with a
// misleading EROFS even though the mount is rw and issue.md is writable (#145).
func (n *IssueDirectoryNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Create scratch file in %s: %s", n.issue.Identifier, name)
	}
	return newScratchInode(ctx, &n.BaseNode, issueDirIno(n.issue.ID), name, out)
}

// Rename persists an editor's atomic save: when a scratch temp file is renamed
// onto issue.md, its buffered bytes are written through the same path a direct
// in-place edit uses (frontmatter validation, read-your-writes verification,
// .error handling, cache invalidation). issue.md is the only writable file here,
// so renames onto any other target — or of the canonical files themselves — are
// rejected.
func (n *IssueDirectoryNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.lfs.debug {
		log.Printf("Rename in %s: %s -> %s", n.issue.Identifier, name, newName)
	}

	// The atomic-save pattern keeps the temp file a sibling of issue.md.
	if newParent.EmbeddedInode().StableAttr().Ino != n.EmbeddedInode().StableAttr().Ino {
		return syscall.EXDEV
	}

	content, ok := scratchRenameBytes(n, name)
	if !ok {
		// name isn't a scratch file we created — e.g. an attempt to rename issue.md
		// itself. The canonical files aren't renamable.
		return syscall.ENOTSUP
	}

	if newName != "issue.md" {
		// A scratch file only has somewhere to persist when renamed onto issue.md,
		// the one editable file in this directory.
		n.lfs.SetIssueError(n.issue.ID, fmt.Sprintf("Operation: rename %s -> %s\nError: only issue.md is writable in this directory; save your changes onto issue.md (atomic save-via-rename onto issue.md is supported).", name, newName))
		return syscall.ENOTSUP
	}

	// Route the buffered bytes through the normal issue write path via a transient
	// file node. Flush returns 0 on success, EINVAL on a parse/validation error,
	// and EIO only on a fatal read-your-writes divergence (the write still reached
	// Linear in that case).
	fileNode := &IssueFileNode{
		BaseNode:     BaseNode{lfs: n.lfs},
		issue:        n.issue,
		content:      content,
		contentReady: true,
		dirty:        true,
	}
	errno := fileNode.Flush(ctx, nil)

	if errno == 0 || errno == syscall.EIO {
		// The write reached Linear. Adopt the fresh issue so issue.md re-renders the
		// stored content, and drop the kernel caches: go-fuse will MvChild the spent
		// scratch inode over issue.md, so issue.md must re-Lookup to a fresh
		// IssueFileNode rather than serve the consumed scratch node.
		n.issue = fileNode.issue
		n.lfs.InvalidateRenamed(issueDirIno(n.issue.ID), name, newName, issueIno(n.issue.ID))
	}

	return errno
}

// Unlink lets editors clean up an abandoned atomic-save temp file (when a save
// is aborted before the rename). Only scratch files we created are removable;
// the canonical entries (issue.md, comments, etc.) are not.
func (n *IssueDirectoryNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if _, ok := scratchRenameBytes(n, name); ok {
		return 0
	}
	return syscall.EPERM
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
	// issue.md is editable-only; links/attachments live in issue.meta.
	content, err := marshal.IssueToMarkdown(&i.issue)
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

	// Resolve the name-bearing relational fields (status, assignee, labels,
	// parent, project, milestone, cycle) to Linear IDs. The resolver owns field
	// ordering, the label-clearing special case, and the per-field error messages.
	if ferr := resolveIssueUpdate(ctx, i.lfs, &i.issue, updates); ferr != nil {
		log.Printf("Failed to resolve update for %s: %s", i.issue.Identifier, ferr.Message)
		i.lfs.SetIssueError(i.issue.ID, ferr.Detail())
		return syscall.EINVAL
	}

	// Call Linear API to update
	if err := i.lfs.mutator().UpdateIssue(ctx, i.issue.ID, updates); err != nil {
		log.Printf("Failed to update issue %s: %v", i.issue.Identifier, err)
		i.lfs.SetIssueError(i.issue.ID, "API error: "+err.Error())
		return syscall.EIO
	}

	if i.lfs.debug {
		log.Printf("Flush: %s updated successfully", i.issue.Identifier)
	}

	// Invalidate kernel cache for this file
	i.lfs.InvalidateUpdated(issueIno(i.issue.ID))
	i.lfs.InvalidateUpdated(metaIno(i.issue.ID)) // issue.meta reflects the edit

	// Edit-commit tail: re-fetch from the API (an independent read catches #136,
	// where a large body silently reverts), verify read-your-writes against the
	// pre-write values still on i.issue, upsert the fresh value, and surface any
	// divergence via .error. The compare closure runs inside commitWriteBack,
	// before i.issue is overwritten below.
	fresh, errno := commitWriteBack(ctx, i.lfs, writeBackSpec[api.Issue]{
		errKey:  i.issue.ID,
		fetch:   func(ctx context.Context) (*api.Issue, error) { return i.lfs.verify().GetIssue(ctx, i.issue.ID) },
		persist: func(ctx context.Context, fresh *api.Issue) error { return i.lfs.UpsertIssue(ctx, *fresh) },
		compare: func(fresh *api.Issue) []writeBackResult {
			var results []writeBackResult
			if want, ok := updates["title"].(string); ok {
				results = append(results, writeBackDivergence("title", want, fresh.Title, i.issue.Title))
			}
			if want, ok := updates["description"].(string); ok {
				results = append(results, writeBackDivergence("description (body)", want, fresh.Description, i.issue.Description))
			}
			return results
		},
	})
	if fresh != nil {
		i.issue = *fresh
	}
	i.dirty = false
	i.contentReady = false // Force re-generate on next read
	return errno
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
var _ fs.NodeMkdirer = (*ChildrenNode)(nil)

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

// Mkdir creates a new sub-issue (child issue) with the given title
func (n *ChildrenNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.lfs.debug {
		log.Printf("Mkdir: creating sub-issue %q under %s", name, n.issue.Identifier)
	}

	// Get team ID from parent issue
	teamID := ""
	if n.issue.Team != nil {
		teamID = n.issue.Team.ID
	}
	if teamID == "" {
		log.Printf("Cannot create sub-issue: parent issue %s has no team", n.issue.Identifier)
		return nil, syscall.EIO
	}

	// Sub-issue creation reports to the parent issue's own .error/.last
	// (issues/{ID}/.error), the nearest writable feedback files; the spec's
	// invalidateExtra also refreshes the team's issues/ listing since a
	// sub-issue lands in both children/ and issues/.
	issue, errno := commitCreate(ctx, n.lfs, n.lfs.issueCreateSpec(
		teamID,
		`create sub-issue "`+name+`"`,
		n.issue.ID,
		childrenDirIno(n.issue.ID),
		func(ctx context.Context) (*api.Issue, error) {
			return n.lfs.mutator().CreateIssue(ctx, map[string]any{
				"teamId":   teamID,
				"title":    name,
				"parentId": n.issue.ID,
			})
		},
	))
	if errno != 0 {
		return nil, errno
	}

	// Return the new issue as a directory node (Mkdir must return a directory)
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

	// Fetch history via repo (cached in SQLite)
	entries, err := h.lfs.GetIssueHistory(ctx, h.issueID)
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
