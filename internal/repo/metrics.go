package repo

// OTEL instruments for the SWR layer (phase 3 of the metrics design,
// docs/plans/2026-07-08-otel-metrics-design.md). The SWR triggers were
// literally round 18's budget leak, so this pair shows leak AND leaker next
// to the budget gauges. Instruments bind once, at SQLiteRepository
// construction, from the globally registered provider (otel.Meter); with no
// provider the global no-op makes every record free.
//
// Cardinality: kind is the six refreshKind constants; decision and outcome
// are the closed enums below. Nothing else becomes an attribute.

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/telemetry"
)

// swrMetrics holds the SWR instruments (meter "linearfs/swr"). The recording
// methods tolerate the zero value (nil instruments record nothing): several
// tests build SQLiteRepository by struct literal, and telemetry must never
// panic a read path.
type swrMetrics struct {
	triggers        metric.Int64Counter // linearfs.swr.triggers {kind, decision}
	refreshOutcomes metric.Int64Counter // linearfs.swr.refresh_outcomes {kind, outcome}
}

func newSWRMetrics() swrMetrics {
	m := otel.Meter("linearfs/swr")
	return swrMetrics{
		triggers: telemetry.MustInt64Counter(m, "linearfs.swr.triggers",
			metric.WithDescription("SWR staleness verdicts, by kind and decision (triggered|fresh|deduped|sem_dropped)")),
		refreshOutcomes: telemetry.MustInt64Counter(m, "linearfs.swr.refresh_outcomes",
			metric.WithDescription("Completed background refreshes, by kind and outcome (ok|error|orphaned)")),
	}
}

// recordTrigger counts one staleness verdict. fresh means swrStale said no;
// triggered/deduped/sem_dropped are triggerBackgroundRefresh's three exits.
// The nil-client (fixture-mode) returns record nothing — there is no SWR
// machinery to observe.
func (m swrMetrics) recordTrigger(kind refreshKind, decision string) {
	if m.triggers == nil {
		return
	}
	m.triggers.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("kind", string(kind)),
		attribute.String("decision", decision)))
}

// recordRefreshOutcome counts one completed background refresh. The outcome
// mirrors the module's orphan classification (orphanOnNotFound): a
// not-found-shaped error means the local rows were orphans (deleted), any
// other error is error, nil is ok.
func (m swrMetrics) recordRefreshOutcome(kind refreshKind, err error) {
	if m.refreshOutcomes == nil {
		return
	}
	outcome := "ok"
	switch {
	case err == nil:
	case api.IsNotFound(err):
		outcome = "orphaned"
	default:
		outcome = "error"
	}
	m.refreshOutcomes.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("kind", string(kind)),
		attribute.String("outcome", outcome)))
}
