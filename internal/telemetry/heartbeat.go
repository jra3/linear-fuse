package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// registerHeartbeat installs the phase-1 instruments that make the pipeline
// verifiable end-to-end: process uptime and a build-info gauge carrying the
// version/commit attributes.
func registerHeartbeat(provider *sdkmetric.MeterProvider, version, commit string) error {
	meter := provider.Meter("linearfs/process")
	start := time.Now()

	_, err := meter.Float64ObservableGauge("linearfs.process.uptime_seconds",
		metric.WithUnit("s"),
		metric.WithDescription("Seconds since the linearfs process started"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			o.Observe(time.Since(start).Seconds())
			return nil
		}),
	)
	if err != nil {
		return err
	}

	buildAttrs := metric.WithAttributes(
		attribute.String("version", version),
		attribute.String("commit", commit),
	)
	_, err = meter.Int64ObservableGauge("linearfs.build.info",
		metric.WithDescription("Build metadata carried as attributes; value is always 1"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(1, buildAttrs)
			return nil
		}),
	)
	return err
}
