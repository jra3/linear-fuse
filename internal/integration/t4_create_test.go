package integration

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// writeCreateSpec writes a full issue spec to teams/{KEY}/issues/_create and
// returns the error surfaced at close (where Flush runs).
func writeCreateSpec(t *testing.T, spec string) error {
	t.Helper()
	path := issuesPath(testTeamKey) + "/_create"
	f, err := os.OpenFile(path, os.O_WRONLY, 0200)
	if err != nil {
		t.Fatalf("open issues/_create: %v", err)
	}
	if _, err := f.Write([]byte(spec)); err != nil {
		_ = f.Close()
		t.Fatalf("write issues/_create: %v", err)
	}
	return f.Close() // Flush runs here; returns the create errno
}

// TestT4_CreateSurfaceIsWriteOnly: issues/_create resolves to a write-only node.
func TestT4_CreateSurfaceIsWriteOnly(t *testing.T) {
	path := issuesPath(testTeamKey) + "/_create"
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat issues/_create: %v", err)
	}
	if info.Mode().Perm()&0o004 != 0 {
		t.Errorf("issues/_create should not be world-readable; mode=%v", info.Mode())
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Error("issues/_create should be write-only (read must fail)")
	}
}

// TestT4_InvalidFrontmatterIsLegible: bad priority and unresolvable status both
// fail with EINVAL and a Field/Error-shaped issues/.error — the same shape.
func TestT4_InvalidFrontmatterIsLegible(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode legibility check")
	}
	cases := []struct {
		name      string
		spec      string
		errSubstr string
	}{
		{"bad priority", "---\ntitle: Bad Priority\npriority: critical\n---\nbody\n", "Field: priority"},
		{"unresolvable status", "---\ntitle: Bad Status\nstatus: __no_such_state__\n---\nbody\n", "Field: status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := writeCreateSpec(t, tc.spec)
			if err == nil {
				t.Fatal("expected EINVAL writing invalid spec, got nil")
			}
			data := readFileUntilContains(t, issuesErrorPath(testTeamKey), tc.errSubstr, errorVisibilityWait)
			if !strings.Contains(string(data), tc.errSubstr) {
				t.Fatalf("issues/.error should contain %q, got: %q", tc.errSubstr, data)
			}
		})
	}
}

// TestT4_ValidSpecFailsAtAPILoudly: without the mock mutator, a valid spec fails
// loudly at the API (mirrors TestMkdirIssueFailureIsLegible) rather than silently.
func TestT4_ValidSpecFailsAtAPILoudly(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode only (no fake => real client + dummy key fails)")
	}
	err := writeCreateSpec(t, "---\ntitle: Loud API Failure Probe\n---\nbody\n")
	if err == nil {
		t.Fatal("expected the create to fail loudly without the mock mutator")
	}
	data := readFileUntilContains(t, issuesErrorPath(testTeamKey), "create issue from spec", errorVisibilityWait)
	if !strings.Contains(string(data), "create issue from spec") {
		t.Fatalf("issues/.error should explain the failed create, got: %q", data)
	}
}

// TestT4_ValidSpecSucceedsWithAssociations: with the mock mutator, a full spec
// creates one issue with its fields set and reports identity to issues/.last.
func TestT4_ValidSpecSucceedsWithAssociations(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode behavioral check; uses the mock mutator")
	}
	enableMockMutations(t)

	spec := "---\n" +
		"title: Full Object Create Probe\n" +
		"priority: high\n" +
		"status: In Progress\n" +
		"labels: [Bug]\n" +
		"due: \"2026-09-01\"\n" +
		"---\n" +
		"A body written at birth.\n"
	if err := writeCreateSpec(t, spec); err != nil {
		t.Fatalf("valid spec create should succeed with mock mutator, got: %v", err)
	}

	// issues/.last reports the new identity.
	data, err := os.ReadFile(issuesLastPath(testTeamKey))
	if err != nil {
		t.Fatalf("read issues/.last: %v", err)
	}
	var entries []map[string]string
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("issues/.last not a YAML list: %v\n%s", err, data)
	}
	// Match by title, not position: the mount is shared, so other tests append too.
	var last map[string]string
	for _, e := range entries {
		if e["title"] == "Full Object Create Probe" {
			last = e
		}
	}
	if last == nil {
		t.Fatalf("issues/.last has no entry for our create; got: %s", data)
	}
	if last["identifier"] == "" || last["path"] == "" {
		t.Fatalf("last entry missing identity: %v", last)
	}

	// The created issue is readable and carries the fields set at birth.
	content, err := os.ReadFile(issueFilePath(testTeamKey, last["path"]))
	if err != nil {
		t.Fatalf("created issue not readable at %q: %v", last["path"], err)
	}
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parse created issue.md: %v", err)
	}
	if got, _ := doc.Frontmatter["title"].(string); got != "Full Object Create Probe" {
		t.Errorf("title not set at birth: %q", got)
	}
	if got, _ := doc.Frontmatter["priority"].(string); got != "high" {
		t.Errorf("priority not set at birth: %q", got)
	}
	if !strings.Contains(string(content), "A body written at birth.") {
		t.Errorf("description body not set at birth:\n%s", content)
	}
	// The associations full-object create exists for: status, labels, due — all
	// resolved and set at birth, read back with real names (mock is store-backed).
	if got, _ := doc.Frontmatter["status"].(string); got != "In Progress" {
		t.Errorf("status not set at birth: %q (want In Progress)", got)
	}
	if got, _ := doc.Frontmatter["due"].(string); got != "2026-09-01" {
		t.Errorf("due not set at birth: %q", got)
	}
	labels, _ := doc.Frontmatter["labels"].([]any)
	hasBug := false
	for _, l := range labels {
		if s, _ := l.(string); s == "Bug" {
			hasBug = true
		}
	}
	if !hasBug {
		t.Errorf("labels not set at birth: %v (want [Bug])", doc.Frontmatter["labels"])
	}
}
