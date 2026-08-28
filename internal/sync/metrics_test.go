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
	"errors"
	"fmt"
	"os"
	"strings"
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
// issue, deferred for the re-enqueued one — and a whole-batch gate (the
// admission ladder refusing the fetch) folds its deferrals into the same
// series.
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

	// Gate path: the ladder refuses the fetch, deferring the whole batch.
	mock.detailsErr = fmt.Errorf("query IssueDetailsBatch deferred by budget ladder (detail reserve): %w", api.ErrDeferred)
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

// probeOutcomeValue returns the probe_outcomes count for one kind+outcome
// pair, or -1 when no such datapoint exists.
func probeOutcomeValue(t *testing.T, rm metricdata.ResourceMetrics, kind, outcome string) int64 {
	t.Helper()
	m, ok := findMetric(rm, "linearfs.sync.probe_outcomes")
	if !ok {
		return -1
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("probe_outcomes data is %T, want Sum[int64]", m.Data)
	}
	for _, dp := range sum.DataPoints {
		k, kok := dp.Attributes.Value(attribute.Key("kind"))
		o, ook := dp.Attributes.Value(attribute.Key("outcome"))
		if kok && ook && k.AsString() == kind && o.AsString() == outcome {
			return dp.Value
		}
	}
	return -1
}

