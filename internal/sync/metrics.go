package sync

// OTEL instruments for the sync layer (phase 3 of the metrics design,
// docs/plans/2026-07-08-otel-metrics-design.md) — the budget's consumers,
// completing the rate-limit causal chain the api+budget instruments started.
// Instruments are created once, at Worker construction, from the globally
// registered provider (otel.Meter) — never per call; with no provider
// registered the global no-op makes every record free. The fourth sync
// instrument, linearfs.sync.prunes, lives in internal/reconcile (the module
// that actually runs the prunes).

import (
	"context"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/telemetry"
)

// syncMetrics holds the Worker-bound sync instruments (meter "linearfs/sync").
type syncMetrics struct {
	cycleDuration      metric.Float64Histogram // linearfs.sync.cycle_duration {mode}, seconds
	detailOutcomes     metric.Int64Counter     // linearfs.sync.detail_outcomes {outcome}
	probeOutcomes      metric.Int64Counter     // linearfs.sync.probe_outcomes {kind, outcome}
	reconcileDeletions metric.Int64Counter     // linearfs.sync.reconcile_deletions {kind}
}

func newSyncMetrics() syncMetrics {
	m := otel.Meter("linearfs/sync")
	return syncMetrics{
		cycleDuration: telemetry.MustFloat64Histogram(m, "linearfs.sync.cycle_duration",
			metric.WithUnit("s"),
			metric.WithDescription("Duration of one sync cycle, by mode (lean|full); budget-skipped cycles record ~0")),
		detailOutcomes: telemetry.MustInt64Counter(m, "linearfs.sync.detail_outcomes",
			metric.WithDescription("Issues leaving syncDetails' per-issue ledger, by outcome (synced|deferred)")),
		probeOutcomes: telemetry.MustInt64Counter(m, "linearfs.sync.probe_outcomes",
			metric.WithDescription("Lean-cycle change-detection probe runs, by kind (team_projects) and outcome (unchanged|changed|error)")),
		reconcileDeletions: telemetry.MustInt64Counter(m, "linearfs.sync.reconcile_deletions",
			metric.WithDescription("Local rows deleted by the scheduled ID-reconcile sweep, by kind (issue)")),
	}
}

// Probe outcome vocabulary: every probe run records exactly one outcome, so a
// probe that never fires is detectable as a missing series. probeKind* names
// the probed entity class ("team_projects" today; the initiatives probe is a
// later diet slice).
type probeOutcome string

const (
	probeUnchanged probeOutcome = "unchanged" // newest page carried nothing past the watermark
	probeChanged   probeOutcome = "changed"   // at least one node upserted, watermark advanced
	probeError     probeOutcome = "error"     // fetch/upsert failure or cancellation — watermark untouched
)

const probeKindTeamProjects = "team_projects"

// recordCycle records one sync cycle's duration, attributed with the cycle's
// mode (lean|full) — the histogram's per-mode sample counts double as the
// cycle-mode counter, so lean/full cadence is visible without a second
// instrument.
func (sm syncMetrics) recordCycle(d time.Duration, mode cycleMode) {
	sm.cycleDuration.Record(context.Background(), d.Seconds(),
		metric.WithAttributes(attribute.String("mode", string(mode))))
}

// recordDetailOutcomes counts issues leaving syncDetails' ledger. Every issue
// lands in exactly one outcome, so summing both series gives issues processed.
func (sm syncMetrics) recordDetailOutcomes(ctx context.Context, synced, deferred int) {
	if synced > 0 {
		sm.detailOutcomes.Add(ctx, int64(synced), metric.WithAttributes(
			attribute.String("outcome", "synced")))
	}
	if deferred > 0 {
		sm.detailOutcomes.Add(ctx, int64(deferred), metric.WithAttributes(
			attribute.String("outcome", "deferred")))
	}
}

// recordProbeOutcome counts one change-detection probe run, attributed with
// the probed kind and its outcome.
func (sm syncMetrics) recordProbeOutcome(kind string, outcome probeOutcome) {
	sm.probeOutcomes.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("kind", kind),
		attribute.String("outcome", string(outcome))))
}

// recordReconcileDeletions counts local rows deleted by the scheduled
// ID-reconcile sweep (#245). Zero-deletion sweeps record nothing, matching
// the sibling counters.
func (sm syncMetrics) recordReconcileDeletions(ctx context.Context, kind string, n int) {
	if n > 0 {
		sm.reconcileDeletions.Add(ctx, int64(n), metric.WithAttributes(
			attribute.String("kind", kind)))
	}
}

// registerPendingDepthGauge installs the linearfs.sync.pending_depth
// observable gauge: the pending_detail_sync backlog, counted only at export
// intervals (a cheap COUNT on a single-purpose table). A count error skips
// the observation — no data point beats a fabricated zero.
func registerPendingDepthGauge(q *db.Queries) {
	meter := otel.Meter("linearfs/sync")
	depth, err := meter.Int64ObservableGauge("linearfs.sync.pending_depth",
		metric.WithDescription("Issues queued in pending_detail_sync awaiting a detail-sync retry"))
	if err != nil {
		log.Printf("telemetry: pending_depth gauge not registered: %v", err)
		return
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		n, err := q.CountPendingDetailSync(ctx)
		if err != nil {
			return nil // skip this observation; the collect must not fail on a DB hiccup
		}
		o.ObserveInt64(depth, n)
		return nil
	}, depth)
	if err != nil {
		log.Printf("telemetry: pending_depth callback not registered: %v", err)
	}
}
