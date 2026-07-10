package sync

// Sync-instrument tests (phase-2 pattern, see internal/api/metrics_test.go):
// a manual-reader SDK provider is installed as the global otel provider, the
// Worker is constructed (instruments bind at construction), and the collected
// metricdata is asserted by hand.
//
// These tests are deliberately NOT parallel: they swap the global meter
// provider. TestMain pins the global to an explicit no-op first, so Workers
// built by the package's other (parallel) tests can never be delegated onto
// a test provider and pollute a collection.

import (
	"context"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

func TestMain(m *testing.M) {
	otel.SetMeterProvider(noopmetric.NewMeterProvider())
	os.Exit(m.Run())
}

// withTestMeter installs a fresh SDK provider as the global meter provider
// and returns its manual reader. Cleanup restores the no-op provider.
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

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return rm
}

func findMetric(rm metricdata.ResourceMetrics, name string) (metricdata.Metrics, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// outcomeValue returns the detail_outcomes count for one outcome, or -1 when
// no such datapoint exists.
func outcomeValue(t *testing.T, rm metricdata.ResourceMetrics, outcome string) int64 {
	t.Helper()
	m, ok := findMetric(rm, "linearfs.sync.detail_outcomes")
	if !ok {
		return -1
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("detail_outcomes data is %T, want Sum[int64]", m.Data)
	}
	for _, dp := range sum.DataPoints {
		if v, ok := dp.Attributes.Value(attribute.Key("outcome")); ok && v.AsString() == outcome {
			return dp.Value
		}
	}
	return -1
}

// TestSyncDetailsRecordsOutcomes: one clean and one unclean issue through
// syncDetails land as detail_outcomes datapoints — synced for the stamped
// issue, deferred for the re-enqueued one — and a whole-batch gate (budget)
// folds its deferrals into the same series.
func TestSyncDetailsRecordsOutcomes(t *testing.T) {
	reader := withTestMeter(t)
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	// issue-bad's relation has no RelatedIssue, so its relation collection
	// upsert fails → unclean → deferred. issue-ok gets the default empty
	// details → clean → synced.
	mock.detailsByIssue["issue-bad"] = &api.IssueDetails{
		Relations: []api.IssueRelation{{ID: "rel-1"}},
	}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	outcome := worker.syncDetails(ctx, []issueRef{
		{ID: "issue-ok", Identifier: "TST-1"},
		{ID: "issue-bad", Identifier: "TST-2"},
	})
	if len(outcome.synced) != 1 || len(outcome.deferred) != 1 {
		t.Fatalf("outcome = %d synced, %d deferred; want 1/1", len(outcome.synced), len(outcome.deferred))
	}

	rm := collectMetrics(t, reader)
	if got := outcomeValue(t, rm, "synced"); got != 1 {
		t.Errorf("detail_outcomes{outcome=synced} = %d, want 1", got)
	}
	if got := outcomeValue(t, rm, "deferred"); got != 1 {
		t.Errorf("detail_outcomes{outcome=deferred} = %d, want 1", got)
	}

	// Gate path: budget over the defer threshold defers the whole batch.
	worker.SetBudgetReporter(&mockBudgetReporter{count: 2000, pct: 90})
	gated := worker.syncDetails(ctx, []issueRef{
		{ID: "issue-g1", Identifier: "TST-3"},
		{ID: "issue-g2", Identifier: "TST-4"},
	})
	if !gated.gated || len(gated.deferred) != 2 {
		t.Fatalf("gated outcome = %+v; want gated with 2 deferred", gated)
	}
	rm = collectMetrics(t, reader)
	if got := outcomeValue(t, rm, "deferred"); got != 3 {
		t.Errorf("detail_outcomes{outcome=deferred} = %d, want 3 (1 unclean + 2 gated)", got)
	}
	if got := outcomeValue(t, rm, "synced"); got != 1 {
		t.Errorf("detail_outcomes{outcome=synced} = %d, want 1 (unchanged by the gate)", got)
	}
}

// TestSyncCycleDurationRecorded: one SyncNow records one cycle_duration
// histogram sample.
func TestSyncCycleDurationRecorded(t *testing.T) {
	reader := withTestMeter(t)
	store := openTestStore(t)
	defer store.Close()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.SyncNow(context.Background()); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}

	rm := collectMetrics(t, reader)
	m, ok := findMetric(rm, "linearfs.sync.cycle_duration")
	if !ok {
		t.Fatal("linearfs.sync.cycle_duration not recorded")
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("cycle_duration data is %T, want Histogram[float64]", m.Data)
	}
	if len(h.DataPoints) != 1 || h.DataPoints[0].Count != 1 {
		t.Errorf("cycle_duration datapoints = %d (count %v), want one sample", len(h.DataPoints), h.DataPoints)
	}
}

// TestPendingDepthGauge: the observable gauge reports the pending_detail_sync
// backlog at collect time — registered at Worker construction, read straight
// from the table.
func TestPendingDepthGauge(t *testing.T) {
	reader := withTestMeter(t)
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := db.Now()
	for _, id := range []string{"issue-1", "issue-2"} {
		if err := store.Queries().UpsertPendingDetailSync(ctx, db.UpsertPendingDetailSyncParams{
			IssueID: id, Identifier: "TST-" + id, QueuedAt: now,
		}); err != nil {
			t.Fatalf("seed pending: %v", err)
		}
	}

	_ = NewWorker(newMockAPIClient(), store, Config{Interval: time.Hour})

	rm := collectMetrics(t, reader)
	m, ok := findMetric(rm, "linearfs.sync.pending_depth")
	if !ok {
		t.Fatal("linearfs.sync.pending_depth not observed")
	}
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("pending_depth data is %T, want Gauge[int64]", m.Data)
	}
	if len(g.DataPoints) != 1 || g.DataPoints[0].Value != 2 {
		t.Errorf("pending_depth = %+v, want one datapoint of 2", g.DataPoints)
	}

	// Draining the queue is visible on the next collect.
	for _, id := range []string{"issue-1", "issue-2"} {
		if err := store.Queries().DeletePendingDetailSync(ctx, id); err != nil {
			t.Fatalf("clear pending %s: %v", id, err)
		}
	}
	rm = collectMetrics(t, reader)
	m, ok = findMetric(rm, "linearfs.sync.pending_depth")
	if !ok {
		t.Fatal("pending_depth missing after clear")
	}
	g = m.Data.(metricdata.Gauge[int64])
	if len(g.DataPoints) != 1 || g.DataPoints[0].Value != 0 {
		t.Errorf("pending_depth after clear = %+v, want 0", g.DataPoints)
	}
}
