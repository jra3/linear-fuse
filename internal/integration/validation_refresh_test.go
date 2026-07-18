package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/fs"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// Validation-failure refresh-and-retry (#246), exercised at the write-handler
// seam: FUSE writes against the mounted fixture store, mutations through the
// mock client, and the catalog refresh through the injected test seam so the
// suite stays network-free.

// catalogRefreshRecorder installs a stub catalog refresher that records every
// call and runs onRefresh (which may upsert the "just created in Linear"
// entity into the fixture store). Restored on cleanup.
func catalogRefreshRecorder(t *testing.T, onRefresh func(ctx context.Context) error) *refreshCalls {
	t.Helper()
	rec := &refreshCalls{}
	lfs.InjectTestCatalogRefresher(func(ctx context.Context, kind fs.CatalogKind, scopeID string) error {
		rec.mu.Lock()
		rec.calls = append(rec.calls, string(kind)+"/"+scopeID)
		rec.mu.Unlock()
		if onRefresh != nil {
			return onRefresh(ctx)
		}
		return nil
	})
	t.Cleanup(func() { lfs.InjectTestCatalogRefresher(nil) })
	return rec
}

type refreshCalls struct {
	mu    gosync.Mutex
	calls []string
}

func (r *refreshCalls) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// createRefreshTestIssue creates a fresh fixture issue via issues/_create (so
// the shared fixture issues stay untouched) and returns its identifier. The
// title is made UNIQUE per call so a later in-process rerun (-count) creates a
// distinct issue with a fresh node — reusing the same identifier would inherit
// the prior run's leftover dirty edit buffer (a failed validation write keeps
// the buffer dirty), which re-flushes on the next write and double-counts
// resolution/refresh attempts.
func createRefreshTestIssue(t *testing.T, title string) string {
	t.Helper()
	title = fmt.Sprintf("%s %d", title, time.Now().UnixNano())
	if err := writeCreateSpec(t, "---\ntitle: "+title+"\n---\nrefresh-retry probe body\n"); err != nil {
		t.Fatalf("create probe issue: %v", err)
	}
	for _, e := range parseLastSidecar(t, issuesLastPath(testTeamKey)) {
		if e["title"] == title && e["identifier"] != "" {
			return e["identifier"]
		}
	}
	t.Fatalf("issues/.last has no entry for %q", title)
	return ""
}

// readIssueError returns the issue-level .error content ("" when clear —
// missing file and empty file both count as clear).
func readIssueError(t *testing.T, identifier string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(issueDirPath(testTeamKey, identifier), ".error"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// TestStaleCatalogWriteSelfHeals: a frontmatter write referencing a label that
// exists remotely but not locally succeeds after exactly one targeted refresh
// + retry — the refresh stub plays "Linear has it", upserting the label the
// way the real refresh's team-metadata drain would.
func TestStaleCatalogWriteSelfHeals(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-only: exercises the injected catalog-refresh seam")
	}
	enableMockMutations(t)
	identifier := createRefreshTestIssue(t, "Stale Catalog Self-Heal Probe")

	const freshLabelID = "label-teammate-fresh"
	// The test's whole premise is a LOCAL MISS on this label; the injected
	// refresh "discovers" it by upserting it into the store. Delete it on
	// cleanup so a later in-process rerun (-count) starts from the same miss
	// instead of resolving immediately and firing no refresh.
	t.Cleanup(func() { _ = testStore.Queries().DeleteLabel(context.Background(), freshLabelID) })

	rec := catalogRefreshRecorder(t, func(ctx context.Context) error {
		label := api.Label{
			ID:    freshLabelID,
			Name:  "TeammateFresh",
			Color: "#00ff00",
			Team:  &api.Team{ID: testTeamID},
		}
		params, err := db.APILabelToDBLabel(label)
		if err != nil {
			return err
		}
		return testStore.Queries().UpsertLabel(ctx, params)
	})

	path := issueFilePath(testTeamKey, identifier)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read probe issue: %v", err)
	}
	modified, err := modifyFrontmatter(content, "labels", []string{"TeammateFresh"})
	if err != nil {
		t.Fatalf("modify frontmatter: %v", err)
	}
	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("write should self-heal via refresh+retry, got: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 || calls[0] != "labels/"+testTeamID {
		t.Errorf("refresh calls = %v, want exactly [labels/%s]", calls, testTeamID)
	}
	if e := readIssueError(t, identifier); e != "" {
		t.Errorf(".error should be clear after a self-healed write, got: %s", e)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read issue: %v", err)
	}
	if !strings.Contains(string(after), "TeammateFresh") {
		t.Errorf("label not applied after self-heal:\n%s", after)
	}
}

