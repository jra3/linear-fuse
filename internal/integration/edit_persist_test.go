package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/marshal"
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

	path := filepath.Join(projectsPath(testTeamKey), "test-project", "milestones", "Alpha Release.md")

	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read milestone: %v", err)
	}
	// Restore the original content through the mount so the shared fixture
	// milestone is left unchanged for later tests (a store-only reseed would not
	// refresh this live node's adopted buffer — see TestFixtureMilestoneFile).
	t.Cleanup(func() { claudeToolWrite(t, path, orig) })

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

// TestOffline_ProjectEditPersistsDescription is the fixture-mode counterpart of
// TestClaudeToolEditPersistsProjectDescription (which requires LINEARFS_WRITE_TESTS
// against the live API, so default CI never runs it). With the mock mutator a
// write+fsync to project.md must persist the edited body, so the mount serves it
// back — and the untouched name survives the round trip.
func TestOffline_ProjectEditPersistsDescription(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	path := projectMDPath()
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}
	t.Cleanup(func() { claudeToolWrite(t, path, orig) })

	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse project.md: %v", err)
	}
	const marker = "project edit persistence probe ZZZ"
	doc.Body = strings.TrimRight(doc.Body, "\n") + "\n\n" + marker
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render project.md: %v", err)
	}
	claudeToolWrite(t, path, edited)

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read project.md: %v", err)
	}
	got := string(after)
	for _, want := range []string{marker, "Test Project"} {
		if !strings.Contains(got, want) {
			t.Errorf("project edit lost %q\n--- got ---\n%s", want, got)
		}
	}
}

// mdFileContaining scans a directory for a non-control .md whose content contains
// marker, retrying until it appears (a create through the mount upserts +
// invalidates, but the entry may lag a beat, and the on-disk filename is often a
// slug the caller can't predict). The mount is shared, so callers match by a
// unique marker, never by position or filename.
func mdFileContaining(t *testing.T, dir, marker string) string {
	t.Helper()
	deadline := time.Now().Add(defaultWaitTime)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if isControlFile(e.Name()) || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err == nil && strings.Contains(string(body), marker) {
				return e.Name()
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, _ := os.ReadDir(dir)
	var dump strings.Builder
	for _, e := range entries {
		body, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		fmt.Fprintf(&dump, "  %s: %.120q\n", e.Name(), body)
	}
	t.Fatalf("no .md file containing %q in %s\n--- listing ---\n%s", marker, dir, dump.String())
	return ""
}

// TestOffline_CommentBodyEditPersists drives CommentNode.Flush: a comment's body
// is edited through the mount and must LAND — after write+fsync the mount serves
// the new body back and the old text is gone. Comment .md files are the pure body
// with no frontmatter, so this is the whole editable surface. Before this,
// comment Update had zero persistence coverage (only marshal round-trips and
// live-API create/delete lifecycle tests existed).
func TestOffline_CommentBodyEditPersists(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	dir := commentsPath(testTeamKey, "TST-1")
	const origMarker = "comment edit probe ORIGINAL ZZZ"
	if err := writeToWriteOnly(t, newCommentPath(testTeamKey, "TST-1"), "[TEST] "+origMarker); err != nil {
		t.Fatalf("create comment via _create: %v", err)
	}

	name := mdFileContaining(t, dir, origMarker)
	path := filepath.Join(dir, name)
	t.Cleanup(func() { _ = os.Remove(path) })

	const newMarker = "comment edit probe UPDATED QQQ"
	claudeToolWrite(t, path, []byte("[TEST] "+newMarker))

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read comment: %v", err)
	}
	got := string(after)
	if !strings.Contains(got, newMarker) {
		t.Errorf("comment edit did not persist %q\n--- got ---\n%s", newMarker, got)
	}
	if strings.Contains(got, origMarker) {
		t.Errorf("comment edit left the stale original body\n--- got ---\n%s", got)
	}
}

