package integration

import (
	"os"
	"strings"
	"testing"
)

func TestCommentsDirectoryListing(t *testing.T) {
	// Create issue with a comment
	issue, cleanup, err := createTestIssue("Comments Listing Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	comment, commentCleanup, err := createTestComment(issue.ID, "Test comment for listing")
	if err != nil {
		t.Fatalf("Failed to create test comment: %v", err)
	}
	defer commentCleanup()
	_ = comment

	waitForCacheExpiry()

	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	// Should have at least new.md and the comment
	hasNewMd := false
	hasComment := false
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			hasNewMd = true
		}
		if strings.HasSuffix(entry.Name(), ".md") && entry.Name() != "new.md" {
			hasComment = true
		}
	}

	if !hasNewMd {
		t.Error("Comments directory should contain new.md")
	}
	if !hasComment {
		t.Error("Comments directory should contain at least one comment file")
	}
}

func TestCommentFilenameFormat(t *testing.T) {
	issue, cleanup, err := createTestIssue("Comment Filename Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	_, commentCleanup, err := createTestComment(issue.ID, "Test comment")
	if err != nil {
		t.Fatalf("Failed to create test comment: %v", err)
	}
	defer commentCleanup()

	waitForCacheExpiry()

	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	// Find comment files (not new.md)
	for _, entry := range entries {
		name := entry.Name()
		if name == "new.md" {
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
	issue, cleanup, err := createTestIssue("Comment Contents Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	commentBody := "This is the comment body text"
	_, commentCleanup, err := createTestComment(issue.ID, commentBody)
	if err != nil {
		t.Fatalf("Failed to create test comment: %v", err)
	}
	defer commentCleanup()

	waitForCacheExpiry()

	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	// Find and read a comment file
	for _, entry := range entries {
		if entry.Name() == "new.md" {
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

		// Check body contains our text (prefixed with [TEST])
		if !strings.Contains(doc.Body, commentBody) {
			t.Errorf("Comment body should contain %q", commentBody)
		}

		break // Only check first comment
	}
}

func TestCreateCommentViaNewMd(t *testing.T) {
	issue, cleanup, err := createTestIssue("Create Comment via new.md Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Write to new.md
	commentBody := "[TEST] Comment created via new.md"
	newMdPath := newCommentPath(testTeamKey, issue.Identifier)

	if err := os.WriteFile(newMdPath, []byte(commentBody), 0644); err != nil {
		t.Fatalf("Failed to write to new.md: %v", err)
	}

	waitForCacheExpiry()

	// Verify comment was created by listing
	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	found := false
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			continue
		}
		content, err := os.ReadFile(commentFilePath(testTeamKey, issue.Identifier, entry.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(content), "Comment created via new.md") {
			found = true
			break
		}
	}

	if !found {
		t.Error("Comment created via new.md not found")
	}
}

func TestDeleteComment(t *testing.T) {
	issue, cleanup, err := createTestIssue("Delete Comment Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	comment, _, err := createTestComment(issue.ID, "Comment to delete")
	if err != nil {
		t.Fatalf("Failed to create test comment: %v", err)
	}

	waitForCacheExpiry()

	// Find the comment file
	entries, err := os.ReadDir(commentsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read comments directory: %v", err)
	}

	var commentFile string
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			continue
		}
		content, err := os.ReadFile(commentFilePath(testTeamKey, issue.Identifier, entry.Name()))
		if err != nil {
			continue
		}
		doc, err := parseFrontmatter(content)
		if err != nil {
			continue
		}
		if id, ok := doc.Frontmatter["id"].(string); ok && id == comment.ID {
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

	waitForCacheExpiry()

	// Verify it's gone
	_, err = os.Stat(commentFilePath(testTeamKey, issue.Identifier, commentFile))
	if err == nil {
		t.Error("Comment file should be deleted")
	}
}

func TestNewMdAlwaysExists(t *testing.T) {
	issue, cleanup, err := createTestIssue("new.md Exists Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// new.md should always be present
	_, err = os.Stat(newCommentPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Errorf("new.md should always exist: %v", err)
	}
}

func TestNewMdReadable(t *testing.T) {
	issue, cleanup, err := createTestIssue("new.md Readable Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	content, err := os.ReadFile(newCommentPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read new.md: %v", err)
	}

	// new.md should be empty or have placeholder text
	_ = content // Content can be empty, that's fine
}
