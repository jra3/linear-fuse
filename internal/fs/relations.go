package fs

import (
	"context"
	"database/sql"
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
var _ fs.NodeUnlinker = (*RelationsNode)(nil)

// dir constructs the read-only listing head. Unlike attachments' best-effort
// Readdir (two independent sources), both fetches here hit the same table, so a
// failure in either fails the whole directory (failReaddirOnError). Every
// relation file is named "{type}-{IDENTIFIER}.rel", so preFilter skips the two
// repo fetches for any other name.
func (n *RelationsNode) dir() listingDir[relationEntry] {
	return listingDir[relationEntry]{
		parent:             n,
		lfs:                n.lfs,
		trio:               n.trio(),
		listing:            func(ctx context.Context, fetchErr *error) infoListing[relationEntry] { return n.listing(ctx, fetchErr) },
		nameOf:             func(e relationEntry) string { return e.name },
		failReaddirOnError: true,
		preFilter:          func(name string) bool { return strings.HasSuffix(name, ".rel") },
		build: func(ctx context.Context, name string, e relationEntry, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
			return n.createRelationFileNode(ctx, name, e.relation, e.isInverse, out)
		},
		unlinkEntry: n.deleteRelation,
	}
}

func (n *RelationsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	return n.dir().readdir(ctx)
}

func (n *RelationsNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.dir().unlink(ctx, name)
}

// deleteRelation is the relations unlink tail (listingDir.unlinkEntry): only an
// outgoing relation can be deleted — the inverse endpoint (blocked-by-*.rel) is
// a projection of the same edge, so deleting it is EPERM (delete from the owning
// side). The resolved entry already holds the relation, so find just hands it
// over.
func (n *RelationsNode) deleteRelation(ctx context.Context, name string, e relationEntry) syscall.Errno {
	if e.isInverse {
		return syscall.EPERM
	}
	rel := e.relation
	return commitDelete(ctx, n.lfs, deleteSpec[api.IssueRelation]{
		op:  `delete relation "` + name + `"`,
		key: collectionErrorKey("relations", n.issueID),
		find: func(context.Context) (*api.IssueRelation, error) {
			return &rel, nil
		},
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

// listing fetches both direction slices and builds the name-derivation module.
// When fetchErr is non-nil the first fetch error is also recorded there
// (Lookup distinguishes "not found" from "couldn't look").
func (n *RelationsNode) listing(ctx context.Context, fetchErr *error) relationListing {
	outgoing, oerr := n.lfs.repo.GetIssueRelations(ctx, n.issueID)
	inverse, ierr := n.lfs.repo.GetIssueInverseRelations(ctx, n.issueID)
	if fetchErr != nil {
		if oerr != nil {
			*fetchErr = oerr
		} else if ierr != nil {
			*fetchErr = ierr
		}
	}
	return relationListing{outgoing: outgoing, inverse: inverse}
}

// trio declares the relations collection's writable surfaces.
func (n *RelationsNode) trio() collectionTrio {
	return collectionTrio{kind: "relations", parentID: n.issueID, onFlush: n.createRelation}
}

func (n *RelationsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return n.dir().lookup(ctx, name, out)
}

func (n *RelationsNode) createRelationFileNode(ctx context.Context, name string, rel api.IssueRelation, isInverse bool, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	node := &RelationFileNode{
		renderFile: renderFile{
			BaseNode: BaseNode{lfs: n.lfs},
			render: func(context.Context) ([]byte, time.Time, time.Time) {
				return []byte(relationContent(rel, isInverse)), rel.UpdatedAt, rel.CreatedAt
			},
		},
		relation:  rel,
		isInverse: isInverse,
		issueID:   n.issueID,
	}
	// One relation surfaces as TWO files — blocks-B.rel under A and
	// blocked-by-A.rel under B — with different renders. They must not share a
	// stable ino: the bridge dedups nodes by ino AFTER the handler returns, and
	// the refreshExisting probe (parent.GetChild) can't see across directories,
	// so a shared ino served one endpoint's content for both paths.
	inoKey := rel.ID
	if isInverse {
		inoKey += "/inverse"
	}
	return n.newRenderInode(ctx, out, name, node, relationIno(inoKey), 30*time.Second), 0
}

// RelationFileNode represents a relation file (read-only info). Deletion is the
// parent RelationsNode's Unlink, so this node embeds renderFile for
// Open/Read/Getattr only.
type RelationFileNode struct {
	renderFile
	relation  api.IssueRelation
	isInverse bool
	issueID   string // parent issue (for the relations/ .error key)
}

// refreshFrom adopts a fresh twin's relation and render closure (refresh.go);
// renderMu doubles as the entity-field lock.
func (n *RelationFileNode) refreshFrom(fresh fs.InodeEmbedder) {
	f, ok := fresh.(*RelationFileNode)
	if !ok {
		return
	}
	n.renderMu.Lock()
	n.render = f.render
	n.relation, n.isInverse, n.issueID = f.relation, f.isInverse, f.issueID
	n.renderMu.Unlock()
}

// createRelation is the relations create surface's onFlush: parse the
// command via parseRelationInput (relationlisting.go), resolve the target
// issue, and run the create tail.
func (n *RelationsNode) createRelation(ctx context.Context, raw []byte) syscall.Errno {
	// relatedID carries the resolved target issue's ID from mutate to persist
	// (the API's echoed relation doesn't include it).
	var relatedID string
	var relationType, relatedIdentifier string

	_, errno := commitCreate(ctx, n.lfs, createSpec[api.IssueRelation]{
		op:  "create relation",
		key: collectionErrorKey("relations", n.issueID),
		mutate: func(ctx context.Context) (*api.IssueRelation, error) {
			var err error
			relationType, relatedIdentifier, err = parseRelationInput(string(raw))
			if err != nil {
				return nil, err
			}

			relatedIssue, err := n.lfs.repo.GetIssueByIdentifier(ctx, relatedIdentifier)
			if err != nil || relatedIssue == nil {
				return nil, &notFoundError{FieldError{Field: "identifier", Value: relatedIdentifier, Message: "unknown issue. Use an existing issue identifier like ENG-123."}}
			}
			relatedID = relatedIssue.ID

			return n.lfs.mutator().CreateIssueRelation(ctx, n.issueID, relatedIssue.ID, relationType)
		},
		// The name derives from the parsed input (through the shared
		// relationFileName) by necessity: the API's echoed relation doesn't
		// include the related issue.
		result: func(*api.IssueRelation) WriteResult {
			return WriteResult{
				Path:  relationFileName(relationType, relatedIdentifier),
				Title: relationType + " " + relatedIdentifier,
			}
		},
		persist: func(ctx context.Context, rel *api.IssueRelation) error {
			now := db.Now()
			// Prefer the server's authoritative relation times; fall back to now()
			// if the mutation echoed a zero time.
			created := rel.CreatedAt
			if created.IsZero() {
				created = now
			}
			updated := rel.UpdatedAt
			if updated.IsZero() {
				updated = now
			}
			return n.lfs.store.Queries().UpsertIssueRelation(ctx, db.UpsertIssueRelationParams{
				ID:             rel.ID,
				IssueID:        n.issueID,
				RelatedIssueID: relatedID,
				Type:           relationType,
				CreatedAt:      sql.NullTime{Time: created, Valid: true},
				UpdatedAt:      sql.NullTime{Time: updated, Valid: true},
				SyncedAt:       now,
			})
		},
		dir: relationsDirIno(n.issueID),
		entryName: func(*api.IssueRelation) string {
			return relationFileName(relationType, relatedIdentifier)
		},
	})
	return errno
}
