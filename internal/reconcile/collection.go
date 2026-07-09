// Package reconcile owns the reconcile-into-SQLite tails shared by the sync
// worker and (in a later step) the repo's SWR refresh path: the
// upsert-all/prune-if-clean collection tail (Collection), the per-issue
// detail persist composed from it (PersistIssueDetails), and the
// embedded-file extraction module (Extractor). Neither internal/sync nor
// internal/repo imports the other, so the shared policy lives here.
package reconcile

import (
	"context"
	"log"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jra3/linear-fuse/internal/telemetry"
)

// CollectionSpec declares how one collection reconciles into SQLite.
// See CONTEXT.md "Sync reconcile tail (syncCollection)".
type CollectionSpec[T any] struct {
	// Label names the collection in log lines ("label", "cycle", "project", …).
	// The detail labels embed the issue ID ("comment <id>"), so Label must
	// NEVER become a metric attribute — that cardinality is unbounded.
	Label string

	// Kind is the collection's closed-enum name for the linearfs.sync.prunes
	// metric attribute: state|label|cycle|project|member|initiative-project|
	// project-label|comment|document|attachment|relation|inverse-relation
	// (plus the repo's upsert-only update kinds, which never prune). Bounded
	// by construction — every caller sets a constant string, never an ID.
	Kind string

	// Items is the complete, drained server-side set to reconcile. Completeness
	// is what licenses prune — a truncated fetch would read as removals.
	Items []T

	// Upsert performs the whole per-item write: convert, entity upsert, any
	// junction upsert, and any nested sub-writes. It returns an error ONLY for a
	// write in the prune's completeness set (the rows the prune deletes against).
	// A nested best-effort sub-write with no prune of its own — e.g. a project's
	// milestones, which live in a capped, never-pruned connection — logs and
	// swallows its own failure and returns nil, so it never suppresses the prune.
	Upsert func(context.Context, T) error

	// Prune deletes the rows the (complete) fetch no longer returned. It runs
	// once, after every upsert, and ONLY if all of them succeeded. A nil prune
	// means the collection is upsert-only (e.g. states, which are
	// workflow-bounded and fetched single-page, so nothing licenses a prune).
	Prune func(context.Context) error
}

// Collection reconciles one collection: upsert every item, then prune the
// rows no longer present — but prune ONLY if every upsert succeeded, so a
// partial fetch never reads as removals. It is the sync-side sibling of the
// write-path tails (commitCreate/commitWriteBack/commitDelete) and owns the
// prune-safety invariant that the metadata sites used to restate by hand as a
// `clean` flag. Reconciliation is best-effort: failures are logged, never
// returned as errors — but the returned clean reports whether every upsert
// succeeded (a prune failure does not affect it), so a caller can gate
// freshness stamps on it; callers that don't care simply ignore it. Pure
// over the spec's closures, so it is unit-tested with recording closures —
// no store or API.
func Collection[T any](ctx context.Context, spec CollectionSpec[T]) (clean bool) {
	clean = true
	for _, item := range spec.Items {
		if err := spec.Upsert(ctx, item); err != nil {
			log.Printf("[reconcile] upsert %s failed: %v", spec.Label, err)
			clean = false
		}
	}
	if spec.Prune == nil {
		return clean // upsert-only collection
	}
	if !clean {
		log.Printf("[reconcile] skipping %s prune: an upsert failed this pass", spec.Label)
		return clean
	}
	if err := spec.Prune(ctx); err != nil {
		log.Printf("[reconcile] prune %s failed: %v", spec.Label, err)
	} else {
		prunesCounter().Add(ctx, 1, metric.WithAttributes(
			attribute.String("collection", spec.Kind)))
	}
	return clean
}

// prunes is the linearfs.sync.prunes counter — a prune that actually
// executed is the destructive half of the reconcile contract, so it is the
// one worth counting (a suppressed prune records nothing). The package has
// no construction point, so the instrument binds lazily on the first firing
// prune; in production that is long after telemetry.Init registered the
// provider, and without one the global no-op makes every Add free.
var (
	prunesOnce sync.Once
	prunes     metric.Int64Counter
)

func prunesCounter() metric.Int64Counter {
	prunesOnce.Do(func() {
		prunes = telemetry.MustInt64Counter(otel.Meter("linearfs/sync"),
			"linearfs.sync.prunes",
			metric.WithDescription("Reconcile prunes that actually executed, by collection kind"))
	})
	return prunes
}
