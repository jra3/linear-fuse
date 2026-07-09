package fs

import (
	"context"
	"errors"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

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

// IssuesNode represents the /teams/{KEY}/issues directory. It holds a team
// snapshot and reports the team's times; Getattr comes from the attrNode mixin.
type IssuesNode struct {
	attrNode
	team api.Team
}

var _ fs.NodeReaddirer = (*IssuesNode)(nil)
var _ fs.NodeLookuper = (*IssuesNode)(nil)
var _ fs.NodeMkdirer = (*IssuesNode)(nil)
var _ fs.NodeRmdirer = (*IssuesNode)(nil)
var _ fs.NodeGetattrer = (*IssuesNode)(nil)

// entity/setEntity snapshot and swap the directory's team under the node's
// volatile-state lock; setEntity is written by the nodeRefresher seam
// (refresh.go).
func (n *IssuesNode) entity() api.Team {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.team
}

func (n *IssuesNode) setEntity(team api.Team) {
	n.stateMu.Lock()
	n.team = team
	n.stateMu.Unlock()
}

func (n *IssuesNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*IssuesNode); ok {
		n.setEntity(f.team)
	}
}

func (n *IssuesNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	issues, err := n.lfs.repo.GetTeamIssues(ctx, n.entity().ID)
	if err != nil {
		return nil, syscall.EIO
	}

	// _create accepts a full issue spec (#149/#151).
	entries := n.trio().entries()
	for _, issue := range issues {
		entries = append(entries, fuse.DirEntry{
			Name: issue.Identifier,
			Mode: syscall.S_IFDIR,
		})
	}

	return fs.NewListDirStream(entries), 0
}

// trio declares the issues collection's writable surfaces: _create takes a
// full issue spec (frontmatter + body).
func (n *IssuesNode) trio() collectionTrio {
	return collectionTrio{kind: "issues", parentID: n.entity().ID, onFlush: n.createIssue}
}

func (n *IssuesNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
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

	node := &IssueDirectoryNode{attrNode: attrNode{BaseNode: BaseNode{lfs: n.lfs}}, issue: *issue}
	return n.newDirInode(ctx, out, issue.Identifier, node, dirAttr(issue.CreatedAt, issue.UpdatedAt), issueDirIno(issue.ID), 30*time.Second), 0
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
	// Rate-limit detection is the shared predicate's job; the circuit breaker
	// stays here — it is a client-side connectivity transient (worth retrying),
	// not the server rate limiting us, so api.IsRateLimited excludes it.
	return api.IsRateLimited(err) || strings.Contains(err.Error(), "circuit breaker")
}

// Mkdir creates a new issue from a directory name
func (n *IssuesNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	team := n.entity()
	if n.lfs.debug {
		log.Printf("Mkdir: %s in team %s (creating issue)", name, team.Key)
	}

	// Quick path: title-only spec. Full-object creation goes through issues/_create.
	issue, errno := commitCreate(ctx, n.lfs, n.lfs.issueCreateSpec(
		team.ID,
		`create issue "`+name+`"`,
		collectionErrorKey("issues", team.ID),
		issuesDirIno(team.ID),
		func(ctx context.Context) (*api.Issue, error) {
			return n.lfs.createIssueFromSpec(ctx, team, map[string]any{"title": name})
		},
	))
	if errno != 0 {
		return nil, errno
	}

	node := &IssueDirectoryNode{attrNode: attrNode{BaseNode: BaseNode{lfs: n.lfs}}, issue: *issue}
	return n.newDirInode(ctx, out, issue.Identifier, node, dirAttr(issue.CreatedAt, issue.UpdatedAt), issueDirIno(issue.ID), 30*time.Second), 0
}

// createIssue is the issues/_create surface's onFlush: writing a full issue
// spec (frontmatter + body) creates one issue with all fields set at birth,
// resolving names to IDs and reporting the new identity to issues/.last (#151).
func (n *IssuesNode) createIssue(ctx context.Context, content []byte) syscall.Errno {
	team := n.entity()
	_, errno := commitCreate(ctx, n.lfs, n.lfs.issueCreateSpec(
		team.ID,
		"create issue from spec",
		collectionErrorKey("issues", team.ID),
		issuesDirIno(team.ID),
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
			return n.lfs.createIssueFromSpec(ctx, team, spec)
		},
	))
	return errno
}

