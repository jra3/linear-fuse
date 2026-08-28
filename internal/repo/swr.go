package repo

// swrRefresh coordinator: the one owner of the repo's stale-while-revalidate
// policy. EVERY SWR surface routes through this module with a swrSpec — that is
// the invariant, not the tally, which drifts every time a surface is added (this
// comment and its twin in metrics.go both still said "six" long after they
// weren't). The module owns the staleness decision (both flavors), the typed
// dedup key, and the orphan-on-not-found classification that the individual
// refresh tails used to each restate by hand.
//
// There are two entry points and the split is by QUESTION, not by caller: every
// READ asks maybeRefreshSWR "has this fallen behind?", and only the write path's
// recheck asks forceRefreshSWR "does this still exist?" — see forceRefreshSWR
// for why staleness cannot answer the second.

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
	kindTeamDocs          refreshKind = "team-docs"
	kindProjectUpdates    refreshKind = "project-updates"
	kindInitiativeUpdates refreshKind = "initiative-updates"
	kindProjectLinks      refreshKind = "project-links"
	kindInitiativeLinks   refreshKind = "initiative-links"
	kindTeamLabels        refreshKind = "team-labels"
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
	// details, a sync_schedule stamp for the team label catalog — nil means
	// never synced); the module applies parseTime.
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

// maybeRefreshSWR is the READ path's entry point for stale-while-revalidate:
// decide staleness per the spec's flavor and, if stale, trigger the
// deduplicated background refresh (wrapped with the orphan classification). In
// fixture mode (nil client) it never fires — before even querying syncedAt.
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

// forceRefreshSWR triggers a spec's refresh WITHOUT asking whether the entity is
// stale. It is maybeRefreshSWR minus the one decision maybeRefreshSWR exists to
// make, and only the recheck entry points below may use it.
//
// Staleness is the wrong question for a recheck. "Serve stale while
// revalidating" asks whether the cache has fallen behind a moving entity; a
// recheck starts from Linear having just REJECTED a write against this row, so
// what is being tested is whether the entity exists at all. An entity deleted
// upstream never gets a new updated_at and never grows a newer doc, so the
// event-driven gate reports it fresh forever (its detail_synced_at is stamped by
// the browse that discovered the orphan) — the hint would be dropped by exactly
// the surfaces #477 is about.
//
// Everything else the trigger provides is kept, because none of it is a
// staleness judgement: the nil-client inertness (fixture mode never fetches),
// the in-flight dedup and the semaphore bound both live in
// triggerBackgroundRefresh, and the prune still runs only behind
// orphanOnNotFound — the refresh re-asks Linear and deletes on Linear's answer.
func (r *SQLiteRepository) forceRefreshSWR(spec swrSpec) {
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

// Entity recheck — the write path's one way to say "Linear just told me
// something about this entity that the cache may not know".
//
// A mutation Linear rejects holds the most authoritative evidence there is,
// and an ENOENT rejection ("Entity not found") means the local row is an
// orphan: the mount would keep listing it, keep opening it, and keep failing
// the same write until an unrelated read or a sync cycle rediscovered the
// truth (#477). The recheck lets the failed write supply the hint without
// letting internal/fs mutate the cache: it triggers the entity's EXISTING
// SWR spec, so the prune stays owned by this layer, behind orphanOnNotFound.
//
// That indirection is the point, not a detour. api.IsNotFound answers on
// message TEXT, so a misclassified rejection is possible; a recheck re-asks
// Linear and prunes only on what Linear says, where deleting the row directly
// off the fs-layer verdict would delete a live entity's cache.
//
// Each one goes through forceRefreshSWR, NOT maybeRefreshSWR: a recheck is not
// "serve stale while revalidating", it is "Linear just told me this row may be
// dead", and a dead entity is fresh forever by every staleness measure the specs
// have. See forceRefreshSWR for the full argument.
//
// Each recheck reaches for the entity-scoped spec that carries the entity's
// orphan classification. There is no recheck for a label or a milestone: no
// SWR surface is scoped to either, and inventing one to serve a failure arm
// would add a refresh nothing reads. Those buffers converge on the sync
// worker's schedule instead.

// RecheckIssue re-asks Linear about an issue (the issue-details spec, whose
// orphan handler is deleteOrphanIssue).
func (r *SQLiteRepository) RecheckIssue(issueID string) {
	r.forceRefreshSWR(r.issueDetailsSpec(issueID))
}

// RecheckProject re-asks Linear about a project (the project-docs spec, whose
// orphan handler is deleteOrphanProject).
func (r *SQLiteRepository) RecheckProject(projectID string) {
	r.forceRefreshSWR(r.projectDocsSpec(projectID))
}

// RecheckInitiative re-asks Linear about an initiative (the initiative-docs
// spec, whose orphan handler is deleteOrphanInitiative).
func (r *SQLiteRepository) RecheckInitiative(initiativeID string) {
	r.forceRefreshSWR(r.initiativeDocsSpec(initiativeID))
}
