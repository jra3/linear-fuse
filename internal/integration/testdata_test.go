package integration

import (
	"context"
	"fmt"
	"os"
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

// writeTestsEnabled returns true if write tests should run
// Set LINEARFS_WRITE_TESTS=1 to enable tests that create/modify issues
func writeTestsEnabled() bool {
	return os.Getenv("LINEARFS_WRITE_TESTS") == "1"
}

// skipIfNoWriteTests skips the test if write tests are not enabled
func skipIfNoWriteTests(t interface{ Skip(...any) }) {
	if !writeTestsEnabled() {
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

// createTestIssue creates an issue via API for testing.
// The title is prefixed with [TEST] and a timestamp.
// Returns the issue and a cleanup function (currently no-op since Linear doesn't have delete).
func createTestIssue(title string, opts ...IssueOption) (*TestIssue, func(), error) {
	rateLimitWait() // Prevent API rate limiting

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	input := map[string]any{
		"teamId": testTeamID,
		"title":  fmt.Sprintf("[TEST] %s %d", title, time.Now().UnixMilli()),
	}

	for _, opt := range opts {
		opt(input)
	}

	issue, err := apiClient.CreateIssue(ctx, input)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create test issue: %w", err)
	}

	// Cleanup function - Linear doesn't have delete, so we just log
	// In a real scenario, we could archive or cancel the issue
	cleanup := func() {
		// No-op for now - test issues stay in workspace with [TEST] prefix
	}

	return &TestIssue{Issue: issue}, cleanup, nil
}

// createTestComment creates a comment on an issue for testing.
func createTestComment(issueID, body string) (*api.Comment, func(), error) {
	rateLimitWait() // Prevent API rate limiting

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	comment, err := apiClient.CreateComment(ctx, issueID, fmt.Sprintf("[TEST] %s", body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create test comment: %w", err)
	}

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = apiClient.DeleteComment(ctx, comment.ID)
	}

	return comment, cleanup, nil
}

// getTestIssue fetches an issue by ID via API (for verification)
func getTestIssue(issueID string) (*api.Issue, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return apiClient.GetIssue(ctx, issueID)
}

// updateTestIssue updates an issue via API
func updateTestIssue(issueID string, updates map[string]any) error {
	rateLimitWait() // Prevent API rate limiting

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return apiClient.UpdateIssue(ctx, issueID, updates)
}

// getTeamStates fetches workflow states for the test team
func getTeamStates() ([]api.State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return apiClient.GetTeamStates(ctx, testTeamID)
}

// findStateByName finds a state by name (case-insensitive)
func findStateByName(name string) (*api.State, error) {
	states, err := getTeamStates()
	if err != nil {
		return nil, err
	}

	for _, s := range states {
		if s.Name == name {
			return &s, nil
		}
	}

	return nil, fmt.Errorf("state %q not found", name)
}

// getUsers fetches all users
func getUsers() ([]api.User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return apiClient.GetUsers(ctx)
}

// findFirstActiveUser finds the first active user for testing
func findFirstActiveUser() (*api.User, error) {
	users, err := getUsers()
	if err != nil {
		return nil, err
	}

	for _, u := range users {
		if u.Active {
			return &u, nil
		}
	}

	return nil, fmt.Errorf("no active users found")
}

// updateTestIssueTitle updates just the title of an issue
func updateTestIssueTitle(issueID, newTitle string) error {
	return updateTestIssue(issueID, map[string]any{"title": newTitle})
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

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = apiClient.DeleteDocument(ctx, doc.ID)
	}

	return doc, cleanup, nil
}

// createTestProjectDocument creates a document attached to a project for testing.
func createTestProjectDocument(projectID, title, content string) (*api.Document, func(), error) {
	rateLimitWait() // Prevent API rate limiting

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	input := map[string]any{
		"title":     fmt.Sprintf("[TEST] %s %d", title, time.Now().UnixMilli()),
		"content":   content,
		"projectId": projectID,
	}

	doc, err := apiClient.CreateDocument(ctx, input)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create test document: %w", err)
	}

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = apiClient.DeleteDocument(ctx, doc.ID)
	}

	return doc, cleanup, nil
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
