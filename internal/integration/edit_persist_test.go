package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
)

// These tests exercise the per-entity EDIT (Flush) persistence paths OFFLINE,
// through the in-memory mutation fake (enableMockMutations). Unlike the create/
// archive/rename lifecycle tests in write_offline_test.go, they assert that a
// content edit to an editable file actually LANDS: after a write+fsync the mount
// serves the new content back. Before them, milestone edits had zero coverage
// and project/initiative edit-persistence only ran under LINEARFS_WRITE_TESTS=1
// against the live API, so the default fixture-mode suite never exercised the
// edit-commit tail (mutate → upsert SQLite → read-your-writes) for these
// entities.

// restoreFixtureMilestone re-seeds the "Alpha Release" milestone so an edit
// test does not leave the shared mount's store mutated for later tests.
func restoreFixtureMilestone(t *testing.T) {
	t.Helper()
	if err := fixtures.PopulateProjectMilestones(context.Background(), testStore,
		fixtures.FixtureAPIProject().ID,
		[]api.ProjectMilestone{fixtures.FixtureAPIProjectMilestone()}); err != nil {
		t.Errorf("restore fixture milestone: %v", err)
	}
}

// TestOffline_MilestoneEditPreservesOtherFields drives MilestoneFileNode.Flush:
// editing ONE milestone field (targetDate) and saving must land the change while
// leaving the untouched fields (name, description) intact on re-read. This is the
// starkest write-path gap — a fully editable entity that had no edit test at all
// — and it guards the whole-entity round trip through the edit-commit tail, not
// just the single edited field.
func TestOffline_MilestoneEditPreservesOtherFields(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)
	t.Cleanup(func() { restoreFixtureMilestone(t) })

	path := filepath.Join(projectsPath(testTeamKey), "test-project", "milestones", "Alpha Release.md")

	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read milestone: %v", err)
	}

	// Edit only the targetDate, leaving name and description untouched.
	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse milestone: %v", err)
	}
	const newDate = "2025-12-01"
	doc.Frontmatter["targetDate"] = newDate
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render milestone: %v", err)
	}
	claudeToolWrite(t, path, edited)

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read milestone: %v", err)
	}
	got := string(after)
	// The edited field landed, and the fields we never touched survived the
	// round trip — a partial-echo from the mutation must not zero them.
	for _, want := range []string{newDate, "Alpha Release", "First alpha release"} {
		if !strings.Contains(got, want) {
			t.Errorf("milestone edit lost %q after saving targetDate=%s\n--- got ---\n%s", want, newDate, got)
		}
	}
}

// TestOffline_AtomicRenameEditPersists drives renameSave: an editor's atomic
// save (write a sibling temp file, rename it over issue.md) must actually LAND
// the edit, not merely avoid corruption. The existing TestWriteContractAtomicRename*
// tests assert only no-corruption/no-EROFS and run without the mock mutator, so
// persist fails there and "a save lands via rename" was unchecked — the exact
// neighborhood of the spent-scratch drop bug (#280). With the mock, the rename's
// inline Flush persists and the consumed scratch re-Looks-up to the fresh
// store-backed node, so the mount serves the edit back.
func TestOffline_AtomicRenameEditPersists(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	path := issueFilePath(testTeamKey, "TST-1")
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	// Restore the original content through the mount so the shared fixture issue
	// is left unchanged for other tests.
	t.Cleanup(func() { claudeToolWrite(t, path, orig) })

	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse issue.md: %v", err)
	}
	const marker = "atomic rename persistence probe ZZZ"
	doc.Body = strings.TrimRight(doc.Body, "\n") + "\n\n" + marker
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render issue.md: %v", err)
	}

	// Atomic save-via-rename: write a sibling scratch temp file, then rename it
	// over the canonical issue.md. The rename routes the bytes straight through
	// issue.md's Flush, so a rejected/failed persist surfaces as a rename error.
	tmp := path + ".tmp.42.cafef00d"
	if err := os.WriteFile(tmp, edited, 0o644); err != nil {
		t.Fatalf("write scratch temp: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		t.Fatalf("atomic rename over issue.md should persist with mock mutator: %v", err)
	}

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read issue.md: %v", err)
	}
	if !strings.Contains(string(after), marker) {
		t.Fatalf("atomic-rename edit did not persist marker %q\n--- got ---\n%s", marker, after)
	}
}
