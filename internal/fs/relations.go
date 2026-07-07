package fs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// RelationsNode represents the /teams/{KEY}/issues/{ID}/relations directory
type RelationsNode struct {
	attrNode
	issueID string
	teamID  string
}

var _ fs.NodeReaddirer = (*RelationsNode)(nil)
var _ fs.NodeLookuper = (*RelationsNode)(nil)
var _ fs.NodeGetattrer = (*RelationsNode)(nil)

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

	entries := n.trio().entries()

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

// trio declares the relations collection's writable surfaces.
func (n *RelationsNode) trio() collectionTrio {
	return collectionTrio{kind: "relations", parentID: n.issueID, onFlush: n.createRelation}
}

func (n *RelationsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok {
		return inode, 0
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
	render := relationRender(rel, isInverse)
	node := &RelationFileNode{
		renderFileNode: renderFileNode{BaseNode: BaseNode{lfs: n.lfs}, render: render},
		relation:       rel,
		isInverse:      isInverse,
		issueID:        n.issueID,
	}
	return n.lfs.newRenderInode(ctx, n, node, render, relationIno(rel.ID), out), 0
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

// RelationFileNode is a read-only rendered file (renderfile.go) that also deletes:
// it embeds renderFileNode for the read surface and adds Unlink. api.IssueRelation
// carries no timestamps, so relationRender reports now() (the timestamp-less
// exception).
type RelationFileNode struct {
	renderFileNode
	relation  api.IssueRelation
	isInverse bool
	issueID   string // parent issue (for the relations/ .error key)
}

var _ fs.NodeUnlinker = (*RelationFileNode)(nil)

func relationRender(rel api.IssueRelation, isInverse bool) renderFn {
	return func(context.Context) ([]byte, time.Time, time.Time) {
		now := time.Now()
		return relationMarkdown(rel, isInverse), now, now
	}
}

func relationMarkdown(rel api.IssueRelation, isInverse bool) []byte {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("type: %s\n", rel.Type))

	if isInverse && rel.Issue != nil {
		sb.WriteString(fmt.Sprintf("from: %s\n", rel.Issue.Identifier))
		if rel.Issue.Title != "" {
			sb.WriteString(fmt.Sprintf("title: %s\n", rel.Issue.Title))
		}
	} else if rel.RelatedIssue != nil {
		sb.WriteString(fmt.Sprintf("to: %s\n", rel.RelatedIssue.Identifier))
		if rel.RelatedIssue.Title != "" {
			sb.WriteString(fmt.Sprintf("title: %s\n", rel.RelatedIssue.Title))
		}
	}

	return []byte(sb.String())
}

func (n *RelationFileNode) Unlink(ctx context.Context, name string) syscall.Errno {
	// Only allow deleting outgoing relations (not inverse)
	if n.isInverse {
		return syscall.EPERM
	}

	// The file node already holds its entity, so find just hands it over.
	return commitDelete(ctx, n.lfs, deleteSpec[api.IssueRelation]{
		op:   `delete relation "` + name + `"`,
		key:  collectionErrorKey("relations", n.issueID),
		find: func(context.Context) (*api.IssueRelation, error) { return &n.relation, nil },
		mutate: func(ctx context.Context, r *api.IssueRelation) error {
			return n.lfs.mutator().DeleteIssueRelation(ctx, r.ID)
		},
		forget: func(ctx context.Context, r *api.IssueRelation) error {
			return n.lfs.store.Queries().DeleteIssueRelation(ctx, r.ID)
		},
		dir:  relationsDirIno(n.issueID),
		name: name,
	})
}

// createRelation is the relations create surface's onFlush: parse
// "type identifier" (or just "identifier", defaulting to "related") and run
// the create tail.
func (n *RelationsNode) createRelation(ctx context.Context, raw []byte) syscall.Errno {
	content := strings.TrimSpace(string(raw))

	// relatedID carries the resolved target issue's ID from mutate to persist
	// (the API's echoed relation doesn't include it).
	var relatedID string
	var relationType, relatedIdentifier string

	_, errno := commitCreate(ctx, n.lfs, createSpec[api.IssueRelation]{
		op:  "create relation",
		key: collectionErrorKey("relations", n.issueID),
		mutate: func(ctx context.Context) (*api.IssueRelation, error) {
			if content == "" {
				return nil, &FieldError{Field: "content", Message: `empty content. Write "<type> <ISSUE-ID>", e.g. "blocks ENG-123".`}
			}
			parts := strings.Fields(content)
			if len(parts) == 1 {
				// Just identifier, default to "related"
				relationType = "related"
				relatedIdentifier = parts[0]
			} else {
				relationType = parts[0]
				relatedIdentifier = parts[1]
			}

			validTypes := map[string]bool{"blocks": true, "duplicate": true, "related": true, "similar": true}
			if !validTypes[relationType] {
				return nil, &FieldError{Field: "type", Value: relationType, Message: "invalid relation type. Use one of: blocks, duplicate, related, similar."}
			}

			relatedIssue, err := n.lfs.repo.GetIssueByIdentifier(ctx, relatedIdentifier)
			if err != nil || relatedIssue == nil {
				return nil, &notFoundError{FieldError{Field: "identifier", Value: relatedIdentifier, Message: "unknown issue. Use an existing issue identifier like ENG-123."}}
			}
			relatedID = relatedIssue.ID

			return n.lfs.mutator().CreateIssueRelation(ctx, n.issueID, relatedIssue.ID, relationType)
		},
		result: func(*api.IssueRelation) WriteResult {
			return WriteResult{
				Path:  relationType + "-" + relatedIdentifier + ".rel",
				Title: relationType + " " + relatedIdentifier,
			}
		},
		persist: func(ctx context.Context, rel *api.IssueRelation) error {
			now := time.Now()
			return n.lfs.store.Queries().UpsertIssueRelation(ctx, db.UpsertIssueRelationParams{
				ID:             rel.ID,
				IssueID:        n.issueID,
				RelatedIssueID: relatedID,
				Type:           relationType,
				CreatedAt:      sql.NullTime{Time: now, Valid: true},
				UpdatedAt:      sql.NullTime{Time: now, Valid: true},
				SyncedAt:       now,
			})
		},
		dir: relationsDirIno(n.issueID),
		entryName: func(*api.IssueRelation) string {
			return relationType + "-" + relatedIdentifier + ".rel"
		},
	})
	return errno
}
