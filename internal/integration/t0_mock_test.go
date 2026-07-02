package integration

import (
	"os"
	"strings"
	"testing"
)

// TestT0_MockMutationClientEnablesOfflineCreate is the T0 (#155) smoke test: with
// the fake mutation client injected, a fixture-mode `mkdir "Title"` runs the full
// create handler tail to success (CreateIssue -> UpsertIssue -> invalidate ->
// ClearWriteError) and the new issue becomes readable — none of which is possible
// with the real dummy-key client. It is the seam that makes the *success* half of
// the write contract provable in `make test`.
func TestT0_MockMutationClientEnablesOfflineCreate(t *testing.T) {
	if liveAPIMode {
		t.Skip("T0 fake is for fixture mode; live mode creates real issues")
	}
	enableMockMutations(t)

	title := "T0 Mock Create Probe"
	if err := os.Mkdir(issueDirPath(testTeamKey, title), 0755); err != nil {
		t.Fatalf("mkdir with mock mutator should succeed offline, got: %v", err)
	}

	// The created issue must be discoverable in the team listing and readable.
	entries, err := os.ReadDir(issuesPath(testTeamKey))
	if err != nil {
		t.Fatalf("read issues dir: %v", err)
	}
	var found string
	for _, e := range entries {
		content, err := os.ReadFile(issueFilePath(testTeamKey, e.Name()))
		if err != nil {
			continue
		}
		doc, err := parseFrontmatter(content)
		if err != nil {
			continue
		}
		if tt, ok := doc.Frontmatter["title"].(string); ok && strings.Contains(tt, "T0 Mock Create Probe") {
			found = e.Name()
			break
		}
	}
	if found == "" {
		t.Fatal("issue created via mock mutator not found in listing")
	}

	// issues/.error must be empty after a successful create.
	errData, _ := os.ReadFile(issuesErrorPath(testTeamKey))
	if strings.TrimSpace(string(errData)) != "" {
		t.Fatalf("expected empty issues/.error after successful create, got: %q", errData)
	}
}

// TestT0_LoudFailureStillWorksWithoutFake guards the opt-in property: without the
// fake, mutations still fail loudly (the real client + dummy key), so the
// #131/#140 loud-failure contract is unaffected by T0.
func TestT0_LoudFailureStillWorksWithoutFake(t *testing.T) {
	if liveAPIMode {
		t.Skip("only meaningful in fixture mode")
	}
	err := os.Mkdir(issueDirPath(testTeamKey, "T0 No-Fake Failure Probe"), 0755)
	if err == nil {
		_ = os.Remove(issueDirPath(testTeamKey, "T0 No-Fake Failure Probe"))
		t.Fatal("expected mkdir to fail without the mock mutator (real client + dummy key)")
	}
}
