package api

// Instrument tests: a manual-reader SDK provider is installed as the global
// otel provider, the code under test is constructed (instruments bind at
// Client/rateBudget construction), and the collected metricdata is asserted.
//
// These tests are deliberately NOT parallel: they swap the global meter
// provider. TestMain pins the global to an explicit no-op first, so
// instruments created by the package's other tests (clients/budgets built
// while no test provider is installed) can never be delegated onto a test
// provider and pollute a collection.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jra3/linear-fuse/internal/testutil"
)

func TestMain(m *testing.M) {
	// Consume the global delegate before any test creates instruments, so
	// only instruments created under an explicitly installed test provider
	// ever reach one.
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

func findMetric(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("metric %s not found", name)
	return metricdata.Metrics{}
}

func attrsMatch(set attribute.Set, kvs []attribute.KeyValue) bool {
	for _, kv := range kvs {
		v, ok := set.Value(kv.Key)
		if !ok || v.String() != kv.Value.String() {
			return false
		}
	}
	return true
}

// counterValue returns the int64 sum datapoint carrying all of kvs, or -1.
func counterValue(t *testing.T, rm metricdata.ResourceMetrics, name string, kvs ...attribute.KeyValue) int64 {
	t.Helper()
	m := findMetric(t, rm, name)
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s: data is %T, want Sum[int64]", name, m.Data)
	}
	for _, dp := range sum.DataPoints {
		if attrsMatch(dp.Attributes, kvs) {
			return dp.Value
		}
	}
	return -1
}

// gaugeValue returns the float64 gauge datapoint carrying all of kvs.
func gaugeValue(t *testing.T, rm metricdata.ResourceMetrics, name string, kvs ...attribute.KeyValue) float64 {
	t.Helper()
	m := findMetric(t, rm, name)
	g, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("%s: data is %T, want Gauge[float64]", name, m.Data)
	}
	for _, dp := range g.DataPoints {
		if attrsMatch(dp.Attributes, kvs) {
			return dp.Value
		}
	}
	t.Fatalf("%s: no datapoint matching %v", name, kvs)
	return 0
}

// histogramPoint returns (count, sum) of the histogram datapoint carrying kvs.
func histogramPoint(t *testing.T, rm metricdata.ResourceMetrics, name string, kvs ...attribute.KeyValue) (uint64, float64) {
	t.Helper()
	m := findMetric(t, rm, name)
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s: data is %T, want Histogram[float64]", name, m.Data)
	}
	for _, dp := range h.DataPoints {
		if attrsMatch(dp.Attributes, kvs) {
			return dp.Count, dp.Sum
		}
	}
	t.Fatalf("%s: no datapoint matching %v", name, kvs)
	return 0, 0
}

func opAttr(op string) attribute.KeyValue      { return attribute.String("op", op) }
func outcomeAttr(o string) attribute.KeyValue  { return attribute.String("outcome", o) }
func tierAttr(p priority) attribute.KeyValue   { return attribute.String("tier", p.String()) }
func decisionAttr(d string) attribute.KeyValue { return attribute.String("decision", d) }
func axisAttr(a string) attribute.KeyValue     { return attribute.String("axis", a) }

// TestQueryRecordsAPIMetrics: one ok, one error, one rate-limited query
// through a mocked Linear server land as linearfs.api.requests datapoints by
// {op, outcome}, with a duration histogram sample per sent request. The
// rate-limited response also lands a budget decision (ratelimited) — the
// admission settled via rateLimited().
func TestQueryRecordsAPIMetrics(t *testing.T) {
	reader := withTestMeter(t)

	mock := testutil.NewMockLinearServer()
	defer mock.Close()
	mock.SetResponse("Viewer", map[string]any{"viewer": map[string]any{"id": "u1", "name": "U"}})
	mock.SetError("Teams", errors.New("boom"))
	mock.SetError("Users", errors.New("RATELIMITED: complexity budget exhausted"))

	client := NewClient("test-key")
	client.SetAPIURL(mock.URL())
	ctx := context.Background()

	if _, err := client.GetViewer(ctx); err != nil {
		t.Fatalf("GetViewer: %v", err)
	}
	if _, err := client.GetTeams(ctx); err == nil {
		t.Fatal("GetTeams: want error")
	}
	// Rate-limited call LAST: settling it snaps the budget windows to zero,
	// which would defer any later query.
	if _, err := client.GetUsers(ctx); err == nil {
		t.Fatal("GetUsers: want error")
	}

	rm := collectMetrics(t, reader)

	for _, tc := range []struct {
		op, outcome string
	}{
		{"Viewer", "ok"},
		{"Teams", "error"},
		{"Users", "ratelimited"},
	} {
		if got := counterValue(t, rm, "linearfs.api.requests", opAttr(tc.op), outcomeAttr(tc.outcome)); got != 1 {
			t.Errorf("requests{op=%s,outcome=%s} = %d, want 1", tc.op, tc.outcome, got)
		}
	}

	if count, _ := histogramPoint(t, rm, "linearfs.api.duration", opAttr("Viewer")); count != 1 {
		t.Errorf("duration{op=Viewer} count = %d, want 1", count)
	}

	// All three were admitted (skeleton tier), and the RATELIMITED response
	// settled its admission as a ratelimited decision.
	if got := counterValue(t, rm, "linearfs.budget.decisions", tierAttr(pSkeleton), decisionAttr("admit")); got != 3 {
		t.Errorf("decisions{tier=skeleton,decision=admit} = %d, want 3", got)
	}
	if got := counterValue(t, rm, "linearfs.budget.decisions", tierAttr(pSkeleton), decisionAttr("ratelimited")); got != 1 {
		t.Errorf("decisions{tier=skeleton,decision=ratelimited} = %d, want 1", got)
	}
}

