package fs

import (
	"context"
	"log"
	"syscall"
	"time"
)

// The edit-commit tail.
//
// Every edit to an existing entity (issue.md, project.md, a comment/doc/label/
// milestone .md) ends with the same invariant sequence once the API has accepted
// the write: re-derive what persisted, verify it read-your-writes against what we
// sent, persist the fresh value to SQLite, and set or clear the .error file. That
// tail was copy-pasted across handlers and, where it was forgotten (label,
// milestone), writes could silently revert without surfacing a failure.
//
// commitWriteBack is the one deep module that owns the tail. Each handler keeps
// its per-entity front half (parse, resolve names→IDs, call the API) and the
// per-entity cache invalidation, then hands the tail a small spec. The module
// depends only on the errorSink seam plus the spec's closures, so it is unit-
// tested with a fake sink and stub closures — no FUSE mount, SQLite, or API.

// errorSink is the minimal surface the tail needs to report read-your-writes
// outcomes via the .error files. *LinearFS satisfies it directly through its
// existing SetWriteError/ClearWriteError methods, so production wiring needs no
// adapter while tests inject a fake.
type errorSink interface {
	SetWriteError(key, message string)
	ClearWriteError(key string)
}

// writeBackSpec describes the per-entity parts of an edit's tail. T is the entity
// type (api.Issue, api.Label, api.ProjectMilestone, …). Everything T-specific
// lives in these closures; the tail itself is fully generic.
type writeBackSpec[T any] struct {
	// errKey identifies the .error file to set or clear (an entity ID, or a
	// collectionErrorKey for collection-scoped entities like labels/milestones).
	errKey string
	// fetch returns the authoritative post-write value. For issues this is an
	// independent API re-fetch (catches server-side silent reverts of large
	// bodies); for entities with no single-entity getter it may return the
	// mutation's echoed response.
	fetch func(ctx context.Context) (*T, error)
	// persist writes the fresh value to SQLite for immediate visibility. nil when
	// the front half already upserted (e.g. milestone updates go through the repo,
	// which upserts atomically).
	persist func(ctx context.Context, fresh *T) error
	// compare reports how the free-text fields persisted, using the pure
	// writeBackDivergence helper. Reads the pre-write originals from the caller's
	// captured state, so it must run before the caller overwrites that state.
	compare func(fresh *T) []writeBackResult
}

// commitWriteBack runs the invariant tail of an edit after the API has accepted
// the write. It returns the fresh value (nil if it could not be fetched) and the
// errno the Flush should return: syscall.EIO on a fatal read-your-writes
// divergence, 0 otherwise (including benign reformats, which leave a note in
// .error but let the close succeed).
//
// Contract:
//   - fetch fails        → the write succeeded but is unverified: clear .error,
//     return (nil, 0). The handler keeps its prior local state.
//   - persist fails      → non-fatal: log and continue (a cache miss must not fail
//     a write that Linear accepted).
//   - no divergence      → clear .error, return (fresh, 0).
//   - benign reformat    → set .error note, return (fresh, 0).
//   - fatal divergence   → set .error, return (fresh, syscall.EIO).
func commitWriteBack[T any](ctx context.Context, sink errorSink, spec writeBackSpec[T]) (fresh *T, errno syscall.Errno) {
	start := time.Now()
	defer func() { recordFuseOp(ctx, "flush", start, errno) }()

	fresh, err := spec.fetch(ctx)
	if err != nil {
		// The API accepted the write; we just could not re-read it to verify.
		// Treat as success (sync will reconcile) and clear any stale error.
		log.Printf("Warning: failed to fetch fresh entity after update (%s): %v", spec.errKey, err)
		sink.ClearWriteError(spec.errKey)
		return nil, 0
	}

	if spec.persist != nil {
		if err := spec.persist(ctx, fresh); err != nil {
			log.Printf("Warning: failed to upsert entity to SQLite (%s): %v", spec.errKey, err)
		}
	}

	divergence, fatal := writeBackError(spec.compare(fresh)...)
	if divergence == "" {
		sink.ClearWriteError(spec.errKey)
		return fresh, 0
	}

	log.Printf("Read-your-writes %s on %s:\n%s", writeBackKind(fatal), spec.errKey, divergence)
	sink.SetWriteError(spec.errKey, divergence)
	if fatal {
		return fresh, syscall.EIO
	}
	return fresh, 0
}
