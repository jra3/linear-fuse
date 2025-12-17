package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Root & Structure Tests
// =============================================================================

func TestRootListing(t *testing.T) {
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		t.Fatalf("Failed to read mount point: %v", err)
	}

	expected := map[string]bool{
		"README.md": false,
		"teams":     false,
		"users":     false,
		"my":        false,
	}

	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; ok {
			expected[entry.Name()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("Expected %q in root directory, not found", name)
		}
	}
}

func TestRootReadmeMdReadable(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(mountPoint, "README.md"))
	if err != nil {
		t.Fatalf("Failed to read README.md: %v", err)
	}

	if len(content) == 0 {
		t.Error("README.md is empty")
	}

	if !strings.Contains(string(content), "Linear") {
		t.Error("README.md should mention Linear")
	}
}

func TestRootReadmeMdPermissions(t *testing.T) {
	info, err := os.Stat(filepath.Join(mountPoint, "README.md"))
	if err != nil {
		t.Fatalf("Failed to stat README.md: %v", err)
	}

	// Check it's a regular file
	if !info.Mode().IsRegular() {
		t.Errorf("README.md should be a regular file, got mode %v", info.Mode())
	}
}

func TestTeamsDirectoryExists(t *testing.T) {
	info, err := os.Stat(teamsPath())
	if err != nil {
		t.Fatalf("Failed to stat teams directory: %v", err)
	}

	if !info.IsDir() {
		t.Error("teams should be a directory")
	}
}

func TestUsersDirectoryExists(t *testing.T) {
	info, err := os.Stat(usersPath())
	if err != nil {
		t.Fatalf("Failed to stat users directory: %v", err)
	}

	if !info.IsDir() {
		t.Error("users should be a directory")
	}
}

func TestMyDirectoryExists(t *testing.T) {
	info, err := os.Stat(myPath())
	if err != nil {
		t.Fatalf("Failed to stat my directory: %v", err)
	}

	if !info.IsDir() {
		t.Error("my should be a directory")
	}
}

func TestNonexistentRootPath(t *testing.T) {
	_, err := os.Stat(filepath.Join(mountPoint, "nonexistent"))
	if err == nil {
		t.Error("Expected error for nonexistent path")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Expected ENOENT, got: %v", err)
	}
}

// =============================================================================
// Teams Tests
// =============================================================================

func TestTeamsListing(t *testing.T) {
	entries, err := os.ReadDir(teamsPath())
	if err != nil {
		t.Fatalf("Failed to read teams directory: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected at least one team")
	}

	// Verify test team is present
	found := false
	for _, entry := range entries {
		if entry.Name() == testTeamKey {
			found = true
			if !entry.IsDir() {
				t.Errorf("Team %s should be a directory", testTeamKey)
			}
			break
		}
	}
	if !found {
		t.Errorf("Expected to find team %q", testTeamKey)
	}
}

func TestTeamDirectoryContents(t *testing.T) {
	entries, err := os.ReadDir(teamPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read team directory: %v", err)
	}

	expected := []string{"team.md", "states.md", "labels.md", "by", "issues", "cycles", "projects"}
	entryNames := make(map[string]bool)
	for _, e := range entries {
		entryNames[e.Name()] = true
	}

	for _, name := range expected {
		if !entryNames[name] {
			t.Errorf("Expected %q in team directory", name)
		}
	}
}

func TestTeamInfoFile(t *testing.T) {
	content, err := os.ReadFile(teamInfoPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read team.md: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Check required fields
	requiredFields := []string{"id", "key", "name"}
	for _, field := range requiredFields {
		if _, ok := doc.Frontmatter[field]; !ok {
			t.Errorf("Missing required field %q in team.md", field)
		}
	}

	// Verify key matches
	if key, ok := doc.Frontmatter["key"].(string); ok {
		if key != testTeamKey {
			t.Errorf("Expected key %q, got %q", testTeamKey, key)
		}
	}
}

func TestTeamStatesFile(t *testing.T) {
	content, err := os.ReadFile(teamStatesPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read states.md: %v", err)
	}

	str := string(content)

	// Should contain table headers
	if !strings.Contains(str, "Name") || !strings.Contains(str, "ID") {
		t.Error("States file should contain Name and ID columns")
	}

	// Should have some states listed
	if len(str) < 100 {
		t.Error("States file seems too short")
	}
}

func TestTeamLabelsFile(t *testing.T) {
	content, err := os.ReadFile(teamLabelsPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read labels.md: %v", err)
	}

	str := string(content)

	// Should contain table headers
	if !strings.Contains(str, "Name") || !strings.Contains(str, "ID") {
		t.Error("Labels file should contain Name and ID columns")
	}
}

func TestTeamMetadataFilesReadOnly(t *testing.T) {
	// Try to write to team.md - should fail
	err := os.WriteFile(teamInfoPath(testTeamKey), []byte("test"), 0644)
	if err == nil {
		t.Error("Expected error writing to team.md (should be read-only)")
	}
}

func TestNonexistentTeam(t *testing.T) {
	_, err := os.Stat(teamPath("NONEXISTENT"))
	if err == nil {
		t.Error("Expected error for nonexistent team")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Expected ENOENT, got: %v", err)
	}
}

func TestTeamIssuesDirectoryExists(t *testing.T) {
	info, err := os.Stat(issuesPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to stat issues directory: %v", err)
	}

	if !info.IsDir() {
		t.Error("issues should be a directory")
	}
}

// =============================================================================
// Issues Tests
// =============================================================================

func TestIssuesDirectoryListing(t *testing.T) {
	entries, err := os.ReadDir(issuesPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read issues directory: %v", err)
	}

	// All entries should be directories with issue identifiers
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Errorf("Expected %s to be a directory", entry.Name())
		}
		// Issue identifiers should contain team key
		if !strings.HasPrefix(entry.Name(), testTeamKey+"-") {
			t.Errorf("Issue identifier %s should start with %s-", entry.Name(), testTeamKey)
		}
	}
}

