package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// /my/ Symlink Tests
// =============================================================================

func TestMyAssignedContainsSymlinks(t *testing.T) {
	entries, err := os.ReadDir(myAssignedPath())
	if err != nil {
		t.Fatalf("Failed to read my/assigned directory: %v", err)
	}

	// Check that entries are symlinks (if any exist)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("Failed to get info for %s: %v", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Expected %s to be a symlink", entry.Name())
		}
	}
}

func TestMyAssignedSymlinkTarget(t *testing.T) {
	entries, err := os.ReadDir(myAssignedPath())
	if err != nil {
		t.Fatalf("Failed to read my/assigned directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No assigned issues to test symlink target")
	}

	// Check first symlink target format
	entry := entries[0]
	linkPath := filepath.Join(myAssignedPath(), entry.Name())
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("Failed to read symlink: %v", err)
	}

	// Target should be relative path to teams/{KEY}/issues/{ID}/ (directory, not file)
	if !strings.Contains(target, "teams/") || !strings.Contains(target, "/issues/") {
		t.Errorf("Symlink target format incorrect: %s", target)
	}
	// Should NOT end with /issue.md - symlinks point to directories now
	if strings.HasSuffix(target, "/issue.md") {
		t.Errorf("Symlink should point to directory, not file: %s", target)
	}
}

func TestMyAssignedSymlinkResolvable(t *testing.T) {
	entries, err := os.ReadDir(myAssignedPath())
	if err != nil {
		t.Fatalf("Failed to read my/assigned directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No assigned issues to test symlink resolution")
	}

	// Symlinks point to issue directories, so read issue.md inside
	linkPath := filepath.Join(myAssignedPath(), entries[0].Name(), "issue.md")
	content, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("Failed to read issue.md through symlink: %v", err)
	}

	// Should have frontmatter
	if !strings.HasPrefix(string(content), "---\n") {
		t.Error("Content read through symlink should have frontmatter")
	}
}

func TestMyCreatedContainsSymlinks(t *testing.T) {
	entries, err := os.ReadDir(myCreatedPath())
	if err != nil {
		t.Fatalf("Failed to read my/created directory: %v", err)
	}

	// Check that entries are symlinks (if any exist)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("Failed to get info for %s: %v", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Expected %s to be a symlink", entry.Name())
		}
	}
}

func TestMyActiveContainsSymlinks(t *testing.T) {
	entries, err := os.ReadDir(myActivePath())
	if err != nil {
		t.Fatalf("Failed to read my/active directory: %v", err)
	}

	// Check that entries are symlinks (if any exist)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("Failed to get info for %s: %v", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Expected %s to be a symlink", entry.Name())
		}
	}
}

func TestMyActiveOnlyNonCompletedIssues(t *testing.T) {
	entries, err := os.ReadDir(myActivePath())
	if err != nil {
		t.Fatalf("Failed to read my/active directory: %v", err)
	}

	// Read each issue through symlink and check status
	for _, entry := range entries {
		linkPath := filepath.Join(myActivePath(), entry.Name())
		content, err := os.ReadFile(linkPath)
		if err != nil {
			continue // Skip if can't read
		}

		doc, err := parseFrontmatter(content)
		if err != nil {
			continue
		}

		// Status should not be completed or canceled
		if status, ok := doc.Frontmatter["status"].(string); ok {
			statusLower := strings.ToLower(status)
			if strings.Contains(statusLower, "done") || strings.Contains(statusLower, "completed") || strings.Contains(statusLower, "canceled") {
				t.Errorf("Active issues should not include completed/canceled status, got %s", status)
			}
		}
	}
}

// =============================================================================
// /users/ Symlink Tests
// =============================================================================

func TestUsersDirectoryContainsUserDirs(t *testing.T) {
	entries, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Failed to read users directory: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected at least one user directory")
	}

	// All entries should be directories
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Errorf("Expected %s to be a directory", entry.Name())
		}
	}
}

func TestUserDirectoryContainsSymlinksAndInfo(t *testing.T) {
	entries, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Failed to read users directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No users to test")
	}

	// Check first user directory
	userDir := filepath.Join(usersPath(), entries[0].Name())
	userEntries, err := os.ReadDir(userDir)
	if err != nil {
		t.Fatalf("Failed to read user directory: %v", err)
	}

	// Should have user.md info file
	hasUserMd := false
	for _, e := range userEntries {
		if e.Name() == "user.md" {
			hasUserMd = true
			break
		}
	}
	if !hasUserMd {
		t.Error("User directory should contain user.md")
	}
}

