package fs

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// relationsDirIno generates a stable inode for an issue's relations directory
func relationsDirIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("relations:" + issueID))
	return h.Sum64()
}

// relationIno generates a stable inode for a relation file
func relationIno(relationID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("relation:" + relationID))
	return h.Sum64()
}

// relationsCreateIno generates a stable inode for the _create trigger file
func relationsCreateIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("relations-create:" + issueID))
	return h.Sum64()
}

// RelationsNode represents the /teams/{KEY}/issues/{ID}/relations directory
type RelationsNode struct {
	BaseNode
	issueID string
	teamID  string
}

var _ fs.NodeReaddirer = (*RelationsNode)(nil)
var _ fs.NodeLookuper = (*RelationsNode)(nil)
var _ fs.NodeGetattrer = (*RelationsNode)(nil)

func (n *RelationsNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *RelationsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Get both outgoing and incoming relations
	relations, err := n.lfs.repo.GetIssueRelations(ctx, n.issueID)
	if err != nil {
		return nil, syscall.EIO
	}
	inverseRelations, err := n.lfs.repo.GetIssueInverseRelations(ctx, n.issueID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Build entries: _create + relation files
	entries := []fuse.DirEntry{
		{Name: "_create", Mode: syscall.S_IFREG},
	}

	// Add outgoing relations (e.g., "blocks-ENG-123.rel")
	for _, rel := range relations {
		if rel.RelatedIssue != nil && rel.RelatedIssue.Identifier != "" {
			name := fmt.Sprintf("%s-%s.rel", rel.Type, rel.RelatedIssue.Identifier)
			entries = append(entries, fuse.DirEntry{Name: name, Mode: syscall.S_IFREG})
		}
	}

	// Add incoming relations (e.g., "blocked-by-ENG-456.rel")
	for _, rel := range inverseRelations {
		if rel.Issue != nil && rel.Issue.Identifier != "" {
			inverseName := inverseRelationType(rel.Type)
			name := fmt.Sprintf("%s-%s.rel", inverseName, rel.Issue.Identifier)
			entries = append(entries, fuse.DirEntry{Name: name, Mode: syscall.S_IFREG})
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (n *RelationsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == "_create" {
		node := &NewRelationNode{
			BaseNode: BaseNode{lfs: n.lfs},
			issueID:  n.issueID,
		}
		now := time.Now()
		out.Attr.Mode = 0200 | syscall.S_IFREG // Write-only
		out.Attr.Uid = n.lfs.uid
		out.Attr.Gid = n.lfs.gid
		out.Attr.Size = 0
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		out.Attr.SetTimes(&now, &now, &now)
		return n.NewInode(ctx, node, fs.StableAttr{
			Mode: syscall.S_IFREG,
			Ino:  relationsCreateIno(n.issueID),
		}), 0
	}

	// Parse relation filename: "type-IDENTIFIER.rel"
	if !strings.HasSuffix(name, ".rel") {
		return nil, syscall.ENOENT
	}

	baseName := strings.TrimSuffix(name, ".rel")
	// Find the relation
	relations, _ := n.lfs.repo.GetIssueRelations(ctx, n.issueID)
	for _, rel := range relations {
		if rel.RelatedIssue != nil && rel.RelatedIssue.Identifier != "" {
			expectedName := fmt.Sprintf("%s-%s", rel.Type, rel.RelatedIssue.Identifier)
			if baseName == expectedName {
				return n.createRelationFileNode(ctx, rel, false, out)
			}
		}
	}

	// Check inverse relations
	inverseRelations, _ := n.lfs.repo.GetIssueInverseRelations(ctx, n.issueID)
	for _, rel := range inverseRelations {
		if rel.Issue != nil && rel.Issue.Identifier != "" {
			inverseName := inverseRelationType(rel.Type)
			expectedName := fmt.Sprintf("%s-%s", inverseName, rel.Issue.Identifier)
			if baseName == expectedName {
				return n.createRelationFileNode(ctx, rel, true, out)
			}
		}
	}

	return nil, syscall.ENOENT
}

func (n *RelationsNode) createRelationFileNode(ctx context.Context, rel api.IssueRelation, isInverse bool, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	node := &RelationFileNode{
		BaseNode:  BaseNode{lfs: n.lfs},
		relation:  rel,
		isInverse: isInverse,
	}
	now := time.Now()
	content := node.generateContent()
	out.Attr.Mode = 0444 | syscall.S_IFREG // Read-only
	out.Attr.Uid = n.lfs.uid
	out.Attr.Gid = n.lfs.gid
	out.Attr.Size = uint64(len(content))
	out.SetAttrTimeout(30 * time.Second)
	out.SetEntryTimeout(30 * time.Second)
	out.Attr.SetTimes(&now, &now, &now)
	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  relationIno(rel.ID),
	}), 0
}

// inverseRelationType returns the inverse relation type name
func inverseRelationType(relType string) string {
	switch relType {
	case "blocks":
		return "blocked-by"
	case "duplicate":
		return "duplicated-by"
	case "related":
		return "related-to"
	case "similar":
		return "similar-to"
	default:
		return relType + "-inverse"
	}
}

// RelationFileNode represents a relation file (read-only info)
type RelationFileNode struct {
	BaseNode
	relation  api.IssueRelation
	isInverse bool
}

var _ fs.NodeGetattrer = (*RelationFileNode)(nil)
var _ fs.NodeOpener = (*RelationFileNode)(nil)
var _ fs.NodeReader = (*RelationFileNode)(nil)
var _ fs.NodeUnlinker = (*RelationFileNode)(nil)

func (n *RelationFileNode) generateContent() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("type: %s\n", n.relation.Type))

	if n.isInverse && n.relation.Issue != nil {
		sb.WriteString(fmt.Sprintf("from: %s\n", n.relation.Issue.Identifier))
		if n.relation.Issue.Title != "" {
			sb.WriteString(fmt.Sprintf("title: %s\n", n.relation.Issue.Title))
		}
	} else if n.relation.RelatedIssue != nil {
		sb.WriteString(fmt.Sprintf("to: %s\n", n.relation.RelatedIssue.Identifier))
		if n.relation.RelatedIssue.Title != "" {
			sb.WriteString(fmt.Sprintf("title: %s\n", n.relation.RelatedIssue.Title))
		}
	}

	return sb.String()
}

