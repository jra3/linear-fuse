package integration

import (
	"os"
	"strings"
	"testing"
)

// =============================================================================
// Issue Editing Tests
// =============================================================================

func TestEditIssueTitle(t *testing.T) {
	issue, cleanup, err := createTestIssue("Original Title")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Read current content
	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	// Modify title
	newTitle := "[TEST] Modified Title"
	modified, err := modifyFrontmatter(content, "title", newTitle)
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	// Write back
	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	// Verify via API
	waitForCacheExpiry()
	updated, err := getTestIssue(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from API: %v", err)
	}

	if updated.Title != newTitle {
		t.Errorf("Expected title %q, got %q", newTitle, updated.Title)
	}
}

func TestEditIssueDescription(t *testing.T) {
	issue, cleanup, err := createTestIssue("Description Edit Test", WithDescription("Original description"))
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	// Parse and modify body
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	newDesc := "Modified description via filesystem"
	doc.Body = newDesc

	// Rebuild content
	modified, err := modifyFrontmatter(content, "title", doc.Frontmatter["title"]) // no-op to preserve frontmatter
	if err != nil {
		t.Fatalf("Failed to rebuild: %v", err)
	}

	// Replace body
	parts := strings.SplitN(string(modified), "---\n", 3)
	if len(parts) == 3 {
		modified = []byte(parts[0] + "---\n" + parts[1] + "---\n" + newDesc)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	waitForCacheExpiry()
	updated, err := getTestIssue(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from API: %v", err)
	}

	if updated.Description != newDesc {
		t.Errorf("Expected description %q, got %q", newDesc, updated.Description)
	}
}

func TestEditIssuePriority(t *testing.T) {
	issue, cleanup, err := createTestIssue("Priority Edit Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	// Change priority to "high"
	modified, err := modifyFrontmatter(content, "priority", "high")
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	waitForCacheExpiry()
	updated, err := getTestIssue(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from API: %v", err)
	}

	// Priority 2 = high in Linear
	if updated.Priority != 2 {
		t.Errorf("Expected priority 2 (high), got %d", updated.Priority)
	}
}

func TestEditIssueStatus(t *testing.T) {
	issue, cleanup, err := createTestIssue("Status Edit Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	// Find an "In Progress" state
	states, err := getTeamStates()
	if err != nil {
		t.Fatalf("Failed to get states: %v", err)
	}

	var inProgressState *string
	for _, s := range states {
		if s.Type == "started" {
			inProgressState = &s.Name
			break
		}
	}
	if inProgressState == nil {
		t.Skip("No 'started' state found in team")
	}

	waitForCacheExpiry()

	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	modified, err := modifyFrontmatter(content, "status", *inProgressState)
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	waitForCacheExpiry()
	updated, err := getTestIssue(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from API: %v", err)
	}

	if updated.State.Name != *inProgressState {
		t.Errorf("Expected status %q, got %q", *inProgressState, updated.State.Name)
	}
}

func TestEditIssueDueDate(t *testing.T) {
	issue, cleanup, err := createTestIssue("Due Date Edit Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	dueDate := "2025-12-31"
	modified, err := modifyFrontmatter(content, "due", dueDate)
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	waitForCacheExpiry()
	updated, err := getTestIssue(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from API: %v", err)
	}

	if updated.DueDate == nil || *updated.DueDate != dueDate {
		t.Errorf("Expected due date %q, got %v", dueDate, updated.DueDate)
	}
}

func TestClearIssueDueDate(t *testing.T) {
	issue, cleanup, err := createTestIssue("Clear Due Date Test", WithDueDate("2025-06-15"))
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	// Remove due field
	modified, err := removeFrontmatterField(content, "due")
	if err != nil {
		t.Fatalf("Failed to remove field: %v", err)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	waitForCacheExpiry()
	updated, err := getTestIssue(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from API: %v", err)
	}

	if updated.DueDate != nil {
		t.Errorf("Expected due date to be cleared, got %v", *updated.DueDate)
	}
}

func TestEditIssueEstimate(t *testing.T) {
	issue, cleanup, err := createTestIssue("Estimate Edit Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	modified, err := modifyFrontmatter(content, "estimate", 5)
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	waitForCacheExpiry()
	updated, err := getTestIssue(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from API: %v", err)
	}

	if updated.Estimate == nil || *updated.Estimate != 5 {
		t.Errorf("Expected estimate 5, got %v", updated.Estimate)
	}
}

func TestEditMultipleFields(t *testing.T) {
	issue, cleanup, err := createTestIssue("Multiple Fields Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	// Modify multiple fields
	modified, err := modifyFrontmatter(content, "title", "[TEST] Updated Multiple")
	if err != nil {
		t.Fatalf("Failed to modify title: %v", err)
	}
	modified, err = modifyFrontmatter(modified, "priority", "medium")
	if err != nil {
		t.Fatalf("Failed to modify priority: %v", err)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	waitForCacheExpiry()
	updated, err := getTestIssue(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from API: %v", err)
	}

	if updated.Title != "[TEST] Updated Multiple" {
		t.Errorf("Title not updated: %s", updated.Title)
	}
	if updated.Priority != 3 { // 3 = medium
		t.Errorf("Priority not updated: %d", updated.Priority)
	}
}

// =============================================================================
// Issue Creation Tests
// =============================================================================

func TestCreateIssueViaMkdir(t *testing.T) {
	title := "[TEST] Created via Mkdir"
	issuePath := issueDirPath(testTeamKey, title)

	if err := os.Mkdir(issuePath, 0755); err != nil {
		t.Fatalf("Failed to create issue via mkdir: %v", err)
	}

	// Wait for issue to appear in listing
	waitForCacheExpiry()

	// List issues and find one with our title
	entries, err := os.ReadDir(issuesPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read issues: %v", err)
	}

	var foundIdentifier string
	for _, entry := range entries {
		path := issueFilePath(testTeamKey, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		doc, err := parseFrontmatter(content)
		if err != nil {
			continue
		}
		if t, ok := doc.Frontmatter["title"].(string); ok && strings.Contains(t, "Created via Mkdir") {
			foundIdentifier = entry.Name()
			break
		}
	}

	if foundIdentifier == "" {
		t.Error("Created issue not found in listing")
	}
}

func TestCreatedIssueReadable(t *testing.T) {
	issue, cleanup, err := createTestIssue("Created Issue Readable Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Should be able to read immediately
	content, err := readFileWithRetry(issueFilePath(testTeamKey, issue.Identifier), defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read created issue: %v", err)
	}

	if len(content) == 0 {
		t.Error("Created issue file is empty")
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	if _, ok := doc.Frontmatter["id"]; !ok {
		t.Error("Created issue missing id field")
	}
}
