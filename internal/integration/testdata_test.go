package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// Rate limiting for API operations to avoid hitting Linear's usage limits
var (
	rateLimitMu   sync.Mutex
	lastAPICall   time.Time
	apiCallDelay  = 1 * time.Second // Minimum delay between API write operations
)

// skipIfNoWriteTests skips the test if write tests are not enabled
// Write tests require live API mode with LINEARFS_WRITE_TESTS=1
func skipIfNoWriteTests(t interface{ Skip(...any) }) {
	if !liveAPIMode {
		t.Skip("Skipped: requires live API (set LINEARFS_LIVE_API=1 and LINEAR_API_KEY)")
	}
	if os.Getenv("LINEARFS_WRITE_TESTS") != "1" {
		t.Skip("Skipped: write tests disabled (set LINEARFS_WRITE_TESTS=1 to enable)")
	}
}

// rateLimitWait ensures we don't make API calls too quickly
func rateLimitWait() {
	rateLimitMu.Lock()
	defer rateLimitMu.Unlock()

	elapsed := time.Since(lastAPICall)
	if elapsed < apiCallDelay {
		time.Sleep(apiCallDelay - elapsed)
	}
	lastAPICall = time.Now()
}

// TestIssue wraps an API issue with test metadata
type TestIssue struct {
	*api.Issue
}

// IssueOption configures a test issue
type IssueOption func(map[string]any)

func WithDescription(desc string) IssueOption {
	return func(input map[string]any) {
		input["description"] = desc
	}
}

func WithPriority(p int) IssueOption {
	return func(input map[string]any) {
		input["priority"] = p
	}
}

func WithDueDate(date string) IssueOption {
	return func(input map[string]any) {
		input["dueDate"] = date
	}
}

func WithEstimate(e int) IssueOption {
	return func(input map[string]any) {
		input["estimate"] = e
	}
}

func WithAssigneeID(userID string) IssueOption {
	return func(input map[string]any) {
		input["assigneeId"] = userID
	}
}

func WithStateID(stateID string) IssueOption {
	return func(input map[string]any) {
		input["stateId"] = stateID
	}
}

// createTestIssue creates an issue via filesystem mkdir for testing.
// The title is prefixed with [TEST] and a timestamp.
// Returns the issue and a cleanup function (currently no-op since Linear doesn't have delete).
func createTestIssue(title string, opts ...IssueOption) (*TestIssue, func(), error) {
	rateLimitWait() // Prevent API rate limiting

	fullTitle := fmt.Sprintf("[TEST] %s %d", title, time.Now().UnixMilli())
	issuePath := fmt.Sprintf("%s/teams/%s/issues/%s", mountPoint, testTeamKey, fullTitle)

	// Create issue via filesystem mkdir - this goes through FUSE and syncs to SQLite
	if err := os.Mkdir(issuePath, 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create test issue via mkdir: %w", err)
	}

	// Read the created issue to get its identifier
	entries, err := os.ReadDir(fmt.Sprintf("%s/teams/%s/issues", mountPoint, testTeamKey))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read issues directory: %w", err)
	}

	// Find the issue we just created by matching title
	for _, entry := range entries {
		issueMdPath := fmt.Sprintf("%s/teams/%s/issues/%s/issue.md", mountPoint, testTeamKey, entry.Name())
		content, err := os.ReadFile(issueMdPath)
		if err != nil {
			continue
		}
		if strings.Contains(string(content), fullTitle) {
			// Parse frontmatter to get the ID
			doc, err := parseFrontmatter(content)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to parse issue.md frontmatter: %w", err)
			}

			identifier := entry.Name()
			id, _ := doc.Frontmatter["id"].(string)

			issue := &api.Issue{
				ID:         id,
				Identifier: identifier,
				Title:      fullTitle,
				Team:       &api.Team{Key: testTeamKey, ID: testTeamID},
			}

			cleanup := func() {
				// No-op for now - test issues stay in workspace with [TEST] prefix
			}

			return &TestIssue{Issue: issue}, cleanup, nil
		}
	}

	return nil, nil, fmt.Errorf("created issue not found in filesystem")
}

// FilesystemIssue represents an issue as read from the filesystem
type FilesystemIssue struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	Status      string
	Priority    string
	Assignee    string
	DueDate     string
	Estimate    int
	Cycle       string
	Project     string
	Labels      []string
}

