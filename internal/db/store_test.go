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

// TestConnectionPragmas asserts the DSN-level pragmas reach every pooled
// connection. busy_timeout in particular is per-connection: configured via a
// one-off Exec it covered only one pooled conn, and a delete's SQLite forget
// racing the sync worker failed instantly with SQLITE_BUSY, leaving a phantom
// row that resurrected the deleted file.
func TestConnectionPragmas(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	var busy int
	if err := store.DB().QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busy < 1000 {
		t.Errorf("busy_timeout = %d, want >= 1000ms so writes wait out the sync worker instead of failing SQLITE_BUSY", busy)
	}
	var fk int
	if err := store.DB().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
	var mode string
	if err := store.DB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
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

	// List all
	teams, err := store.Queries().ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}

	if len(teams) != 1 {
		t.Errorf("Expected 1 team, got %d", len(teams))
	}
	if teams[0].Name != team.Name {
		t.Errorf("Name mismatch: got %s, want %s", teams[0].Name, team.Name)
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

// TestMigrateAddsDetailSyncedAt: the bootstrap-ALTER migration. A database
// created BEFORE issues.detail_synced_at existed must open cleanly (CREATE
// TABLE IF NOT EXISTS leaves the old table untouched, so Open's migrateSchema
// has to ALTER it in), gain the column, and keep its rows readable — including
// through sqlc's explicit-column scans, which expect the new column. Also
// proves idempotence: reopening the migrated database must not fail on a
// duplicate ALTER.
func TestMigrateAddsDetailSyncedAt(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "old.db")

	// Build the pre-migration database by hand: the issues table WITHOUT
	// detail_synced_at, plus one row.
	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE issues (
		id TEXT PRIMARY KEY,
		identifier TEXT UNIQUE NOT NULL,
		team_id TEXT NOT NULL,
		title TEXT NOT NULL,
		description TEXT,
		state_id TEXT,
		state_name TEXT,
		state_type TEXT,
		assignee_id TEXT,
		assignee_email TEXT,
		creator_id TEXT,
		creator_email TEXT,
		priority INTEGER DEFAULT 0,
		project_id TEXT,
		project_name TEXT,
		cycle_id TEXT,
		cycle_name TEXT,
		parent_id TEXT,
		due_date TEXT,
		estimate REAL,
		url TEXT,
		branch_name TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		started_at DATETIME,
		completed_at DATETIME,
		canceled_at DATETIME,
		archived_at DATETIME,
		synced_at DATETIME NOT NULL,
		data JSON NOT NULL
	)`); err != nil {
		t.Fatalf("create old issues table: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO issues (id, identifier, team_id, title, created_at, updated_at, synced_at, data)
		VALUES ('issue-old', 'TST-OLD', 'team-1', 'Pre-migration issue', ?, ?, ?, ?)`,
		Now(), Now(), Now(), []byte("{}")); err != nil {
		t.Fatalf("insert old row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	// Open through the Store — schema init no-ops on the existing table,
	// migrateSchema must add the column.
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open on pre-migration db failed: %v", err)
	}
	defer store.Close()

	has, err := tableHasColumn(store.DB(), "issues", "detail_synced_at")
	if err != nil {
		t.Fatalf("tableHasColumn: %v", err)
	}
	if !has {
		t.Fatal("issues.detail_synced_at missing after Open — migration did not run")
	}

	ctx := context.Background()

	// The pre-existing row reads back with a NULL stamp. Note the ALTER
	// appends the column at the END of the table (schema.sql declares it
	// before data) — this exercises the explicit-column scans that make the
	// two layouts equivalent.
	got, err := store.Queries().GetIssueByID(ctx, "issue-old")
	if err != nil {
		t.Fatalf("GetIssueByID on migrated db: %v", err)
	}
	if got.DetailSyncedAt.Valid {
		t.Errorf("pre-migration row detail_synced_at = %v, want NULL", got.DetailSyncedAt.Time)
	}
	if got.Title != "Pre-migration issue" {
		t.Errorf("pre-migration row title = %q — column scan misaligned", got.Title)
	}

	// The stamp works on the migrated table.
	if err := store.Queries().StampIssueDetailSynced(ctx, StampIssueDetailSyncedParams{
		DetailSyncedAt: ToNullTime(Now()), ID: "issue-old",
	}); err != nil {
		t.Fatalf("StampIssueDetailSynced on migrated db: %v", err)
	}
	fresh, err := store.Queries().GetIssueDetailFreshness(ctx, "issue-old")
	if err != nil {
		t.Fatalf("GetIssueDetailFreshness: %v", err)
	}
	if !fresh.DetailSyncedAt.Valid {
		t.Error("detail_synced_at still NULL after stamp on migrated db")
	}
	store.Close()

	// Idempotent: reopening the already-migrated database succeeds.
	again, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen migrated db failed: %v", err)
	}
	again.Close()
}

// TestUpsertIssuePreservesDetailSyncedAt: UpsertIssue deliberately omits
// detail_synced_at from its INSERT list and conflict SET clause — the stamp is
// owned by the detail-sync paths, so an entity sync upsert must neither set it
// (fresh insert stays NULL) nor clobber it (conflict update preserves it).
func TestUpsertIssuePreservesDetailSyncedAt(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	data := &IssueData{
		ID: "issue-1", Identifier: "TST-1", Title: "Issue", TeamID: "team-1",
		CreatedAt: Now(), UpdatedAt: Now(), Data: []byte("{}"),
	}
	if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
		t.Fatalf("insert: %v", err)
	}
	fresh, err := store.Queries().GetIssueDetailFreshness(ctx, "issue-1")
	if err != nil {
		t.Fatalf("GetIssueDetailFreshness: %v", err)
	}
	if fresh.DetailSyncedAt.Valid {
		t.Errorf("detail_synced_at = %v after insert, want NULL", fresh.DetailSyncedAt.Time)
	}

	stamp := Now()
	if err := store.Queries().StampIssueDetailSynced(ctx, StampIssueDetailSyncedParams{
		DetailSyncedAt: ToNullTime(stamp), ID: "issue-1",
	}); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	// A subsequent entity upsert (the sync worker refreshing the issue row)
	// must preserve the stamp.
	data.Title = "Issue updated"
	if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
		t.Fatalf("conflict upsert: %v", err)
	}
	fresh, err = store.Queries().GetIssueDetailFreshness(ctx, "issue-1")
	if err != nil {
		t.Fatalf("GetIssueDetailFreshness after upsert: %v", err)
	}
	if !fresh.DetailSyncedAt.Valid {
		t.Fatal("detail_synced_at cleared by UpsertIssue — the upsert must preserve the stamp")
	}
	if !fresh.DetailSyncedAt.Time.Equal(stamp) {
		t.Errorf("detail_synced_at = %v after upsert, want preserved %v", fresh.DetailSyncedAt.Time, stamp)
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