// Rmdir archives an issue (soft delete)
func (n *IssuesNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	team := n.entity()
	if n.lfs.debug {
		log.Printf("Rmdir: %s in team %s (archiving issue)", name, team.Key)
	}

	return commitDelete(ctx, n.lfs, deleteSpec[api.Issue]{
		op:  `archive issue "` + name + `"`,
		key: collectionErrorKey("issues", team.ID),
		find: func(ctx context.Context) (*api.Issue, error) {
			issues, err := n.lfs.repo.GetTeamIssues(ctx, team.ID)
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
		dir:  issuesDirIno(team.ID),
		name: name,
		invalidateExtra: func(i *api.Issue) {
			// The archived issue must also vanish from recent/ immediately
			// (symmetric with the create tail's recent/ coherence).
			n.lfs.InvalidateDeleted(recentDirIno(team.ID), name)
		},
	})
}

// IssueDirectoryNode represents /teams/{KEY}/issues/{ID}/ directory
type IssueDirectoryNode struct {
	attrNode
	issue api.Issue
}

var _ fs.NodeReaddirer = (*IssueDirectoryNode)(nil)
var _ fs.NodeLookuper = (*IssueDirectoryNode)(nil)
var _ fs.NodeCreater = (*IssueDirectoryNode)(nil)
var _ fs.NodeRenamer = (*IssueDirectoryNode)(nil)
var _ fs.NodeUnlinker = (*IssueDirectoryNode)(nil)

func (n *IssueDirectoryNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return fs.NewListDirStream(n.manifest().entries()), 0
}

func (n *IssueDirectoryNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if child, ok := n.manifest().find(name); ok {
		return child.build(ctx, out)
	}
	return nil, syscall.ENOENT
}

// manifest declares an issue directory's static children: the editable issue.md,
// the read-through issue.meta, the generated history.md, the .error/.last
// sidecars, and the comments/docs/children/attachments/relations subdirs. Issue
// children have no dynamic tail and a uniform 30s timeout.
// entity/setEntity snapshot and swap the directory's issue under the node's
// volatile-state lock: setEntity is written by the Rename write-back and the
// nodeRefresher seam (refresh.go), which pushes freshly-fetched state into
// this node when go-fuse dedups a later Lookup onto it.
func (n *IssueDirectoryNode) entity() api.Issue {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.issue
}

func (n *IssueDirectoryNode) setEntity(issue api.Issue) {
	n.stateMu.Lock()
	n.issue = issue
	n.stateMu.Unlock()
}

func (n *IssueDirectoryNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*IssueDirectoryNode); ok {
		n.setEntity(f.issue)
	}
}

func (n *IssueDirectoryNode) manifest() *dirManifest {
	issue := n.entity() // snapshot captured by the build closures
	teamID := ""
	if issue.Team != nil {
		teamID = issue.Team.ID
	}
	m := newDirManifest(&n.BaseNode, issue.ID, issue.CreatedAt, issue.UpdatedAt, 30*time.Second)

	// issue.md is editable-only; identity/links/relations live in issue.meta.
	m.file("issue.md", issueIno(issue.ID), func(ctx context.Context) (fs.InodeEmbedder, []byte, syscall.Errno) {
		content, err := marshal.IssueToMarkdown(&issue)
		if err != nil {
			return nil, nil, syscall.EIO
		}
		return &IssueFileNode{
			BaseNode:   BaseNode{lfs: n.lfs},
			issue:      issue,
			editBuffer: editBuffer{content: content},
		}, content, 0
	})

	// issue.meta: read-only server-managed fields, rendered read-through from the
	// freshest issue so an edit to issue.md is reflected here.
	lfs := n.lfs
	ident := issue.Identifier
	m.metaFile("issue.meta", func(ctx context.Context) ([]byte, time.Time, time.Time) {
		iss := &issue
		if fresh, err := lfs.FetchIssueByIdentifier(ctx, ident); err == nil && fresh != nil {
			iss = fresh
		}
		att, _ := lfs.repo.GetIssueAttachments(ctx, iss.ID)
		b, err := marshal.IssueMetaToMarkdown(iss, att...)
		if err != nil {
			return nil, iss.UpdatedAt, iss.CreatedAt
		}
		return b, iss.UpdatedAt, iss.CreatedAt
	})

	// history.md: a read-only generated file, rendered fresh from the issue's
	// activity history on each read. It reports the issue's own times; a transient
	// fetch failure renders an empty file rather than making the entry vanish.
	m.renderFile("history.md", historyIno(issue.ID), func(ctx context.Context) ([]byte, time.Time, time.Time) {
		entries, err := lfs.repo.GetIssueHistory(ctx, issue.ID)
		if err != nil {
			log.Printf("Failed to fetch history for %s: %v", issue.Identifier, err)
			return nil, issue.UpdatedAt, issue.CreatedAt
		}
		return marshal.HistoryToMarkdown(issue.Identifier, entries), issue.UpdatedAt, issue.CreatedAt
	})

	m.errorFile(".error")
	m.lastFile(".last") // successes of sub-issues created under this issue (via children/)

	m.subdir("comments", commentsDirIno(issue.ID), func() dirChild {
		return &CommentsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: n.lfs}}, issueID: issue.ID, teamID: teamID}
	})
	m.subdir("docs", docsDirIno(issue.ID), func() dirChild {
		return &DocsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: n.lfs}}, issueID: issue.ID}
	})
	m.subdir("children", childrenDirIno(issue.ID), func() dirChild {
		return &ChildrenNode{attrNode: attrNode{BaseNode: BaseNode{lfs: n.lfs}}, issue: issue}
	})
	m.subdir("attachments", attachmentsDirIno(issue.ID), func() dirChild {
		return &AttachmentsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: n.lfs}}, issueID: issue.ID}
	})
	m.subdir("relations", relationsDirIno(issue.ID), func() dirChild {
		return &RelationsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: n.lfs}}, issueID: issue.ID, teamID: teamID}
	})

	return m
}

