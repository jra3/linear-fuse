package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHistoryFileReadable exercises the renderFile read path for history.md:
// after moving it off a KEEP_CACHE-baked node onto renderFile (DIRECT_IO), it
// must still stat as a read-only regular file and read without error (empty is
// fine for a fixture issue with no recorded history).
func TestHistoryFileReadable(t *testing.T) {
	entries, err := os.ReadDir(issuesPath(testTeamKey))
	if err != nil {
		t.Fatalf("read issues dir: %v", err)
	}
	var issueID string
	for _, e := range entries {
		if e.IsDir() {
			issueID = e.Name()
			break
		}
	}
	if issueID == "" {
		t.Skip("no fixture issues to test")
	}

	historyPath := filepath.Join(issueDirPath(testTeamKey, issueID), "history.md")
	info, err := os.Stat(historyPath)
	if err != nil {
		t.Fatalf("stat history.md: %v", err)
	}
	if info.IsDir() {
		t.Fatal("history.md is a directory")
	}
	if info.Mode().Perm() != 0444 {
		t.Errorf("history.md mode = %v, want 0444", info.Mode().Perm())
	}
	if _, err := os.ReadFile(historyPath); err != nil {
		t.Fatalf("read history.md: %v", err)
	}
}

// TestProjectUpdateFileReadable exercises the renderFile read path for a project
// update file. If the workspace carries any update .md, it must read without
// error and carry the shared update frontmatter (health:). The project comes
// from someProjectSlug: hardcoding test-project made the "no updates dir" skip
// unconditional under a live key, so the test passed by doing nothing (#395).
func TestProjectUpdateFileReadable(t *testing.T) {
	updatesDir := filepath.Join(someProjectDir(t), "updates")
	entries, err := os.ReadDir(updatesDir)
	if err != nil {
		t.Skipf("no project updates dir: %v", err)
	}
	var updateFile string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") && !strings.HasPrefix(e.Name(), "_") {
			updateFile = e.Name()
			break
		}
	}
	if updateFile == "" {
		t.Skip("no project update files in workspace")
	}

	content, err := os.ReadFile(filepath.Join(updatesDir, updateFile))
	if err != nil {
		t.Fatalf("read update file %s: %v", updateFile, err)
	}
	if !strings.Contains(string(content), "health:") {
		t.Errorf("update file %s missing health frontmatter:\n%s", updateFile, content)
	}
}
