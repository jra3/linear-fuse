package fs

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
)

func TestSQLiteFilteredQueries(t *testing.T) {
	// Create a LinearFS with SQLite enabled
	cfg := &config.Config{
		APIKey: "test-key",
		Cache: config.CacheConfig{
			TTL:        100 * time.Millisecond,
			MaxEntries: 100,
		},
	}

	lfs, err := NewLinearFS(cfg, true)
	if err != nil {
		t.Fatalf("NewLinearFS failed: %v", err)
	}
	defer lfs.Close()

	// Enable SQLite with temp database
	dbPath := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()

	// Open store directly for test setup
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}

	// Inject store directly (bypassing sync worker start)
	lfs.store = store

	// Setup: Add test issues to database
	now := time.Now()
	// Data column contains full issue JSON (mirrors api.Issue structure)
	testIssues := []db.IssueData{
		{
			ID:         "issue-1",
			Identifier: "TST-1",
			TeamID:     "team-1",
			Title:      "Issue 1 - High Priority",
			StateName:  strPtr("In Progress"),
			StateType:  strPtr("started"),
			Priority:   4, // Urgent
			CreatedAt:  now,
			UpdatedAt:  now,
			Data: []byte(`{
				"id":"issue-1","identifier":"TST-1","title":"Issue 1 - High Priority",
				"priority":4,"state":{"id":"st-1","name":"In Progress","type":"started"},
				"team":{"id":"team-1"},
				"labels":{"nodes":[{"id":"lbl-1","name":"bug"}]}
			}`),
		},
		{
			ID:            "issue-2",
			Identifier:    "TST-2",
			TeamID:        "team-1",
			Title:         "Issue 2 - Assigned",
			StateName:     strPtr("Todo"),
			StateType:     strPtr("unstarted"),
			AssigneeID:    strPtr("user-1"),
			AssigneeEmail: strPtr("user@example.com"),
			Priority:      2, // Medium
			CreatedAt:     now,
			UpdatedAt:     now,
			Data: []byte(`{
				"id":"issue-2","identifier":"TST-2","title":"Issue 2 - Assigned",
				"priority":2,"state":{"id":"st-2","name":"Todo","type":"unstarted"},
				"team":{"id":"team-1"},"assignee":{"id":"user-1","email":"user@example.com"},
				"labels":{"nodes":[{"id":"lbl-2","name":"feature"}]}
			}`),
		},
		{
			ID:         "issue-3",
			Identifier: "TST-3",
			TeamID:     "team-1",
			Title:      "Issue 3 - Unassigned",
			StateName:  strPtr("Todo"),
			StateType:  strPtr("unstarted"),
			Priority:   1, // Low
			CreatedAt:  now,
			UpdatedAt:  now,
			Data: []byte(`{
				"id":"issue-3","identifier":"TST-3","title":"Issue 3 - Unassigned",
				"priority":1,"state":{"id":"st-2","name":"Todo","type":"unstarted"},
				"team":{"id":"team-1"},
				"labels":{"nodes":[{"id":"lbl-1","name":"bug"},{"id":"lbl-2","name":"feature"}]}
			}`),
		},
	}

	for _, issue := range testIssues {
		if err := store.Queries().UpsertIssue(ctx, issue.ToUpsertParams()); err != nil {
			t.Fatalf("UpsertIssue failed: %v", err)
		}
	}

	// Verify data was inserted
	allIssues, err := store.Queries().ListTeamIssues(ctx, "team-1")
	if err != nil {
		t.Fatalf("ListTeamIssues failed: %v", err)
	}
	t.Logf("Total issues in db: %d", len(allIssues))
	for _, iss := range allIssues {
		t.Logf("Issue: %s, StateName: %v, Priority: %v, AssigneeID: %v",
			iss.Identifier, iss.StateName, iss.Priority, iss.AssigneeID)
	}

	// Direct query test to verify SQLite query works
	directIssues, err := store.Queries().ListTeamIssuesByStateName(ctx, db.ListTeamIssuesByStateNameParams{
		TeamID:    "team-1",
		StateName: sql.NullString{String: "Todo", Valid: true},
	})
	t.Logf("Direct ListTeamIssuesByStateName 'Todo' result: %d issues, err=%v", len(directIssues), err)

	t.Run("GetFilteredIssuesByStatus", func(t *testing.T) {
		issues, err := lfs.GetFilteredIssuesByStatus(ctx, "team-1", "Todo")
		if err != nil {
			t.Fatalf("GetFilteredIssuesByStatus failed: %v", err)
		}
		if len(issues) != 2 {
			t.Errorf("Expected 2 Todo issues, got %d", len(issues))
		}
	})

	t.Run("GetFilteredIssuesByPriority", func(t *testing.T) {
		issues, err := lfs.GetFilteredIssuesByPriority(ctx, "team-1", 4)
		if err != nil {
			t.Fatalf("GetFilteredIssuesByPriority failed: %v", err)
		}
		if len(issues) != 1 {
			t.Errorf("Expected 1 urgent priority issue, got %d", len(issues))
		}
		if len(issues) > 0 && issues[0].Identifier != "TST-1" {
			t.Errorf("Expected TST-1, got %s", issues[0].Identifier)
		}
	})

	t.Run("GetFilteredIssuesByAssignee", func(t *testing.T) {
		issues, err := lfs.GetFilteredIssuesByAssignee(ctx, "team-1", "user-1")
		if err != nil {
			t.Fatalf("GetFilteredIssuesByAssignee failed: %v", err)
		}
		if len(issues) != 1 {
			t.Errorf("Expected 1 assigned issue, got %d", len(issues))
		}
		if len(issues) > 0 && issues[0].Identifier != "TST-2" {
			t.Errorf("Expected TST-2, got %s", issues[0].Identifier)
		}
	})

	t.Run("GetFilteredIssuesUnassigned", func(t *testing.T) {
		issues, err := lfs.GetFilteredIssuesUnassigned(ctx, "team-1")
		if err != nil {
			t.Fatalf("GetFilteredIssuesUnassigned failed: %v", err)
		}
		if len(issues) != 2 {
			t.Errorf("Expected 2 unassigned issues, got %d", len(issues))
		}
	})

	t.Run("GetFilteredIssuesByLabel", func(t *testing.T) {
		issues, err := lfs.GetFilteredIssuesByLabel(ctx, "team-1", "bug")
		if err != nil {
			t.Fatalf("GetFilteredIssuesByLabel failed: %v", err)
		}
		if len(issues) != 2 {
			t.Errorf("Expected 2 bug-labeled issues, got %d", len(issues))
		}
	})

	t.Run("EmptyResults", func(t *testing.T) {
		issues, err := lfs.GetFilteredIssuesByStatus(ctx, "team-1", "Done")
		if err != nil {
			t.Fatalf("GetFilteredIssuesByStatus failed: %v", err)
		}
		if len(issues) != 0 {
			t.Errorf("Expected 0 Done issues, got %d", len(issues))
		}
	})
}

func strPtr(s string) *string {
	return &s
}
