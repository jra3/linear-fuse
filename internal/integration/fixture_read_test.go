package integration

// Fixture-based read tests that run without live API
// These tests use pre-populated SQLite fixtures

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Issue Read Tests (fixture: TST-1)
// =============================================================================

func TestFixtureIssueDirectoryContents(t *testing.T) {
	issuePath := issueDirPath(testTeamKey, "TST-1")
	entries, err := os.ReadDir(issuePath)
	if err != nil {
		t.Fatalf("Failed to read issue directory: %v", err)
	}

	// Should have issue.md, comments/, docs/ at minimum
	hasIssueMd := false
	hasComments := false
	hasDocs := false
	for _, entry := range entries {
		switch entry.Name() {
		case "issue.md":
			hasIssueMd = true
		case "comments":
			hasComments = true
		case "docs":
			hasDocs = true
		}
	}

	if !hasIssueMd {
		t.Error("Issue directory should contain issue.md")
	}
	if !hasComments {
		t.Error("Issue directory should contain comments/")
	}
	if !hasDocs {
		t.Error("Issue directory should contain docs/")
	}
}

func TestFixtureIssueFileReadable(t *testing.T) {
	content, err := os.ReadFile(issueFilePath(testTeamKey, "TST-1"))
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

func TestFixtureIssueFileContainsRequiredFields(t *testing.T) {
	content, err := os.ReadFile(issueFilePath(testTeamKey, "TST-1"))
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

func TestFixtureIssueFileDescription(t *testing.T) {
	content, err := os.ReadFile(issueFilePath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("Failed to read issue file: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// TST-1 has description "This is test issue 1"
	if !strings.Contains(doc.Body, "test issue 1") {
		t.Errorf("Issue body should contain description, got: %q", doc.Body)
	}
}

func TestFixtureIssueWithNoAssignee(t *testing.T) {
	// TST-7 is the unassigned issue
	content, err := os.ReadFile(issueFilePath(testTeamKey, "TST-7"))
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
// Comments Read Tests (fixture: TST-1 has 3 comments)
// =============================================================================

func TestFixtureCommentsDirectoryListing(t *testing.T) {
	entries, err := os.ReadDir(commentsPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	// Should have _create + 3 comments
	hasNewMd := false
	commentCount := 0
	for _, entry := range entries {
		if entry.Name() == "_create" {
			hasNewMd = true
		} else if strings.HasSuffix(entry.Name(), ".md") {
			commentCount++
		}
	}

	if !hasNewMd {
		t.Error("Comments directory should contain _create")
	}
	if commentCount != 3 {
		t.Errorf("Expected 3 comments, got %d", commentCount)
	}
}

func TestFixtureCommentFilenameFormat(t *testing.T) {
	entries, err := os.ReadDir(commentsPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if name == "_create" {
			continue
		}
		// Should be in format: NNN-YYYY-MM-DDTHH-MM.md
		if !strings.HasSuffix(name, ".md") {
			t.Errorf("Comment file %s should have .md extension", name)
		}
		if !strings.Contains(name, "-") {
			t.Errorf("Comment file %s should contain timestamp separator", name)
		}
	}
}

func TestFixtureCommentFileContents(t *testing.T) {
	entries, err := os.ReadDir(commentsPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	testedCount := 0
	for _, entry := range entries {
		if entry.Name() == "_create" {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		content, err := os.ReadFile(commentFilePath(testTeamKey, "TST-1", entry.Name()))
		if err != nil {
			t.Fatalf("Failed to read comment file %s: %v", entry.Name(), err)
		}

		doc, err := parseFrontmatter(content)
		if err != nil {
			t.Fatalf("Failed to parse comment frontmatter for %s: %v", entry.Name(), err)
		}

		// Check required fields
		if _, ok := doc.Frontmatter["id"]; !ok {
			t.Errorf("Comment %s missing id field", entry.Name())
		}
		if _, ok := doc.Frontmatter["author"]; !ok {
			t.Errorf("Comment %s missing author field", entry.Name())
		}
		if _, ok := doc.Frontmatter["created"]; !ok {
			t.Errorf("Comment %s missing created field", entry.Name())
		}

		// Body should contain "Test comment"
		if !strings.Contains(doc.Body, "Test comment") {
			t.Errorf("Comment %s body should contain 'Test comment', got: %q", entry.Name(), doc.Body)
		}

		testedCount++
	}

	if testedCount == 0 {
		t.Skip("No comment files found to test")
	}
}

func TestFixtureNewMdAlwaysExists(t *testing.T) {
	// _create should always be present in comments directory
	_, err := os.Stat(newCommentPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Errorf("_create should always exist: %v", err)
	}
}

func TestFixtureNewMdWriteOnly(t *testing.T) {
	// _create should be write-only (0200), so reading should fail
	_, err := os.ReadFile(newCommentPath(testTeamKey, "TST-1"))
	if err == nil {
		t.Error("_create should be write-only and not readable")
	}
}

// =============================================================================
// Documents Read Tests (fixture: TST-1 has 2 documents)
// =============================================================================

func TestFixtureDocsDirectoryListing(t *testing.T) {
	entries, err := os.ReadDir(docsPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	// Should have _create + 2 documents
	hasNewMd := false
	docCount := 0
	for _, entry := range entries {
		if entry.Name() == "_create" {
			hasNewMd = true
		} else if strings.HasSuffix(entry.Name(), ".md") {
			docCount++
		}
	}

	if !hasNewMd {
		t.Error("Docs directory should contain _create")
	}
	if docCount != 2 {
		t.Errorf("Expected 2 documents, got %d", docCount)
	}
}

func TestFixtureDocumentFilenameFormat(t *testing.T) {
	entries, err := os.ReadDir(docsPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if name == "_create" {
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			t.Errorf("Document file %s should have .md extension", name)
		}
	}
}

func TestFixtureDocumentFileContents(t *testing.T) {
	entries, err := os.ReadDir(docsPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	testedCount := 0
	for _, entry := range entries {
		if entry.Name() == "_create" {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		content, err := os.ReadFile(docFilePath(testTeamKey, "TST-1", entry.Name()))
		if err != nil {
			t.Fatalf("Failed to read document file %s: %v", entry.Name(), err)
		}

		doc, err := parseFrontmatter(content)
		if err != nil {
			t.Fatalf("Failed to parse document frontmatter for %s: %v", entry.Name(), err)
		}

		// Check required fields
		if _, ok := doc.Frontmatter["id"]; !ok {
			t.Errorf("Document %s missing id field", entry.Name())
		}
		if _, ok := doc.Frontmatter["title"]; !ok {
			t.Errorf("Document %s missing title field", entry.Name())
		}

		// Body should have content
		if len(doc.Body) == 0 {
			t.Errorf("Document %s body should not be empty", entry.Name())
		}

		testedCount++
	}

	if testedCount == 0 {
		t.Skip("No document files found to test")
	}
}

func TestFixtureDocsNewMdAlwaysExists(t *testing.T) {
	_, err := os.Stat(newDocPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Errorf("docs/_create should always exist: %v", err)
	}
}

func TestFixtureDocsNewMdWriteOnly(t *testing.T) {
	_, err := os.ReadFile(newDocPath(testTeamKey, "TST-1"))
	if err == nil {
		t.Error("docs/_create should be write-only and not readable")
	}
}

// =============================================================================
// Project Read Tests (fixture: test-project)
// =============================================================================

func TestFixtureProjectDirectoryContainsInfoFile(t *testing.T) {
	projectPath := filepath.Join(projectsPath(testTeamKey), "test-project")
	entries, err := os.ReadDir(projectPath)
	if err != nil {
		t.Fatalf("Failed to read project directory: %v", err)
	}

	hasProjectMd := false
	for _, entry := range entries {
		if entry.Name() == "project.md" {
			hasProjectMd = true
			break
		}
	}

	if !hasProjectMd {
		t.Error("Project directory should contain project.md")
	}
}

func TestFixtureProjectInfoFile(t *testing.T) {
	projectInfoPath := filepath.Join(projectsPath(testTeamKey), "test-project", "project.md")
	content, err := os.ReadFile(projectInfoPath)
	if err != nil {
		t.Fatalf("Failed to read project.md: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Check required fields
	requiredFields := []string{"id", "name", "slug", "status"}
	for _, field := range requiredFields {
		if _, ok := doc.Frontmatter[field]; !ok {
			t.Errorf("Missing required field %q in project.md", field)
		}
	}
}

func TestFixtureProjectIssueSymlinks(t *testing.T) {
	// TST-6 is assigned to test-project
	projectPath := filepath.Join(projectsPath(testTeamKey), "test-project")
	entries, err := os.ReadDir(projectPath)
	if err != nil {
		t.Fatalf("Failed to read project directory: %v", err)
	}

	hasIssueSymlink := false
	for _, entry := range entries {
		if entry.Name() == "TST-6" {
			hasIssueSymlink = true
			// Verify it's a symlink
			if entry.Type()&os.ModeSymlink == 0 {
				t.Error("TST-6 should be a symlink")
			}
			break
		}
	}

	if !hasIssueSymlink {
		t.Error("Project should contain symlink to TST-6")
	}
}

// =============================================================================
// Symlink Resolution Tests
// =============================================================================

func TestFixtureMyAssignedSymlinkTarget(t *testing.T) {
	entries, err := os.ReadDir(myAssignedPath())
	if err != nil {
		t.Fatalf("Failed to read my/assigned: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No assigned issues to test")
	}

	// Check that symlinks point to correct location
	testedCount := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}

		linkPath := filepath.Join(myAssignedPath(), entry.Name())
		target, err := os.Readlink(linkPath)
		if err != nil {
			t.Errorf("Failed to read symlink %s: %v", entry.Name(), err)
			continue
		}

		// Target should be relative path to teams/<KEY>/issues/<ID>
		if !strings.Contains(target, "teams/") || !strings.Contains(target, "/issues/") {
			t.Errorf("Symlink %s target should point to teams/*/issues/*, got: %s", entry.Name(), target)
		}
		testedCount++
	}

	if testedCount == 0 {
		t.Skip("No symlinks found to test")
	}
}

func TestFixtureMyAssignedSymlinkResolvable(t *testing.T) {
	entries, err := os.ReadDir(myAssignedPath())
	if err != nil {
		t.Fatalf("Failed to read my/assigned: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No assigned issues to test")
	}

	// Verify symlinks can be resolved
	testedCount := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}

		linkPath := filepath.Join(myAssignedPath(), entry.Name())
		// Try to read through the symlink
		info, err := os.Stat(linkPath)
		if err != nil {
			t.Errorf("Could not resolve symlink %s: %v", entry.Name(), err)
			continue
		}

		if !info.IsDir() {
			t.Errorf("Symlink %s should resolve to a directory", entry.Name())
		}
		testedCount++
	}

	if testedCount == 0 {
		t.Skip("No symlinks found to test")
	}
}

func TestFixtureUserIssueSymlinkResolvable(t *testing.T) {
	// Get first user
	userEntries, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Failed to read users: %v", err)
	}

	if len(userEntries) == 0 {
		t.Skip("No users")
	}

	// Read user directory
	userDir := userPath(userEntries[0].Name())
	entries, err := os.ReadDir(userDir)
	if err != nil {
		t.Fatalf("Failed to read user directory: %v", err)
	}

	// Verify all symlinks resolve
	testedCount := 0
	for _, entry := range entries {
		if entry.Name() == "user.md" {
			continue
		}
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}

		linkPath := filepath.Join(userDir, entry.Name())
		info, err := os.Stat(linkPath)
		if err != nil {
			t.Errorf("Could not resolve user issue symlink %s: %v", entry.Name(), err)
			continue
		}

		if !info.IsDir() {
			t.Errorf("User issue symlink %s should resolve to a directory", entry.Name())
		}
		testedCount++
	}

	if testedCount == 0 {
		t.Skip("No symlinks found to test")
	}
}

// =============================================================================
// By-Status Filter Tests
// =============================================================================

func TestFixtureByStatusListing(t *testing.T) {
	byStatusBasePath := filepath.Join(byPath(testTeamKey), "status")
	entries, err := os.ReadDir(byStatusBasePath)
	if err != nil {
		t.Fatalf("Failed to read by/status: %v", err)
	}

	// Should have directories for each state
	expectedStates := map[string]bool{
		"Backlog":     false,
		"Todo":        false,
		"In Progress": false,
		"Done":        false,
		"Canceled":    false,
	}

	for _, entry := range entries {
		if _, ok := expectedStates[entry.Name()]; ok {
			expectedStates[entry.Name()] = true
		}
	}

	for state, found := range expectedStates {
		if !found {
			t.Errorf("Expected by/status/%s directory", state)
		}
	}
}

func TestFixtureByStatusContainsIssues(t *testing.T) {
	// "In Progress" should contain TST-1, TST-4, TST-6
	inProgressPath := byStatusPath(testTeamKey, "In Progress")
	entries, err := os.ReadDir(inProgressPath)
	if err != nil {
		t.Fatalf("Failed to read by/status/In Progress: %v", err)
	}

	found := make(map[string]bool)
	for _, entry := range entries {
		found[entry.Name()] = true
	}

	expectedIssues := []string{"TST-1", "TST-4", "TST-6"}
	for _, id := range expectedIssues {
		if !found[id] {
			t.Errorf("Expected %s in by/status/In Progress", id)
		}
	}
}
