package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
)

// writeProjectMD is claudeToolWrite without the t.Fatal on commit failure —
// validation tests need the flush errno back.
func writeProjectMD(t *testing.T, path string, content string) error {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open %s for write: %v", path, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		t.Fatalf("write %s: %v", path, err)
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func projectMDPath() string {
	return filepath.Join(projectsPath(testTeamKey), "test-project", "project.md")
}

func projectErrorPath() string {
	return filepath.Join(projectsPath(testTeamKey), "test-project", ".error")
}

// TestProjectLabelsCatalogFile: the root catalog renders names, group/retired
// markers, and the assignment rules; it is read-only; and — because renderFile
// serves it DIRECT_IO with no node-level cache — a sync-side row upserted
// mid-test is visible on the very next read, with NO kernel invalidation
// surface needed (tested, not asserted).
func TestProjectLabelsCatalogFile(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic catalog")
	}
	path := filepath.Join(mountPoint, "project-labels.md")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat catalog: %v", err)
	}
	if info.Mode().Perm() != 0444 {
		t.Errorf("catalog mode = %v, want 0444", info.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"Area", "Backend", "Frontend", "Ops", "Legacy", // all five, retired included
		"group: true", "retired: true", "parent: Area",
		"At most ONE child from each group", // the rules prose
	} {
		if !strings.Contains(content, want) {
			t.Errorf("catalog missing %q:\n%s", want, content)
		}
	}

	// DIRECT_IO freshness: a catalog row upserted straight to the store (the
	// sync worker's write path) appears on the next read, no invalidation.
	params, err := db.APIProjectLabelToDBProjectLabel(fixtures.FixtureAPIProjectLabels()[3])
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	params.ID, params.Name = "plabel-midtest", "Midtest-Fresh"
	if err := testStore.Queries().UpsertProjectLabel(context.Background(), params); err != nil {
		t.Fatalf("mid-test upsert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testStore.DB().Exec("DELETE FROM project_labels WHERE id = 'plabel-midtest'")
	})
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read catalog: %v", err)
	}
	if !strings.Contains(string(after), "Midtest-Fresh") {
		t.Error("mid-test catalog upsert not visible on next read (DIRECT_IO regression)")
	}
}

// TestProjectLabelsTeamSymlink: the per-team alias resolves to the root
// catalog and reads through.
func TestProjectLabelsTeamSymlink(t *testing.T) {
	link := filepath.Join(mountPoint, "teams", testTeamKey, "project-labels.md")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "../../project-labels.md" {
		t.Errorf("symlink target = %q, want ../../project-labels.md", target)
	}
	data, err := os.ReadFile(link)
	if err != nil {
		t.Fatalf("read through symlink: %v", err)
	}
	if !strings.Contains(string(data), "# Project Labels") {
		t.Error("read through the symlink did not reach the catalog")
	}
}

// TestProjectLabelsRenderAndAssign: project.md renders labelIds as names;
// editing the list resolves names back to IDs, sends ONE full-set
// UpdateProject through the mock, and survives a re-read. The retired label
// already on the project carries through (full-set write re-sends it).
func TestProjectLabelsRenderAndAssign(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: drives the mock mutation client")
	}
	enableMockMutations(t)

	orig, err := os.ReadFile(projectMDPath())
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}
	if !strings.Contains(string(orig), "labels:") ||
		!strings.Contains(string(orig), "Backend") || !strings.Contains(string(orig), "Legacy") {
		t.Fatalf("project.md should render labels [Backend Legacy] as names:\n%s", orig)
	}
	t.Cleanup(func() { _ = writeProjectMD(t, projectMDPath(), string(orig)) })

	// Swap Backend -> Frontend (same group, one child at a time), keep the
	// retired Legacy (carried through).
	edited := strings.Replace(string(orig), "Backend", "Frontend", 1)
	if err := writeProjectMD(t, projectMDPath(), edited); err != nil {
		t.Fatalf("labeled save failed: %v", err)
	}
	if data, _ := os.ReadFile(projectErrorPath()); strings.TrimSpace(string(data)) != "" {
		t.Fatalf(".error non-empty after a valid label edit: %q", data)
	}
	after, err := readFileWithRetry(projectMDPath(), defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !strings.Contains(string(after), "Frontend") || strings.Contains(string(after), "Backend") {
		t.Errorf("label edit did not persist through the mock round-trip:\n%s", after)
	}
	if !strings.Contains(string(after), "Legacy") {
		t.Errorf("carried-through retired label was dropped by the full-set write:\n%s", after)
	}
}

