package repo

// SWR-instrument tests (phase-2 pattern, see internal/api/metrics_test.go):
// a manual-reader SDK provider is installed as the global otel provider, the
// instruments are bound (newSWRMetrics), and the collected metricdata is
// asserted by hand. The specs under test carry recording closures, so nothing
// touches SQLite or the network (the swr_test.go precedent).
//
// These tests are deliberately NOT parallel: they swap the global meter
// provider. TestMain pins the global to an explicit no-op first, so
// repositories built by the package's other (parallel) tests can never be
// delegated onto a test provider and pollute a collection.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jra3/linear-fuse/internal/api"
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

// newMetricsTestRepo is newSWRTestRepo plus bound instruments — call it only
// after withTestMeter so the instruments bind to the test provider.
func newMetricsTestRepo(t *testing.T) *SQLiteRepository {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &SQLiteRepository{
		client:             api.NewClient("test-key"),
		stalenessThreshold: defaultStalenessThreshold,
		refreshing:         make(map[string]bool),
		refreshContext:     ctx,
		refreshCancel:      cancel,
		refreshSem:         make(chan struct{}, maxConcurrentRefreshes),
		metrics:            newSWRMetrics(),
	}
	t.Cleanup(r.Close)
	return r
}

// swrCounterValue returns the named counter's count for {kind, key2=val2}, or
// -1 when no such datapoint (or the whole metric) exists.
func swrCounterValue(t *testing.T, reader *sdkmetric.ManualReader, name string, kind refreshKind, attrKey, attrVal string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s data is %T, want Sum[int64]", name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				k, kok := dp.Attributes.Value(attribute.Key("kind"))
				v, vok := dp.Attributes.Value(attribute.Key(attrKey))
				if kok && vok && k.AsString() == string(kind) && v.AsString() == attrVal {
					return dp.Value
				}
			}
		}
	}
	return -1
}

// waitForCounter polls the reader until the datapoint reaches want (the
// refresh-outcome recordings happen inside the background goroutine, after
// the test's refresh closure has already returned).
func waitForCounter(t *testing.T, reader *sdkmetric.ManualReader, name string, kind refreshKind, attrKey, attrVal string, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := swrCounterValue(t, reader, name, kind, attrKey, attrVal); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("%s{kind=%s,%s=%s} = %d, want %d (timed out)", name, kind, attrKey, attrVal, got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// staleSpec is an always-stale TTL spec (never synced) with the given refresh.
func staleSpec(kind refreshKind, id string, refresh func(context.Context) error) swrSpec {
	return swrSpec{
		kind:     kind,
		id:       id,
		syncedAt: func() (interface{}, error) { return nil, nil },
		refresh:  refresh,
	}
}

// TestSWRTriggersCounter drives all four decisions: fresh (swrStale says no),
// sem_dropped (semaphore full), triggered, and deduped (same key in flight).
func TestSWRTriggersCounter(t *testing.T) {
	reader := withTestMeter(t)
	r := newMetricsTestRepo(t)

	// fresh: recently synced TTL surface — the refresh must not fire.
	r.maybeRefreshSWR(swrSpec{
		kind:     kindProjectDocs,
		id:       "p1",
		syncedAt: func() (interface{}, error) { return time.Now(), nil },
		refresh: func(context.Context) error {
			t.Error("fresh spec fired a refresh")
			return nil
		},
	})

	// sem_dropped: fill the semaphore, then a stale spec is dropped.
	for i := 0; i < maxConcurrentRefreshes; i++ {
		r.refreshSem <- struct{}{}
	}
	r.maybeRefreshSWR(staleSpec(kindHistory, "h-dropped", func(context.Context) error {
		t.Error("sem-dropped spec fired a refresh")
		return nil
	}))
	for i := 0; i < maxConcurrentRefreshes; i++ {
		<-r.refreshSem
	}

	// triggered, then deduped while the first is still in flight.
	started := make(chan struct{})
	block := make(chan struct{})
	r.maybeRefreshSWR(staleSpec(kindHistory, "h1", func(context.Context) error {
		close(started)
		<-block
		return nil
	}))
	<-started
	r.maybeRefreshSWR(staleSpec(kindHistory, "h1", func(context.Context) error {
		t.Error("deduped spec fired a second refresh")
		return nil
	}))
	close(block)

	for _, tc := range []struct {
		kind     refreshKind
		decision string
		want     int64
	}{
		{kindProjectDocs, "fresh", 1},
		{kindHistory, "sem_dropped", 1},
		{kindHistory, "triggered", 1},
		{kindHistory, "deduped", 1},
	} {
		if got := swrCounterValue(t, reader, "linearfs.swr.triggers", tc.kind, "decision", tc.decision); got != tc.want {
			t.Errorf("triggers{kind=%s,decision=%s} = %d, want %d", tc.kind, tc.decision, got, tc.want)
		}
	}
}

// TestSWRRefreshOutcomesCounter: a nil-error refresh is ok, a not-found error
// is orphaned (mirroring the module's orphan classification), anything else
// is error.
func TestSWRRefreshOutcomesCounter(t *testing.T) {
	reader := withTestMeter(t)
	r := newMetricsTestRepo(t)

	r.maybeRefreshSWR(staleSpec(kindProjectDocs, "ok1", func(context.Context) error {
		return nil
	}))
	r.maybeRefreshSWR(staleSpec(kindProjectUpdates, "err1", func(context.Context) error {
		return errors.New("boom")
	}))
	orphaned := make(chan struct{}, 1)
	spec := staleSpec(kindInitiativeDocs, "gone1", func(context.Context) error {
		return fmt.Errorf("GraphQL error: Entity not found: Initiative")
	})
	spec.orphan = func(context.Context) { orphaned <- struct{}{} }
	r.maybeRefreshSWR(spec)

	waitForCounter(t, reader, "linearfs.swr.refresh_outcomes", kindProjectDocs, "outcome", "ok", 1)
	waitForCounter(t, reader, "linearfs.swr.refresh_outcomes", kindProjectUpdates, "outcome", "error", 1)
	waitForCounter(t, reader, "linearfs.swr.refresh_outcomes", kindInitiativeDocs, "outcome", "orphaned", 1)

	// The orphaned outcome coincides with the module's orphan classification
	// actually firing.
	select {
	case <-orphaned:
	case <-time.After(2 * time.Second):
		t.Error("orphan closure did not fire for the not-found refresh")
	}
}

// TestSWRMetricsNilClientRecordsNothing: fixture mode (nil client) returns
// before any recording — both from the coordinator and from a direct trigger.
func TestSWRMetricsNilClientRecordsNothing(t *testing.T) {
	reader := withTestMeter(t)
	r := &SQLiteRepository{metrics: newSWRMetrics()} // nil client

	r.maybeRefreshSWR(staleSpec(kindHistory, "x", func(context.Context) error { return nil }))
	r.triggerBackgroundRefresh(kindHistory, "y", func(context.Context) error { return nil })

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "linearfs.swr.triggers" || m.Name == "linearfs.swr.refresh_outcomes" {
				t.Errorf("%s recorded with a nil client", m.Name)
			}
		}
	}
}
