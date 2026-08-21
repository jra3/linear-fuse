package db

import (
	"context"
	"database/sql"
)

// DeleteIssueCascade removes an issue and every row keyed off it: comments,
// documents, attachments, embedded files, relations, the history cache and any
// pending detail sync. Steps run in order; a failing step is reported to onErr
// with the family it covers and the sweep continues, because partial cleanup
// beats no cleanup and no caller has a recovery action beyond logging. Reports
// whether the issue row itself was deleted.
//
// Two callers share it: the repo's orphan cleanup (an issue Linear no longer
// has) and the sync worker's team rebuild (#427 — a team whose key was renamed
// out from under its cached identifiers). A second copy of the list would drift
// from the first the next time an issue grows a dependent table, which is the
// mistake that leaves orphan rows nothing ever collects.
func DeleteIssueCascade(ctx context.Context, q *Queries, issueID string, onErr func(family string, err error)) bool {
	steps := []struct {
		family string
		del    func() error
	}{
		{"comments", func() error { return q.DeleteIssueComments(ctx, issueID) }},
		{"documents", func() error {
			return q.DeleteIssueDocuments(ctx, sql.NullString{String: issueID, Valid: true})
		}},
		{"attachments", func() error { return q.DeleteIssueAttachments(ctx, issueID) }},
		{"embedded files", func() error { return q.DeleteIssueEmbeddedFiles(ctx, issueID) }},
		{"relations", func() error { return q.DeleteIssueRelations(ctx, issueID) }},
		{"history", func() error { return q.DeleteIssueHistoryCache(ctx, issueID) }},
		{"pending sync", func() error { return q.DeletePendingDetailSync(ctx, issueID) }},
	}
	for _, step := range steps {
		if err := step.del(); err != nil {
			onErr(step.family, err)
		}
	}
	if err := q.DeleteIssue(ctx, issueID); err != nil {
		onErr("issue", err)
		return false
	}
	return true
}
