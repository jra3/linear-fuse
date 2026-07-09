package telemetry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jra3/linear-fuse/internal/config"
)

// TestNewRequestLogDisabled: the off-by-default gate — no writer, no file,
// zero side effects.
func TestNewRequestLogDisabled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "requests.jsonl")

	w, err := NewRequestLog(config.TelemetryRequestsConfig{Enabled: false, Path: path})
	if err != nil {
		t.Fatalf("NewRequestLog(disabled) error: %v", err)
	}
	if w != nil {
		t.Fatal("NewRequestLog(disabled) returned a writer, want nil")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("disabled request log created a file at %s", path)
	}
}

// TestNewRequestLogEnabled: enabled config yields a working writer at the
// configured path (parent directories created), backed by the same
// rotatingWriter as the metrics export.
func TestNewRequestLogEnabled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "requests.jsonl")

	w, err := NewRequestLog(config.TelemetryRequestsConfig{Enabled: true, Path: path})
	if err != nil {
		t.Fatalf("NewRequestLog(enabled) error: %v", err)
	}
	if w == nil {
		t.Fatal("NewRequestLog(enabled) returned nil writer")
	}

	line := []byte(`{"op":"TestOp"}` + "\n")
	if _, err := w.Write(line); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading request log: %v", err)
	}
	if string(got) != string(line) {
		t.Errorf("file content = %q, want %q", got, line)
	}
}
