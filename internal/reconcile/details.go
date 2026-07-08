package reconcile

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// Deps carries what PersistIssueDetails needs to write: the sqlc query
// surface and the embedded-file extraction hook (typically
// (*Extractor).ExtractAndStore; nil skips extraction).
type Deps struct {
	Q       *db.Queries
	Extract func(ctx context.Context, issueID, content, source string)
}

// PersistIssueDetails stores one issue's fetched details — comments,
// documents, attachments, relations, and inverse relations — through the
// shared Collection tail, five calls per issue. The module contributes the
// CLEAN guard (a failed convert/upsert marks the collection unclean and
// suppresses its prune — the fix for the old silent-prune bug, where a failed
// upsert left a row's synced_at un-stamped yet the prune still deleted it);
// this function contributes COMPLETENESS via pruneWhenComplete below. A prune
// therefore fires only when the fetch was clean AND complete.
//
// The cutoff is taken by the caller BEFORE the fetch: any row upserted after
// that instant (a comment created through FUSE while the fetch was in flight)
// carries a newer synced_at and survives pruning even though the fetch
// response predates it. Completeness relies on the API client's
// all-or-nothing batch semantics — a partially-failed response never reaches
// this function as a short-but-"complete" details struct.
//
// The returned clean is true iff all five collections were clean; both
// callers gate the issue's detail_synced_at stamp on it (the worker also
// gates the pending-queue dequeue).
func PersistIssueDetails(ctx context.Context, deps Deps, issueID string, details *api.IssueDetails, cutoff time.Time) (clean bool) {
	// Completeness here is *page*-shaped rather than *drain*-shaped: a full
	// page (len == the query's page size) may be truncated, so pruning against
	// it would delete real rows. pruneWhenComplete passes the real prune only
	// on a short (provably complete) page and nil otherwise.
	pruneWhenComplete := func(complete bool, fn func(context.Context) error) func(context.Context) error {
		if !complete {
			return nil // a full page may be truncated — pruning against it would delete real rows
		}
		return fn
	}

	clean = true

	clean = Collection(ctx, CollectionSpec[api.Comment]{
		Label: "comment " + issueID,
		Items: details.Comments,
		Upsert: func(ctx context.Context, comment api.Comment) error {
			params, err := db.APICommentToDBComment(comment, issueID)
			if err != nil {
				return err
			}
			upsertErr := deps.Q.UpsertComment(ctx, params)
			// Embedded files are a nested best-effort sub-write to a
			// separate, never-pruned table — outside this prune's
			// completeness set — so extraction runs regardless of the
			// upsert result and cannot affect cleanliness.
			if deps.Extract != nil {
				deps.Extract(ctx, issueID, comment.Body, "comment:"+comment.ID)
			}
			return upsertErr
		},
		Prune: pruneWhenComplete(len(details.Comments) < api.IssueDetailsPageSize, func(ctx context.Context) error {
			return deps.Q.PruneIssueComments(ctx, db.PruneIssueCommentsParams{IssueID: issueID, SyncedAt: cutoff})
		}),
	}) && clean

	clean = Collection(ctx, CollectionSpec[api.Document]{
		Label: "document " + issueID,
		Items: details.Documents,
		Upsert: func(ctx context.Context, doc api.Document) error {
			params, err := db.APIDocumentToDBDocument(doc)
			if err != nil {
				return err
			}
			return deps.Q.UpsertDocument(ctx, params)
		},
		Prune: pruneWhenComplete(len(details.Documents) < api.IssueDetailsPageSize, func(ctx context.Context) error {
			return deps.Q.PruneIssueDocuments(ctx, db.PruneIssueDocumentsParams{IssueID: sql.NullString{String: issueID, Valid: true}, SyncedAt: cutoff})
		}),
	}) && clean

	clean = Collection(ctx, CollectionSpec[api.Attachment]{
		Label: "attachment " + issueID,
		Items: details.Attachments,
		Upsert: func(ctx context.Context, attachment api.Attachment) error {
			params, err := db.APIAttachmentToDBAttachment(attachment, issueID)
			if err != nil {
				return err
			}
			return deps.Q.UpsertAttachment(ctx, params)
		},
		Prune: pruneWhenComplete(len(details.Attachments) < api.IssueDetailsPageSize, func(ctx context.Context) error {
			return deps.Q.PruneIssueAttachments(ctx, db.PruneIssueAttachmentsParams{IssueID: issueID, SyncedAt: cutoff})
		}),
	}) && clean

	// Relations: the outgoing rows this issue owns (issue_id = issueID).
	// Before this, relations were persisted ONLY by the FUSE create
	// handler, so a relation made in Linear's own UI never appeared as a
	// .rel file and one deleted there lingered as a phantom.
	clean = Collection(ctx, CollectionSpec[api.IssueRelation]{
		Label: "relation " + issueID,
		Items: details.Relations,
		Upsert: func(ctx context.Context, rel api.IssueRelation) error {
			if rel.RelatedIssue == nil {
				return fmt.Errorf("relation %s has no relatedIssue", rel.ID)
			}
			return deps.Q.UpsertIssueRelation(ctx, db.IssueRelationUpsertParams(rel, issueID, rel.RelatedIssue.ID))
		},
		Prune: pruneWhenComplete(len(details.Relations) < api.IssueRelationsPageSize, func(ctx context.Context) error {
			return deps.Q.PruneIssueRelations(ctx, db.PruneIssueRelationsParams{IssueID: issueID, SyncedAt: cutoff})
		}),
	}) && clean

	// Inverse relations are rows OWNED BY THE OTHER issue (issue_id =
	// the other side). Upserting them makes a UI-created relation
	// visible from this end before the owning issue's own detail sync
	// runs — but they are outside this fetch's completeness set (only
	// the owning issue's drained fetch may license their deletion), so
	// this collection is upsert-only, like states.
	clean = Collection(ctx, CollectionSpec[api.IssueRelation]{
		Label: "inverse relation " + issueID,
		Items: details.InverseRelations,
		Upsert: func(ctx context.Context, rel api.IssueRelation) error {
			if rel.Issue == nil {
				return fmt.Errorf("inverse relation %s has no issue", rel.ID)
			}
			return deps.Q.UpsertIssueRelation(ctx, db.IssueRelationUpsertParams(rel, rel.Issue.ID, issueID))
		},
		Prune: nil,
	}) && clean

	return clean
}