// TestProjectLabelsValidationErrors: each policy rejection is EINVAL with an
// actionable .error that names the offense and points at the catalog — before
// any mutation fires (no mock injected: a mutation attempt would fail loudly
// with a different message).
func TestProjectLabelsValidationErrors(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode legibility check")
	}
	orig, err := os.ReadFile(projectMDPath())
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}
	base := string(orig)
	t.Cleanup(func() { _ = writeProjectMD(t, projectMDPath(), base) })

	swap := func(from, to string) string {
		s := strings.Replace(base, from, to, 1)
		if s == base {
			t.Fatalf("fixture drift: %q not present in project.md:\n%s", from, base)
		}
		return s
	}

	cases := []struct {
		name      string
		content   string
		errSubstr []string
	}{
		{"unknown name", swap("Backend", "Nonexistent-Label"),
			[]string{"unknown project label", "project-labels.md"}},
		{"group cannot be applied", swap("Backend", "Area"),
			[]string{"label group", "Backend", "Frontend"}}, // error names the children
		// Legacy -> Frontend leaves [Backend, Frontend]: two children of Area.
		{"two children of one group", swap("Legacy", "Frontend"),
			[]string{"one child", "Area"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := writeProjectMD(t, projectMDPath(), tc.content); err == nil {
				t.Fatal("expected EINVAL, save succeeded")
			}
			data := readFileUntilContains(t, projectErrorPath(), tc.errSubstr[0], errorVisibilityWait)
			for _, want := range tc.errSubstr {
				if !strings.Contains(string(data), want) {
					t.Errorf(".error missing %q: %q", want, data)
				}
			}
		})
	}
}

// TestProjectLabelsRetiredNewlyApplied: clearing labels then re-adding the
// retired one flips it from carried-through (allowed) to newly-applied
// (policy-rejected — deliberately stricter than the API, which live-verified
// accepts it).
func TestProjectLabelsRetiredNewlyApplied(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: drives the mock mutation client")
	}
	enableMockMutations(t)

	orig, err := os.ReadFile(projectMDPath())
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}
	t.Cleanup(func() { _ = writeProjectMD(t, projectMDPath(), string(orig)) })

	// Step 1: delete the labels block entirely (the key line plus its YAML
	// list items) — the mount-wide delete-the-line clear contract.
	var kept []string
	inLabels := false
	for _, line := range strings.Split(string(orig), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "labels:") {
			inLabels = true
			continue
		}
		if inLabels && strings.HasPrefix(trimmed, "- ") {
			continue
		}
		inLabels = false
		kept = append(kept, line)
	}
	if err := writeProjectMD(t, projectMDPath(), strings.Join(kept, "\n")); err != nil {
		t.Fatalf("delete-the-line clear failed: %v", err)
	}
	after, err := readFileWithRetry(projectMDPath(), defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if strings.Contains(string(after), "labels:") {
		t.Fatalf("labels line survived the clear:\n%s", after)
	}

	// Step 2: with the set now empty, applying the retired label is NEW — the
	// policy rejects what the wire would accept.
	relabeled := strings.Replace(string(after), "name:", "labels: [Legacy]\nname:", 1)
	if err := writeProjectMD(t, projectMDPath(), relabeled); err == nil {
		t.Fatal("expected EINVAL newly applying a retired label, save succeeded")
	}
	data := readFileUntilContains(t, projectErrorPath(), "retired", errorVisibilityWait)
	if !strings.Contains(string(data), "retired") || !strings.Contains(string(data), "Legacy") {
		t.Errorf(".error should explain the retired rejection: %q", data)
	}
}

// TestProjectLabelsStaleIDRoundTrip: a labelId the catalog does not know
// renders VERBATIM, and re-saving the untouched file is a no-op success — the
// round-trip invariant that keeps a cold/stale catalog from stripping labels.
func TestProjectLabelsStaleIDRoundTrip(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: seeds a ghost labelId straight into the store")
	}
	enableMockMutations(t)
	ctx := context.Background()

	ghost := fixtures.FixtureAPIProject()
	ghost.ID, ghost.Slug, ghost.Name = "project-ghost", "ghost-project", "Ghost Project"
	ghost.LabelIds = []string{"plabel-vanished"}
	if err := fixtures.PopulateProject(ctx, testStore, ghost, fixtures.FixtureAPITeam().ID); err != nil {
		t.Fatalf("seed ghost project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testStore.DB().Exec("DELETE FROM projects WHERE id = 'project-ghost'")
		_, _ = testStore.DB().Exec("DELETE FROM project_teams WHERE project_id = 'project-ghost'")
	})

	path := filepath.Join(projectsPath(testTeamKey), "ghost-project", "project.md")
	data, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read ghost project.md: %v", err)
	}
	if !strings.Contains(string(data), "plabel-vanished") {
		t.Fatalf("unknown labelId must render verbatim, never dropped:\n%s", data)
	}

	// Untouched re-save: the verbatim ID resolves via current-member
	// passthrough, the set diff is empty, no mutation fires, no .error.
	if err := writeProjectMD(t, path, string(data)); err != nil {
		t.Fatalf("untouched re-save of a stale-ID render failed: %v", err)
	}
	errPath := filepath.Join(projectsPath(testTeamKey), "ghost-project", ".error")
	if e, _ := os.ReadFile(errPath); strings.TrimSpace(string(e)) != "" {
		t.Errorf(".error non-empty after an untouched stale-ID save: %q", e)
	}
	if after, _ := os.ReadFile(path); !strings.Contains(string(after), "plabel-vanished") {
		t.Errorf("stale labelId stripped by the no-op save:\n%s", after)
	}
}
