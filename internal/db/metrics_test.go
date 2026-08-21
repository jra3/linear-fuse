package db

// Persistence-layer instrument tests (phase-2 pattern, see
// internal/api/metrics_test.go): a manual-reader SDK provider is installed as
// the global otel provider, the Store is opened (instruments bind at Open),
// and the collected metricdata is asserted by hand.
//
// These tests are deliberately NOT parallel: they swap the global meter
// provider. TestMain pins the global to an explicit no-op first, so the Stores
// opened by the package's other tests can never be delegated onto a test
// provider and pollute a collection — otel delegates instruments created
// before the first SetMeterProvider exactly once, and every test in this
// package opens a Store.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/telemetry"
)

func TestMain(m *testing.M) {
	otel.SetMeterProvider(noopmetric.NewMeterProvider())
	os.Exit(m.Run())
}

// withTestMeter installs a fresh SDK provider as the global meter provider and
// returns its manual reader. Cleanup restores the no-op provider. Open the
// Store AFTER calling this: instruments bind at Open.
func withTestMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(noopmetric.NewMeterProvider())
		_ = provider.Shutdown(context.Background())
	})
	return reader
}

func openMetricsTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func collectDB(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return rm
}

func findDBMetric(rm metricdata.ResourceMetrics, name string) (metricdata.Metrics, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// opCount returns the linearfs.db.ops count for {op, in_tx}, or -1 when no
// such datapoint exists.
func opCount(t *testing.T, rm metricdata.ResourceMetrics, op string, inTx bool) int64 {
	t.Helper()
	m, ok := findDBMetric(rm, "linearfs.db.ops")
	if !ok {
		return -1
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("linearfs.db.ops is %T, want Sum[int64]", m.Data)
	}
	for _, dp := range sum.DataPoints {
		gotOp, _ := dp.Attributes.Value("op")
		gotTx, _ := dp.Attributes.Value("in_tx")
		if gotOp.AsString() == op && gotTx.AsBool() == inTx {
			return dp.Value
		}
	}
	return -1
}

// TestEveryChokepointMethodIsInstrumented pins the ticket's coverage claim:
// all four DBTX methods record, so no call site anywhere needs to change.
func TestEveryChokepointMethodIsInstrumented(t *testing.T) {
	reader := withTestMeter(t)
	store := openMetricsTestStore(t)
	ctx := context.Background()

	// One of each, straight through the store's own wrapper.
	if _, err := store.qdb.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", "nobody"); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	rows, err := store.qdb.QueryContext(ctx, "SELECT id FROM issues LIMIT 1")
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	_ = rows.Close()
	var one int
	if err := store.qdb.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("QueryRowContext: %v", err)
	}
	stmt, err := store.qdb.PrepareContext(ctx, "SELECT 1")
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	_ = stmt.Close()

	rm := collectDB(t, reader)
	for _, op := range []string{opExec, opQuery, opQueryRow, opPrepare} {
		if got := opCount(t, rm, op, false); got < 1 {
			t.Errorf("linearfs.db.ops{op=%s,in_tx=false} = %d, want >= 1", op, got)
		}
	}
	if _, ok := findDBMetric(rm, "linearfs.db.op_duration"); !ok {
		t.Error("linearfs.db.op_duration not recorded")
	}
}

