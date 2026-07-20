// Package atrest centralizes the at-rest permission posture for every on-disk
// artifact LinearFS writes (the SQLite cache, the embedded-file byte cache, the
// telemetry/request JSONL logs). All of these hold a local mirror of the user's
// private Linear data, so they are owner-only: 0700 dirs, 0600 files.
//
// The helpers are best-effort by design. They are called at startup on every
// known artifact regardless of who created it, so an artifact an older binary
// left 0644 is tightened on the next start (self-heal) and future drift is
// corrected for free. A chmod that fails (e.g. the artifact is owned by another
// user, or was removed under us) must not block a mount or a write — the failure
// is logged, counted (linearfs.atrest.chmod_failures), and swallowed; the 0700
// parent dir still bounds group/other reach.
package atrest

import (
	"context"
	"log"
	"os"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// Mode constants for the two artifact kinds. Use these instead of bare octal at
// each call site so the posture lives in one place.
const (
	// DirMode is the mode for a directory holding LinearFS state.
	DirMode os.FileMode = 0o700
	// FileMode is the mode for a file holding LinearFS state.
	FileMode os.FileMode = 0o600
)

// Artifact names the kind of on-disk artifact a Chmod call is tightening — the
// only attribute on the failure counter. A closed enum, never a path: paths
// reaching Chmod include embedded-file names, which are unbounded
// remote-derived strings (cardinality poison).
type Artifact string

const (
	// ArtifactDB is the SQLite state: the linearfs config dir, cache.db, and
	// its -wal/-shm sidecars (internal/db).
	ArtifactDB Artifact = "db"
	// ArtifactEmbedded is the embedded-file byte cache: its dir and the
	// cached attachment files (internal/fs).
	ArtifactEmbedded Artifact = "embedded"
	// ArtifactLogs is the telemetry/request JSONL logs: their dir and the
	// log files plus rotated .1 sidecars (internal/telemetry).
	ArtifactLogs Artifact = "logs"
)

// chmodFailures is the linearfs.atrest.chmod_failures counter (#352) — a
// genuine tighten failure means an artifact silently stays loose with the only
// other signal buried in journald, so it is the one thing worth counting here
// (a missing artifact records nothing; it simply does not exist yet). The
// package has no construction point, so the instrument binds lazily on the
// first counted failure, like reconcile's prune counter; without a registered
// provider the global no-op makes the Add free. Constructed against the bare
// otel API — telemetry.MustInt64Counter is unavailable here because
// internal/telemetry imports atrest (rotate.go tightens the log files) — but
// replicating its contract: a creation failure degrades to a logged no-op,
// never a panic. Telemetry must never take the process down, and this package
// doubly so (its whole charter is never blocking a mount or a write).
var (
	chmodFailuresOnce sync.Once
	chmodFailures     metric.Int64Counter
)

func chmodFailuresCounter() metric.Int64Counter {
	chmodFailuresOnce.Do(func() {
		c, err := otel.Meter("linearfs/atrest").Int64Counter(
			"linearfs.atrest.chmod_failures",
			metric.WithDescription("Genuine at-rest tighten failures (missing artifacts excluded), by artifact kind"))
		if err != nil {
			log.Printf("telemetry: creating linearfs.atrest.chmod_failures: %v", err)
			c, _ = noop.NewMeterProvider().Meter("noop").Int64Counter("linearfs.atrest.chmod_failures")
		}
		chmodFailures = c
	})
	return chmodFailures
}

// Chmod tightens path to mode, best-effort. A missing file is not an error (the
// artifact simply does not exist yet); any other failure is logged, counted
// under the artifact kind, and swallowed so it never blocks a mount or a write.
func Chmod(path string, mode os.FileMode, artifact Artifact) {
	if err := os.Chmod(path, mode); err != nil && !os.IsNotExist(err) {
		log.Printf("[atrest] warning: could not tighten %s to %04o: %v", path, mode, err)
		chmodFailuresCounter().Add(context.Background(), 1,
			metric.WithAttributes(attribute.String("artifact", string(artifact))))
	}
}
