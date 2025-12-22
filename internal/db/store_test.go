package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestOpenAndClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Verify file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestUpsertAndGetIssue(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Create test issue
	issue := api.Issue{
		ID:          "issue-123",
		Identifier:  "TST-1",
		Title:       "Test Issue",
		Description: "This is a test",
		Priority:    2,
		State: api.State{
			ID:   "state-1",
			Name: "Todo",
			Type: "unstarted",
		},
		Assignee: &api.User{
			ID:    "user-1",
			Email: "test@example.com",
		},
		Team: &api.Team{
			ID:  "team-1",
			Key: "TST",
		},
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
		URL:       "https://linear.app/test/issue/TST-1",
	}

	// Convert and upsert
	data, err := APIIssueToDBIssue(issue)
	if err != nil {
		t.Fatalf("APIIssueToDBIssue failed: %v", err)
	}

	err = store.Queries().UpsertIssue(ctx, data.ToUpsertParams())
	if err != nil {
		t.Fatalf("UpsertIssue failed: %v", err)
	}

	// Get by identifier
	got, err := store.Queries().GetIssueByIdentifier(ctx, "TST-1")
	if err != nil {
		t.Fatalf("GetIssueByIdentifier failed: %v", err)
	}

	if got.ID != issue.ID {
		t.Errorf("ID mismatch: got %s, want %s", got.ID, issue.ID)
	}
	if got.Title != issue.Title {
		t.Errorf("Title mismatch: got %s, want %s", got.Title, issue.Title)
	}

	// Convert back to api.Issue
	apiIssue, err := DBIssueToAPIIssue(got)
	if err != nil {
		t.Fatalf("DBIssueToAPIIssue failed: %v", err)
	}

	if apiIssue.ID != issue.ID {
		t.Errorf("Converted ID mismatch: got %s, want %s", apiIssue.ID, issue.ID)
	}
	if apiIssue.Assignee == nil || apiIssue.Assignee.Email != issue.Assignee.Email {
		t.Error("Assignee not properly preserved in JSON")
	}
}

func TestListTeamIssues(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Insert multiple issues
	teamID := "team-1"
	for i := 1; i <= 5; i++ {
		issue := api.Issue{
			ID:         "issue-" + string(rune('a'+i)),
			Identifier: "TST-" + string(rune('0'+i)),
			Title:      "Test Issue " + string(rune('0'+i)),
			Priority:   i % 4,
			State: api.State{
				ID:   "state-1",
				Name: "Todo",
				Type: "unstarted",
			},
			Team:      &api.Team{ID: teamID, Key: "TST"},
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour),
			UpdatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		}

		data, _ := APIIssueToDBIssue(issue)
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("Insert issue %d failed: %v", i, err)
		}
	}

	// List all team issues
	issues, err := store.Queries().ListTeamIssues(ctx, teamID)
	if err != nil {
		t.Fatalf("ListTeamIssues failed: %v", err)
	}

	if len(issues) != 5 {
		t.Errorf("Expected 5 issues, got %d", len(issues))
	}

	// Verify ordering by updated_at DESC
	for i := 1; i < len(issues); i++ {
		if issues[i-1].UpdatedAt.Before(issues[i].UpdatedAt) {
			t.Error("Issues not ordered by updated_at DESC")
			break
		}
	}
}

func TestListTeamIssuesByState(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	stateID := "state-todo"

	// Insert issues with different states
	states := []struct {
		id   string
		name string
	}{
		{stateID, "Todo"},
		{stateID, "Todo"},
		{"state-done", "Done"},
	}

	for i, s := range states {
		data := &IssueData{
			ID:         "issue-" + string(rune('a'+i)),
			Identifier: "TST-" + string(rune('0'+i)),
			Title:      "Issue " + string(rune('0'+i)),
			TeamID:     teamID,
			StateID:    &s.id,
			StateName:  &s.name,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Data:       json.RawMessage("{}"),
		}
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// Query by state ID
	issues, err := store.Queries().ListTeamIssuesByState(ctx, ListTeamIssuesByStateParams{
		TeamID:  teamID,
		StateID: toNullString(&stateID),
	})
	if err != nil {
		t.Fatalf("ListTeamIssuesByState failed: %v", err)
	}

	if len(issues) != 2 {
		t.Errorf("Expected 2 issues in Todo state, got %d", len(issues))
	}
}

func TestSyncMeta(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	now := time.Now()

	// Upsert sync meta
	err := store.Queries().UpsertSyncMeta(ctx, UpsertSyncMetaParams{
		TeamID:       teamID,
		LastSyncedAt: now,
		IssueCount:   sql.NullInt64{Int64: 100, Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertSyncMeta failed: %v", err)
	}

	// Get sync meta
	meta, err := store.Queries().GetSyncMeta(ctx, teamID)
	if err != nil {
		t.Fatalf("GetSyncMeta failed: %v", err)
	}

	if meta.IssueCount.Int64 != 100 {
		t.Errorf("Expected issue count 100, got %d", meta.IssueCount.Int64)
	}
}

func TestTeams(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	team := api.Team{
		ID:        "team-1",
		Key:       "TST",
		Name:      "Test Team",
		Icon:      "icon",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Upsert team
	err := store.Queries().UpsertTeam(ctx, APITeamToDBTeam(team))
	if err != nil {
		t.Fatalf("UpsertTeam failed: %v", err)
	}

	// Get by key
	got, err := store.Queries().GetTeamByKey(ctx, "TST")
	if err != nil {
		t.Fatalf("GetTeamByKey failed: %v", err)
	}

	if got.Name != team.Name {
		t.Errorf("Name mismatch: got %s, want %s", got.Name, team.Name)
	}

	// List all
	teams, err := store.Queries().ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}

	if len(teams) != 1 {
		t.Errorf("Expected 1 team, got %d", len(teams))
	}
}

func TestSearchIssues(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Insert issues with searchable content
	issues := []struct {
		id    string
		title string
		desc  string
	}{
		{"1", "Fix authentication bug", "Users cannot login"},
		{"2", "Add dark mode", "Support for dark theme"},
		{"3", "Login page redesign", "New login page design"},
	}

	teamID := "team-1"
	for i, iss := range issues {
		data := &IssueData{
			ID:          iss.id,
			Identifier:  "TST-" + string(rune('1'+i)),
			Title:       iss.title,
			Description: &iss.desc,
			TeamID:      teamID,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
			Data:        json.RawMessage("{}"),
		}
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// Search for "login"
	results, err := store.SearchIssues(ctx, "login")
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	// Should find issues mentioning login
	if len(results) < 2 {
		t.Errorf("Expected at least 2 results for 'login', got %d", len(results))
	}
}

func TestWithTransaction(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"

	// Successful transaction
	err := store.WithTx(ctx, func(q *Queries) error {
		data := &IssueData{
			ID:         "tx-issue-1",
			Identifier: "TST-TX1",
			Title:      "Transaction Test",
			TeamID:     teamID,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Data:       json.RawMessage("{}"),
		}
		return q.UpsertIssue(ctx, data.ToUpsertParams())
	})
	if err != nil {
		t.Fatalf("Transaction failed: %v", err)
	}

	// Verify commit
	_, err = store.Queries().GetIssueByIdentifier(ctx, "TST-TX1")
	if err != nil {
		t.Error("Issue not found after commit")
	}
}

// Helpers

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	return store
}
