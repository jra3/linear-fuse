package telemetry

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jra3/linear-fuse/internal/config"
)

// requestLogMaxBytes caps requests.jsonl before rollover. The cap is fixed
// (not configurable): the request log is a debug instrument for bounded
// observation runs, and a generous cap keeps a whole cold-start run in one
// file. The rotatingWriter bounds disk at ~2x this.
const requestLogMaxBytes = 100 * 1024 * 1024

// NewRequestLog builds the writer for the per-request JSONL debug log
// (telemetry.requests.* in config — an application debug log, NOT an OTEL
// signal; the meter pipeline is untouched). Returns (nil, nil) when
// disabled: callers skip wiring entirely and the api client does zero work.
// The writer is the same size-capped rotatingWriter the metrics file export
// uses; the caller owns Close.
func NewRequestLog(cfg config.TelemetryRequestsConfig) (io.WriteCloser, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	path := cfg.Path
	if path == "" {
		path = config.DefaultRequestLogPath()
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	return newRotatingWriter(path, requestLogMaxBytes)
}