// TestProbeOutcomesRecorded: each probeInitiatives run lands in exactly one
// probe_outcomes datapoint — unchanged when the watermark holds, changed when
// it doesn't, error when the probe query fails.
func TestProbeOutcomesRecorded(t *testing.T) {
	reader := withTestMeter(t)
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	updated := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	mock.initiatives = []api.Initiative{{ID: "init-1", Slug: "q1", Name: "Q1", UpdatedAt: updated}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	// Arm the watermark via the full workspace sync, then probe: unchanged.
	if err := worker.syncWorkspace(ctx); err != nil {
		t.Fatalf("syncWorkspace: %v", err)
	}
	worker.probeInitiatives(ctx)
	rm := collectMetrics(t, reader)
	if got := probeOutcomeValue(t, rm, "initiatives", "unchanged"); got != 1 {
		t.Errorf("probe_outcomes{initiatives,unchanged} = %d, want 1", got)
	}

	// A newer updatedAt: changed.
	mock.initiatives[0].UpdatedAt = updated.Add(time.Minute)
	worker.probeInitiatives(ctx)
	rm = collectMetrics(t, reader)
	if got := probeOutcomeValue(t, rm, "initiatives", "changed"); got != 1 {
		t.Errorf("probe_outcomes{initiatives,changed} = %d, want 1", got)
	}

	// A failing probe query: error.
	mock.initiativesProbeErr = errors.New("probe boom")
	worker.probeInitiatives(ctx)
	rm = collectMetrics(t, reader)
	if got := probeOutcomeValue(t, rm, "initiatives", "error"); got != 1 {
		t.Errorf("probe_outcomes{initiatives,error} = %d, want 1", got)
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

// cycleOutcomeCount returns the cycle_duration sample count for one
// mode+outcome series, or -1 when no such datapoint exists. The per-series
// Count is the point of the outcome attribute: the buckets cannot separate a
// ~0s deferred cycle from a healthy one (default bounds start at 5 — seconds
// here), but the counts can.
func cycleOutcomeCount(t *testing.T, rm metricdata.ResourceMetrics, mode, outcome string) uint64 {
	t.Helper()
	m, ok := findMetric(rm, "linearfs.sync.cycle_duration")
	if !ok {
		return 0
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("cycle_duration data is %T, want Histogram[float64]", m.Data)
	}
	for _, dp := range h.DataPoints {
		md, mok := dp.Attributes.Value(attribute.Key("mode"))
		o, ook := dp.Attributes.Value(attribute.Key("outcome"))
		if mok && ook && md.AsString() == mode && o.AsString() == outcome {
			return dp.Count
		}
	}
	return 0
}

// TestCycleOutcomeRecorded: a full cycle carries what its drains did. The
// three values are not decorative — deferred is the one that withheld the
// stamp, and failed is the one that stamped ANYWAY (the accepted asymmetry),
// so recording both as "complete" would hide exactly the case worth watching.
func TestCycleOutcomeRecorded(t *testing.T) {
	reader := withTestMeter(t)
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour, FullSyncInterval: time.Hour})

	// Driven at cycleFull directly: the mode schedule is worker_test's
	// subject, this test's is the attribute each mode carries.
	if err := worker.syncCycle(ctx, cycleFull); err != nil {
		t.Fatalf("complete cycle: %v", err)
	}
	if got := cycleOutcomeCount(t, collectMetrics(t, reader), "full", "complete"); got != 1 {
		t.Errorf("cycle_duration{mode=full,outcome=complete} count = %d, want 1", got)
	}

	// The ladder refuses the workspace drain: deferred (and, per the stamp
	// rule, this is the cycle that leaves the full sync due).
	mock.workspaceErr = fmt.Errorf("workspace: %w", api.ErrBudget)
	if err := worker.syncCycle(ctx, cycleFull); err != nil {
		t.Fatalf("deferred cycle: %v", err)
	}
	if got := cycleOutcomeCount(t, collectMetrics(t, reader), "full", "deferred"); got != 1 {
		t.Errorf("cycle_duration{mode=full,outcome=deferred} count = %d, want 1", got)
	}

	// A non-budget failure: recorded as failed, distinct from both.
	mock.workspaceErr = errors.New("boom: internal server error")
	if err := worker.syncCycle(ctx, cycleFull); err != nil {
		t.Fatalf("failed cycle: %v", err)
	}
	rm := collectMetrics(t, reader)
	if got := cycleOutcomeCount(t, rm, "full", "failed"); got != 1 {
		t.Errorf("cycle_duration{mode=full,outcome=failed} count = %d, want 1", got)
	}
	if got := cycleOutcomeCount(t, rm, "full", "complete"); got != 1 {
		t.Errorf("cycle_duration{mode=full,outcome=complete} count = %d, want 1 (unchanged)", got)
	}
}

// TestLeanCycleCarriesNoOutcome: lean cycles run no skeleton-tier drain and
// never stamp, so "complete" would assert something no code checks. The
// attribute is absent, not defaulted.
func TestLeanCycleCarriesNoOutcome(t *testing.T) {
	reader := withTestMeter(t)
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour, FullSyncInterval: time.Hour})

	// First cycle is full (fresh store) and stamps; the second runs lean.
	if err := worker.syncAllTeams(ctx); err != nil {
		t.Fatalf("full cycle: %v", err)
	}
	if err := worker.syncAllTeams(ctx); err != nil {
		t.Fatalf("lean cycle: %v", err)
	}

	m, ok := findMetric(collectMetrics(t, reader), "linearfs.sync.cycle_duration")
	if !ok {
		t.Fatal("linearfs.sync.cycle_duration not recorded")
	}
	h := m.Data.(metricdata.Histogram[float64])
	var lean int
	for _, dp := range h.DataPoints {
		md, _ := dp.Attributes.Value(attribute.Key("mode"))
		if md.AsString() != "lean" {
			continue
		}
		lean++
		if _, ok := dp.Attributes.Value(attribute.Key("outcome")); ok {
			t.Errorf("lean datapoint carries an outcome attribute: %v", dp.Attributes)
		}
	}
	if lean != 1 {
		t.Errorf("lean datapoints = %d, want 1", lean)
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

// =============================================================================
// Write-burst wiring (#490)
// =============================================================================

// The burst detector itself lives in internal/db and its window arithmetic is
// unit-tested there. The pair that matters is here, because this is where both
// shapes exist: the per-issue cascade loop #427 shipped, and the single
// transaction PR #488 replaced it with. Same statements, same count — one
// alarm.
//
// Both depend on the real clock: the detector trips on 64 autocommit writes
// inside one second, so the loop has to average under ~15 ms per write. A
// measured autocommit write is ~667 µs, so there is 20x of headroom. A failure
// here means the disk got two orders of magnitude slower, not that the
// threshold is wrong.

// burstCounts returns linearfs.db.write_burst keyed by its caller attribute.
func burstCounts(t *testing.T, rm metricdata.ResourceMetrics) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	m, ok := findMetric(rm, "linearfs.db.write_burst")
	if !ok {
		return out
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("write_burst is %T, want Sum[int64]", m.Data)
	}
	for _, dp := range sum.DataPoints {
		caller, _ := dp.Attributes.Value("caller")
		out[caller.AsString()] = dp.Value
	}
	return out
}

