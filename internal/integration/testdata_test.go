package integration

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// Rate limiting for API operations to avoid hitting Linear's usage limits
var (
	rateLimitMu  sync.Mutex
	lastAPICall  time.Time
	apiCallDelay = 1 * time.Second // Minimum delay between API write operations
)

// The mode interlocks (skipIfNoWriteTests / skipIfLiveAPI) live in
// modes_test.go, alongside the workspace-derived identifier pickers.

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

// mkdirIssueRetries bounds the EAGAIN retry loop in createTestIssue, and
// mkdirIssueBackoff is the wait before the first retry (doubled each time).
// Sized against what the create path actually asks for: a rate-limited create
// returns EAGAIN with "wait a few seconds and retry", and the live write suite
// creates ~35 issues in four minutes, so it generates that backpressure itself.
const (
	mkdirIssueRetries = 4
	mkdirIssueBackoff = 2 * time.Second
)

// mkdirIssueWithRetry creates the issue directory, honoring the retry contract
// the mount documents: EAGAIN means "the create did not take effect, wait and
// retry" (#131/#257), so treating it as fatal — which is what this helper used
// to do — makes every live run flaky in proportion to the rate-limit pressure
// the run itself generates (#399). Every other errno is a real failure and is
// returned immediately.
//
// Caveat worth knowing when a retry does fire: EAGAIN is also what a create
// whose POST was cancelled in flight returns, and in that case whether Linear
// processed it is genuinely unknown — a retry can leave a duplicate behind. The
// suite accepts that over a guaranteed flake; the underlying ambiguity is the
// open question in #399, and it lives in the create path, not here.
func mkdirIssueWithRetry(issuePath string) error {
	backoff := mkdirIssueBackoff
	var err error
	for attempt := 0; attempt <= mkdirIssueRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(backoff)
			backoff *= 2
			rateLimitWait()
		}
		err = os.Mkdir(issuePath, 0755)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		log.Printf("createTestIssue: mkdir %q returned EAGAIN (attempt %d/%d), retrying — "+
			"this is the mount's documented transient-create signal, not a failure",
			filepath.Base(issuePath), attempt+1, mkdirIssueRetries+1)
	}
	return fmt.Errorf("still EAGAIN after %d attempts: %w", mkdirIssueRetries+1, err)
}

// issueOptionInput folds the caller's options into an IssueUpdateInput. The
// IssueOption constructors already write Linear's own field names
// (description, dueDate, stateId, cycleId, …), so the map goes to
// UpdateIssue untranslated.
func issueOptionInput(opts []IssueOption) map[string]any {
	input := make(map[string]any, len(opts))
	for _, opt := range opts {
		opt(input)
	}
	return input
}

// configureTestIssue applies the fields mkdir cannot carry, and returns the
// issue as the server now holds it.
//
// Every createTestIssue caller is behind skipIfNoWriteTests, so this only ever
// runs live and apiClient is the real client — in fixture mode it would reach
// offlineAPIServer and fail by design.
func configureTestIssue(id, identifier string, input map[string]any) (*api.Issue, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rateLimitWait()
	if err := apiClient.UpdateIssue(ctx, id, input); err != nil {
		return nil, fmt.Errorf("apply test-issue options %v to %s: %w", keys, identifier, err)
	}

	// The mutation went straight to Linear, so the mount still serves the
	// pre-update issue out of SQLite. Refetch and upsert, the same thing a
	// write through the mount would have done in its flush handler.
	rateLimitWait()
	fresh, err := apiClient.GetIssue(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("refetch %s after applying %v: %w", identifier, keys, err)
	}
	if err := lfs.UpsertIssue(ctx, *fresh); err != nil {
		return nil, fmt.Errorf("upsert %s after applying %v: %w", identifier, keys, err)
	}

	// The identifier search above read issue.md, which primes the kernel page
	// cache with the pre-update body — a later read would otherwise be served
	// the stale bytes rather than the fields we just set. Stat for the inode
	// the kernel knows this file by and drop it. Best-effort: a miss here
	// costs freshness, not correctness, and must not fail setup.
	if fi, err := os.Stat(issueFilePath(testTeamKey, identifier)); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			lfs.InvalidateUpdated(st.Ino)
		}
	}

	return fresh, nil
}