// TestInTxMarksTransactionBoundQueries pins the attribute the whole map turns
// on: a Queries built by Store.WithTx reports in_tx=true, the store's own
// reports false. Without this the in_tx ratio would read 0% forever and look
// like a finding rather than a bug.
func TestInTxMarksTransactionBoundQueries(t *testing.T) {
	reader := withTestMeter(t)
	store := openMetricsTestStore(t)
	ctx := context.Background()

	if err := store.Queries().DeleteIssue(ctx, "autocommit-write"); err != nil {
		t.Fatalf("autocommit delete: %v", err)
	}
	err := store.WithTx(ctx, func(q *Queries) error {
		return q.DeleteIssue(ctx, "transactional-write")
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	rm := collectDB(t, reader)
	if got := opCount(t, rm, opExec, true); got != 1 {
		t.Errorf("linearfs.db.ops{op=exec,in_tx=true} = %d, want 1", got)
	}
	if got := opCount(t, rm, opExec, false); got < 1 {
		t.Errorf("linearfs.db.ops{op=exec,in_tx=false} = %d, want >= 1", got)
	}
}

// TestOpDurationBucketsSeparateAutocommitFromInTx is the ticket's "Done when"
// on the histogram: a ~3 µs in-transaction write and a ~667 µs autocommit
// fsync must not land in the same bucket. Under the SDK's default boundaries —
// which start at 0 and jump to 5, in a histogram measured in seconds — both
// land in the first bucket and the instrument shows nothing.
//
// The assertion is on the collected Bounds, so it fails if the advisory
// boundaries ever stop reaching the SDK (a View override, an API change),
// not merely if the constant is edited.
func TestOpDurationBucketsSeparateAutocommitFromInTx(t *testing.T) {
	reader := withTestMeter(t)
	m := newDBMetrics()

	const inTxWrite = 0.000003       // 3 µs, one statement inside a transaction
	const autocommitWrite = 0.000667 // 667 µs, one statement + its WAL fsync
	m.opDuration.Record(context.Background(), inTxWrite)
	m.opDuration.Record(context.Background(), autocommitWrite)

	metrics, ok := findDBMetric(collectDB(t, reader), "linearfs.db.op_duration")
	if !ok {
		t.Fatal("linearfs.db.op_duration not recorded")
	}
	hist, ok := metrics.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("op_duration is %T, want Histogram[float64]", metrics.Data)
	}
	if len(hist.DataPoints) != 1 {
		t.Fatalf("datapoints = %d, want 1", len(hist.DataPoints))
	}
	dp := hist.DataPoints[0]

	if len(dp.Bounds) != len(opDurationBoundaries) {
		t.Fatalf("bounds = %v, want the advisory ladder %v", dp.Bounds, opDurationBoundaries)
	}
	for i, want := range opDurationBoundaries {
		if dp.Bounds[i] != want {
			t.Fatalf("bounds = %v, want the advisory ladder %v", dp.Bounds, opDurationBoundaries)
		}
	}

	fast := bucketOf(dp.Bounds, inTxWrite)
	slow := bucketOf(dp.Bounds, autocommitWrite)
	if slow-fast < 4 {
		t.Errorf("3 µs landed in bucket %d and 667 µs in bucket %d — %d apart, want at least 4",
			fast, slow, slow-fast)
	}
	if fast == 0 {
		t.Error("3 µs landed in the underflow bucket: the ladder starts too high to resolve an in-transaction write")
	}
	if slow == len(dp.Bounds) {
		t.Error("667 µs landed in the overflow bucket: the ladder ends too low to resolve an autocommit fsync")
	}
	if got := dp.BucketCounts[fast]; got != 1 {
		t.Errorf("bucket %d count = %d, want 1", fast, got)
	}
	if got := dp.BucketCounts[slow]; got != 1 {
		t.Errorf("bucket %d count = %d, want 1", slow, got)
	}
}

// bucketOf returns the index of the explicit-histogram bucket holding v: the
// first bound v does not exceed, or len(bounds) for the overflow bucket.
func bucketOf(bounds []float64, v float64) int {
	for i, b := range bounds {
		if v <= b {
			return i
		}
	}
	return len(bounds)
}

// TestBurstDetectorWindow drives the tumbling window with synthetic timestamps
// — the wiring tests in internal/sync depend on the real clock, this one pins
// the arithmetic without it.
func TestBurstDetectorWindow(t *testing.T) {
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	t.Run("under the threshold never trips", func(t *testing.T) {
		b := &burstDetector{}
		for i := 0; i < burstThreshold-1; i++ {
			if b.observe(base.Add(time.Duration(i) * time.Millisecond)) {
				t.Fatalf("tripped on write %d, below the threshold of %d", i+1, burstThreshold)
			}
		}
	})

	t.Run("the Nth write inside the window trips", func(t *testing.T) {
		b := &burstDetector{}
		for i := 0; i < burstThreshold-1; i++ {
			b.observe(base.Add(time.Duration(i) * time.Millisecond))
		}
		if !b.observe(base.Add(burstThreshold * time.Millisecond)) {
			t.Fatalf("write %d inside the window did not trip", burstThreshold)
		}
	})

	t.Run("writes spread wider than the window never trip", func(t *testing.T) {
		b := &burstDetector{}
		for i := 0; i < burstThreshold*4; i++ {
			at := base.Add(time.Duration(i) * (burstWindow + time.Millisecond))
			if b.observe(at) {
				t.Fatalf("a trickle of one write per %v tripped at write %d", burstWindow, i+1)
			}
		}
	})

	t.Run("a long run keeps tripping so the count tracks volume", func(t *testing.T) {
		b := &burstDetector{}
		trips := 0
		for i := 0; i < burstThreshold*3; i++ {
			if b.observe(base.Add(time.Duration(i) * time.Microsecond)) {
				trips++
			}
		}
		if trips != 3 {
			t.Errorf("trips over %d writes = %d, want 3", burstThreshold*3, trips)
		}
	})
}

// TestZeroValueMetricsDoNotPanic pins the nil-tolerance the layer needs:
// ctxDetachDBTX is built in more than one place and by tests, and telemetry
// must never panic a query.
func TestZeroValueMetricsDoNotPanic(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()

	uninstrumented := ctxDetachDBTX{inner: raw} // zero dbMetrics, nil burst detector
	ctx := context.Background()
	var one int
	if err := uninstrumented.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("QueryRowContext on an uninstrumented wrapper: %v", err)
	}
	if _, err := uninstrumented.ExecContext(ctx, "CREATE TABLE t (id TEXT)"); err != nil {
		t.Fatalf("ExecContext on an uninstrumented wrapper: %v", err)
	}
	// And the write path stays silent rather than reaching a nil burst counter.
	for i := 0; i < burstThreshold*2; i++ {
		if _, err := uninstrumented.ExecContext(ctx, "INSERT INTO t (id) VALUES (?)", i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
}

// TestBurstCallerSkipsTheDBPackage pins what the caller attribute is for: the
// logical operation, not sqlc's generated method or a shared helper like
// DeleteIssueCascade. Called from inside this package every candidate frame up
// to the test runner is internal/db, so a walk that did not skip them would
// name one — the positive case (a real internal/sync caller) is pinned by
// TestWriteBurstFiresOnAnUnbatchedCascade.
func TestBurstCallerSkipsTheDBPackage(t *testing.T) {
	got := callerThroughDBFrames()
	if strings.HasPrefix(got, "internal/db.") {
		t.Errorf("burstCaller() = %q, want the first frame outside internal/db", got)
	}
	if got == "" || got == "unknown" {
		t.Errorf("burstCaller() = %q, want a named frame", got)
	}
}

// callerThroughDBFrames stands in for the generated Queries methods the real
// walk skips.
func callerThroughDBFrames() string { return burstCaller() }

// TestDBMetricsReachTheJSONLExport is the ticket's other "Done when": the
// instruments must arrive in both renderings. The journald projection is
// asserted next to it in internal/telemetry (summary_test.go); this is the
// file leg, end to end through the real pipeline — a real Store's writes, the
// real provider, JSONL on disk carrying the full cardinality the summary line
// drops.
func TestDBMetricsReachTheJSONLExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	shutdown, err := telemetry.Init(config.TelemetryConfig{
		File: config.TelemetryFileConfig{
			Enabled:   true,
			Path:      path,
			Interval:  time.Hour, // rely on shutdown's final flush, not the ticker
			MaxSizeMB: 1,
		},
	}, "v0.0.0-test", "deadbeef")
	if err != nil {
		t.Fatalf("telemetry.Init: %v", err)
	}
	t.Cleanup(func() { otel.SetMeterProvider(noopmetric.NewMeterProvider()) })

	store := openMetricsTestStore(t)
	ctx := context.Background()

	// One transaction-bound write, then enough autocommit writes to trip a burst.
	if err := store.WithTx(ctx, func(q *Queries) error {
		return q.DeleteIssue(ctx, "transactional-write")
	}); err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	for i := 0; i <= burstThreshold; i++ {
		if err := store.Queries().DeleteIssue(ctx, fmt.Sprintf("autocommit-write-%d", i)); err != nil {
			t.Fatalf("autocommit delete %d: %v", i, err)
		}
	}

	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(flushCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"linearfs.db.ops",
		"linearfs.db.op_duration",
		"linearfs.db.write_burst",
		"in_tx",
		"caller",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("JSONL export missing %q", want)
		}
	}
}