// projectsList extracts the initiative.md projects: frontmatter as a string slice.
func projectsList(doc *marshal.Document) []string {
	var out []string
	if v, ok := doc.Frontmatter["projects"]; ok {
		if list, ok := v.([]any); ok {
			for _, e := range list {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestOffline_InitiativeProjectLinkPersists drives the initiative.md projects:
// reconcile (reconcileLinks -> persistInitiativeProjectLink): adding then removing
// a project slug must each land in the junction so the mount serves the changed
// list back. The junction bumps no updatedAt (b642867), so this through-the-mount
// round trip is the only offline guard that link/unlink actually persist.
func TestOffline_InitiativeProjectLinkPersists(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	dir, err := firstInitiativeDir()
	if err != nil {
		t.Skipf("no initiative fixture: %v", err)
	}
	path := filepath.Join(dir, "initiative.md")
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read initiative.md: %v", err)
	}
	t.Cleanup(func() { claudeToolWrite(t, path, orig) })

	const slug = "test-project"
	setProjects := func(projects []string) []string {
		doc, err := marshal.Parse(orig)
		if err != nil {
			t.Fatalf("parse initiative.md: %v", err)
		}
		doc.Frontmatter["projects"] = projects
		edited, err := marshal.Render(doc)
		if err != nil {
			t.Fatalf("render initiative.md: %v", err)
		}
		claudeToolWrite(t, path, edited)
		after, err := readFileWithRetry(path, defaultWaitTime)
		if err != nil {
			t.Fatalf("re-read initiative.md: %v", err)
		}
		got, err := marshal.Parse(after)
		if err != nil {
			t.Fatalf("parse re-read initiative.md: %v", err)
		}
		return projectsList(got)
	}

	// Link: the slug must appear in the persisted list.
	if got := setProjects([]string{slug}); !contains(got, slug) {
		t.Errorf("after linking, projects = %v, want it to contain %q", got, slug)
	}
	// Unlink: an empty list must persist (the junction row is deleted).
	if got := setProjects(nil); contains(got, slug) {
		t.Errorf("after unlinking, projects = %v, want %q gone", got, slug)
	}
}

// TestOffline_ProjectInitiativesLinkPersists is the reverse-direction junction
// test: editing project.md's initiatives: list (names) must persist the same
// initiative<->project junction that TestOffline_InitiativeProjectLinkPersists
// drives from the initiative side.
func TestOffline_ProjectInitiativesLinkPersists(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	path := projectMDPath()
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}
	t.Cleanup(func() { claudeToolWrite(t, path, orig) })

	const name = "Test Initiative"
	initiativesList := func(doc *marshal.Document) []string {
		var out []string
		if v, ok := doc.Frontmatter["initiatives"]; ok {
			if list, ok := v.([]any); ok {
				for _, e := range list {
					if s, ok := e.(string); ok {
						out = append(out, s)
					}
				}
			}
		}
		return out
	}
	setInitiatives := func(initiatives []string) []string {
		doc, err := marshal.Parse(orig)
		if err != nil {
			t.Fatalf("parse project.md: %v", err)
		}
		doc.Frontmatter["initiatives"] = initiatives
		edited, err := marshal.Render(doc)
		if err != nil {
			t.Fatalf("render project.md: %v", err)
		}
		claudeToolWrite(t, path, edited)
		after, err := readFileWithRetry(path, defaultWaitTime)
		if err != nil {
			t.Fatalf("re-read project.md: %v", err)
		}
		got, err := marshal.Parse(after)
		if err != nil {
			t.Fatalf("parse re-read project.md: %v", err)
		}
		return initiativesList(got)
	}

	if got := setInitiatives([]string{name}); !contains(got, name) {
		t.Errorf("after linking, initiatives = %v, want it to contain %q", got, name)
	}
	if got := setInitiatives(nil); contains(got, name) {
		t.Errorf("after unlinking, initiatives = %v, want %q gone", got, name)
	}
}

// Label Update persistence is covered by TestLabelEditPersists (fs unit level):
// editing a label through the shared integration mount invalidates the by/label
// filtered view, and the dummy-key re-fetch that follows would 401→EIO and pollute
// later tests, so the label-edit round trip is asserted directly against the store.

// TestOffline_ProjectNameEditPersists drives ProjectInfoNode.Flush for the editable
// name field (TestOffline_ProjectEditPersistsDescription covers the body). The
// edited name must land and read back.
func TestOffline_ProjectNameEditPersists(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	path := projectMDPath()
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}
	t.Cleanup(func() { claudeToolWrite(t, path, orig) })

	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse project.md: %v", err)
	}
	newName := fmt.Sprint(doc.Frontmatter["name"]) + " EDITED ZZZ"
	doc.Frontmatter["name"] = newName
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render project.md: %v", err)
	}
	claudeToolWrite(t, path, edited)

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read project.md: %v", err)
	}
	got, err := marshal.Parse(after)
	if err != nil {
		t.Fatalf("parse re-read project.md: %v", err)
	}
	if n := fmt.Sprint(got.Frontmatter["name"]); n != newName {
		t.Errorf("project name did not persist: got %q, want %q", n, newName)
	}
}

// TestOffline_InitiativeNameEditPersists drives InitiativeInfoNode.Flush for the
// editable name field (the body is covered by
// TestOffline_InitiativeEditPersistsDescription).
func TestOffline_InitiativeNameEditPersists(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	dir, err := firstInitiativeDir()
	if err != nil {
		t.Skipf("no initiative fixture: %v", err)
	}
	path := filepath.Join(dir, "initiative.md")
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read initiative.md: %v", err)
	}
	t.Cleanup(func() { claudeToolWrite(t, path, orig) })

	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse initiative.md: %v", err)
	}
	newName := fmt.Sprint(doc.Frontmatter["name"]) + " EDITED ZZZ"
	doc.Frontmatter["name"] = newName
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render initiative.md: %v", err)
	}
	claudeToolWrite(t, path, edited)

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read initiative.md: %v", err)
	}
	got, err := marshal.Parse(after)
	if err != nil {
		t.Fatalf("parse re-read initiative.md: %v", err)
	}
	if n := fmt.Sprint(got.Frontmatter["name"]); n != newName {
		t.Errorf("initiative name did not persist: got %q, want %q", n, newName)
	}
}