// createTestIssue creates an issue via filesystem mkdir for testing.
// The title is prefixed with [TEST] and a timestamp.
// Returns the issue and a cleanup function (currently no-op since Linear doesn't have delete).
//
// mkdir carries the title and nothing else, so any IssueOption the caller
// passes is applied afterward by configureTestIssue, and the returned issue is
// the server's post-update copy rather than a locally-built stub.
func createTestIssue(title string, opts ...IssueOption) (*TestIssue, func(), error) {
	rateLimitWait() // Prevent API rate limiting

	fullTitle := fmt.Sprintf("[TEST] %s %d", title, time.Now().UnixMilli())
	issuePath := fmt.Sprintf("%s/teams/%s/issues/%s", mountPoint, testTeamKey, fullTitle)

	// Create issue via filesystem mkdir - this goes through FUSE and syncs to SQLite
	if err := mkdirIssueWithRetry(issuePath); err != nil {
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
			identifier := entry.Name()

			// The id lives in issue.meta, not issue.md: #148 moved the volatile
			// server fields out of the editable file. Reading it from issue.md
			// and swallowing the miss with `_` is what handed every live test an
			// api.Issue{ID: ""}, which then failed 14 tests as "not found in
			// SQLite" and "Argument Validation Error" (#396). Hence: read the
			// sidecar, and fail loudly on a miss.
			metaContent, err := os.ReadFile(issueMetaPath(testTeamKey, identifier))
			if err != nil {
				return nil, nil, fmt.Errorf("read issue.meta for %s: %w", identifier, err)
			}
			meta, err := parseFrontmatter(metaContent)
			if err != nil {
				return nil, nil, fmt.Errorf("parse issue.meta frontmatter for %s: %w", identifier, err)
			}
			id, ok := meta.Frontmatter["id"].(string)
			if !ok || id == "" {
				return nil, nil, fmt.Errorf("issue.meta for %s carries no id; every id-keyed "+
					"assertion downstream would fail as 'not found'. Frontmatter: %v",
					identifier, meta.Frontmatter)
			}

			issue := &api.Issue{
				ID:         id,
				Identifier: identifier,
				Title:      fullTitle,
				Team:       &api.Team{Key: testTeamKey, ID: testTeamID},
			}

			// mkdir carries only the title, so every option the caller passed
			// still has to be applied. Skipping this is what made 7 tests set
			// themselves up wrong in silence — clearing a due date that was
			// never set, editing an empty description, removing a cycle
			// membership that never existed (#405).
			if input := issueOptionInput(opts); len(input) > 0 {
				fresh, err := configureTestIssue(id, identifier, input)
				if err != nil {
					return nil, nil, err
				}
				issue = fresh
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
		ID:         dbIssue.ID,
		Identifier: dbIssue.Identifier,
		Title:      dbIssue.Title,
		Priority:   int(dbIssue.Priority.Int64),
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

// getTeamStates fetches workflow states for the test team (via the combined
// metadata query — the per-collection client read methods were deleted with
// the drain audit; the drained GetTeamMetadata is the production path).
func getTeamStates() ([]api.State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	md, err := apiClient.GetTeamMetadata(ctx, testTeamID)
	if err != nil {
		return nil, err
	}
	return md.States, nil
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

// getTeamCycles fetches cycles for the test team (see getTeamStates for why
// this goes through the combined metadata query)
func getTeamCycles() ([]api.Cycle, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	md, err := apiClient.GetTeamMetadata(ctx, testTeamID)
	if err != nil {
		return nil, err
	}
	return md.Cycles, nil
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
	md, err := apiClient.GetTeamMetadata(ctx, testTeamID)
	if err != nil {
		return err
	}
	states := md.States

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