// TestBudgetGaugesReflectSnapshot: the observable gauges read the budget's
// snapshot — server-reported limit/remaining/reset per axis, plus in-flight
// reservations — driven by a fake clock and synthetic headers, no HTTP.
func TestBudgetGaugesReflectSnapshot(t *testing.T) {
	reader := withTestMeter(t)
	clock := newFakeClock()
	b := testBudget(clock)

	// One settled round-trip seeds both axes (op cost 50) ...
	adm, _ := b.admit("Op1", pWrite)
	if adm == nil {
		t.Fatal("first admit refused")
	}
	adm.observe(fullHeaders(50, 1000000, 900000, 2500, 2000, clock.t.Add(30*time.Minute)))

	// ... and one unsettled admission holds an in-flight reservation at the
	// learned cost.
	adm2, _ := b.admit("Op1", pWrite)
	if adm2 == nil {
		t.Fatal("second admit refused")
	}
	defer adm2.release()

	rm := collectMetrics(t, reader)

	checks := []struct {
		name string
		axis string
		want float64
	}{
		{"linearfs.budget.limit", "complexity", 1000000},
		{"linearfs.budget.limit", "requests", 2500},
		{"linearfs.budget.remaining", "complexity", 900000},
		{"linearfs.budget.remaining", "requests", 2000},
		{"linearfs.budget.inflight", "complexity", 50},
		{"linearfs.budget.inflight", "requests", 1},
		{"linearfs.budget.reset_seconds", "complexity", 1800},
		{"linearfs.budget.reset_seconds", "requests", 1800},
	}
	for _, c := range checks {
		if got := gaugeValue(t, rm, c.name, axisAttr(c.axis)); got != c.want {
			t.Errorf("%s{axis=%s} = %v, want %v", c.name, c.axis, got, c.want)
		}
	}

	// The reconcile that parsed X-Complexity also recorded it.
	count, sum := histogramPoint(t, rm, "linearfs.api.complexity", opAttr("Op1"))
	if count != 1 || sum != 50 {
		t.Errorf("complexity{op=Op1} = count:%d,sum:%v, want count:1,sum:50", count, sum)
	}
}

// TestBudgetGaugesSkipUnseenAxes: before the first response, no
// remaining/limit datapoints exist (no fabricated zeros), but inflight is
// observed — a stuck reservation must be visible regardless.
func TestBudgetGaugesSkipUnseenAxes(t *testing.T) {
	reader := withTestMeter(t)
	clock := newFakeClock()
	b := testBudget(clock)

	adm, _ := b.admit("Op1", pWrite)
	if adm == nil {
		t.Fatal("admit refused on unseen budget")
	}
	defer adm.release()

	rm := collectMetrics(t, reader)
	if got := gaugeValue(t, rm, "linearfs.budget.inflight", axisAttr("requests")); got != 1 {
		t.Errorf("inflight{axis=requests} = %v, want 1", got)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "linearfs.budget.remaining" || m.Name == "linearfs.budget.limit" {
				t.Errorf("%s present before any response was observed", m.Name)
			}
		}
	}
}

// TestBudgetDecisionsCounter: ladder verdicts, driven exactly like the
// ratebudget fake-clock tests — a drained complexity window defers a detail
// read, admits a write, and a RATELIMITED settle records its own decision.
func TestBudgetDecisionsCounter(t *testing.T) {
	reader := withTestMeter(t)
	clock := newFakeClock()
	b := testBudget(clock)
	seedWindows(b,
		window{limit: 1000000, remaining: 120000, resetAt: clock.t.Add(time.Hour), seen: true},
		window{limit: 2500, remaining: 2400, resetAt: clock.t.Add(time.Hour), seen: true},
	)

	if adm, _ := b.admit("X", pDetail); adm != nil {
		t.Fatal("detail admit should defer on a drained window")
	}
	adm, _ := b.admit("X", pWrite)
	if adm == nil {
		t.Fatal("write admit refused")
	}
	adm.rateLimited(fullHeaders(10, 1000000, 0, 2500, 2400, clock.t.Add(time.Hour)))

	rm := collectMetrics(t, reader)
	for _, tc := range []struct {
		tier     priority
		decision string
		want     int64
	}{
		{pDetail, "defer", 1},
		{pWrite, "admit", 1},
		{pWrite, "ratelimited", 1},
	} {
		if got := counterValue(t, rm, "linearfs.budget.decisions", tierAttr(tc.tier), decisionAttr(tc.decision)); got != tc.want {
			t.Errorf("decisions{tier=%s,decision=%s} = %d, want %d", tc.tier, tc.decision, got, tc.want)
		}
	}
}

// TestClientBudgetSnapshot: the sync worker's BudgetReporter, now on server
// truth — zero until the requests axis has been seen, then
// (limit-remaining, percent).
func TestClientBudgetSnapshot(t *testing.T) {
	client := NewClient("test-key")

	if count, pct := client.BudgetSnapshot(); count != 0 || pct != 0 {
		t.Errorf("unseen BudgetSnapshot = (%d, %v), want (0, 0)", count, pct)
	}

	clock := newFakeClock()
	seedWindows(client.budget,
		window{limit: 1000000, remaining: 900000, resetAt: clock.t.Add(time.Hour), seen: true},
		window{limit: 2500, remaining: 2000, resetAt: clock.t.Add(time.Hour), seen: true},
	)
	count, pct := client.BudgetSnapshot()
	if count != 500 || pct != 20.0 {
		t.Errorf("BudgetSnapshot = (%d, %v), want (500, 20)", count, pct)
	}
}
