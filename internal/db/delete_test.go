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
	comments, err := store.Queries().ListIssueComments(ctx, "issue-1")
	if err != nil || len(comments) != 1 {
		t.Fatalf("Comment should exist: err=%v n=%d", err, len(comments))
	}

	// Delete it
	if err := store.Queries().DeleteComment(ctx, "comment-1"); err != nil {
		t.Fatalf("DeleteComment failed: %v", err)
	}

	// Verify it's gone
	comments, _ = store.Queries().ListIssueComments(ctx, "issue-1")
	if len(comments) != 0 {
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

func TestDeleteDocument(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	now := time.Now()
	issueID := sql.NullString{String: "issue-1", Valid: true}
	err := store.Queries().UpsertDocument(ctx, UpsertDocumentParams{
		ID:       "doc-1",
		SlugID:   "test-doc",
		Title:    "Test Document",
		IssueID:  issueID,
		SyncedAt: now,
		Data:     json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatalf("UpsertDocument failed: %v", err)
	}

	// Verify it exists
	docs, err := store.Queries().ListIssueDocuments(ctx, issueID)
	if err != nil || len(docs) != 1 {
		t.Fatalf("Document should exist: err=%v n=%d", err, len(docs))
	}

	// Delete it
	if err := store.Queries().DeleteDocument(ctx, "doc-1"); err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}

	// Verify it's gone
	docs, _ = store.Queries().ListIssueDocuments(ctx, issueID)
	if len(docs) != 0 {
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

	// Verify documents are gone
	docs, _ := store.Queries().ListIssueDocuments(ctx, sql.NullString{String: issueID, Valid: true})
	if len(docs) != 0 {
		t.Errorf("Expected 0 documents after delete, got %d", len(docs))
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

// =============================================================================
// Update/Initiative Delete Tests
// =============================================================================

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
	docs, _ := store.Queries().ListProjectDocuments(ctx, sql.NullString{String: projectID, Valid: true})
	if len(docs) != 0 {
		t.Errorf("Expected 0 documents after delete, got %d", len(docs))
	}
}
