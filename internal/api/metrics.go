package api

// OTEL instruments for the api and budget layers (phase 2 of the metrics
// design, docs/plans/2026-07-08-otel-metrics-design.md). Instruments are
// created once, at Client/rateBudget construction, from the globally
// registered provider (otel.Meter) — never per call. When no provider has
// been registered (unit tests, library use) the global no-op provider makes
// every record free, so no nil checks or enable flags exist anywhere.
//
// Naming: linearfs.<layer>.<name>. Attribute cardinality is deliberately
// tiny — op names are the ~30 extractOpName values, tiers the 5 priority
// names, outcomes/decisions closed enums. Nothing else becomes an attribute.

import (
	"context"
	"errors"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jra3/linear-fuse/internal/telemetry"
)

// apiMetrics holds the api-layer instruments (meter "linearfs/api"):
// what happened on the wire, per operation.
type apiMetrics struct {
	requests metric.Int64Counter     // linearfs.api.requests {op, outcome}
	duration metric.Float64Histogram // linearfs.api.duration {op}, seconds
}

func newAPIMetrics() apiMetrics {
	m := otel.Meter("linearfs/api")
	return apiMetrics{
		requests: telemetry.MustInt64Counter(m, "linearfs.api.requests",
			metric.WithDescription("GraphQL requests completed, by operation and outcome (ok|error|ratelimited)")),
		duration: telemetry.MustFloat64Histogram(m, "linearfs.api.duration",
			metric.WithUnit("s"),
			metric.WithDescription("GraphQL request duration by operation")),
	}
}

// outcomeFor classifies a completed request's error into the closed outcome
// enum shared by linearfs.api.requests and the request debug log
// (requestlog.go) — one classification, two consumers.
func outcomeFor(err error) string {
	switch {
	case err == nil:
		return "ok"
	case IsRateLimited(err):
		return "ratelimited"
	default:
		return "error"
	}
}

// record counts one completed request (one that was actually sent — budget
// deferrals never reach here; they land in linearfs.budget.decisions).
func (am apiMetrics) record(ctx context.Context, op string, elapsed time.Duration, err error) {
	outcome := outcomeFor(err)
	am.requests.Add(ctx, 1, metric.WithAttributes(
		attribute.String("op", op),
		attribute.String("outcome", outcome)))
	am.duration.Record(ctx, elapsed.Seconds(),
		metric.WithAttributes(attribute.String("op", op)))
}

// budgetMetrics holds the synchronous budget-layer instruments, owned by
// rateBudget (created in newRateBudget). linearfs.api.complexity lives here
// too: the budget's reconcile is the ONE place that parses X-Complexity, so
// it records the histogram — headers are never parsed twice.
type budgetMetrics struct {
	complexity   metric.Float64Histogram // linearfs.api.complexity {op}
	decisions    metric.Int64Counter     // linearfs.budget.decisions {tier, decision}
	waitDuration metric.Float64Histogram // linearfs.budget.wait_duration, seconds
}

func newBudgetMetrics() budgetMetrics {
	apiMeter := otel.Meter("linearfs/api")
	budgetMeter := otel.Meter("linearfs/budget")
	return budgetMetrics{
		complexity: telemetry.MustFloat64Histogram(apiMeter, "linearfs.api.complexity",
			metric.WithDescription("Actual X-Complexity cost of each response, by operation")),
		decisions: telemetry.MustInt64Counter(budgetMeter, "linearfs.budget.decisions",
			metric.WithDescription("Rate-budget ladder verdicts, by tier and decision (admit|defer|wait|ratelimited)")),
		waitDuration: telemetry.MustFloat64Histogram(budgetMeter, "linearfs.budget.wait_duration",
			metric.WithUnit("s"),
			metric.WithDescription("Time spent waiting on rate limiting (limiter smoothing and mutation window waits)")),
	}
}

// recordDecision counts one ladder verdict. Decisions count EVENTS, not
// requests: a mutation that is denied, waits for the window, and is
// re-admitted records defer, wait, then the re-admit's verdict.
func (bm budgetMetrics) recordDecision(tier priority, decision string) {
	bm.decisions.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("tier", tier.String()),
		attribute.String("decision", decision)))
}

// recordWait records one rate-limit wait (successor to APIStats'
// rateLimitWaitNs total).
func (bm budgetMetrics) recordWait(d time.Duration) {
	bm.waitDuration.Record(context.Background(), d.Seconds())
}

// registerBudgetGauges installs the observable budget gauges: one callback
// reading b.snapshot() (a single acquisition of the budget's existing mutex)
// observes remaining/limit/inflight/reset_seconds for both axes. Axes the
// server has not reported yet are skipped (no data point beats a fabricated
// zero); inflight is always observed — a stuck reservation is exactly the
// leak these gauges exist to show.
func registerBudgetGauges(b *rateBudget) {
	meter := otel.Meter("linearfs/budget")
	remaining, err1 := meter.Float64ObservableGauge("linearfs.budget.remaining",
		metric.WithDescription("Server-reported budget remaining this window, per axis"))
	limit, err2 := meter.Float64ObservableGauge("linearfs.budget.limit",
		metric.WithDescription("Server-reported hourly budget limit, per axis"))
	inflight, err3 := meter.Float64ObservableGauge("linearfs.budget.inflight",
		metric.WithDescription("Cost reserved by unsettled admissions, per axis"))
	reset, err4 := meter.Float64ObservableGauge("linearfs.budget.reset_seconds",
		metric.WithUnit("s"),
		metric.WithDescription("Seconds until the server-reported window reset, per axis"))
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		log.Printf("telemetry: budget gauges not registered: %v", err)
		return
	}

	observeAxis := func(o metric.Observer, axis string, s axisSnapshot) {
		attrs := metric.WithAttributes(attribute.String("axis", axis))
		o.ObserveFloat64(inflight, s.inFlight, attrs)
		if !s.seen {
			return
		}
		o.ObserveFloat64(remaining, s.remaining, attrs)
		o.ObserveFloat64(limit, s.limit, attrs)
		o.ObserveFloat64(reset, s.resetSeconds, attrs)
	}
	_, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		cx, rq := b.snapshot()
		observeAxis(o, "complexity", cx)
		observeAxis(o, "requests", rq)
		return nil
	}, remaining, limit, inflight, reset)
	if err != nil {
		log.Printf("telemetry: budget gauge callback not registered: %v", err)
	}
}
