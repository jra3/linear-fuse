package fs

// Serving-layer metrics tests. linearfs.fuse.* and linearfs.embedded_files.fetch
// bind lazily on first record (the package has no construction choke), so — like
// the reconcile prune-counter tests — this installs ONE manual-reader provider
// for the whole binary in TestMain, before any test can bind the instruments.
// The record tests assert on a synthetic op name / a before-after delta so real
// commit-tail and fetch activity from other fs tests can't perturb them.

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var metricsReader *sdkmetric.ManualReader

func TestMain(m *testing.M) {
	metricsReader = sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricsReader)))
	os.Exit(m.Run())
}

// counterValue returns the Int64 sum datapoint for metric name matching every
// attribute in want, or 0 when none has been recorded.
func counterValue(t *testing.T, name string, want map[string]string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			if md.Name != name {
				continue
			}
			sum, ok := md.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s data is %T, want Sum[int64]", name, md.Data)
			}
			for _, dp := range sum.DataPoints {
				if attrsMatch(dp.Attributes, want) {
					return dp.Value
				}
			}
		}
	}
	return 0
}

func attrsMatch(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.AsString() != v {
			return false
		}
	}
	return true
}

func TestOutcomeForErrno(t *testing.T) {
	t.Parallel()
	cases := map[syscall.Errno]string{
		0:                "ok",
		syscall.EINVAL:   "einval",
		syscall.EIO:      "eio",
		syscall.EAGAIN:   "eagain",
		syscall.ENOENT:   "enoent",
		syscall.EMSGSIZE: "emsgsize",
		syscall.EPERM:    "eperm",
		syscall.EACCES:   "eacces",
		syscall.ENOSPC:   "other",
	}
	for errno, want := range cases {
		if got := outcomeForErrno(errno); got != want {
			t.Errorf("outcomeForErrno(%v) = %q, want %q", errno, got, want)
		}
	}
}

// TestRecordFuseOp records under a synthetic op so no production path collides,
// and asserts the outcome is derived from the errno.
func TestRecordFuseOp(t *testing.T) {
	ctx := context.Background()
	recordFuseOp(ctx, "smoke-test-op", time.Now(), syscall.EINVAL)

	if got := counterValue(t, "linearfs.fuse.ops",
		map[string]string{"op": "smoke-test-op", "outcome": "einval"}); got != 1 {
		t.Errorf("ops{op=smoke-test-op,outcome=einval} = %d, want 1", got)
	}
	// The wrong-outcome datapoint must not exist for this synthetic op.
	if got := counterValue(t, "linearfs.fuse.ops",
		map[string]string{"op": "smoke-test-op", "outcome": "ok"}); got != 0 {
		t.Errorf("ops{op=smoke-test-op,outcome=ok} = %d, want 0", got)
	}
}

// TestRecordEmbeddedFetch asserts a fetch bumps its tier counter by exactly one
// (a before-after delta, since the memory|disk|cdn enum is shared with real
// fetch paths).
func TestRecordEmbeddedFetch(t *testing.T) {
	ctx := context.Background()
	before := counterValue(t, "linearfs.embedded_files.fetch", map[string]string{"source": "disk"})
	recordEmbeddedFetch(ctx, "disk")
	after := counterValue(t, "linearfs.embedded_files.fetch", map[string]string{"source": "disk"})
	if after != before+1 {
		t.Errorf("embedded_files.fetch{source=disk} = %d, want %d", after, before+1)
	}
}
