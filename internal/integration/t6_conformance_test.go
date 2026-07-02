package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// T6 (#157): additive write-contract conformance. These tests assert the unified
// contract holds across entities end-to-end; they add no production code.

// parseLastSidecar reads a .last file and returns its YAML list (empty allowed).
func parseLastSidecar(t *testing.T, path string) []map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	var entries []map[string]string
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("%s is not a YAML list: %v\n%s", path, err, data)
	}
	return entries
}

// assertEditableOnly fails if the editable file at path contains any forbidden
// (server-managed) frontmatter field.
func assertEditableOnly(t *testing.T, path string, forbidden ...string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, f := range forbidden {
		if _, ok := doc.Frontmatter[f]; ok {
			t.Errorf("%s should not contain server field %q (belongs in .meta)", filepath.Base(path), f)
		}
	}
}

// TestWriteContractMetaSplitGeneralizes: the editable-in/server-out rule holds
// for issue.md, project.md, AND initiative.md — each editable file is free of
// volatile server fields and has a sibling .meta carrying them.
func TestWriteContractMetaSplitGeneralizes(t *testing.T) {
	forbidden := []string{"id", "slug", "url", "updated"}

	// issue
	assertEditableOnly(t, issueFilePath(testTeamKey, "TST-1"), append(forbidden, "identifier")...)
	assertMetaHasFields(t, issueMetaPath(testTeamKey, "TST-1"), "id", "identifier", "url", "updated")

	// project
	projMD := filepath.Join(projectsPath(testTeamKey), "test-project", "project.md")
	assertEditableOnly(t, projMD, forbidden...)
	assertMetaHasFields(t, projectMetaPath(testTeamKey, "test-project"), "id", "slug", "url", "updated")

	// initiative (skip if none)
	inits, err := os.ReadDir(initiativesPath())
	if err == nil && firstRealEntry(inits) != "" {
		slug := firstRealEntry(inits)
		assertEditableOnly(t, filepath.Join(initiativesPath(), slug, "initiative.md"), forbidden...)
		assertMetaHasFields(t, initiativeMetaPath(slug), "id", "slug", "url", "updated")
	}
}

