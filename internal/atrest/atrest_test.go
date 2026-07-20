package atrest

// Chmod failure-counter tests. Like linearfs.sync.prunes (internal/reconcile),
// the instrument binds lazily on the first counted failure and the package has
// no construction point, so ONE manual-reader provider is installed for the
// whole binary in TestMain, before any test can bind it. Cross-test isolation
// comes from the artifact attribute: each test asserts on its own unique
// Artifact value, so other tests' failures can never collide with an assertion.

import (
	"context"
	"os"
	"path/filepath"
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

// chmodFailuresValue returns the linearfs.atrest.chmod_failures count for one
// artifact, or 0 when no such datapoint has been recorded.
func chmodFailuresValue(t *testing.T, artifact Artifact) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "linearfs.atrest.chmod_failures" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("chmod_failures data is %T, want Sum[int64]", m.Data)
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value("artifact"); ok && v.AsString() == string(artifact) {
					return dp.Value
				}
			}
		}
	}
	return 0
}

// notADir returns a path whose parent component is a regular file, so any
// chmod on it fails with ENOTDIR — a genuine failure reachable without root
// (EPERM would need a foreign-owner file).
func notADir(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "plainfile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return filepath.Join(f, "child")
}

// TestChmodFailureRecordsMetric: a genuine chmod failure (not missing-file)
// increments linearfs.atrest.chmod_failures under its artifact.
func TestChmodFailureRecordsMetric(t *testing.T) {
	const artifact Artifact = "test-genuine-failure"
	Chmod(notADir(t), FileMode, artifact)
	if got := chmodFailuresValue(t, artifact); got != 1 {
		t.Errorf("chmod_failures{artifact=%s} = %d, want 1", artifact, got)
	}
}

// TestChmodMissingFileNotCounted: a missing artifact is not a failure — it
// simply does not exist yet — so nothing is counted (mirroring the existing
// swallow of os.IsNotExist).
func TestChmodMissingFileNotCounted(t *testing.T) {
	const artifact Artifact = "test-missing-file"
	Chmod(filepath.Join(t.TempDir(), "does-not-exist"), FileMode, artifact)
	if got := chmodFailuresValue(t, artifact); got != 0 {
		t.Errorf("chmod_failures{artifact=%s} = %d, want 0 (missing file is not a failure)", artifact, got)
	}
}

// TestChmodSuccessNotCounted: a successful tighten records nothing, and
// actually tightens.
func TestChmodSuccessNotCounted(t *testing.T) {
	const artifact Artifact = "test-success"
	f := filepath.Join(t.TempDir(), "loose")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	Chmod(f, FileMode, artifact)
	info, err := os.Stat(f)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != FileMode {
		t.Errorf("mode = %04o, want %04o", perm, FileMode)
	}
	if got := chmodFailuresValue(t, artifact); got != 0 {
		t.Errorf("chmod_failures{artifact=%s} = %d, want 0 (success is not a failure)", artifact, got)
	}
}