// TestNonexistentNameFailsAfterOneRefresh: a genuinely nonexistent name still
// fails with the same .error message as before — after exactly one refresh
// attempt, never more.
func TestNonexistentNameFailsAfterOneRefresh(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-only: exercises the injected catalog-refresh seam")
	}
	enableMockMutations(t)
	identifier := createRefreshTestIssue(t, "Nonexistent Name Probe")

	rec := catalogRefreshRecorder(t, nil) // refresh finds nothing new

	path := issueFilePath(testTeamKey, identifier)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read probe issue: %v", err)
	}
	modified, err := modifyFrontmatter(content, "status", "__no_such_state__")
	if err != nil {
		t.Fatalf("modify frontmatter: %v", err)
	}
	if err := os.WriteFile(path, modified, 0644); err == nil {
		t.Fatal("write with a nonexistent status should fail (EINVAL)")
	}

	calls := rec.snapshot()
	if len(calls) != 1 || calls[0] != "states/"+testTeamID {
		t.Errorf("refresh calls = %v, want exactly [states/%s]", calls, testTeamID)
	}
	errContent := readIssueError(t, identifier)
	if !strings.Contains(errContent, "unknown state: __no_such_state__") {
		t.Errorf(".error lost the pre-refresh message, got: %s", errContent)
	}
	if !strings.Contains(errContent, "states.md") {
		t.Errorf(".error lost the states.md pointer, got: %s", errContent)
	}
}

// rejectingMutator is the mock mutation client with UpdateIssue failing the
// way a server-side rejection does — classifyMutationErr territory.
type rejectingMutator struct {
	*mockmutation.Client
}

func (r rejectingMutator) UpdateIssue(ctx context.Context, issueID string, input map[string]any) error {
	return errors.New("server rejected the update")
}

// TestAPIRejectionDoesNotRefresh: an API-side rejection takes the existing
// classifier path untouched — resolution succeeded locally, so the catalog
// refresh never fires.
func TestAPIRejectionDoesNotRefresh(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-only: exercises the injected catalog-refresh seam")
	}
	enableMockMutations(t)
	identifier := createRefreshTestIssue(t, "API Rejection Probe")

	// Swap in the rejecting mutator for the edit itself (restored before
	// enableMockMutations' own cleanup resets to the real client).
	lfs.InjectTestMutationClient(rejectingMutator{mockmutation.New(
		mockmutation.WithTeamKey(testTeamKey),
		mockmutation.WithStore(lfs.GetStore()),
	)})
	t.Cleanup(func() { lfs.InjectTestMutationClient(nil) })

	rec := catalogRefreshRecorder(t, nil)

	path := issueFilePath(testTeamKey, identifier)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read probe issue: %v", err)
	}
	// "Done" resolves against the fixture catalog, so the failure is purely
	// the mutation's.
	modified, err := modifyFrontmatter(content, "status", "Done")
	if err != nil {
		t.Fatalf("modify frontmatter: %v", err)
	}
	if err := os.WriteFile(path, modified, 0644); err == nil {
		t.Fatal("write should surface the API rejection")
	}

	if calls := rec.snapshot(); len(calls) != 0 {
		t.Errorf("API-side rejection must not trigger a catalog refresh, got: %v", calls)
	}
	errContent := readIssueError(t, identifier)
	if !strings.Contains(errContent, "update issue") {
		t.Errorf(".error should carry the classifier's mutation message, got: %s", errContent)
	}
}