// Create accepts an editor's atomic-save temp file (e.g. issue.md.tmp.<pid>.<rand>)
// as an in-memory scratch buffer. Rename then routes its bytes into issue.md's
// write path. Without this, go-fuse rejects the temp-file create with a
// misleading EROFS even though the mount is rw and issue.md is writable (#145).
func (n *IssueDirectoryNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	issue := n.entity()
	if n.lfs.debug {
		log.Printf("Create scratch file in %s: %s", issue.Identifier, name)
	}
	return newScratchInode(ctx, &n.BaseNode, issueDirIno(issue.ID), name, out)
}

// Rename persists an editor's atomic save: a scratch temp file renamed onto
// issue.md is written through issue.md's normal Flush path. The tail (EXDEV /
// target guard / flush / adopt-on-{0,EIO} / invalidate) is the shared
// renameSave module.
func (n *IssueDirectoryNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	issue := n.entity()
	if n.lfs.debug {
		log.Printf("Rename in %s: %s -> %s", issue.Identifier, name, newName)
	}

	var fileNode *IssueFileNode
	return renameSave(ctx, n.lfs, name, newParent, newName, renameSaveSpec{
		targetName: "issue.md",
		errKey:     issue.ID,
		dirIno:     n.EmbeddedInode().StableAttr().Ino,
		fileIno:    issueIno(issue.ID),
		scratch:    func(oldName string) ([]byte, bool) { return scratchRenameBytes(n, oldName) },
		flush: func(ctx context.Context, content []byte) syscall.Errno {
			fileNode = &IssueFileNode{
				BaseNode:   BaseNode{lfs: n.lfs},
				issue:      issue,
				editBuffer: editBuffer{content: content, dirty: true},
			}
			return fileNode.Flush(ctx, nil)
		},
		adopt: func() { n.setEntity(fileNode.issue) },
	})
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
	editBuffer
	issue api.Issue

	// Write buffer and cached content
}

var _ fs.NodeGetattrer = (*IssueFileNode)(nil)
var _ fs.NodeOpener = (*IssueFileNode)(nil)
var _ fs.NodeReader = (*IssueFileNode)(nil)
var _ fs.NodeWriter = (*IssueFileNode)(nil)
var _ fs.NodeFlusher = (*IssueFileNode)(nil)
var _ fs.NodeFsyncer = (*IssueFileNode)(nil)
var _ fs.NodeSetattrer = (*IssueFileNode)(nil)

func (i *IssueFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	// One lock for size + times: a concurrent refresh (refresh.go) swaps
	// content and entity atomically, so the read must snapshot both together.
	i.mu.Lock()
	size := len(i.content)
	created, updated := i.issue.CreatedAt, i.issue.UpdatedAt
	i.mu.Unlock()
	fileAttr(size, created, updated).fill(&out.Attr, &i.BaseNode)
	return 0
}

// refreshFrom adopts a fresh twin's issue and rendered content unless an edit
// is in flight — the dirty buffer is the user's and always wins (refresh.go).
func (i *IssueFileNode) refreshFrom(fresh fs.InodeEmbedder) {
	if f, ok := fresh.(*IssueFileNode); ok {
		i.refresh(f.content, func() { i.issue = f.issue })
	}
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
		msg, errno := classifyMutationErr("update issue", err)
		i.lfs.SetIssueError(i.issue.ID, msg)
		return errno
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
	return errno
}

// ChildrenNode represents the /teams/{KEY}/issues/{ID}/children/ directory
type ChildrenNode struct {
	attrNode
	issue api.Issue
}

var _ fs.NodeReaddirer = (*ChildrenNode)(nil)
var _ fs.NodeLookuper = (*ChildrenNode)(nil)
var _ fs.NodeGetattrer = (*ChildrenNode)(nil)
var _ fs.NodeMkdirer = (*ChildrenNode)(nil)

func (n *ChildrenNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Query children from database by parent_id
	children, err := n.lfs.repo.GetIssueChildren(ctx, n.issue.ID)
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
	children, err := n.lfs.repo.GetIssueChildren(ctx, n.issue.ID)
	if err != nil {
		return nil, syscall.EIO
	}
	for _, child := range children {
		if child.Identifier == name {
			// The link lives at issues/{PARENT}/children/{ID}; the sibling
			// issue dir is two levels up.
			target := "../../" + child.Identifier
			return n.newSymlinkInode(ctx, out, target, child.CreatedAt, child.UpdatedAt), 0
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
	node := &IssueDirectoryNode{attrNode: attrNode{BaseNode: BaseNode{lfs: n.lfs}}, issue: *issue}
	return n.newDirInode(ctx, out, issue.Identifier, node, dirAttr(issue.CreatedAt, issue.UpdatedAt), issueDirIno(issue.ID), 30*time.Second), 0
}
