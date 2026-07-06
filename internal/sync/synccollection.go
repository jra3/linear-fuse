package sync

import (
	"context"
	"log"
)

// syncCollectionSpec declares how one metadata collection reconciles into
// SQLite. See CONTEXT.md "Sync reconcile tail (syncCollection)".
type syncCollectionSpec[T any] struct {
	// label names the collection in log lines ("label", "cycle", "project", …).
	label string

	// items is the complete, drained server-side set to reconcile. Completeness
	// is what licenses prune — a truncated fetch would read as removals.
	items []T

	// upsert performs the whole per-item write: convert, entity upsert, any
	// junction upsert, and any nested sub-writes. It returns an error ONLY for a
	// write in the prune's completeness set (the rows the prune deletes against).
	// A nested best-effort sub-write with no prune of its own — e.g. a project's
	// milestones, which live in a capped, never-pruned connection — logs and
	// swallows its own failure and returns nil, so it never suppresses the prune.
	upsert func(context.Context, T) error

	// prune deletes the rows the (complete) fetch no longer returned. It runs
	// once, after every upsert, and ONLY if all of them succeeded. A nil prune
	// means the collection is upsert-only (e.g. states, which are
	// workflow-bounded and fetched single-page, so nothing licenses a prune).
	prune func(context.Context) error
}

// syncCollection reconciles one metadata collection: upsert every item, then
// prune the rows no longer present — but prune ONLY if every upsert succeeded,
// so a partial fetch never reads as removals. It is the sync-side sibling of the
// write-path tails (commitCreate/commitWriteBack/commitDelete) and owns the
// prune-safety invariant that the metadata sites used to restate by hand as a
// `clean` flag. Sync is best-effort: failures are logged, never returned. Pure
// over the spec's closures, so it is unit-tested with recording closures — no
// store or API.
func syncCollection[T any](ctx context.Context, spec syncCollectionSpec[T]) {
	clean := true
	for _, item := range spec.items {
		if err := spec.upsert(ctx, item); err != nil {
			log.Printf("[sync] upsert %s failed: %v", spec.label, err)
			clean = false
		}
	}
	if spec.prune == nil {
		return // upsert-only collection
	}
	if !clean {
		log.Printf("[sync] skipping %s prune: an upsert failed this pass", spec.label)
		return
	}
	if err := spec.prune(ctx); err != nil {
		log.Printf("[sync] prune %s failed: %v", spec.label, err)
	}
}
