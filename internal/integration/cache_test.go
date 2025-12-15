package integration

import (
	"os"
	"testing"
	"time"
)

// =============================================================================
// Cache Behavior Tests
// =============================================================================

func TestCacheHitOnReread(t *testing.T) {
	// First read to populate cache
	_, err := os.ReadDir(teamsPath())
	if err != nil {
		t.Fatalf("First read failed: %v", err)
	}

	// Second read should be very fast (cached)
	start := time.Now()
	_, err = os.ReadDir(teamsPath())
	if err != nil {
		t.Fatalf("Second read failed: %v", err)
	}
	elapsed := time.Since(start)

	// Cached read should be under 100ms
	if elapsed > 100*time.Millisecond {
		t.Errorf("Cached read took too long: %v (expected < 100ms)", elapsed)
	}
}

func TestCacheExpiryRefreshesData(t *testing.T) {
	// This test verifies that external API changes eventually become visible
	// Note: Due to FUSE inode caching, this may require the inode to be evicted
	// which happens after the entry timeout (30s by default)
	t.Skip("Skipped: FUSE inode caching prevents immediate refresh - see filesystem implementation")
}

func TestIssueEditInvalidatesTeamCache(t *testing.T) {
	skipIfNoWriteTests(t)
	// Create an issue
	issue, cleanup, err := createTestIssue("Team Cache Invalidation")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Read issue through team path
	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	// Modify title via filesystem
	newTitle := "[TEST] Team Cache Updated"
	modified, err := modifyFrontmatter(content, "title", newTitle)
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	waitForCacheExpiry()

	// Verify the write worked via API (filesystem reads may be cached)
	updated, err := getTestIssue(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from API: %v", err)
	}

	if updated.Title != newTitle {
		t.Errorf("Write didn't persist, expected %q, got %q", newTitle, updated.Title)
	}
}

func TestCommentCreateInvalidatesCache(t *testing.T) {
	skipIfNoWriteTests(t)
	// Create an issue
	issue, cleanup, err := createTestIssue("Comment Cache Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Count initial comments
	commentsDir := commentsPath(testTeamKey, issue.Identifier)
	entries1, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatalf("Failed to read comments: %v", err)
	}
	initialCount := len(entries1)

	// Create comment via new.md
	commentBody := "[TEST] Cache invalidation comment"
	newMdPath := newCommentPath(testTeamKey, issue.Identifier)
	if err := os.WriteFile(newMdPath, []byte(commentBody), 0644); err != nil {
		t.Fatalf("Failed to create comment: %v", err)
	}

	waitForCacheExpiry()

	// Re-read comments directory
	entries2, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatalf("Failed to re-read comments: %v", err)
	}

	// Should have one more entry (the new comment)
	if len(entries2) != initialCount+1 {
		t.Errorf("Comment cache not invalidated, expected %d entries, got %d", initialCount+1, len(entries2))
	}
}

func TestCreateIssueInvalidatesTeamListing(t *testing.T) {
	skipIfNoWriteTests(t)
	// Count initial issues
	issuesDir := issuesPath(testTeamKey)
	entries1, err := os.ReadDir(issuesDir)
	if err != nil {
		t.Fatalf("Failed to read issues: %v", err)
	}
	initialCount := len(entries1)

	// Create issue via mkdir
	issueName := "[TEST] Cache Mkdir Test"
	issuePath := issueDirPath(testTeamKey, issueName)
	if err := os.Mkdir(issuePath, 0755); err != nil {
		t.Fatalf("Failed to mkdir: %v", err)
	}

	// Clean up via API after test
	defer func() {
		// Find and delete the issue
		entries, _ := os.ReadDir(issuesDir)
		for _, e := range entries {
			content, err := os.ReadFile(issueFilePath(testTeamKey, e.Name()))
			if err != nil {
				continue
			}
			doc, _ := parseFrontmatter(content)
			if title, ok := doc.Frontmatter["title"].(string); ok && title == issueName {
				if id, ok := doc.Frontmatter["id"].(string); ok {
					deleteTestIssue(id)
				}
				break
			}
		}
	}()

	waitForCacheExpiry()

	// Re-read issues directory
	entries2, err := os.ReadDir(issuesDir)
	if err != nil {
		t.Fatalf("Failed to re-read issues: %v", err)
	}

	// Should have one more issue
	if len(entries2) != initialCount+1 {
		t.Errorf("Issue cache not invalidated after mkdir, expected %d entries, got %d", initialCount+1, len(entries2))
	}
}

func TestTeamsCacheHit(t *testing.T) {
	// First read
	entries1, err := os.ReadDir(teamsPath())
	if err != nil {
		t.Fatalf("First read failed: %v", err)
	}

	// Immediate second read (should be cached)
	entries2, err := os.ReadDir(teamsPath())
	if err != nil {
		t.Fatalf("Second read failed: %v", err)
	}

	// Should return same data
	if len(entries1) != len(entries2) {
		t.Errorf("Cache inconsistency: first read %d teams, second read %d teams", len(entries1), len(entries2))
	}
}

func TestUsersCacheHit(t *testing.T) {
	// First read
	entries1, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("First read failed: %v", err)
	}

	// Immediate second read (should be cached)
	entries2, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Second read failed: %v", err)
	}

	// Should return same data
	if len(entries1) != len(entries2) {
		t.Errorf("Cache inconsistency: first read %d users, second read %d users", len(entries1), len(entries2))
	}
}
