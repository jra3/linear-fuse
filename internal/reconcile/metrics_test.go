package reconcile

// Prune-counter tests. The linearfs.sync.prunes instrument binds lazily on
// the first firing prune (the package has no construction point), so unlike
// the api/sync/repo metrics tests — which install a fresh provider per test —
// this package installs ONE manual-reader provider for the whole binary in
// TestMain, before any test can bind the instrument. Cross-test isolation
// comes from the collection attribute: each test asserts on its own unique
// Kind, so other tests' prunes (including the empty-Kind ones from
// collection_test) can never collide with an assertion.

import (
	"context"
	"errors"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var metricsReader *sdkmetric.ManualReader

func TestMain(m *testing.M) {
	metricsReader = sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricsReader)))
	os.Exit(m.Run())
}

// prunesValue returns the linearfs.sync.prunes count for one collection kind,
// or 0 when no such datapoint has been recorded.
func prunesValue(t *testing.T, kind string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "linearfs.sync.prunes" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("prunes data is %T, want Sum[int64]", m.Data)
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value("collection"); ok && v.AsString() == kind {
					return dp.Value
				}
			}
		}
	}
	return 0
}

// metricSpec builds a minimal spec whose prune succeeds unless told otherwise.
func metricSpec(kind string, upsertErr, pruneErr error) CollectionSpec[string] {
	return CollectionSpec[string]{
		Label:  "metrics test " + kind,
		Kind:   kind,
		Items:  []string{"a"},
		Upsert: func(context.Context, string) error { return upsertErr },
		Prune:  func(context.Context) error { return pruneErr },
	}
}

// TestCollectionPruneRecordsMetric: a prune that actually executes records one
// linearfs.sync.prunes count under its collection kind.
func TestCollectionPruneRecordsMetric(t *testing.T) {
	Collection(context.Background(), metricSpec("kind-fires", nil, nil))
	if got := prunesValue(t, "kind-fires"); got != 1 {
		t.Errorf("prunes{collection=kind-fires} = %d, want 1", got)
	}
}

// TestCollectionSuppressedPruneRecordsNothing: an unclean pass suppresses the
// prune, so nothing is counted — the metric counts executed prunes only.
func TestCollectionSuppressedPruneRecordsNothing(t *testing.T) {
	Collection(context.Background(), metricSpec("kind-suppressed", errors.New("upsert boom"), nil))
	if got := prunesValue(t, "kind-suppressed"); got != 0 {
		t.Errorf("prunes{collection=kind-suppressed} = %d, want 0 (suppressed prune)", got)
	}
}

// TestCollectionFailedPruneRecordsNothing: a prune that ran but failed did not
// delete anything — it must not count as an executed prune.
func TestCollectionFailedPruneRecordsNothing(t *testing.T) {
	Collection(context.Background(), metricSpec("kind-prune-err", nil, errors.New("prune boom")))
	if got := prunesValue(t, "kind-prune-err"); got != 0 {
		t.Errorf("prunes{collection=kind-prune-err} = %d, want 0 (prune failed)", got)
	}
}
