package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/config"
)

// Init tests are not parallel: Init swaps the global otel meter provider.

func TestInitFileExportDisabledByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	cfg := config.TelemetryConfig{
		File: config.TelemetryFileConfig{
			Enabled: false, // explicit: the gate under test
			Path:    path,
		},
	}

	shutdown, err := Init(cfg, "test", "deadbeef")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file export disabled but %s exists (stat err = %v)", path, err)
	}
}

func TestInitEndToEndJSONLWithHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	cfg := config.TelemetryConfig{
		File: config.TelemetryFileConfig{
			Enabled:   true,
			Path:      path,
			Interval:  time.Hour, // rely on shutdown's final flush, not the ticker
			MaxSizeMB: 1,
		},
	}

	shutdown, err := Init(cfg, "v9.9.9", "cafebabe")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("no JSONL lines written")
	}
	for i, line := range lines {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Errorf("line %d is not valid JSON: %v (%q)", i, err, line)
		}
	}

	got := string(data)
	for _, want := range []string{
		"linearfs.process.uptime_seconds",
		"linearfs.build.info",
		"v9.9.9",
		"cafebabe",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("JSONL output missing %q", want)
		}
	}
}

func TestInitBadFilePathDegradesToSummaryOnly(t *testing.T) {
	// A path whose parent cannot be created: file export should be skipped,
	// Init must still succeed (telemetry never blocks mounting).
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg := config.TelemetryConfig{
		File: config.TelemetryFileConfig{
			Enabled: true,
			Path:    filepath.Join(blocker, "sub", "metrics.jsonl"), // parent is a file
		},
	}

	shutdown, err := Init(cfg, "test", "deadbeef")
	if err != nil {
		t.Fatalf("Init should degrade, not fail: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
