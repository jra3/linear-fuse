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
// is logged and swallowed; the 0700 parent dir still bounds group/other reach.
package atrest

import (
	"log"
	"os"
)

// Mode constants for the two artifact kinds. Use these instead of bare octal at
// each call site so the posture lives in one place.
const (
	// DirMode is the mode for a directory holding LinearFS state.
	DirMode os.FileMode = 0o700
	// FileMode is the mode for a file holding LinearFS state.
	FileMode os.FileMode = 0o600
)

// Chmod tightens path to mode, best-effort. A missing file is not an error (the
// artifact simply does not exist yet); any other failure is logged and
// swallowed so it never blocks a mount or a write.
func Chmod(path string, mode os.FileMode) {
	if err := os.Chmod(path, mode); err != nil && !os.IsNotExist(err) {
		log.Printf("[atrest] warning: could not tighten %s to %04o: %v", path, mode, err)
	}
}
