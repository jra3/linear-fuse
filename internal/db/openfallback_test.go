package db

import (
	"bytes"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureLog redirects the standard logger into a buffer for the duration of
// the test. Safe without a mutex: the tests that use it are sequential, and Go
// holds a package's parallel tests until every sequential one has finished.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	})
	return &buf
}

// TestIncompatibleCacheDeletionIsLoud: Open's delete-and-recreate fallback is
// data loss — the user's whole local copy of their workspace, re-fetched from
// Linear over the following sync cycles. It used to happen silently, which is
// what made #430's index-over-an-ALTER-added-column bug feel like "the cache
// got slow once" instead of an error. The delete still happens; it now says so.
func TestIncompatibleCacheDeletionIsLoud(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	// A cache from a build whose issues table predates team_id. schema.sql's
	// CREATE TABLE IF NOT EXISTS leaves it alone and idx_issues_team then
	// fails "no such column" — the shape of a genuinely incompatible cache.
	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatalf("create old issues table: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO issues (id, title) VALUES ('old', 'user data')`); err != nil {
		t.Fatalf("insert old row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	logs := captureLog(t)

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open on an incompatible cache failed instead of rebuilding: %v", err)
	}
	defer store.Close()

	var rows int
	if err := store.DB().QueryRow(`SELECT count(*) FROM issues`).Scan(&rows); err != nil {
		t.Fatalf("count issues in rebuilt cache: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rebuilt cache still holds %d row(s) — the fixture did not exercise the delete path", rows)
	}

	out := logs.String()
	for _, want := range []string{dbPath, "no such column", "re-fetches"} {
		if !strings.Contains(out, want) {
			t.Errorf("deletion log does not mention %q — a user cannot tell what was thrown away or why.\ngot: %s", want, out)
		}
	}
}

// TestBrokenEmbeddedSchemaIsReportedNotMasked: the same "no such column" on a
// path that had no cache is not an incompatible cache — it is this binary's
// own schema.sql failing on an EMPTY database, i.e. a linear-fuse bug. The old
// code answered it by deleting a file that did not exist and retrying, so the
// caller saw a second, identical failure with the real cause ("we shipped a
// broken schema") indistinguishable from "your cache was stale". It must
// surface as a startup error, and leave no half-built file behind for the next
// start to misread as an incompatible cache.
func TestBrokenEmbeddedSchemaIsReportedNotMasked(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	real := schemaSQL
	schemaSQL = real + "\nCREATE INDEX IF NOT EXISTS idx_bogus ON teams(no_such_col);\n"
	t.Cleanup(func() { schemaSQL = real })

	store, err := Open(dbPath)
	if err == nil {
		store.Close()
		t.Fatal("Open succeeded with a broken embedded schema")
	}
	if !strings.Contains(err.Error(), "no such column") {
		t.Errorf("error lost the cause: %v", err)
	}
	if !strings.Contains(err.Error(), dbPath) {
		t.Errorf("error does not name the path it failed to initialize: %v", err)
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Errorf("half-built cache left at %s — the next start reads it as an incompatible cache and reports the wrong cause", dbPath)
	}
}
