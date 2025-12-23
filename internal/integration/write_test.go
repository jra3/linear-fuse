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
	skipIfNoWriteTests(t)
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

	// Verify via filesystem (re-read the file)
	updated, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if updated.Title != newTitle {
		t.Errorf("Expected title %q, got %q", newTitle, updated.Title)
	}

	// Also verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.Title != newTitle {
		t.Errorf("SQLite title mismatch: expected %q, got %q", newTitle, sqliteIssue.Title)
	}
}

func TestEditIssueDescription(t *testing.T) {
	skipIfNoWriteTests(t)
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

	// Verify via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.Description != newDesc {
		t.Errorf("Expected description %q, got %q", newDesc, fsIssue.Description)
	}

	// Verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.Description != newDesc {
		t.Errorf("SQLite description mismatch: expected %q, got %q", newDesc, sqliteIssue.Description)
	}
}

func TestEditIssuePriority(t *testing.T) {
	skipIfNoWriteTests(t)
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

	// Verify via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.Priority != "high" {
		t.Errorf("Expected priority 'high', got %q", fsIssue.Priority)
	}

	// Verify SQLite state (priority 2 = high in Linear)
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.Priority != 2 {
		t.Errorf("SQLite priority mismatch: expected 2 (high), got %d", sqliteIssue.Priority)
	}
}

func TestEditIssueStatus(t *testing.T) {
	skipIfNoWriteTests(t)
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

	// Verify via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.Status != *inProgressState {
		t.Errorf("Expected status %q, got %q", *inProgressState, fsIssue.Status)
	}

	// Verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.State.Name != *inProgressState {
		t.Errorf("SQLite status mismatch: expected %q, got %q", *inProgressState, sqliteIssue.State.Name)
	}
}

func TestEditIssueDueDate(t *testing.T) {
	skipIfNoWriteTests(t)
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

	// Verify via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.DueDate != dueDate {
		t.Errorf("Expected due date %q, got %q", dueDate, fsIssue.DueDate)
	}

	// Verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.DueDate == nil || *sqliteIssue.DueDate != dueDate {
		t.Errorf("SQLite due date mismatch: expected %q, got %v", dueDate, sqliteIssue.DueDate)
	}
}

func TestClearIssueDueDate(t *testing.T) {
	skipIfNoWriteTests(t)
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

	// Verify via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.DueDate != "" {
		t.Errorf("Expected due date to be cleared, got %q", fsIssue.DueDate)
	}

	// Verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.DueDate != nil {
		t.Errorf("SQLite due date should be nil, got %v", *sqliteIssue.DueDate)
	}
}

func TestEditIssueEstimate(t *testing.T) {
	skipIfNoWriteTests(t)
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

	modified, err := modifyFrontmatter(content, "estimate", 4)
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	// Verify via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.Estimate != 4 {
		t.Errorf("Expected estimate 4, got %d", fsIssue.Estimate)
	}

	// Verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.Estimate == nil {
		t.Error("SQLite estimate should not be nil")
	} else if *sqliteIssue.Estimate != 4 {
		t.Errorf("SQLite estimate mismatch: expected 4, got %v", *sqliteIssue.Estimate)
	}
}

func TestEditMultipleFields(t *testing.T) {
	skipIfNoWriteTests(t)
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

	// Verify via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.Title != "[TEST] Updated Multiple" {
		t.Errorf("Title not updated: %s", fsIssue.Title)
	}
	if fsIssue.Priority != "medium" {
		t.Errorf("Priority not updated: %s", fsIssue.Priority)
	}

	// Verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.Title != "[TEST] Updated Multiple" {
		t.Errorf("SQLite title not updated: %s", sqliteIssue.Title)
	}
	if sqliteIssue.Priority != 3 { // 3 = medium
		t.Errorf("SQLite priority not updated: %d", sqliteIssue.Priority)
	}
}

// =============================================================================
// Issue Creation Tests
// =============================================================================

func TestCreateIssueViaMkdir(t *testing.T) {
	skipIfNoWriteTests(t)
	title := "[TEST] Created via Mkdir"
	issuePath := issueDirPath(testTeamKey, title)

	if err := os.Mkdir(issuePath, 0755); err != nil {
		t.Fatalf("Failed to create issue via mkdir: %v", err)
	}

	// No wait needed - kernel cache is invalidated on mkdir
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
	skipIfNoWriteTests(t)
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

// =============================================================================
// Cycle Tests
// =============================================================================

func TestReadIssueCycle(t *testing.T) {
	skipIfNoWriteTests(t)

	// Find a cycle to use
	cycle, err := findFirstActiveCycle()
	if err != nil {
		t.Skipf("No cycles available for testing: %v", err)
	}

	// Create an issue with a cycle
	issue, cleanup, err := createTestIssue("Cycle Read Test", WithCycleID(cycle.ID))
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Read the issue and verify cycle is in frontmatter
	content, err := readFileWithRetry(issueFilePath(testTeamKey, issue.Identifier), defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	cycleName, ok := doc.Frontmatter["cycle"].(string)
	if !ok {
		t.Fatal("Issue should have cycle field in frontmatter")
	}

	if cycleName != cycle.Name {
		t.Errorf("Expected cycle %q, got %q", cycle.Name, cycleName)
	}
}

func TestSetIssueCycle(t *testing.T) {
	skipIfNoWriteTests(t)

	// Find a cycle to use
	cycle, err := findFirstActiveCycle()
	if err != nil {
		t.Skipf("No cycles available for testing: %v", err)
	}

	// Create an issue without a cycle
	issue, cleanup, err := createTestIssue("Cycle Set Test")
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

	// Add cycle to frontmatter
	modified, err := modifyFrontmatter(content, "cycle", cycle.Name)
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	// Write back
	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	// Verify via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.Cycle != cycle.Name {
		t.Errorf("Expected cycle %q, got %q", cycle.Name, fsIssue.Cycle)
	}

	// Verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.Cycle == nil {
		t.Fatal("SQLite issue should have a cycle after update")
	}

	if sqliteIssue.Cycle.ID != cycle.ID {
		t.Errorf("SQLite cycle ID mismatch: expected %q, got %q", cycle.ID, sqliteIssue.Cycle.ID)
	}
}

func TestRemoveIssueCycle(t *testing.T) {
	skipIfNoWriteTests(t)

	// Find a cycle to use
	cycle, err := findFirstActiveCycle()
	if err != nil {
		t.Skipf("No cycles available for testing: %v", err)
	}

	// Create an issue with a cycle
	issue, cleanup, err := createTestIssue("Cycle Remove Test", WithCycleID(cycle.ID))
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

	// Verify it has the cycle first
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	if _, ok := doc.Frontmatter["cycle"]; !ok {
		t.Fatal("Issue should have cycle before removal")
	}

	// Remove cycle from frontmatter
	modified, err := removeFrontmatterField(content, "cycle")
	if err != nil {
		t.Fatalf("Failed to remove cycle: %v", err)
	}

	// Write back
	if err := os.WriteFile(path, modified, 0644); err != nil {
		t.Fatalf("Failed to write issue: %v", err)
	}

	// Verify via filesystem
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Failed to get issue from filesystem: %v", err)
	}

	if fsIssue.Cycle != "" {
		t.Errorf("Issue should not have a cycle after removal, got %q", fsIssue.Cycle)
	}

	// Verify SQLite state
	sqliteIssue, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue from SQLite: %v", err)
	}

	if sqliteIssue.Cycle != nil {
		t.Errorf("SQLite issue should not have a cycle after removal, got %q", sqliteIssue.Cycle.Name)
	}
}
