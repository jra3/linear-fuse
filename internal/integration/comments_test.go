package integration

import (
	"os"
	"strings"
	"testing"
)

func TestCommentsDirectoryListing(t *testing.T) {
	skipIfNoWriteTests(t)
	// Create issue
	issue, cleanup, err := createTestIssue("Comments Listing Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Create comment via filesystem (_create)
	commentBody := "[TEST] Comment for listing test"
	newMdPath := newCommentPath(testTeamKey, issue.Identifier)
	if err := os.WriteFile(newMdPath, []byte(commentBody), 0644); err != nil {
		t.Fatalf("Failed to write to _create: %v", err)
	}

	// Read comments directory
	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	// Should have at least _create and the comment
	hasNewMd := false
	hasComment := false
	for _, entry := range entries {
		if entry.Name() == "_create" {
			hasNewMd = true
		}
		if strings.HasSuffix(entry.Name(), ".md") && entry.Name() != "_create" {
			hasComment = true
		}
	}

	if !hasNewMd {
		t.Error("Comments directory should contain _create")
	}
	if !hasComment {
		t.Error("Comments directory should contain at least one comment file")
	}
}

func TestCommentFilenameFormat(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Comment Filename Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Create comment via filesystem
	newMdPath := newCommentPath(testTeamKey, issue.Identifier)
	if err := os.WriteFile(newMdPath, []byte("[TEST] Comment for filename test"), 0644); err != nil {
		t.Fatalf("Failed to write to _create: %v", err)
	}

	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	// Find comment files (not _create)
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
			t.Errorf("Comment file %s should contain timestamp", name)
		}
	}
}

func TestCommentFileContents(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Comment Contents Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Create comment via filesystem
	commentBody := "This is the comment body text"
	newMdPath := newCommentPath(testTeamKey, issue.Identifier)
	if err := os.WriteFile(newMdPath, []byte(commentBody), 0644); err != nil {
		t.Fatalf("Failed to write to _create: %v", err)
	}

	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	// Find and read a comment file
	for _, entry := range entries {
		if entry.Name() == "_create" {
			continue
		}

		content, err := os.ReadFile(commentFilePath(testTeamKey, issue.Identifier, entry.Name()))
		if err != nil {
			t.Fatalf("Failed to read comment file: %v", err)
		}

		doc, err := parseFrontmatter(content)
		if err != nil {
			t.Fatalf("Failed to parse comment frontmatter: %v", err)
		}

		// Check required fields
		if _, ok := doc.Frontmatter["id"]; !ok {
			t.Error("Comment missing id field")
		}
		if _, ok := doc.Frontmatter["author"]; !ok {
			t.Error("Comment missing author field")
		}
		if _, ok := doc.Frontmatter["created"]; !ok {
			t.Error("Comment missing created field")
		}

		// Check body contains our text
		if !strings.Contains(doc.Body, commentBody) {
			t.Errorf("Comment body should contain %q", commentBody)
		}

		break // Only check first comment
	}
}

func TestCreateCommentViaNewMd(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Create Comment via _create Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Write to _create
	commentBody := "[TEST] Comment created via _create"
	newMdPath := newCommentPath(testTeamKey, issue.Identifier)

	if err := os.WriteFile(newMdPath, []byte(commentBody), 0644); err != nil {
		t.Fatalf("Failed to write to _create: %v", err)
	}

	// No wait needed - kernel cache invalidated on filesystem write
	// Verify comment was created by listing
	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	found := false
	for _, entry := range entries {
		if entry.Name() == "_create" {
			continue
		}
		content, err := os.ReadFile(commentFilePath(testTeamKey, issue.Identifier, entry.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(content), "Comment created via _create") {
			found = true
			break
		}
	}

	if !found {
		t.Error("Comment created via _create not found")
	}
}

func TestDeleteComment(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Delete Comment Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Create comment via filesystem
	newMdPath := newCommentPath(testTeamKey, issue.Identifier)
	if err := os.WriteFile(newMdPath, []byte("[TEST] Comment to delete"), 0644); err != nil {
		t.Fatalf("Failed to write to _create: %v", err)
	}

	// Find the comment file
	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	var commentFile string
	for _, entry := range entries {
		if entry.Name() != "_create" && strings.HasSuffix(entry.Name(), ".md") {
			commentFile = entry.Name()
			break
		}
	}

	if commentFile == "" {
		t.Fatal("Could not find comment file to delete")
	}

	// Delete the comment
	if err := os.Remove(commentFilePath(testTeamKey, issue.Identifier, commentFile)); err != nil {
		t.Fatalf("Failed to delete comment: %v", err)
	}

	// Verify it's gone from directory listing
	entries, err = os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to re-read comments directory: %v", err)
	}

	for _, entry := range entries {
		if entry.Name() == commentFile {
			t.Error("Comment file should be deleted")
		}
	}
}

func TestNewMdAlwaysExists(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("_create Exists Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// _create should always be present
	_, err = os.Stat(newCommentPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Errorf("_create should always exist: %v", err)
	}
}

func TestNewMdWriteOnly(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("_create Write-Only Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// _create should be write-only (0200), so reading should fail
	_, err = os.ReadFile(newCommentPath(testTeamKey, issue.Identifier))
	if err == nil {
		t.Error("_create should be write-only and not readable")
	}

	// Verify we can still write to it
	err = os.WriteFile(newCommentPath(testTeamKey, issue.Identifier), []byte("[TEST] Write-only test"), 0644)
	if err != nil {
		t.Errorf("_create should be writable: %v", err)
	}
}
