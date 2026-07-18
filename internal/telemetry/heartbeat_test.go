package telemetry

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// readUptime collects the manual reader and returns the single
// linearfs.process.uptime_seconds gauge value, or -1 if absent.
func readUptime(t *testing.T, r *sdkmetric.ManualReader) float64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "linearfs.process.uptime_seconds" {
				g, ok := m.Data.(metricdata.Gauge[float64])
				if !ok || len(g.DataPoints) == 0 {
					t.Fatalf("uptime is %T with %d points", m.Data, len(g.DataPoints))
				}
				return g.DataPoints[0].Value
			}
		}
	}
	return -1
}

// TestUptimeTracksTimeSinceStart is the #256 regression guard: the uptime gauge
// must report seconds since the process (registration) start and keep growing
// across collects — NOT time-since-last-collect or a per-collect reset, which
// would peg it near one export interval forever (the field symptom: 300.001s at
// ~93min real uptime). Two collects across a real sleep: the value must advance
// by at least the slept duration, proving `start` is captured once and never
// reset by a collect.
func TestUptimeTracksTimeSinceStart(t *testing.T) {
	r := sdkmetric.NewManualReader()
	p := sdkmetric.NewMeterProvider(sdkmetric.WithReader(r))
	if err := registerHeartbeat(p, "v", "c"); err != nil {
		t.Fatalf("registerHeartbeat: %v", err)
	}

	const sleep = 120 * time.Millisecond
	v0 := readUptime(t, r)
	time.Sleep(sleep)
	v1 := readUptime(t, r)

	if v0 < 0 || v1 < 0 {
		t.Fatalf("uptime gauge not found (v0=%.4f v1=%.4f)", v0, v1)
	}
	// The second collect must reflect the elapsed sleep, not restart from ~0.
	if delta := v1 - v0; delta < sleep.Seconds()*0.75 {
		t.Errorf("uptime advanced %.4fs across a %.3fs sleep — gauge is measuring time-since-collect, not since start (#256)", delta, sleep.Seconds())
	}
}
