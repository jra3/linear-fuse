package integration

import (
	"os"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
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

	// Verify the write worked via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.Title != newTitle {
		t.Errorf("Filesystem title mismatch, expected %q, got %q", newTitle, fsIssue.Title)
	}

	// Verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.Title != newTitle {
		t.Errorf("SQLite title mismatch, expected %q, got %q", newTitle, sqliteIssue.Title)
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

	// Create comment via _create
	commentBody := "[TEST] Cache invalidation comment"
	newMdPath := newCommentPath(testTeamKey, issue.Identifier)
	if err := os.WriteFile(newMdPath, []byte(commentBody), 0644); err != nil {
		t.Fatalf("Failed to create comment: %v", err)
	}

	// No wait needed - kernel cache invalidated on filesystem write
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

func TestCommentVisibleImmediatelyAfterCreate(t *testing.T) {
	skipIfNoWriteTests(t)
	// This test verifies that after creating a comment via _create,
	// the new comment is visible immediately (cache insertion, not invalidation)

	issue, cleanup, err := createTestIssue("Immediate Visibility Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry() // Wait for initial cache to settle

	// First read populates the cache
	commentsDir := commentsPath(testTeamKey, issue.Identifier)
	entries1, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatalf("Failed to read comments: %v", err)
	}
	initialCount := len(entries1)

	// Create comment via _create
	commentBody := "[TEST] Immediate visibility comment"
	newMdPath := newCommentPath(testTeamKey, issue.Identifier)
	if err := os.WriteFile(newMdPath, []byte(commentBody), 0644); err != nil {
		t.Fatalf("Failed to create comment: %v", err)
	}

	// NO wait needed - kernel cache is invalidated on filesystem write
	// Re-read comments directory - should see new comment immediately
	entries2, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatalf("Failed to re-read comments: %v", err)
	}

	// Should have one more entry without waiting for cache expiry
	if len(entries2) != initialCount+1 {
		t.Errorf("Comment not immediately visible after creation, expected %d entries, got %d", initialCount+1, len(entries2))
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
					_ = deleteTestIssue(id)
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
	// Add small delay and retry to handle potential FUSE timing issues on Linux CI
	var entries2 []os.DirEntry
	for i := 0; i < 3; i++ {
		entries2, err = os.ReadDir(teamsPath())
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Second read failed after retries (first read got %d entries): %v", len(entries1), err)
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
	// Add small delay and retry to handle potential FUSE timing issues on Linux CI
	var entries2 []os.DirEntry
	for i := 0; i < 3; i++ {
		entries2, err = os.ReadDir(usersPath())
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Second read failed after retries (first read got %d entries): %v", len(entries1), err)
	}

	// Should return same data
	if len(entries1) != len(entries2) {
		t.Errorf("Cache inconsistency: first read %d users, second read %d users", len(entries1), len(entries2))
	}
}

// =============================================================================
// Cache Invalidation Immediate Visibility Tests
// =============================================================================

func TestIssueEditImmediateVisibility(t *testing.T) {
	skipIfNoWriteTests(t)

	// Create an issue via API
	issue, cleanup, err := createTestIssue("Edit Visibility Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	// Wait for API-created issue to be visible in cache
	waitForCacheExpiry()

	// Read issue.md
	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	// Modify the title
	newTitle := "[TEST] Edit Visibility Updated"
	modified, err := modifyFrontmatter(content, "title", newTitle)
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	// Write the modified content
	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Immediately re-read - NO wait needed after filesystem write
	reread, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to re-read issue: %v", err)
	}

	// Verify the new title is visible
	doc, err := parseFrontmatter(reread)
	if err != nil {
		t.Fatalf("Failed to parse re-read content: %v", err)
	}

	if title, ok := doc.Frontmatter["title"].(string); !ok || title != newTitle {
		t.Errorf("Title not immediately visible after edit, expected %q, got %q", newTitle, title)
	}
}

func TestStatusChangeByDirectoryVisibility(t *testing.T) {
	skipIfNoWriteTests(t)

	// Get available states
	states, err := getTeamStates()
	if err != nil {
		t.Fatalf("Failed to get team states: %v", err)
	}

	// Find two different states to switch between
	var fromState, toState *api.State
	for i := range states {
		if states[i].Type == "unstarted" && fromState == nil {
			fromState = &states[i]
		} else if states[i].Type == "started" && toState == nil {
			toState = &states[i]
		}
	}

	if fromState == nil || toState == nil {
		t.Skip("Could not find suitable states (need 'unstarted' and 'started' types)")
	}

	// Create issue in the initial state
	issue, cleanup, err := createTestIssue("Status Visibility Test", WithStateID(fromState.ID))
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	// Wait for API-created issue to be visible
	waitForCacheExpiry()

	// Verify issue appears in initial status directory
	fromStatusPath := byStatusPath(testTeamKey, fromState.Name)
	if !dirContains(fromStatusPath, issue.Identifier) {
		t.Fatalf("Issue not found in initial status directory %s", fromState.Name)
	}

	// Read and modify status via issue.md
	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	modified, err := modifyFrontmatter(content, "status", toState.Name)
	if err != nil {
		t.Fatalf("Failed to modify status: %v", err)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Immediately check directories - NO wait needed after filesystem write
	toStatusPath := byStatusPath(testTeamKey, toState.Name)

	// Issue should now be in the new status directory
	if !dirContains(toStatusPath, issue.Identifier) {
		t.Errorf("Issue not immediately visible in new status directory %s", toState.Name)
	}

	// Issue should no longer be in the old status directory
	if dirContains(fromStatusPath, issue.Identifier) {
		t.Errorf("Issue still visible in old status directory %s after status change", fromState.Name)
	}
}

func TestIssueArchiveImmediateVisibility(t *testing.T) {
	skipIfNoWriteTests(t)

	// Create an issue via mkdir (this way we control it entirely via filesystem)
	issueName := "[TEST] Archive Visibility Test"
	issuesDir := issuesPath(testTeamKey)
	issuePath := issueDirPath(testTeamKey, issueName)

	if err := os.Mkdir(issuePath, 0755); err != nil {
		t.Fatalf("Failed to create issue via mkdir: %v", err)
	}

	// Wait for the mkdir to complete and cache to settle
	waitForCacheExpiry()

	// Find the created issue's identifier
	entries, err := os.ReadDir(issuesDir)
	if err != nil {
		t.Fatalf("Failed to read issues directory: %v", err)
	}

	var issueIdentifier string
	for _, e := range entries {
		content, err := os.ReadFile(issueFilePath(testTeamKey, e.Name()))
		if err != nil {
			continue
		}
		doc, _ := parseFrontmatter(content)
		if title, ok := doc.Frontmatter["title"].(string); ok && title == issueName {
			issueIdentifier = e.Name()
			break
		}
	}

	if issueIdentifier == "" {
		t.Fatal("Could not find created issue")
	}

	// Verify issue is in the listing
	if !dirContains(issuesDir, issueIdentifier) {
		t.Fatalf("Issue not visible in issues directory before archive")
	}

	// Archive via rmdir
	archivePath := issueDirPath(testTeamKey, issueIdentifier)
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("Failed to archive issue via rmdir: %v", err)
	}

	// Immediately check listing - NO wait needed after filesystem write
	if dirContains(issuesDir, issueIdentifier) {
		t.Errorf("Issue still visible in issues directory immediately after archive")
	}
}
