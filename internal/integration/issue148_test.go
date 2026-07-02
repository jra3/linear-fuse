package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// =============================================================================
// Issue #148 corroboration — "Agent ergonomics: return created identifiers, and
// stop issue.md self-mutating on write"
//
// These are *characterization* tests: they run in the default fixture mode (no
// API, no network) and assert the current, unfixed behavior that #148 reports,
// so the retrospective's two P0 claims are grounded in something CI can see.
// Each test is written to FAIL once the corresponding P0 ships — the failure
// message says what to change. They are the executable receipts for #148.
// =============================================================================

// hasEntry reports whether dir contains an entry named name.
func hasEntry(t *testing.T, dir, name string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.Name() == name {
			return true
		}
	}
	return false
}

// TestIssue148_CreateHandsBackNoIdentifier is the T1/#149 receipt (inverted from
// its original characterization form). Every writable-collection surface now
// exposes BOTH a `.error` failure sidecar and a `.last` success sidecar, present
// and readable even before any create. With the mock mutator (T0), a create
// appends the new entity's identity to `.last`, so the agent recovers it in one
// deterministic read instead of scavenging.
func TestIssue148_CreateHandsBackNoIdentifier(t *testing.T) {
	surfaces := []struct {
		name string
		dir  string
	}{
		{"issues", issuesPath(testTeamKey)},
		{"comments", commentsPath(testTeamKey, "TST-1")},
		{"docs", docsPath(testTeamKey, "TST-1")},
		{"labels", labelsPath(testTeamKey)},
	}

	for _, s := range surfaces {
		t.Run(s.name, func(t *testing.T) {
			// Both sidecars are present and readable (symmetric feedback contract).
			for _, sidecar := range []string{".error", ".last"} {
				if !hasEntry(t, s.dir, sidecar) {
					t.Fatalf("%s: expected a %q sidecar", s.name, sidecar)
				}
				if _, err := os.ReadFile(filepath.Join(s.dir, sidecar)); err != nil {
					t.Fatalf("%s: %q not readable: %v", s.name, sidecar, err)
				}
			}
		})
	}
}

// TestIssue148_LastReportsCreatedIssueIdentity is the behavioral half of T1/#149:
// with the mock mutator, creating an issue appends its identifier/url/path to
// issues/.last as a YAML list, so a batch create is recoverable in one read.
func TestIssue148_LastReportsCreatedIssueIdentity(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode behavioral check; uses the mock mutator")
	}
	enableMockMutations(t)

	title := "Last Sidecar Identity Probe"
	if err := os.Mkdir(issueDirPath(testTeamKey, title), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	data, err := os.ReadFile(issuesLastPath(testTeamKey))
	if err != nil {
		t.Fatalf("read issues/.last: %v", err)
	}
	var entries []map[string]string
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("issues/.last is not a YAML list: %v\n%s", err, data)
	}
	// Match by title, not position: the mount is shared across tests.
	var last map[string]string
	for _, e := range entries {
		if e["title"] == title {
			last = e
		}
	}
	if last == nil {
		t.Fatalf("issues/.last has no entry for our create; got: %q", data)
	}
	if last["identifier"] == "" {
		t.Errorf("last entry missing identifier: %v", last)
	}
	if last["path"] == "" {
		t.Errorf("last entry missing path (addressable name): %v", last)
	}
	if last["url"] == "" {
		t.Errorf("last entry missing url: %v", last)
	}
	// The reported path must resolve to a readable issue.
	if _, err := os.ReadFile(issueFilePath(testTeamKey, last["path"])); err != nil {
		t.Errorf("path %q from .last does not resolve to a readable issue: %v", last["path"], err)
	}
}

