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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestWithTransaction(t *testing.T) {
	t.Parallel()
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

func TestListTeamIssuesByAssignee(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	assigneeID := "user-1"
	assigneeEmail := "user@example.com"

	// Insert issues with different assignees
	for i, aid := range []string{assigneeID, assigneeID, "user-2"} {
		aEmail := "user" + string(rune('1'+i)) + "@example.com"
		if aid == assigneeID {
			aEmail = assigneeEmail
		}
		data := &IssueData{
			ID:            "issue-" + string(rune('a'+i)),
			Identifier:    "TST-" + string(rune('1'+i)),
			Title:         "Issue " + string(rune('1'+i)),
			TeamID:        teamID,
			AssigneeID:    &aid,
			AssigneeEmail: &aEmail,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
			Data:          json.RawMessage("{}"),
		}
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// Query by assignee ID
	issues, err := store.Queries().ListTeamIssuesByAssignee(ctx, ListTeamIssuesByAssigneeParams{
		TeamID:     teamID,
		AssigneeID: toNullString(&assigneeID),
	})
	if err != nil {
		t.Fatalf("ListTeamIssuesByAssignee failed: %v", err)
	}
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues for assignee, got %d", len(issues))
	}

	// Query by assignee email
	issuesByEmail, err := store.Queries().ListTeamIssuesByAssigneeEmail(ctx, ListTeamIssuesByAssigneeEmailParams{
		TeamID:        teamID,
		AssigneeEmail: toNullString(&assigneeEmail),
	})
	if err != nil {
		t.Fatalf("ListTeamIssuesByAssigneeEmail failed: %v", err)
	}
	if len(issuesByEmail) != 2 {
		t.Errorf("Expected 2 issues for assignee email, got %d", len(issuesByEmail))
	}
}

func TestListTeamUnassignedIssues(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	assigneeID := "user-1"

	// Insert issues: 2 unassigned, 1 assigned
	for i := 0; i < 3; i++ {
		data := &IssueData{
			ID:         "issue-" + string(rune('a'+i)),
			Identifier: "TST-" + string(rune('1'+i)),
			Title:      "Issue " + string(rune('1'+i)),
			TeamID:     teamID,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Data:       json.RawMessage("{}"),
		}
		if i == 2 {
			data.AssigneeID = &assigneeID
		}
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// Query unassigned issues
	issues, err := store.Queries().ListTeamUnassignedIssues(ctx, teamID)
	if err != nil {
		t.Fatalf("ListTeamUnassignedIssues failed: %v", err)
	}
	if len(issues) != 2 {
		t.Errorf("Expected 2 unassigned issues, got %d", len(issues))
	}
}

func TestListTeamIssuesByPriority(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"

	// Insert issues with different priorities
	priorities := []int{1, 2, 2, 3, 0} // 1=urgent, 2=high, 3=medium, 0=none
	for i, p := range priorities {
		data := &IssueData{
			ID:         "issue-" + string(rune('a'+i)),
			Identifier: "TST-" + string(rune('1'+i)),
			Title:      "Issue " + string(rune('1'+i)),
			TeamID:     teamID,
			Priority:   p,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Data:       json.RawMessage("{}"),
		}
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// Query by priority 2 (high)
	issues, err := store.Queries().ListTeamIssuesByPriority(ctx, ListTeamIssuesByPriorityParams{
		TeamID:   teamID,
		Priority: toNullInt64(2),
	})
	if err != nil {
		t.Fatalf("ListTeamIssuesByPriority failed: %v", err)
	}
	if len(issues) != 2 {
		t.Errorf("Expected 2 high priority issues, got %d", len(issues))
	}
}

func TestListIssuesByParent(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"
	parentID := "parent-issue"

	// Insert parent issue
	parentData := &IssueData{
		ID:         parentID,
		Identifier: "TST-PARENT",
		Title:      "Parent Issue",
		TeamID:     teamID,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Data:       json.RawMessage("{}"),
	}
	if err := store.Queries().UpsertIssue(ctx, parentData.ToUpsertParams()); err != nil {
		t.Fatalf("Insert parent failed: %v", err)
	}

	// Insert child issues
	for i := 0; i < 3; i++ {
		data := &IssueData{
			ID:         "child-" + string(rune('a'+i)),
			Identifier: "TST-CHILD-" + string(rune('1'+i)),
			Title:      "Child " + string(rune('1'+i)),
			TeamID:     teamID,
			ParentID:   &parentID,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Data:       json.RawMessage("{}"),
		}
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("Insert child failed: %v", err)
		}
	}

	// Query by parent
	children, err := store.Queries().ListTeamIssuesByParent(ctx, toNullString(&parentID))
	if err != nil {
		t.Fatalf("ListTeamIssuesByParent failed: %v", err)
	}
	if len(children) != 3 {
		t.Errorf("Expected 3 children, got %d", len(children))
	}
}

func TestAPIIssueConversion(t *testing.T) {
	t.Parallel()
	issue := api.Issue{
		ID:          "test-id",
		Identifier:  "TST-123",
		Title:       "Test Issue",
		Description: "A description",
		Priority:    2,
		State: api.State{
			ID:   "state-1",
			Name: "Todo",
			Type: "unstarted",
		},
		Assignee: &api.User{
			ID:    "user-1",
			Email: "test@example.com",
			Name:  "Test User",
		},
		Team: &api.Team{
			ID:  "team-1",
			Key: "TST",
		},
		Project: &api.Project{
			ID:   "project-1",
			Name: "Test Project",
		},
		Cycle: &api.IssueCycle{
			ID:   "cycle-1",
			Name: "Sprint 1",
		},
		Parent: &api.ParentIssue{
			ID:         "parent-1",
			Identifier: "TST-100",
		},
		DueDate:   strPtr("2025-01-15"),
		Estimate:  float64Ptr(3.0),
		URL:       "https://linear.app/test",
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
	}

	// Convert to DB
	data, err := APIIssueToDBIssue(issue)
	if err != nil {
		t.Fatalf("APIIssueToDBIssue failed: %v", err)
	}

	// Verify fields
	if data.ID != issue.ID {
		t.Errorf("ID mismatch")
	}
	if data.TeamID != issue.Team.ID {
		t.Errorf("TeamID mismatch")
	}
	if *data.StateID != issue.State.ID {
		t.Errorf("StateID mismatch")
	}
	if *data.AssigneeID != issue.Assignee.ID {
		t.Errorf("AssigneeID mismatch")
	}
	if *data.ProjectID != issue.Project.ID {
		t.Errorf("ProjectID mismatch")
	}
	if *data.CycleID != issue.Cycle.ID {
		t.Errorf("CycleID mismatch")
	}
	if *data.ParentID != issue.Parent.ID {
		t.Errorf("ParentID mismatch")
	}
	if *data.DueDate != *issue.DueDate {
		t.Errorf("DueDate mismatch")
	}
	if *data.Estimate != *issue.Estimate {
		t.Errorf("Estimate mismatch")
	}
}

func TestAPIIssueConversionWithNils(t *testing.T) {
	t.Parallel()
	// Issue with minimal fields (nil optionals)
	issue := api.Issue{
		ID:         "test-id",
		Identifier: "TST-123",
		Title:      "Minimal Issue",
		Team:       &api.Team{ID: "team-1"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	data, err := APIIssueToDBIssue(issue)
	if err != nil {
		t.Fatalf("APIIssueToDBIssue failed: %v", err)
	}

	// Nil fields should remain nil
	if data.AssigneeID != nil {
		t.Error("AssigneeID should be nil")
	}
	if data.ProjectID != nil {
		t.Error("ProjectID should be nil")
	}
	if data.CycleID != nil {
		t.Error("CycleID should be nil")
	}
	if data.ParentID != nil {
		t.Error("ParentID should be nil")
	}
	if data.DueDate != nil {
		t.Error("DueDate should be nil")
	}
}

func TestDBIssuesToAPIIssues(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"

	// Insert issues
	for i := 0; i < 3; i++ {
		issue := api.Issue{
			ID:         "issue-" + string(rune('a'+i)),
			Identifier: "TST-" + string(rune('1'+i)),
			Title:      "Issue " + string(rune('1'+i)),
			Team:       &api.Team{ID: teamID},
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		data, _ := APIIssueToDBIssue(issue)
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("UpsertIssue failed: %v", err)
		}
	}

	// List and convert back
	dbIssues, err := store.Queries().ListTeamIssues(ctx, teamID)
	if err != nil {
		t.Fatalf("ListTeamIssues failed: %v", err)
	}

	apiIssues, err := DBIssuesToAPIIssues(dbIssues)
	if err != nil {
		t.Fatalf("DBIssuesToAPIIssues failed: %v", err)
	}

	if len(apiIssues) != 3 {
		t.Errorf("Expected 3 API issues, got %d", len(apiIssues))
	}

	for _, issue := range apiIssues {
		if issue.ID == "" {
			t.Error("Converted issue has empty ID")
		}
		if issue.Identifier == "" {
			t.Error("Converted issue has empty Identifier")
		}
	}
}

func TestAPITeamConversion(t *testing.T) {
	t.Parallel()
	team := api.Team{
		ID:        "team-1",
		Key:       "TST",
		Name:      "Test Team",
		Icon:      "icon",
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
	}

	params := APITeamToDBTeam(team)

	if params.ID != team.ID {
		t.Errorf("ID mismatch")
	}
	if params.Key != team.Key {
		t.Errorf("Key mismatch")
	}
	if params.Icon.String != team.Icon {
		t.Errorf("Icon mismatch")
	}
}

func TestDefaultDBPath(t *testing.T) {
	t.Parallel()
	path := DefaultDBPath()
	if path == "" {
		t.Error("DefaultDBPath should not be empty")
	}
	if !filepath.IsAbs(path) {
		t.Error("DefaultDBPath should be absolute")
	}
}

func TestDeleteIssue(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Insert an issue
	data := &IssueData{
		ID:         "to-delete",
		Identifier: "TST-DEL",
		Title:      "To Delete",
		TeamID:     "team-1",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Data:       json.RawMessage("{}"),
	}
	if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Verify it exists
	_, err := store.Queries().GetIssueByIdentifier(ctx, "TST-DEL")
	if err != nil {
		t.Fatalf("Issue should exist: %v", err)
	}

	// Delete by identifier
	if err := store.Queries().DeleteIssueByIdentifier(ctx, "TST-DEL"); err != nil {
		t.Fatalf("DeleteIssueByIdentifier failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetIssueByIdentifier(ctx, "TST-DEL")
	if err == nil {
		t.Error("Issue should be deleted")
	}
}

func TestGetTeamIssueCount(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-1"

	// Initially empty
	count, err := store.Queries().GetTeamIssueCount(ctx, teamID)
	if err != nil {
		t.Fatalf("GetTeamIssueCount failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 issues initially, got %d", count)
	}

	// Insert some issues
	for i := 0; i < 5; i++ {
		data := &IssueData{
			ID:         "issue-" + string(rune('a'+i)),
			Identifier: "TST-" + string(rune('1'+i)),
			Title:      "Issue",
			TeamID:     teamID,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Data:       json.RawMessage("{}"),
		}
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("UpsertIssue failed: %v", err)
		}
	}

	// Check count
	count, _ = store.Queries().GetTeamIssueCount(ctx, teamID)
	if count != 5 {
		t.Errorf("Expected 5 issues, got %d", count)
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

func strPtr(s string) *string {
	return &s
}

func float64Ptr(f float64) *float64 {
	return &f
}

func toNullInt64(i int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(i), Valid: true}
}