func TestUserInfoFile(t *testing.T) {
	entries, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Failed to read users directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No users to test")
	}

	// Read user.md from first user
	userInfoPath := filepath.Join(usersPath(), entries[0].Name(), "user.md")
	content, err := os.ReadFile(userInfoPath)
	if err != nil {
		t.Fatalf("Failed to read user.md: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Check required fields
	requiredFields := []string{"id", "email", "name"}
	for _, field := range requiredFields {
		if _, ok := doc.Frontmatter[field]; !ok {
			t.Errorf("Missing required field %q in user.md", field)
		}
	}
}

func TestUserIssueSymlinks(t *testing.T) {
	entries, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Failed to read users directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No users to test")
	}

	// Check first user directory for symlinks
	userDir := filepath.Join(usersPath(), entries[0].Name())
	userEntries, err := os.ReadDir(userDir)
	if err != nil {
		t.Fatalf("Failed to read user directory: %v", err)
	}

	// Check that non-user.md entries are symlinks
	for _, entry := range userEntries {
		if entry.Name() == "user.md" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("Failed to get info for %s: %v", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Expected %s to be a symlink in user directory", entry.Name())
		}
	}
}

// =============================================================================
// /teams/{KEY}/projects/ Symlink Tests
// =============================================================================

func TestProjectsDirectoryContainsProjects(t *testing.T) {
	entries, err := os.ReadDir(projectsPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read projects directory: %v", err)
	}

	// All entries should be directories
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Errorf("Expected %s to be a directory", entry.Name())
		}
	}
}

func TestProjectDirectoryContainsInfoFile(t *testing.T) {
	entries, err := os.ReadDir(projectsPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read projects directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No projects to test")
	}

	// Check first project directory
	projectDir := filepath.Join(projectsPath(testTeamKey), entries[0].Name())
	projectEntries, err := os.ReadDir(projectDir)
	if err != nil {
		t.Fatalf("Failed to read project directory: %v", err)
	}

	// Should have project.md info file
	hasProjectMd := false
	for _, e := range projectEntries {
		if e.Name() == "project.md" {
			hasProjectMd = true
			break
		}
	}
	if !hasProjectMd {
		t.Error("Project directory should contain project.md")
	}
}

func TestProjectInfoFile(t *testing.T) {
	entries, err := os.ReadDir(projectsPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read projects directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No projects to test")
	}

	// Read project.md from first project
	projectInfoPath := filepath.Join(projectsPath(testTeamKey), entries[0].Name(), "project.md")
	content, err := os.ReadFile(projectInfoPath)
	if err != nil {
		t.Fatalf("Failed to read project.md: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Check required fields
	requiredFields := []string{"id", "name", "url"}
	for _, field := range requiredFields {
		if _, ok := doc.Frontmatter[field]; !ok {
			t.Errorf("Missing required field %q in project.md", field)
		}
	}
}

func TestProjectIssueSymlinks(t *testing.T) {
	entries, err := os.ReadDir(projectsPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read projects directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No projects to test")
	}

	// Check first project directory for symlinks
	projectDir := filepath.Join(projectsPath(testTeamKey), entries[0].Name())
	projectEntries, err := os.ReadDir(projectDir)
	if err != nil {
		t.Fatalf("Failed to read project directory: %v", err)
	}

	// Check that issue entries (not project.md or docs/) are symlinks
	for _, entry := range projectEntries {
		if entry.Name() == "project.md" || entry.Name() == "docs" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("Failed to get info for %s: %v", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Expected %s to be a symlink in project directory", entry.Name())
		}
	}
}

// =============================================================================
// Symlink Write-Through Tests
// =============================================================================

func TestSymlinkResolution(t *testing.T) {
	skipIfNoWriteTests(t)
	// Create test issue
	issue, cleanup, err := createTestIssue("Symlink Resolution Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Find the symlink in my/created
	entries, err := os.ReadDir(myCreatedPath())
	if err != nil {
		t.Fatalf("Failed to read my/created: %v", err)
	}

	var symlinkPath string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), issue.Identifier) {
			symlinkPath = filepath.Join(myCreatedPath(), entry.Name())
			break
		}
	}

	if symlinkPath == "" {
		t.Skip("Issue symlink not found in my/created")
	}

	// Verify it's a symlink
	info, err := os.Lstat(symlinkPath)
	if err != nil {
		t.Fatalf("Failed to lstat symlink: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("Expected a symlink")
	}

	// Verify symlink target format
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("Failed to readlink: %v", err)
	}
	expectedSuffix := fmt.Sprintf("teams/%s/issues/%s/issue.md", testTeamKey, issue.Identifier)
	if !strings.HasSuffix(target, expectedSuffix) {
		t.Errorf("Symlink target should end with %q, got %q", expectedSuffix, target)
	}

	// Read through symlink
	content, err := os.ReadFile(symlinkPath)
	if err != nil {
		t.Fatalf("Failed to read through symlink: %v", err)
	}

	// Verify content has expected structure
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Verify identifier matches
	if id, ok := doc.Frontmatter["identifier"].(string); !ok || id != issue.Identifier {
		t.Errorf("Issue identifier mismatch through symlink, expected %q", issue.Identifier)
	}
}