// TestIssue148_EditableFileColocatesVolatileServerFields is the T2/#150 receipt
// (inverted from its characterization form). issue.md now carries only editable
// fields — none of the write-volatile server fields (`updated`, `url`, `id`,
// `identifier`) that used to churn the file under an editor's read-before-write
// cache. Those live in a sibling read-only issue.meta. So a successful write no
// longer rewrites the bytes the writer wrote.
func TestIssue148_EditableFileColocatesVolatileServerFields(t *testing.T) {
	dir := issueDirPath(testTeamKey, "TST-1")

	if !hasEntry(t, dir, "issue.meta") {
		t.Fatal("expected issue.meta sidecar (T2/#150 meta split)")
	}

	content, err := os.ReadFile(issueFilePath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parse issue.md frontmatter: %v", err)
	}

	// Editable fields remain in issue.md...
	for _, f := range []string{"title", "status", "priority"} {
		if _, ok := doc.Frontmatter[f]; !ok {
			t.Fatalf("expected editable field %q in issue.md frontmatter", f)
		}
	}
	// ...and the write-volatile server fields are GONE from it.
	for _, f := range []string{"updated", "url", "id", "identifier"} {
		if _, ok := doc.Frontmatter[f]; ok {
			t.Errorf("issue.md still contains server field %q — should live in issue.meta", f)
		}
	}

	// issue.meta carries the server-managed fields, read-only.
	metaContent, err := os.ReadFile(issueMetaPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("read issue.meta: %v", err)
	}
	meta, err := parseFrontmatter(metaContent)
	if err != nil {
		t.Fatalf("parse issue.meta frontmatter: %v", err)
	}
	for _, f := range []string{"id", "identifier", "url", "updated", "created"} {
		if _, ok := meta.Frontmatter[f]; !ok {
			t.Errorf("issue.meta missing server field %q", f)
		}
	}
	// issue.meta must be read-only.
	if err := os.WriteFile(issueMetaPath(testTeamKey, "TST-1"), []byte("x"), 0644); err == nil {
		t.Error("issue.meta should be read-only, but a write succeeded")
	}
}

// TestIssue148_ProjectInitiativeMetaSplit is the T3/#156 receipt: the "editable
// in, server-managed out" rule is general — project.md and initiative.md are now
// editable-only, with server fields in read-only project.meta/initiative.meta.
func TestIssue148_ProjectInitiativeMetaSplit(t *testing.T) {
	// --- project ---
	projMD := filepath.Join(projectsPath(testTeamKey), "test-project", "project.md")
	content, err := os.ReadFile(projMD)
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parse project.md: %v", err)
	}
	if _, ok := doc.Frontmatter["name"]; !ok {
		t.Error("project.md missing editable field 'name'")
	}
	for _, f := range []string{"id", "slug", "url", "updated", "status"} {
		if _, ok := doc.Frontmatter[f]; ok {
			t.Errorf("project.md still contains server field %q — should live in project.meta", f)
		}
	}
	assertMetaHasFields(t, projectMetaPath(testTeamKey, "test-project"), "id", "slug", "url", "status", "updated")
	if err := os.WriteFile(projectMetaPath(testTeamKey, "test-project"), []byte("x"), 0644); err == nil {
		t.Error("project.meta should be read-only, but a write succeeded")
	}

	// --- initiative (skip if none) ---
	inits, err := os.ReadDir(initiativesPath())
	if err != nil || firstRealEntry(inits) == "" {
		return
	}
	slug := firstRealEntry(inits)
	initContent, err := os.ReadFile(filepath.Join(initiativesPath(), slug, "initiative.md"))
	if err != nil {
		t.Fatalf("read initiative.md: %v", err)
	}
	idoc, err := parseFrontmatter(initContent)
	if err != nil {
		t.Fatalf("parse initiative.md: %v", err)
	}
	if _, ok := idoc.Frontmatter["name"]; !ok {
		t.Error("initiative.md missing editable field 'name'")
	}
	for _, f := range []string{"id", "slug", "url", "updated", "status"} {
		if _, ok := idoc.Frontmatter[f]; ok {
			t.Errorf("initiative.md still contains server field %q — should live in initiative.meta", f)
		}
	}
	assertMetaHasFields(t, initiativeMetaPath(slug), "id", "slug", "url", "status", "updated")
}

// TestIssue148_TypedNameNeqResultingPath is a lightweight corroboration of the
// "typed name ≠ resulting path" friction (bump #4): a project's on-disk directory
// is its slug, not the human title. An agent that `mkdir`s a titled project must
// then discover the slug before it can address the project. We assert the fixture
// project is addressed by a slug-shaped name (lowercased, no spaces), documenting
// the transform the agent has to reverse.
func TestIssue148_TypedNameNeqResultingPath(t *testing.T) {
	entries, err := os.ReadDir(projectsPath(testTeamKey))
	if err != nil {
		t.Fatalf("read projects dir: %v", err)
	}
	var slug string
	for _, e := range entries {
		if e.IsDir() {
			slug = e.Name()
			break
		}
	}
	if slug == "" {
		t.Skip("no project fixture to inspect")
	}
	if slug != strings.ToLower(slug) || strings.Contains(slug, " ") {
		t.Fatalf("expected a slug-shaped project dir (lowercase, no spaces); got %q — "+
			"if this is now the human title, the slug-translation friction is gone", slug)
	}
	t.Logf("corroborated: project addressed by slug %q, not its human title — "+
		"caller must resolve the slug after create", slug)
}
