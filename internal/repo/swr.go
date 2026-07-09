package repo

// swrRefresh coordinator: the one owner of the repo's stale-while-revalidate
// policy. Every SWR surface (six today) routes through maybeRefreshSWR with a
// swrSpec; the module owns the staleness decision (both flavors), the typed
// dedup key, and the orphan-on-not-found classification that the individual
// refresh tails used to each restate by hand.

import (
	"context"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// refreshKind names one SWR surface. Dedup keys are minted only through
// key(), so two surfaces can never collide (or silently diverge) on a
// hand-built string.
type refreshKind string

const (
	kindIssueDetails      refreshKind = "issue-details"
	kindHistory           refreshKind = "history"
	kindProjectDocs       refreshKind = "project-docs"
	kindInitiativeDocs    refreshKind = "initiative-docs"
	kindProjectUpdates    refreshKind = "project-updates"
	kindInitiativeUpdates refreshKind = "initiative-updates"
)

// key is the one factory for a refresh's dedup-map key.
func (k refreshKind) key(id string) string {
	return string(k) + ":" + id
}

// swrSpec declares one SWR surface: how to decide staleness, how to refresh,
// and what to delete when the entity turns out to be gone upstream.
type swrSpec struct {
	kind refreshKind
	id   string

	// syncedAt returns the raw last-sync instant (a MAX() aggregate for the
	// doc/update surfaces, the issues.detail_synced_at stamp for issue
	// details — nil means never synced); the module applies parseTime.
	syncedAt func() (interface{}, error)

	// changedAt selects the staleness flavor: nil means TTL (threshold-driven);
	// non-nil means event-driven — it returns the entity's last-change instant,
	// and ok=false means the instant is unknown (entity not in DB), which
	// suppresses the refresh entirely (discovery belongs to the sync worker).
	changedAt func() (time.Time, bool)

	// refresh does the fetch + persist. It runs in the background, deduplicated
	// by kind.key(id).
	refresh func(ctx context.Context) error

	// orphan deletes the local rows when refresh's error is Linear's
	// entity-not-found rejection (the deleteOrphan* helpers). The module owns
	// this classification; refresh tails don't inspect their own errors.
	orphan func(ctx context.Context)
}

// swrStale is the pure staleness decision behind maybeRefreshSWR, one function
// for both flavors (the staleSince precedent — this comparison has
// historically hidden timezone bugs, so it is unit-tested directly):
//
//   - TTL (eventDriven=false): staleSince against the threshold. This is the
//     ONLY flavor the catch-up threshold (SetCatchUpMode) reaches — explicit,
//     grilled policy, not an accident: extending catch-up suppression to the
//     event-driven surfaces would save duplicate fetches the rateBudget ladder
//     already governs, at the cost of silently-empty comments/ listings during
//     big syncs — the worst failure mode for an agent-facing filesystem.
//     Flipping later is a one-line policy change here.
//   - Event-driven (eventDriven=true): stale when never synced (query error,
//     nil, or zero instant) or when the entity changed after the last sync.
//     The threshold is deliberately not consulted.
func swrStale(syncedAt interface{}, syncedErr error, changed time.Time, eventDriven bool, threshold time.Duration) bool {
	if !eventDriven {
		return staleSince(syncedAt, syncedErr, threshold)
	}
	if syncedErr != nil || syncedAt == nil {
		return true
	}
	synced := parseTime(syncedAt)
	return synced.IsZero() || changed.After(synced)
}

// staleSince reports whether a cached entity's last-sync instant is older than
// threshold. A query error or a nil instant (never synced) counts as stale, so
// the caller refreshes. Pure, so the parseTime/threshold rule — historically a
// source of timezone-comparison bugs — is unit-tested directly.
func staleSince(syncedAt interface{}, err error, threshold time.Duration) bool {
	return err != nil || syncedAt == nil || time.Since(parseTime(syncedAt)) > threshold
}

// orphanOnNotFound wraps a refresh with the orphan classification: when the
// refresh fails with Linear's "Entity not found" rejection, the local row is
// an orphan and orphan deletes it — otherwise every FUSE traversal would
// retrigger the same failing refresh forever. Any other error passes through
// untouched. Pure over its closures, so it is unit-tested with recorders.
func orphanOnNotFound(refresh func(context.Context) error, orphan func(context.Context)) func(context.Context) error {
	return func(ctx context.Context) error {
		err := refresh(ctx)
		if err != nil && orphan != nil && api.IsNotFound(err) {
			orphan(ctx)
		}
		return err
	}
}

// maybeRefreshSWR is the one entry point for stale-while-revalidate: decide
// staleness per the spec's flavor and, if stale, trigger the deduplicated
// background refresh (wrapped with the orphan classification). In fixture
// mode (nil client) it never fires — before even querying syncedAt.
func (r *SQLiteRepository) maybeRefreshSWR(spec swrSpec) {
	if r.client == nil {
		return
	}

	var changed time.Time
	eventDriven := spec.changedAt != nil
	if eventDriven {
		var ok bool
		changed, ok = spec.changedAt()
		if !ok {
			return // change instant unknown (entity not in DB) — no refresh
		}
	}

	ts, err := spec.syncedAt()
	if !swrStale(ts, err, changed, eventDriven, r.stalenessThreshold) {
		r.metrics.recordTrigger(spec.kind, "fresh")
		return
	}

	r.triggerBackgroundRefresh(spec.kind, spec.id, orphanOnNotFound(spec.refresh, spec.orphan))
}

// issueChangedAt is the event source for issue-scoped surfaces (details,
// history): the issue's updated_at column. ok=false when the issue isn't in
// the DB yet — the sync worker owns discovery, so no refresh fires.
func (r *SQLiteRepository) issueChangedAt(issueID string) func() (time.Time, bool) {
	return func() (time.Time, bool) {
		t, err := r.store.Queries().GetIssueUpdatedAt(context.Background(), issueID)
		return t, err == nil
	}
}