// dbExecCount returns linearfs.db.ops for {op=exec, in_tx}.
func dbExecCount(t *testing.T, rm metricdata.ResourceMetrics, inTx bool) int64 {
	t.Helper()
	m, ok := findMetric(rm, "linearfs.db.ops")
	if !ok {
		return -1
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("db.ops is %T, want Sum[int64]", m.Data)
	}
	for _, dp := range sum.DataPoints {
		op, _ := dp.Attributes.Value("op")
		tx, _ := dp.Attributes.Value("in_tx")
		if op.AsString() == "exec" && tx.AsBool() == inTx {
			return dp.Value
		}
	}
	return -1
}

const burstTestIssues = 40 // x8 statements per cascade = 320 writes

// seedIssuesForBurst seeds burstTestIssues cached issues and returns their IDs.
// The seeding itself is 40-ish autocommit writes, deliberately under the
// detector's threshold of 64, so neither test trips before the part it is
// measuring.
func seedIssuesForBurst(t *testing.T, store *db.Store, at time.Time) []string {
	t.Helper()
	ids := make([]string, 0, burstTestIssues)
	for i := 1; i <= burstTestIssues; i++ {
		id := fmt.Sprintf("issue-%d", i)
		seedCachedIssue(t, store, "team-1", "TST", id, fmt.Sprintf("TST-%d", i), at)
		ids = append(ids, id)
	}
	return ids
}

// TestWriteBurstFiresOnAnUnbatchedCascade is the defect's shape: a per-issue
// DeleteIssueCascade loop in autocommit, which is what the #427 repair did
// before review caught it. The instrument has to see it, and has to name the
// function running the loop rather than sqlc's generated delete.
func TestWriteBurstFiresOnAnUnbatchedCascade(t *testing.T) {
	reader := withTestMeter(t)
	store := openTestStore(t) // after withTestMeter: instruments bind at Open
	defer store.Close()
	ctx := context.Background()

	ids := seedIssuesForBurst(t, store, rekeyTime(t))
	for _, id := range ids {
		db.DeleteIssueCascade(ctx, store.Queries(), id, func(family string, err error) {
			t.Errorf("cascade %s for %s: %v", family, id, err)
		})
	}

	rm := collectMetrics(t, reader)
	bursts := burstCounts(t, rm)
	if len(bursts) == 0 {
		t.Fatalf("write_burst recorded nothing for %d autocommit writes", burstTestIssues*8)
	}
	for caller, n := range bursts {
		if !strings.HasPrefix(caller, "internal/sync.") {
			t.Errorf("burst attributed to %q, want the internal/sync frame running the loop", caller)
		}
		t.Logf("write_burst{caller=%s} = %d", caller, n)
	}
	if got := dbExecCount(t, rm, true); got != -1 {
		t.Errorf("db.ops{op=exec,in_tx=true} = %d, want no such series: nothing here is transactional", got)
	}
}

// TestWriteBurstStaysSilentOnTheRebuildTransaction is the fix's shape: the
// same cascades, same count, inside Store.WithTx. The detector must not fire —
// an alarm that flagged the batched form as the defect would make the in_tx
// ratio unreadable.
func TestWriteBurstStaysSilentOnTheRebuildTransaction(t *testing.T) {
	reader := withTestMeter(t)
	store := openTestStore(t) // after withTestMeter: instruments bind at Open
	defer store.Close()
	at := rekeyTime(t)

	seedTeamRow(t, store, "team-1", "QA", "Quality")
	seedIssuesForBurst(t, store, at)
	seedWatermark(t, store, "team-1", at)

	worker := NewWorker(newMockAPIClient(), store, Config{Interval: time.Hour})
	worker.rebuildTeamIssues(context.Background(), api.Team{ID: "team-1", Key: "QA", Name: "Quality"})

	rm := collectMetrics(t, reader)
	if bursts := burstCounts(t, rm); len(bursts) > 0 {
		t.Errorf("write_burst fired on the batched rebuild: %v", bursts)
	}
	// Not vacuous: the statements really ran, and ran transaction-bound.
	if got := dbExecCount(t, rm, true); got < burstTestIssues*8 {
		t.Errorf("db.ops{op=exec,in_tx=true} = %d, want >= %d", got, burstTestIssues*8)
	}
}
