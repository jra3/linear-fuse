package telemetry

// Instrument-creation helpers for the linearfs.* instrument sites (phase 2
// established the pattern privately in internal/api; the phase-3 sites in
// sync/repo/reconcile share these instead of re-copying them). They degrade an
// instrument-creation failure (an invalid name — a programming error) to a
// logged no-op instead of a nil that would panic at record time: telemetry
// must never take the process down. Callers still bind instruments once, at
// construction, from otel.Meter — these helpers touch only the otel API
// packages, never the SDK.

import (
	"log"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// MustInt64Counter returns the counter, or a logged no-op on creation failure.
func MustInt64Counter(m metric.Meter, name string, opts ...metric.Int64CounterOption) metric.Int64Counter {
	c, err := m.Int64Counter(name, opts...)
	if err != nil {
		log.Printf("telemetry: creating %s: %v", name, err)
		c, _ = noop.NewMeterProvider().Meter("noop").Int64Counter(name)
	}
	return c
}

// MustFloat64Histogram returns the histogram, or a logged no-op on creation
// failure.
func MustFloat64Histogram(m metric.Meter, name string, opts ...metric.Float64HistogramOption) metric.Float64Histogram {
	h, err := m.Float64Histogram(name, opts...)
	if err != nil {
		log.Printf("telemetry: creating %s: %v", name, err)
		h, _ = noop.NewMeterProvider().Meter("noop").Float64Histogram(name)
	}
	return h
}