// getIssueFromFilesystem reads and parses an issue from the mounted filesystem.
// This is the preferred way to verify issue state in write tests.
func getIssueFromFilesystem(identifier string) (*FilesystemIssue, error) {
	path := fmt.Sprintf("%s/teams/%s/issues/%s/issue.md", mountPoint, testTeamKey, identifier)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read issue.md: %w", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	issue := &FilesystemIssue{
		Description: doc.Body,
	}

	// Extract frontmatter fields
	if v, ok := doc.Frontmatter["id"].(string); ok {
		issue.ID = v
	}
	if v, ok := doc.Frontmatter["identifier"].(string); ok {
		issue.Identifier = v
	}
	if v, ok := doc.Frontmatter["title"].(string); ok {
		issue.Title = v
	}
	if v, ok := doc.Frontmatter["status"].(string); ok {
		issue.Status = v
	}
	if v, ok := doc.Frontmatter["priority"].(string); ok {
		issue.Priority = v
	}
	if v, ok := doc.Frontmatter["assignee"].(string); ok {
		issue.Assignee = v
	}
	if v, ok := doc.Frontmatter["due"].(string); ok {
		issue.DueDate = v
	}
	if v, ok := doc.Frontmatter["estimate"].(int); ok {
		issue.Estimate = v
	}
	if v, ok := doc.Frontmatter["cycle"].(string); ok {
		issue.Cycle = v
	}
	if v, ok := doc.Frontmatter["project"].(string); ok {
		issue.Project = v
	}
	if labels, ok := doc.Frontmatter["labels"].([]interface{}); ok {
		for _, l := range labels {
			if s, ok := l.(string); ok {
				issue.Labels = append(issue.Labels, s)
			}
		}
	}

	return issue, nil
}

// getIssueFromSQLite fetches an issue directly from the SQLite database.
// This verifies that the write was persisted to the database layer.
func getIssueFromSQLite(issueID string) (*api.Issue, error) {
	if lfs == nil || lfs.GetStore() == nil {
		return nil, fmt.Errorf("SQLite store not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dbIssue, err := lfs.GetStore().Queries().GetIssueByID(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("issue not found in SQLite: %w", err)
	}

	// Convert db.Issue to api.Issue (basic fields)
	issue := &api.Issue{
		ID:          dbIssue.ID,
		Identifier:  dbIssue.Identifier,
		Title:       dbIssue.Title,
		Priority:    int(dbIssue.Priority.Int64),
	}

	if dbIssue.Description.Valid {
		issue.Description = dbIssue.Description.String
	}
	if dbIssue.StateName.Valid {
		issue.State = api.State{Name: dbIssue.StateName.String}
		if dbIssue.StateType.Valid {
			issue.State.Type = dbIssue.StateType.String
		}
	}
	if dbIssue.DueDate.Valid {
		issue.DueDate = &dbIssue.DueDate.String
	}
	if dbIssue.Estimate.Valid {
		est := dbIssue.Estimate.Float64
		issue.Estimate = &est
	}
	if dbIssue.CycleName.Valid {
		issue.Cycle = &api.IssueCycle{Name: dbIssue.CycleName.String}
		if dbIssue.CycleID.Valid {
			issue.Cycle.ID = dbIssue.CycleID.String
		}
	}

	return issue, nil
}

// getTeamStates fetches workflow states for the test team
func getTeamStates() ([]api.State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return apiClient.GetTeamStates(ctx, testTeamID)
}

// createTestDocument creates a document attached to an issue for testing.
func createTestDocument(issueID, title, content string) (*api.Document, func(), error) {
	rateLimitWait() // Prevent API rate limiting

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	input := map[string]any{
		"title":   fmt.Sprintf("[TEST] %s %d", title, time.Now().UnixMilli()),
		"content": content,
		"issueId": issueID,
	}

	doc, err := apiClient.CreateDocument(ctx, input)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create test document: %w", err)
	}

	// Upsert to SQLite so document is immediately visible in filesystem
	if lfs != nil {
		if err := lfs.UpsertDocument(ctx, *doc); err != nil {
			// Log warning but don't fail - sync worker will pick it up eventually
			fmt.Printf("Warning: failed to upsert document to SQLite: %v\n", err)
		}
	}

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = apiClient.DeleteDocument(ctx, doc.ID)
	}

	return doc, cleanup, nil
}

// getTeamCycles fetches cycles for the test team
func getTeamCycles() ([]api.Cycle, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return apiClient.GetTeamCycles(ctx, testTeamID)
}

// findFirstActiveCycle finds the first active (current or future) cycle for testing
func findFirstActiveCycle() (*api.Cycle, error) {
	cycles, err := getTeamCycles()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	for _, c := range cycles {
		// Check if cycle is current or future
		if c.EndsAt.After(now) {
			return &c, nil
		}
	}

	// If no active cycles, return the most recent one
	if len(cycles) > 0 {
		return &cycles[0], nil
	}

	return nil, fmt.Errorf("no cycles found")
}

// WithCycleID sets the cycle for a test issue
func WithCycleID(cycleID string) IssueOption {
	return func(input map[string]any) {
		input["cycleId"] = cycleID
	}
}

// deleteTestIssue archives/cancels a test issue (Linear doesn't have hard delete)
func deleteTestIssue(issueID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find canceled state for the team
	states, err := apiClient.GetTeamStates(ctx, testTeamID)
	if err != nil {
		return err
	}

	var canceledID string
	for _, s := range states {
		if s.Type == "canceled" {
			canceledID = s.ID
			break
		}
	}

	if canceledID != "" {
		return apiClient.UpdateIssue(ctx, issueID, map[string]any{"stateId": canceledID})
	}

	return nil
}
