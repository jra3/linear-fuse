// Package telemetry owns the OTEL metrics pipeline for linearfs.
//
// One data source, two renderings: a single SDK MeterProvider feeds
//   - an always-on journald summary — a PeriodicReader (5 min) whose exporter
//     renders one compact human-readable log line from whatever instruments
//     exist, and
//   - an opt-in JSONL file export — a second PeriodicReader (config-gated,
//     default off) writing one JSON line per export through a size-capped
//     rotation writer.
//
// Init registers the provider globally (otel.SetMeterProvider), so instrument
// sites elsewhere in the tree just call otel.Meter("linearfs/<layer>") and
// never import the SDK. Metrics only — no tracer is ever configured.
package telemetry

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/jra3/linear-fuse/internal/config"
)

// summaryInterval is how often the always-on journald summary line is emitted.
const summaryInterval = 5 * time.Minute

// Defaults applied when the config-gated file exporter is enabled but a field
// was left zero.
const (
	defaultFileInterval  = 60 * time.Second
	defaultFileMaxSizeMB = 50
)

// Init builds the metrics pipeline from cfg and registers the resulting
// MeterProvider globally. version/commit come from the cmd package's ldflags
// vars and are carried on the linearfs.build.info heartbeat gauge.
//
// The returned shutdown flushes both readers (a final export) and releases the
// file writer; call it on unmount/exit. Failure to set up the optional file
// exporter degrades to summary-only (logged, not fatal) — telemetry must never
// block mounting.
func Init(cfg config.TelemetryConfig, version, commit string) (func(context.Context) error, error) {
	res := resource.NewSchemaless(
		attribute.String("service.name", "linearfs"),
		attribute.String("service.version", version),
	)

	opts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			newSummaryExporter(log.Printf),
			sdkmetric.WithInterval(summaryInterval),
		)),
	}

	var rot *rotatingWriter
	if cfg.File.Enabled {
		if rw, reader, err := newFileReader(cfg.File); err != nil {
			log.Printf("telemetry: file export disabled: %v", err)
		} else {
			rot = rw
			opts = append(opts, sdkmetric.WithReader(reader))
		}
	}

	provider := sdkmetric.NewMeterProvider(opts...)
	otel.SetMeterProvider(provider)

	if err := registerHeartbeat(provider, version, commit); err != nil {
		log.Printf("telemetry: heartbeat registration failed: %v", err)
	}

	shutdown := func(ctx context.Context) error {
		err := provider.Shutdown(ctx)
		if rot != nil {
			if cerr := rot.Close(); err == nil {
				err = cerr
			}
		}
		return err
	}
	return shutdown, nil
}

// newFileReader builds the JSONL file export leg: a rotation writer at the
// configured path feeding a stdoutmetric exporter (one compact JSON line per
// export) on a PeriodicReader at the configured interval.
func newFileReader(fc config.TelemetryFileConfig) (*rotatingWriter, sdkmetric.Reader, error) {
	path := fc.Path
	if path == "" {
		path = config.DefaultTelemetryPath()
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}

	maxMB := fc.MaxSizeMB
	if maxMB <= 0 {
		maxMB = defaultFileMaxSizeMB
	}
	interval := fc.Interval
	if interval <= 0 {
		interval = defaultFileInterval
	}

	rw, err := newRotatingWriter(path, int64(maxMB)*1024*1024)
	if err != nil {
		return nil, nil, err
	}
	exp, err := stdoutmetric.New(stdoutmetric.WithWriter(rw))
	if err != nil {
		_ = rw.Close()
		return nil, nil, err
	}
	return rw, sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(interval)), nil
}