// TestOffline_DocumentBodyAndTitleEditPersist drives DocumentFileNode.Flush: a
// doc is created on an issue, then its body is edited through the mount and must
// LAND while the untouched title survives the round trip. The live TestUpdateDocument
// re-reads immediately after the write with no mock mutator, so it asserts only a
// page-cache echo, not that the edit reached the store — this is the fixture-mode
// persistence twin (mutate → upsert → read-your-writes adopt).
func TestOffline_DocumentBodyAndTitleEditPersist(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	dir := docsPath(testTeamKey, "TST-1")
	const titleMarker = "DocTitleZZZ"
	const origMarker = "DocBodyOriginalZZZ"
	// Create via named filename; a '# Title' heading sets the title, the rest is
	// the body. The on-disk name is slugged, so scan for the created file.
	if err := os.WriteFile(docFilePath(testTeamKey, "TST-1", "Doc Probe.md"),
		[]byte("# "+titleMarker+"\n\n"+origMarker+"\n"), 0o644); err != nil {
		t.Fatalf("create doc via filename: %v", err)
	}
	name := mdFileContaining(t, dir, origMarker)
	path := filepath.Join(dir, name)
	t.Cleanup(func() { _ = os.Remove(path) })

	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read created doc: %v", err)
	}
	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse doc: %v", err)
	}
	const newMarker = "DocBodyUpdatedQQQ"
	doc.Body = newMarker
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render doc: %v", err)
	}
	claudeToolWrite(t, path, edited)

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read doc: %v", err)
	}
	got := string(after)
	for _, want := range []string{newMarker, titleMarker} {
		if !strings.Contains(got, want) {
			t.Errorf("doc edit lost %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, origMarker) {
		t.Errorf("doc edit left the stale original body\n--- got ---\n%s", got)
	}
}

// TestOffline_IssueFieldEditsPersist drives IssueFileNode.Flush for FRONTMATTER
// fields (priority, estimate), not the body. The existing offline issue tests
// cover only a body edit via atomic rename; per-field frontmatter persistence ran
// only live under LINEARFS_WRITE_TESTS. This edits two scalar fields through the
// mount and asserts both land while the untouched title survives — the whole-entity
// round trip through the edit-commit tail (mutate → verify GetIssue → upsert → adopt).
func TestOffline_IssueFieldEditsPersist(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	path := issueFilePath(testTeamKey, "TST-1")
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	t.Cleanup(func() { claudeToolWrite(t, path, orig) })

	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse issue.md: %v", err)
	}
	origTitle := fmt.Sprint(doc.Frontmatter["title"])

	// Flip each field to a value different from the current one, so the Flush
	// actually diffs and mutates rather than short-circuiting as a no-op.
	newPriority := "urgent"
	if fmt.Sprint(doc.Frontmatter["priority"]) == "urgent" {
		newPriority = "low"
	}
	newEstimate := 7
	if fmt.Sprint(doc.Frontmatter["estimate"]) == "7" {
		newEstimate = 8
	}
	doc.Frontmatter["priority"] = newPriority
	doc.Frontmatter["estimate"] = newEstimate
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render issue.md: %v", err)
	}
	claudeToolWrite(t, path, edited)

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read issue.md: %v", err)
	}
	got, err := marshal.Parse(after)
	if err != nil {
		t.Fatalf("parse re-read issue.md: %v", err)
	}
	if p := fmt.Sprint(got.Frontmatter["priority"]); p != newPriority {
		t.Errorf("priority did not persist: got %q, want %q", p, newPriority)
	}
	if e := fmt.Sprint(got.Frontmatter["estimate"]); e != fmt.Sprint(newEstimate) {
		t.Errorf("estimate did not persist: got %q, want %d", e, newEstimate)
	}
	if title := fmt.Sprint(got.Frontmatter["title"]); title != origTitle {
		t.Errorf("untouched title not preserved: got %q, want %q", title, origTitle)
	}
}

// TestOffline_InitiativeEditPersistsDescription is the initiative counterpart of
// the project test: the fixture-mode version of
// TestClaudeToolEditPersistsInitiativeDescription. A write+fsync to
// initiative.md persists the edited body through the mock mutator, and the
// untouched name survives the round trip.
func TestOffline_InitiativeEditPersistsDescription(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline edit-persistence check; uses the mock mutator")
	}
	enableMockMutations(t)

	dir, err := firstInitiativeDir()
	if err != nil {
		t.Skipf("no initiative fixture: %v", err)
	}
	path := filepath.Join(dir, "initiative.md")
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read initiative.md: %v", err)
	}
	t.Cleanup(func() { claudeToolWrite(t, path, orig) })

	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse initiative.md: %v", err)
	}
	const marker = "initiative edit persistence probe ZZZ"
	doc.Body = strings.TrimRight(doc.Body, "\n") + "\n\n" + marker
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render initiative.md: %v", err)
	}
	claudeToolWrite(t, path, edited)

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read initiative.md: %v", err)
	}
	got := string(after)
	for _, want := range []string{marker, "Test Initiative"} {
		if !strings.Contains(got, want) {
			t.Errorf("initiative edit lost %q\n--- got ---\n%s", want, got)
		}
	}
}