// TestWriteContractLastSidecarShape: .last on a collection is read-only (0444)
// and parses as a YAML list of {identifier,url,path,title,status}. Creates one
// issue first (via the mock) so the key assertions aren't vacuous on an empty log.
func TestWriteContractLastSidecarShape(t *testing.T) {
	info, err := os.Stat(issuesLastPath(testTeamKey))
	if err != nil {
		t.Fatalf("stat issues/.last: %v", err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Errorf(".last should be read-only; mode=%v", info.Mode())
	}

	if !liveAPIMode {
		enableMockMutations(t)
		if err := os.Mkdir(issueDirPath(testTeamKey, "Last Shape Probe"), 0755); err != nil {
			t.Fatalf("seed create: %v", err)
		}
	}

	entries := parseLastSidecar(t, issuesLastPath(testTeamKey))
	if !liveAPIMode && len(entries) == 0 {
		t.Fatal("expected at least one .last entry after a create")
	}
	for _, e := range entries {
		for _, k := range []string{"identifier", "url", "path", "title", "status"} {
			if _, ok := e[k]; !ok {
				t.Errorf(".last entry missing key %q: %v", k, e)
			}
		}
	}
}

// TestWriteContractAgentLoop exercises the end-to-end shape an agent uses:
// batch create via _create (recover ids from .last), no-op rewrites that stay
// byte-stable, and a failure that leaves the success log intact.
func TestWriteContractAgentLoop(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode agent-loop; uses the mock mutator")
	}
	enableMockMutations(t)

	// (a) Batch create via N sequential _create writes; recover every id from .last.
	marker := "agentloop-batch"
	for i := 0; i < 2; i++ {
		spec := fmt.Sprintf("---\ntitle: %s-%d\npriority: medium\n---\nbody %d\n", marker, i, i)
		if err := writeCreateSpec(t, spec); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	entries := parseLastSidecar(t, issuesLastPath(testTeamKey))
	found := 0
	for _, e := range entries {
		if strings.HasPrefix(e["title"], marker) {
			found++
			if _, err := os.ReadFile(issueFilePath(testTeamKey, e["path"])); err != nil {
				t.Errorf("created issue %q from .last not reachable: %v", e["path"], err)
			}
		}
	}
	if found < 2 {
		t.Fatalf("expected >=2 %q entries in .last, found %d", marker, found)
	}

	// (a2) A non-issue create surface also reports to its .last: create a comment
	// on TST-1 and confirm comments/.last gains an entry (guards the other five
	// AppendWriteSuccess producers beyond team-issue creates).
	commentBody := "agentloop comment marker"
	if err := os.WriteFile(newCommentPath(testTeamKey, "TST-1"), []byte(commentBody), 0200); err != nil {
		t.Fatalf("create comment: %v", err)
	}
	commentsLast := filepath.Join(commentsPath(testTeamKey, "TST-1"), ".last")
	cfound := false
	for _, e := range parseLastSidecar(t, commentsLast) {
		if strings.Contains(e["title"], "agentloop comment marker") {
			cfound = true
		}
	}
	if !cfound {
		t.Errorf("comments/.last has no entry after a comment create")
	}

	// (b) No-op rewrite of an editable file is byte-stable and clears .error.
	noopByteStable(t, issueFilePath(testTeamKey, "TST-1"), issueDirPath(testTeamKey, "TST-1"))

	// (c) Same byte-stability for project.md and initiative.md.
	//
	// NOTE: offline this is a weaker check than (b). The mock mutator has no
	// single-entity reader, so commitWriteBack's read-your-writes fetch (which
	// still uses the real API client) fails and the handler keeps its unchanged
	// local state — the re-read is byte-identical largely by construction. The
	// invariant that actually prevents self-mutation now — the editable file
	// carrying no server-managed fields at all — is guarded structurally by
	// TestWriteContractMetaSplitGeneralizes. This step remains a useful guard
	// that a no-op write returns success (no errno) and leaves .error clean.
	projDir := filepath.Join(projectsPath(testTeamKey), "test-project")
	noopByteStable(t, filepath.Join(projDir, "project.md"), projDir)
	inits, err := os.ReadDir(initiativesPath())
	if err == nil && firstRealEntry(inits) != "" {
		initDir := filepath.Join(initiativesPath(), firstRealEntry(inits))
		noopByteStable(t, filepath.Join(initDir, "initiative.md"), initDir)
	}

	// (d) A subsequent EINVAL create must not wipe the earlier successes from .last.
	before := len(parseLastSidecar(t, issuesLastPath(testTeamKey)))
	if err := writeCreateSpec(t, "---\ntitle: Doomed\npriority: critical\n---\nx\n"); err == nil {
		t.Fatal("expected EINVAL for invalid priority")
	}
	after := parseLastSidecar(t, issuesLastPath(testTeamKey))
	if len(after) < before {
		t.Errorf(".last shrank after a failed create: %d -> %d (append log should survive)", before, len(after))
	}
}

// noopByteStable writes an editable file's exact bytes back and asserts the
// re-read frontmatter is byte-identical (no self-mutation) and .error is empty.
func noopByteStable(t *testing.T, filePath, dirPath string) {
	t.Helper()
	orig, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read %s: %v", filePath, err)
	}
	if frontmatterOf(string(orig)) == "" {
		t.Fatalf("%s has no frontmatter block — byte-stability check would be vacuous", filepath.Base(filePath))
	}
	claudeToolWrite(t, filePath, orig) // fails the test if close/commit errors

	after, err := readFileWithRetry(filePath, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read %s: %v", filePath, err)
	}
	if frontmatterOf(string(orig)) != frontmatterOf(string(after)) {
		t.Errorf("%s frontmatter changed across a no-op rewrite (self-mutation):\n--- before ---\n%s\n--- after ---\n%s",
			filepath.Base(filePath), frontmatterOf(string(orig)), frontmatterOf(string(after)))
	}
	if data, _ := os.ReadFile(filepath.Join(dirPath, ".error")); strings.TrimSpace(string(data)) != "" {
		t.Errorf("%s: .error non-empty after a faithful no-op write: %q", filepath.Base(filePath), data)
	}
}

// frontmatterOf returns the YAML frontmatter block (between the --- fences).
func frontmatterOf(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return ""
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return ""
	}
	return rest[:end]
}
