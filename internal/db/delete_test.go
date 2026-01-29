package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// =============================================================================
// Core Entity Delete Tests
// =============================================================================

func TestDeleteComment(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Insert a comment
	now := time.Now()
	err := store.Queries().UpsertComment(ctx, UpsertCommentParams{
		ID:        "comment-1",
		IssueID:   "issue-1",
		Body:      "Test comment",
		CreatedAt: now,
		UpdatedAt: now,
		SyncedAt:  now,
		Data:      json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertComment failed: %v", err)
	}

	// Verify it exists
	_, err = store.Queries().GetComment(ctx, "comment-1")
	if err != nil {
		t.Fatalf("Comment should exist: %v", err)
	}

	// Delete it
	if err := store.Queries().DeleteComment(ctx, "comment-1"); err != nil {
		t.Fatalf("DeleteComment failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetComment(ctx, "comment-1")
	if err == nil {
		t.Error("Comment should be deleted")
	}
}

func TestDeleteComment_NonExistent(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Deleting non-existent comment should not error
	if err := store.Queries().DeleteComment(ctx, "nonexistent"); err != nil {
		t.Errorf("DeleteComment on non-existent should not error: %v", err)
	}
}

func TestDeleteCycle(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	err := store.Queries().UpsertCycle(ctx, UpsertCycleParams{
		ID:       "cycle-1",
		TeamID:   "team-1",
		Number:   1,
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertCycle failed: %v", err)
	}

	// Verify it exists
	_, err = store.Queries().GetCycle(ctx, "cycle-1")
	if err != nil {
		t.Fatalf("Cycle should exist: %v", err)
	}

	// Delete it
	if err := store.Queries().DeleteCycle(ctx, "cycle-1"); err != nil {
		t.Fatalf("DeleteCycle failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetCycle(ctx, "cycle-1")
	if err == nil {
		t.Error("Cycle should be deleted")
	}
}

func TestDeleteDocument(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	err := store.Queries().UpsertDocument(ctx, UpsertDocumentParams{
		ID:       "doc-1",
		SlugID:   "test-doc",
		Title:    "Test Document",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	// Verify it exists
	_, err = store.Queries().GetDocument(ctx, "doc-1")
	if err != nil {
		t.Fatalf("Document should exist: %v", err)
	}

	// Delete it
	if err := store.Queries().DeleteDocument(ctx, "doc-1"); err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetDocument(ctx, "doc-1")
	if err == nil {
		t.Error("Document should be deleted")
	}
}

func TestDeleteInitiative(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	err := store.Queries().UpsertInitiative(ctx, UpsertInitiativeParams{
		ID:       "init-1",
		SlugID:   "test-initiative",
		Name:     "Test Initiative",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertInitiative failed: %v", err)
	}

	// Verify it exists
	_, err = store.Queries().GetInitiative(ctx, "init-1")
	if err != nil {
		t.Fatalf("Initiative should exist: %v", err)
	}

	// Delete it
	if err := store.Queries().DeleteInitiative(ctx, "init-1"); err != nil {
		t.Fatalf("DeleteInitiative failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetInitiative(ctx, "init-1")
	if err == nil {
		t.Error("Initiative should be deleted")
	}
}

func TestDeleteIssueByID(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// Insert an issue
	data := &IssueData{
		ID:         "issue-to-delete-byid",
		Identifier: "TST-DELID",
		Title:      "To Delete By ID",
		TeamID:     "team-1",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Data:       json.RawMessage("{}"),
	}
	if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Delete by ID
	if err := store.Queries().DeleteIssue(ctx, "issue-to-delete-byid"); err != nil {
		t.Fatalf("DeleteIssue failed: %v", err)
	}

	// Verify it's gone
	_, err := store.Queries().GetIssueByID(ctx, "issue-to-delete-byid")
	if err == nil {
		t.Error("Issue should be deleted")
	}
}

func TestDeleteLabel(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	err := store.Queries().UpsertLabel(ctx, UpsertLabelParams{
		ID:       "label-1",
		TeamID:   sql.NullString{String: "team-1", Valid: true},
		Name:     "Test Label",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertLabel failed: %v", err)
	}

	// Verify it exists
	_, err = store.Queries().GetLabel(ctx, "label-1")
	if err != nil {
		t.Fatalf("Label should exist: %v", err)
	}

	// Delete it
	if err := store.Queries().DeleteLabel(ctx, "label-1"); err != nil {
		t.Fatalf("DeleteLabel failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetLabel(ctx, "label-1")
	if err == nil {
		t.Error("Label should be deleted")
	}
}

func TestDeleteProject(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
		ID:       "project-1",
		SlugID:   "test-project",
		Name:     "Test Project",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertProject failed: %v", err)
	}

	// Verify it exists
	_, err = store.Queries().GetProject(ctx, "project-1")
	if err != nil {
		t.Fatalf("Project should exist: %v", err)
	}

	// Delete it
	if err := store.Queries().DeleteProject(ctx, "project-1"); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetProject(ctx, "project-1")
	if err == nil {
		t.Error("Project should be deleted")
	}
}

func TestDeleteState(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	err := store.Queries().UpsertState(ctx, UpsertStateParams{
		ID:       "state-1",
		TeamID:   "team-1",
		Name:     "Todo",
		Type:     "unstarted",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertState failed: %v", err)
	}

	// Verify it exists
	_, err = store.Queries().GetState(ctx, "state-1")
	if err != nil {
		t.Fatalf("State should exist: %v", err)
	}

	// Delete it
	if err := store.Queries().DeleteState(ctx, "state-1"); err != nil {
		t.Fatalf("DeleteState failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetState(ctx, "state-1")
	if err == nil {
		t.Error("State should be deleted")
	}
}

func TestDeleteUser(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	err := store.Queries().UpsertUser(ctx, UpsertUserParams{
		ID:       "user-1",
		Email:    "test@example.com",
		Name:     "Test User",
		Active:   1,
		Admin:    0,
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertUser failed: %v", err)
	}

	// Verify it exists
	_, err = store.Queries().GetUser(ctx, "user-1")
	if err != nil {
		t.Fatalf("User should exist: %v", err)
	}

	// Delete it
	if err := store.Queries().DeleteUser(ctx, "user-1"); err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetUser(ctx, "user-1")
	if err == nil {
		t.Error("User should be deleted")
	}
}

// =============================================================================
// Cascade Delete Tests (Parent Entity Deletion)
// =============================================================================

func TestDeleteIssueComments(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	issueID := "issue-with-comments"
	now := time.Now()

	// Insert multiple comments for the same issue
	for i := 0; i < 3; i++ {
		err := store.Queries().UpsertComment(ctx, UpsertCommentParams{
			ID:        "comment-" + string(rune('a'+i)),
			IssueID:   issueID,
			Body:      "Comment " + string(rune('1'+i)),
			CreatedAt: now,
			UpdatedAt: now,
			SyncedAt:  now,
			Data:      json.RawMessage("{}"),
		})
		if err != nil {
			t.Fatalf("UpsertComment failed: %v", err)
		}
	}

	// Verify comments exist
	comments, err := store.Queries().ListIssueComments(ctx, issueID)
	if err != nil {
		t.Fatalf("ListIssueComments failed: %v", err)
	}
	if len(comments) != 3 {
		t.Fatalf("Expected 3 comments, got %d", len(comments))
	}

	// Delete all comments for the issue
	if err := store.Queries().DeleteIssueComments(ctx, issueID); err != nil {
		t.Fatalf("DeleteIssueComments failed: %v", err)
	}

	// Verify all comments are gone
	comments, _ = store.Queries().ListIssueComments(ctx, issueID)
	if len(comments) != 0 {
		t.Errorf("Expected 0 comments after delete, got %d", len(comments))
	}
}

func TestDeleteIssueDocuments(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	issueID := "issue-with-docs"
	now := time.Now()

	// Insert documents for the issue
	for i := 0; i < 2; i++ {
		err := store.Queries().UpsertDocument(ctx, UpsertDocumentParams{
			ID:       "doc-" + string(rune('a'+i)),
			SlugID:   "doc-" + string(rune('a'+i)),
			Title:    "Doc " + string(rune('1'+i)),
			IssueID:  sql.NullString{String: issueID, Valid: true},
			SyncedAt: now,
			Data:     json.RawMessage("{}"),
		})
		if err != nil {
			t.Fatalf("UpsertDocument failed: %v", err)
		}
	}

	// Delete all documents for the issue
	if err := store.Queries().DeleteIssueDocuments(ctx, sql.NullString{String: issueID, Valid: true}); err != nil {
		t.Fatalf("DeleteIssueDocuments failed: %v", err)
	}

	// Verify documents are gone by trying to get them
	_, err := store.Queries().GetDocument(ctx, "doc-a")
	if err == nil {
		t.Error("Document doc-a should be deleted")
	}
}

func TestDeleteTeamIssues(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-to-clear"

	// Insert issues for the team
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
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// Verify issues exist
	issues, _ := store.Queries().ListTeamIssues(ctx, teamID)
	if len(issues) != 3 {
		t.Fatalf("Expected 3 issues, got %d", len(issues))
	}

	// Delete all team issues
	if err := store.Queries().DeleteTeamIssues(ctx, teamID); err != nil {
		t.Fatalf("DeleteTeamIssues failed: %v", err)
	}

	// Verify all gone
	issues, _ = store.Queries().ListTeamIssues(ctx, teamID)
	if len(issues) != 0 {
		t.Errorf("Expected 0 issues after delete, got %d", len(issues))
	}
}

func TestDeleteTeamLabels(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-with-labels"
	now := time.Now()

	// Insert labels for the team
	for i := 0; i < 3; i++ {
		err := store.Queries().UpsertLabel(ctx, UpsertLabelParams{
			ID:       "label-" + string(rune('a'+i)),
			TeamID:   sql.NullString{String: teamID, Valid: true},
			Name:     "Label " + string(rune('1'+i)),
			SyncedAt: now,
			Data:     json.RawMessage("{}"),
		})
		if err != nil {
			t.Fatalf("UpsertLabel failed: %v", err)
		}
	}

	// Delete all team labels
	if err := store.Queries().DeleteTeamLabels(ctx, sql.NullString{String: teamID, Valid: true}); err != nil {
		t.Fatalf("DeleteTeamLabels failed: %v", err)
	}

	// Verify labels are gone
	labels, _ := store.Queries().ListTeamLabels(ctx, sql.NullString{String: teamID, Valid: true})
	if len(labels) != 0 {
		t.Errorf("Expected 0 labels after delete, got %d", len(labels))
	}
}

func TestDeleteTeamStates(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-with-states"
	now := time.Now()

	// Insert states for the team
	stateTypes := []string{"unstarted", "started", "completed"}
	for i, st := range stateTypes {
		err := store.Queries().UpsertState(ctx, UpsertStateParams{
			ID:       "state-" + string(rune('a'+i)),
			TeamID:   teamID,
			Name:     "State " + string(rune('1'+i)),
			Type:     st,
			SyncedAt: now,
			Data:     json.RawMessage("{}"),
		})
		if err != nil {
			t.Fatalf("UpsertState failed: %v", err)
		}
	}

	// Delete all team states
	if err := store.Queries().DeleteTeamStates(ctx, teamID); err != nil {
		t.Fatalf("DeleteTeamStates failed: %v", err)
	}

	// Verify states are gone
	states, _ := store.Queries().ListTeamStates(ctx, teamID)
	if len(states) != 0 {
		t.Errorf("Expected 0 states after delete, got %d", len(states))
	}
}

func TestDeleteTeamCycles(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	teamID := "team-with-cycles"
	now := time.Now()

	// Insert cycles for the team
	for i := 0; i < 3; i++ {
		err := store.Queries().UpsertCycle(ctx, UpsertCycleParams{
			ID:       "cycle-" + string(rune('a'+i)),
			TeamID:   teamID,
			Number:   int64(i + 1),
			SyncedAt: now,
			Data:     json.RawMessage("{}"),
		})
		if err != nil {
			t.Fatalf("UpsertCycle failed: %v", err)
		}
	}

	// Delete all team cycles
	if err := store.Queries().DeleteTeamCycles(ctx, teamID); err != nil {
		t.Fatalf("DeleteTeamCycles failed: %v", err)
	}

	// Verify cycles are gone
	cycles, _ := store.Queries().ListTeamCycles(ctx, teamID)
	if len(cycles) != 0 {
		t.Errorf("Expected 0 cycles after delete, got %d", len(cycles))
	}
}

// =============================================================================
// Junction Table Delete Tests
// =============================================================================

func TestDeleteInitiativeProject(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()

	// Create initiative and project first
	if err := store.Queries().UpsertInitiative(ctx, UpsertInitiativeParams{
		ID:       "init-jp",
		SlugID:   "init-jp",
		Name:     "Initiative",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
		ID:       "proj-jp",
		SlugID:   "proj-jp",
		Name:     "Project",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create association
	err := store.Queries().UpsertInitiativeProject(ctx, UpsertInitiativeProjectParams{
		InitiativeID: "init-jp",
		ProjectID:    "proj-jp",
		SyncedAt:     now,
	})
	if err != nil {
		t.Fatalf("UpsertInitiativeProject failed: %v", err)
	}

	// Delete specific association
	err = store.Queries().DeleteInitiativeProject(ctx, DeleteInitiativeProjectParams{
		InitiativeID: "init-jp",
		ProjectID:    "proj-jp",
	})
	if err != nil {
		t.Fatalf("DeleteInitiativeProject failed: %v", err)
	}
}

func TestDeleteInitiativeProjects(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	initiativeID := "init-all-projs"

	// Create initiative
	if err := store.Queries().UpsertInitiative(ctx, UpsertInitiativeParams{
		ID:       initiativeID,
		SlugID:   "init-all",
		Name:     "Initiative",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create projects and associations
	for i := 0; i < 3; i++ {
		projID := "proj-" + string(rune('a'+i))
		if err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
			ID:       projID,
			SlugID:   projID,
			Name:     "Project " + string(rune('1'+i)),
			SyncedAt: now,
			Data:     json.RawMessage("{}"),
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := store.Queries().UpsertInitiativeProject(ctx, UpsertInitiativeProjectParams{
			InitiativeID: initiativeID,
			ProjectID:    projID,
			SyncedAt:     now,
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Delete all projects for initiative
	if err := store.Queries().DeleteInitiativeProjects(ctx, initiativeID); err != nil {
		t.Fatalf("DeleteInitiativeProjects failed: %v", err)
	}
}

func TestDeleteProjectTeam(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()

	// Create project and team
	if err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
		ID:       "proj-pt",
		SlugID:   "proj-pt",
		Name:     "Project",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertTeam(ctx, UpsertTeamParams{
		ID:        "team-pt",
		Key:       "TPT",
		Name:      "Team",
		CreatedAt: sql.NullTime{Time: now, Valid: true},
		UpdatedAt: sql.NullTime{Time: now, Valid: true},
		SyncedAt:  now,
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create association
	if err := store.Queries().UpsertProjectTeam(ctx, UpsertProjectTeamParams{
		ProjectID: "proj-pt",
		TeamID:    "team-pt",
		SyncedAt:  now,
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Delete specific association
	err := store.Queries().DeleteProjectTeam(ctx, DeleteProjectTeamParams{
		ProjectID: "proj-pt",
		TeamID:    "team-pt",
	})
	if err != nil {
		t.Fatalf("DeleteProjectTeam failed: %v", err)
	}
}

func TestDeleteProjectTeams(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	projectID := "proj-all-teams"

	// Create project
	if err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
		ID:       projectID,
		SlugID:   projectID,
		Name:     "Project",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create teams and associations
	for i := 0; i < 2; i++ {
		teamID := "team-" + string(rune('a'+i))
		if err := store.Queries().UpsertTeam(ctx, UpsertTeamParams{
			ID:        teamID,
			Key:       "T" + string(rune('A'+i)),
			Name:      "Team " + string(rune('1'+i)),
			CreatedAt: sql.NullTime{Time: now, Valid: true},
			UpdatedAt: sql.NullTime{Time: now, Valid: true},
			SyncedAt:  now,
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := store.Queries().UpsertProjectTeam(ctx, UpsertProjectTeamParams{
			ProjectID: projectID,
			TeamID:    teamID,
			SyncedAt:  now,
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Delete all teams for project
	if err := store.Queries().DeleteProjectTeams(ctx, projectID); err != nil {
		t.Fatalf("DeleteProjectTeams failed: %v", err)
	}
}

func TestDeleteTeamMember(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()

	// Create team and user
	if err := store.Queries().UpsertTeam(ctx, UpsertTeamParams{
		ID:        "team-tm",
		Key:       "TTM",
		Name:      "Team",
		CreatedAt: sql.NullTime{Time: now, Valid: true},
		UpdatedAt: sql.NullTime{Time: now, Valid: true},
		SyncedAt:  now,
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := store.Queries().UpsertUser(ctx, UpsertUserParams{
		ID:       "user-tm",
		Email:    "user@example.com",
		Name:     "User",
		Active:   1,
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create membership
	if err := store.Queries().UpsertTeamMember(ctx, UpsertTeamMemberParams{
		TeamID:   "team-tm",
		UserID:   "user-tm",
		SyncedAt: now,
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Delete specific membership
	err := store.Queries().DeleteTeamMember(ctx, DeleteTeamMemberParams{
		TeamID: "team-tm",
		UserID: "user-tm",
	})
	if err != nil {
		t.Fatalf("DeleteTeamMember failed: %v", err)
	}
}

func TestDeleteTeamMembers(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	teamID := "team-all-members"

	// Create team
	if err := store.Queries().UpsertTeam(ctx, UpsertTeamParams{
		ID:        teamID,
		Key:       "TAM",
		Name:      "Team",
		CreatedAt: sql.NullTime{Time: now, Valid: true},
		UpdatedAt: sql.NullTime{Time: now, Valid: true},
		SyncedAt:  now,
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create users and memberships
	for i := 0; i < 3; i++ {
		userID := "user-" + string(rune('a'+i))
		if err := store.Queries().UpsertUser(ctx, UpsertUserParams{
			ID:       userID,
			Email:    "user" + string(rune('1'+i)) + "@example.com",
			Name:     "User " + string(rune('1'+i)),
			Active:   1,
			SyncedAt: now,
			Data:     json.RawMessage("{}"),
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := store.Queries().UpsertTeamMember(ctx, UpsertTeamMemberParams{
			TeamID:   teamID,
			UserID:   userID,
			SyncedAt: now,
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Delete all members for team
	if err := store.Queries().DeleteTeamMembers(ctx, teamID); err != nil {
		t.Fatalf("DeleteTeamMembers failed: %v", err)
	}
}

// =============================================================================
// Update/Initiative Delete Tests
// =============================================================================

func TestDeleteInitiativeUpdate(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()

	// Create initiative first
	if err := store.Queries().UpsertInitiative(ctx, UpsertInitiativeParams{
		ID:       "init-update",
		SlugID:   "init-update",
		Name:     "Initiative",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create update
	err := store.Queries().UpsertInitiativeUpdate(ctx, UpsertInitiativeUpdateParams{
		ID:           "init-upd-1",
		InitiativeID: "init-update",
		Body:         "Update body",
		CreatedAt:    now,
		UpdatedAt:    now,
		SyncedAt:     now,
		Data:         json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertInitiativeUpdate failed: %v", err)
	}

	// Delete update
	if err := store.Queries().DeleteInitiativeUpdate(ctx, "init-upd-1"); err != nil {
		t.Fatalf("DeleteInitiativeUpdate failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetInitiativeUpdate(ctx, "init-upd-1")
	if err == nil {
		t.Error("Initiative update should be deleted")
	}
}

func TestDeleteInitiativeUpdates(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	initiativeID := "init-all-updates"

	// Create initiative
	if err := store.Queries().UpsertInitiative(ctx, UpsertInitiativeParams{
		ID:       initiativeID,
		SlugID:   "init-all-updates",
		Name:     "Initiative",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create multiple updates
	for i := 0; i < 3; i++ {
		if err := store.Queries().UpsertInitiativeUpdate(ctx, UpsertInitiativeUpdateParams{
			ID:           "init-upd-" + string(rune('a'+i)),
			InitiativeID: initiativeID,
			Body:         "Update " + string(rune('1'+i)),
			CreatedAt:    now,
			UpdatedAt:    now,
			SyncedAt:     now,
			Data:         json.RawMessage("{}"),
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Delete all updates for initiative
	if err := store.Queries().DeleteInitiativeUpdates(ctx, initiativeID); err != nil {
		t.Fatalf("DeleteInitiativeUpdates failed: %v", err)
	}

	// Verify all gone
	updates, _ := store.Queries().ListInitiativeUpdates(ctx, initiativeID)
	if len(updates) != 0 {
		t.Errorf("Expected 0 updates after delete, got %d", len(updates))
	}
}

func TestDeleteProjectUpdate(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()

	// Create project first
	if err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
		ID:       "proj-update",
		SlugID:   "proj-update",
		Name:     "Project",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create update
	err := store.Queries().UpsertProjectUpdate(ctx, UpsertProjectUpdateParams{
		ID:        "proj-upd-1",
		ProjectID: "proj-update",
		Body:      "Update body",
		CreatedAt: now,
		UpdatedAt: now,
		SyncedAt:  now,
		Data:      json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertProjectUpdate failed: %v", err)
	}

	// Delete update
	if err := store.Queries().DeleteProjectUpdate(ctx, "proj-upd-1"); err != nil {
		t.Fatalf("DeleteProjectUpdate failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetProjectUpdate(ctx, "proj-upd-1")
	if err == nil {
		t.Error("Project update should be deleted")
	}
}

func TestDeleteProjectUpdates(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	projectID := "proj-all-updates"

	// Create project
	if err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
		ID:       projectID,
		SlugID:   "proj-all-updates",
		Name:     "Project",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create multiple updates
	for i := 0; i < 3; i++ {
		if err := store.Queries().UpsertProjectUpdate(ctx, UpsertProjectUpdateParams{
			ID:        "proj-upd-" + string(rune('a'+i)),
			ProjectID: projectID,
			Body:      "Update " + string(rune('1'+i)),
			CreatedAt: now,
			UpdatedAt: now,
			SyncedAt:  now,
			Data:      json.RawMessage("{}"),
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Delete all updates for project
	if err := store.Queries().DeleteProjectUpdates(ctx, projectID); err != nil {
		t.Fatalf("DeleteProjectUpdates failed: %v", err)
	}

	// Verify all gone
	updates, _ := store.Queries().ListProjectUpdates(ctx, projectID)
	if len(updates) != 0 {
		t.Errorf("Expected 0 updates after delete, got %d", len(updates))
	}
}

func TestDeleteProjectMilestone(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()

	// Create project first
	if err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
		ID:       "proj-milestone",
		SlugID:   "proj-milestone",
		Name:     "Project",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create milestone
	err := store.Queries().UpsertProjectMilestone(ctx, UpsertProjectMilestoneParams{
		ID:        "milestone-1",
		ProjectID: "proj-milestone",
		Name:      "Milestone 1",
		SyncedAt:  now,
		Data:      json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertProjectMilestone failed: %v", err)
	}

	// Delete milestone
	if err := store.Queries().DeleteProjectMilestone(ctx, "milestone-1"); err != nil {
		t.Fatalf("DeleteProjectMilestone failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Queries().GetProjectMilestone(ctx, "milestone-1")
	if err == nil {
		t.Error("Project milestone should be deleted")
	}
}

func TestDeleteProjectMilestones(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	projectID := "proj-all-milestones"

	// Create project
	if err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
		ID:       projectID,
		SlugID:   "proj-all-milestones",
		Name:     "Project",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create multiple milestones
	for i := 0; i < 3; i++ {
		if err := store.Queries().UpsertProjectMilestone(ctx, UpsertProjectMilestoneParams{
			ID:        "milestone-" + string(rune('a'+i)),
			ProjectID: projectID,
			Name:      "Milestone " + string(rune('1'+i)),
			SyncedAt:  now,
			Data:      json.RawMessage("{}"),
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Delete all milestones for project
	if err := store.Queries().DeleteProjectMilestones(ctx, projectID); err != nil {
		t.Fatalf("DeleteProjectMilestones failed: %v", err)
	}

	// Verify all gone
	milestones, _ := store.Queries().ListProjectMilestones(ctx, projectID)
	if len(milestones) != 0 {
		t.Errorf("Expected 0 milestones after delete, got %d", len(milestones))
	}
}

func TestDeleteProjectDocuments(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	projectID := "proj-all-docs"

	// Create project
	if err := store.Queries().UpsertProject(ctx, UpsertProjectParams{
		ID:       projectID,
		SlugID:   "proj-all-docs",
		Name:     "Project",
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create documents for project
	for i := 0; i < 2; i++ {
		if err := store.Queries().UpsertDocument(ctx, UpsertDocumentParams{
			ID:        "pdoc-" + string(rune('a'+i)),
			SlugID:    "pdoc-" + string(rune('a'+i)),
			Title:     "Doc " + string(rune('1'+i)),
			ProjectID: sql.NullString{String: projectID, Valid: true},
			SyncedAt:  now,
			Data:      json.RawMessage("{}"),
		}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Delete all documents for project
	if err := store.Queries().DeleteProjectDocuments(ctx, sql.NullString{String: projectID, Valid: true}); err != nil {
		t.Fatalf("DeleteProjectDocuments failed: %v", err)
	}

	// Verify documents are gone
	_, err := store.Queries().GetDocument(ctx, "pdoc-a")
	if err == nil {
		t.Error("Document pdoc-a should be deleted")
	}
}
