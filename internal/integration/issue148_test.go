package integration

import (
	"os"
	"strings"
	"testing"
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

// TestIssue148_CreateHandsBackNoIdentifier corroborates P0 #1 ("mkdir returns no
// handle"). Every writable-collection surface exposes a `.error` failure sidecar
// but NO success sidecar (`.last` or `.created`). So after a create there is no
// deterministic channel that reports the new identifier/url/path — the agent must
// scavenge (`my/created/`, re-list, grep each issue.md) to recover it.
//
// When P0 #1 ships (a `.last`/`.created` sidecar symmetric with `.error`), this
// test flips to failing and should be inverted to assert the sidecar exists.
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
			// Failure sidecar is present (the existing #140 contract)...
			if !hasEntry(t, s.dir, ".error") {
				t.Fatalf("%s: expected a .error failure sidecar", s.name)
			}
			// ...but no success sidecar hands the created identity back.
			for _, success := range []string{".last", ".created"} {
				if hasEntry(t, s.dir, success) {
					t.Fatalf("%s: found success sidecar %q — P0 #1 appears to have shipped; "+
						"invert this test to assert %q reports the new identifier/url/path",
						s.name, success, success)
				}
			}
		})
	}
}

// TestIssue148_EditableFileColocatesVolatileServerFields corroborates P0 #2 ("the
// file rewrites itself the instant I write it"). The root cause is structural:
// issue.md carries server-managed, write-volatile fields (`updated`, `url`, `id`,
// `identifier`, and — when set — `branch`/`creator`) in the SAME frontmatter as
// the user-editable fields, and there is no separate read-only `issue.meta`. So a
// successful write re-marshals the node with a bumped `updated:` (plus injected
// branch/url), which changes the file's size/content under the editor's
// read-before-write cache → "File has been modified since read".
//
// When P0 #2 ships (split volatile server fields into a read-only issue.meta),
// this test flips to failing and should be inverted: issue.md must contain only
// editable fields, and issue.meta must exist and carry the server-managed ones.
func TestIssue148_EditableFileColocatesVolatileServerFields(t *testing.T) {
	dir := issueDirPath(testTeamKey, "TST-1")

	// There is no read-only metadata sidecar yet: server fields live in issue.md.
	if hasEntry(t, dir, "issue.meta") {
		t.Fatalf("found issue.meta — P0 #2 appears to have shipped; invert this test "+
			"to assert issue.md holds only editable fields and issue.meta holds server fields")
	}

	content, err := os.ReadFile(issueFilePath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parse issue.md frontmatter: %v", err)
	}

	// The editable fields an agent means to set...
	for _, f := range []string{"title", "status", "priority"} {
		if _, ok := doc.Frontmatter[f]; !ok {
			t.Fatalf("expected editable field %q in issue.md frontmatter", f)
		}
	}

	// ...are colocated with server-managed, write-volatile fields. `updated` is
	// the one that bumps on every successful write and drives the staleness churn.
	volatile := []string{"updated", "url", "id", "identifier"}
	var found []string
	for _, f := range volatile {
		if _, ok := doc.Frontmatter[f]; ok {
			found = append(found, f)
		}
	}
	if len(found) == 0 {
		t.Fatalf("expected server-managed fields %v colocated in issue.md (the #148 "+
			"self-mutation precondition); found none — did the meta split land?", volatile)
	}
	if _, ok := doc.Frontmatter["updated"]; !ok {
		t.Fatalf("expected write-volatile `updated:` in issue.md (bumps on every write, "+
			"causing 'modified since read'); colocated server fields were %v", found)
	}
	t.Logf("corroborated: issue.md colocates editable fields with server-managed %v "+
		"(no issue.meta split) — self-mutation on write is structurally possible", found)
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