func (n *RelationFileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := n.generateContent()
	now := time.Now()
	out.Mode = 0444 // Read-only
	n.SetOwner(out)
	out.Size = uint64(len(content))
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *RelationFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *RelationFileNode) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	content := []byte(n.generateContent())
	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return fuse.ReadResultData(content[off:end]), 0
}

func (n *RelationFileNode) Unlink(ctx context.Context, name string) syscall.Errno {
	// Only allow deleting outgoing relations (not inverse)
	if n.isInverse {
		return syscall.EPERM
	}

	// Delete via API
	if err := n.lfs.client.DeleteIssueRelation(ctx, n.relation.ID); err != nil {
		return syscall.EIO
	}

	// Delete from local DB
	if err := n.lfs.store.Queries().DeleteIssueRelation(ctx, n.relation.ID); err != nil {
		log.Printf("[relations] delete from DB failed: %v", err)
	}

	return 0
}

// NewRelationNode represents the _create file for creating new relations
type NewRelationNode struct {
	BaseNode
	issueID string
}

var _ fs.NodeGetattrer = (*NewRelationNode)(nil)
var _ fs.NodeSetattrer = (*NewRelationNode)(nil)
var _ fs.NodeOpener = (*NewRelationNode)(nil)
var _ fs.NodeWriter = (*NewRelationNode)(nil)
var _ fs.NodeFlusher = (*NewRelationNode)(nil)

func (n *NewRelationNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0200 // Write-only
	n.SetOwner(out)
	out.Size = 0
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *NewRelationNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	// Allow truncation (for > redirect) - just return success
	out.Mode = 0200
	n.SetOwner(out)
	out.Size = 0
	return 0
}

func (n *NewRelationNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return &relationCreateHandle{}, fuse.FOPEN_DIRECT_IO, 0
}

func (n *NewRelationNode) Write(ctx context.Context, fh fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	handle, ok := fh.(*relationCreateHandle)
	if !ok {
		return 0, syscall.EIO
	}
	handle.buffer = append(handle.buffer, data...)
	return uint32(len(data)), 0
}

func (n *NewRelationNode) Flush(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	handle, ok := fh.(*relationCreateHandle)
	if !ok || len(handle.buffer) == 0 {
		return 0
	}

	// Parse the content: "type identifier" or just "identifier" (defaults to "related")
	content := strings.TrimSpace(string(handle.buffer))
	handle.buffer = nil

	if content == "" {
		return syscall.EINVAL
	}

	parts := strings.Fields(content)
	var relationType, relatedIdentifier string

	if len(parts) == 1 {
		// Just identifier, default to "related"
		relationType = "related"
		relatedIdentifier = parts[0]
	} else if len(parts) >= 2 {
		relationType = parts[0]
		relatedIdentifier = parts[1]
	}

	// Validate relation type
	validTypes := map[string]bool{"blocks": true, "duplicate": true, "related": true, "similar": true}
	if !validTypes[relationType] {
		return syscall.EINVAL
	}

	// Lookup the related issue
	relatedIssue, err := n.lfs.repo.GetIssueByIdentifier(ctx, relatedIdentifier)
	if err != nil || relatedIssue == nil {
		return syscall.ENOENT
	}

	// Create the relation via API
	rel, err := n.lfs.client.CreateIssueRelation(ctx, n.issueID, relatedIssue.ID, relationType)
	if err != nil {
		return syscall.EIO
	}

	// Store in local DB
	now := time.Now()
	if err := n.lfs.store.Queries().UpsertIssueRelation(ctx, db.UpsertIssueRelationParams{
		ID:             rel.ID,
		IssueID:        n.issueID,
		RelatedIssueID: relatedIssue.ID,
		Type:           relationType,
		CreatedAt:      sql.NullTime{Time: now, Valid: true},
		UpdatedAt:      sql.NullTime{Time: now, Valid: true},
		SyncedAt:       now,
	}); err != nil {
		log.Printf("[relations] upsert to DB failed: %v", err)
	}

	// Invalidate cache
	n.lfs.InvalidateKernelInode(relationsDirIno(n.issueID))

	return 0
}

type relationCreateHandle struct {
	buffer []byte
}
