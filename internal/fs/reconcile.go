package fs

import (
	"context"
	"log"

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
// (the API mutation plus a best-effort junction-row write) lives in the caller's
// link/unlink closures. Like resolveIssueUpdate, it is pure of the errorSink and
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
	// success, a best-effort junction-row write. A returned error aborts the
	// reconcile and propagates unchanged.
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
// this is their shared persist. Best-effort: a cache-write miss is logged, never
// fatal — a write Linear already accepted must not fail on a stale local cache,
// and the sync worker reconciles.
func (lfs *LinearFS) persistInitiativeProjectLink(ctx context.Context, initiativeID, projectID string, linked bool) {
	if lfs.store == nil {
		return
	}
	if linked {
		if err := lfs.store.Queries().UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
			InitiativeID: initiativeID,
			ProjectID:    projectID,
			SyncedAt:     db.Now(),
		}); err != nil {
			log.Printf("Warning: failed to upsert initiative-project to SQLite: %v", err)
		}
		return
	}
	if err := lfs.store.Queries().DeleteInitiativeProject(ctx, db.DeleteInitiativeProjectParams{
		InitiativeID: initiativeID,
		ProjectID:    projectID,
	}); err != nil {
		log.Printf("Warning: failed to delete initiative-project from SQLite: %v", err)
	}
}
