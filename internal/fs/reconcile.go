package fs

import (
	"context"
	"fmt"

	"github.com/jra3/linear-fuse/internal/db"
)

// Link reconciliation — the shared front half for editing a many-to-many link
// set (today: project↔initiative). Editing project.md's `initiatives:` list and
// initiatives.md's `projects:` list are mirror images of one algorithm: diff the
// desired member names against the current ones, resolve each delta to an ID,
// and link/unlink it. That algorithm was hand-copied in both ProjectInfoNode.Flush
// and InitiativeInfoNode.Flush, differing only in which name resolves, the
// argument order to the shared mutation, and the .error field label.
//
// reconcileLinks is the one module that owns the algorithm; the per-side effect
// (the API mutation plus the junction-row write, both fatal on failure) lives in
// the caller's link/unlink closures. Like resolveIssueUpdate, it is pure of the errorSink and
// of any entity type — it works only on ID strings and name lists — so it is
// unit-tested with fake closures, no FUSE mount, SQLite, or API. It returns a
// classifiable error: a *FieldError for a name that will not resolve (→ EINVAL),
// the wrapped mutation error otherwise (→ EIO/EAGAIN via classifyMutationErr).

// linkReconcileSpec describes one side of a many-to-many link edit.
type linkReconcileSpec struct {
	// current and desired are the member names before and after the edit (an
	// initiative's project slugs, or a project's initiative names).
	current []string
	desired []string
	// resolve turns a member name into its ID. A failure is reported as a
	// *FieldError tagged with field/hint.
	resolve func(ctx context.Context, name string) (string, error)
	// link and unlink apply one membership change: the API mutation plus, on
	// success, the junction-row write (persistInitiativeProjectLink), whose
	// failure is fatal — a returned error (from either) aborts the reconcile and
	// propagates unchanged.
	link   func(ctx context.Context, id string) error
	unlink func(ctx context.Context, id string) error
	// field and hint label a resolve failure in .error: field is the frontmatter
	// key ("projects"/"initiatives"), hint a trailing sentence pointing at where
	// valid values live.
	field string
	hint  string
}

// reconcileLinks diffs desired against current, links each added member and
// unlinks each removed one, stopping on the first failure. See the package
// comment above for the failure model.
func reconcileLinks(ctx context.Context, spec linkReconcileSpec) error {
	currentSet := make(map[string]bool, len(spec.current))
	for _, name := range spec.current {
		currentSet[name] = true
	}
	desiredSet := make(map[string]bool, len(spec.desired))
	for _, name := range spec.desired {
		desiredSet[name] = true
	}

	// Additions: desired but not current.
	for _, name := range spec.desired {
		if currentSet[name] {
			continue
		}
		id, err := spec.resolve(ctx, name)
		if err != nil {
			return &FieldError{Field: spec.field, Value: name, Message: err.Error() + spec.hint}
		}
		if err := spec.link(ctx, id); err != nil {
			return err
		}
	}

	// Removals: current but not desired.
	for _, name := range spec.current {
		if desiredSet[name] {
			continue
		}
		id, err := spec.resolve(ctx, name)
		if err != nil {
			return &FieldError{Field: spec.field, Value: name, Message: err.Error() + spec.hint}
		}
		if err := spec.unlink(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// persistInitiativeProjectLink writes (linked) or removes (!linked) the
// initiative↔project junction row in SQLite for immediate visibility. Both edit
// sides drive the same junction with the same (initiativeID, projectID) pair, so
// this is their shared persist.
//
// Reflection gates success (the #276 contract): unlike other edits, a
// link/unlink bumps NEITHER side's updatedAt, so a swallowed junction-write is a
// silent-stale the sync worker never reconciles — the failure the create tail
// now rejects, but here without even a next-sync safety net. So a failed
// junction write is returned, not logged-and-swallowed: the caller aborts the
// reconcile and surfaces it in .error + EIO. The mutation itself is idempotent
// (re-linking an already-linked pair is a no-op), so the message says re-saving
// is safe rather than warning against a retry.
func (lfs *LinearFS) persistInitiativeProjectLink(ctx context.Context, initiativeID, projectID string, linked bool) error {
	if lfs.store == nil {
		return nil
	}
	if linked {
		if err := lfs.store.Queries().UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
			InitiativeID: initiativeID,
			ProjectID:    projectID,
			SyncedAt:     db.Now(),
		}); err != nil {
			return fmt.Errorf("the link was applied on Linear but the local cache could not be updated, so it may not appear locally until the next sync (re-saving is safe): %w", err)
		}
		return nil
	}
	if err := lfs.store.Queries().DeleteInitiativeProject(ctx, db.DeleteInitiativeProjectParams{
		InitiativeID: initiativeID,
		ProjectID:    projectID,
	}); err != nil {
		return fmt.Errorf("the unlink was applied on Linear but the local cache could not be updated, so it may still appear locally until the next sync (re-saving is safe): %w", err)
	}
	return nil
}