func TestIssueDirectoryContents(t *testing.T) {
	skipIfNoWriteTests(t)
	// Create a test issue
	issue, cleanup, err := createTestIssue("Directory Contents Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	// Wait for it to appear
	err = waitForDirEntry(issuesPath(testTeamKey), issue.Identifier, defaultWaitTime)
	if err != nil {
		t.Fatalf("Issue directory didn't appear: %v", err)
	}

	entries, err := os.ReadDir(issueDirPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read issue directory: %v", err)
	}

	hasIssueMd := false
	hasComments := false
	for _, entry := range entries {
		if entry.Name() == "issue.md" {
			hasIssueMd = true
		}
		if entry.Name() == "comments" {
			hasComments = true
		}
	}

	if !hasIssueMd {
		t.Error("Issue directory should contain issue.md")
	}
	if !hasComments {
		t.Error("Issue directory should contain comments/")
	}
}

func TestIssueFileReadable(t *testing.T) {
	skipIfNoWriteTests(t)
	// Create a test issue
	issue, cleanup, err := createTestIssue("File Readable Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	// Wait for cache
	waitForCacheExpiry()

	content, err := readFileWithRetry(issueFilePath(testTeamKey, issue.Identifier), defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue file: %v", err)
	}

	if len(content) == 0 {
		t.Error("Issue file is empty")
	}

	// Should have frontmatter
	if !strings.HasPrefix(string(content), "---\n") {
		t.Error("Issue file should start with YAML frontmatter")
	}
}

func TestIssueFileContainsRequiredFields(t *testing.T) {
	skipIfNoWriteTests(t)
	// Create a test issue with known content
	issue, cleanup, err := createTestIssue("Required Fields Test", WithDescription("Test description"))
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	content, err := readFileWithRetry(issueFilePath(testTeamKey, issue.Identifier), defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue file: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	requiredFields := []string{"id", "identifier", "title", "status", "priority", "url", "created", "updated"}
	for _, field := range requiredFields {
		if _, ok := doc.Frontmatter[field]; !ok {
			t.Errorf("Missing required field %q", field)
		}
	}
}

func TestIssueFileDescription(t *testing.T) {
	skipIfNoWriteTests(t)
	desc := "This is a test description for the issue body"
	issue, cleanup, err := createTestIssue("Description Test", WithDescription(desc))
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	content, err := readFileWithRetry(issueFilePath(testTeamKey, issue.Identifier), defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue file: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	if !strings.Contains(doc.Body, desc) {
		t.Errorf("Issue body should contain description %q, got %q", desc, doc.Body)
	}
}

func TestNonexistentIssue(t *testing.T) {
	_, err := os.Stat(issueDirPath(testTeamKey, testTeamKey+"-999999"))
	if err == nil {
		t.Error("Expected error for nonexistent issue")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Expected ENOENT, got: %v", err)
	}
}

func TestIssueWithNoAssignee(t *testing.T) {
	skipIfNoWriteTests(t)
	// Create issue without assignee
	issue, cleanup, err := createTestIssue("No Assignee Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	content, err := readFileWithRetry(issueFilePath(testTeamKey, issue.Identifier), defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue file: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Assignee should not be present or be empty
	if assignee, ok := doc.Frontmatter["assignee"]; ok && assignee != nil && assignee != "" {
		t.Errorf("Expected no assignee, got %v", assignee)
	}
}

// =============================================================================
// My Directory Tests
// =============================================================================

func TestMyDirectoryContents(t *testing.T) {
	entries, err := os.ReadDir(myPath())
	if err != nil {
		t.Fatalf("Failed to read my directory: %v", err)
	}

	expected := []string{"assigned", "created", "active"}
	entryNames := make(map[string]bool)
	for _, e := range entries {
		entryNames[e.Name()] = true
	}

	for _, name := range expected {
		if !entryNames[name] {
			t.Errorf("Expected %q in /my/ directory", name)
		}
	}
}

func TestMyAssignedAccessible(t *testing.T) {
	_, err := os.ReadDir(myAssignedPath())
	if err != nil {
		t.Fatalf("Failed to read my/assigned directory: %v", err)
	}
}

func TestMyCreatedAccessible(t *testing.T) {
	_, err := os.ReadDir(myCreatedPath())
	if err != nil {
		t.Fatalf("Failed to read my/created directory: %v", err)
	}
}

func TestMyActiveAccessible(t *testing.T) {
	_, err := os.ReadDir(myActivePath())
	if err != nil {
		t.Fatalf("Failed to read my/active directory: %v", err)
	}
}

// =============================================================================
// Users Tests
// =============================================================================

func TestUsersListing(t *testing.T) {
	entries, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Failed to read users directory: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected at least one user")
	}

	// All entries should be directories
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Errorf("Expected %s to be a directory", entry.Name())
		}
	}
}

// =============================================================================
// Cycles Tests
// =============================================================================

func TestCyclesDirectoryAccessible(t *testing.T) {
	_, err := os.ReadDir(cyclesPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read cycles directory: %v", err)
	}
}

// =============================================================================
// Projects Tests
// =============================================================================

func TestProjectsDirectoryAccessible(t *testing.T) {
	_, err := os.ReadDir(projectsPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read projects directory: %v", err)
	}
}
