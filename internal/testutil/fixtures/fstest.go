package fixtures

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/repo"
)

// NewTestSQLiteStore creates a SQLite store in a temp directory with automatic cleanup.
func NewTestSQLiteStore(t *testing.T) *db.Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}

	t.Cleanup(func() {
		store.Close()
	})

	return store
}

// NewTestSQLiteRepository creates a SQLiteRepository backed by a temp database.
// This is useful for tests that need to verify SQLite-specific behavior.
func NewTestSQLiteRepository(t *testing.T) (*repo.SQLiteRepository, *db.Store) {
	t.Helper()

	store := NewTestSQLiteStore(t)
	sqliteRepo := repo.NewSQLiteRepository(store, nil)

	t.Cleanup(func() {
		sqliteRepo.Close()
	})

	return sqliteRepo, store
}

// PopulateTestData inserts a standard set of test fixtures into the SQLite store.
// This includes:
// - 1 team (TST)
// - 5 workflow states
// - 3 labels
// - 3 users
// - 5 issues with various states, assignees, and labels
func PopulateTestData(ctx context.Context, store *db.Store) error {
	q := store.Queries()

	// Insert team
	team := FixtureAPITeam()
	teamParams := db.APITeamToDBTeam(team)
	if err := q.UpsertTeam(ctx, teamParams); err != nil {
		return err
	}

	// Insert states
	for _, state := range FixtureAPIStates() {
		stateParams, err := db.APIStateToDBState(state, team.ID)
		if err != nil {
			return err
		}
		if err := q.UpsertState(ctx, stateParams); err != nil {
			return err
		}
	}

	// Insert labels
	for _, label := range FixtureAPILabels() {
		label.Team = &api.Team{ID: team.ID} // team-scoped fixture labels
		labelParams, err := db.APILabelToDBLabel(label)
		if err != nil {
			return err
		}
		if err := q.UpsertLabel(ctx, labelParams); err != nil {
			return err
		}
	}

	// Insert users
	for _, user := range FixtureAPIUsers() {
		userParams, err := db.APIUserToDBUser(user)
		if err != nil {
			return err
		}
		if err := q.UpsertUser(ctx, userParams); err != nil {
			return err
		}
	}

	// Insert issues
	issues := FixtureAPIIssues(5)
	for _, issue := range issues {
		issueData, err := db.APIIssueToDBIssue(issue)
		if err != nil {
			return err
		}
		if err := q.UpsertIssue(ctx, issueData.ToUpsertParams()); err != nil {
			return err
		}
	}

	return nil
}

// PopulateTeam inserts a team with its associated states, labels, and issues.
// This is useful for setting up specific test scenarios.
func PopulateTeam(
	ctx context.Context,
	store *db.Store,
	team api.Team,
	states []api.State,
	labels []api.Label,
	issues []api.Issue,
) error {
	q := store.Queries()

	// Insert team
	teamParams := db.APITeamToDBTeam(team)
	if err := q.UpsertTeam(ctx, teamParams); err != nil {
		return err
	}

	// Insert states
	for _, state := range states {
		stateParams, err := db.APIStateToDBState(state, team.ID)
		if err != nil {
			return err
		}
		if err := q.UpsertState(ctx, stateParams); err != nil {
			return err
		}
	}

	// Insert labels
	for _, label := range labels {
		label.Team = &api.Team{ID: team.ID} // team-scoped fixture labels
		labelParams, err := db.APILabelToDBLabel(label)
		if err != nil {
			return err
		}
		if err := q.UpsertLabel(ctx, labelParams); err != nil {
			return err
		}
	}

	// Insert issues
	for _, issue := range issues {
		issueData, err := db.APIIssueToDBIssue(issue)
		if err != nil {
			return err
		}
		if err := q.UpsertIssue(ctx, issueData.ToUpsertParams()); err != nil {
			return err
		}
	}

	return nil
}

// PopulateUsers inserts users into the SQLite store.
func PopulateUsers(ctx context.Context, store *db.Store, users []api.User) error {
	q := store.Queries()
	for _, user := range users {
		userParams, err := db.APIUserToDBUser(user)
		if err != nil {
			return err
		}
		if err := q.UpsertUser(ctx, userParams); err != nil {
			return err
		}
	}
	return nil
}

// PopulateComments inserts comments for an issue into the SQLite store.
func PopulateComments(ctx context.Context, store *db.Store, issueID string, comments []api.Comment) error {
	q := store.Queries()
	for _, comment := range comments {
		params, err := db.APICommentToDBComment(comment, issueID)
		if err != nil {
			return err
		}
		if err := q.UpsertComment(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// PopulateDocuments inserts documents into the SQLite store.
func PopulateDocuments(ctx context.Context, store *db.Store, docs []api.Document) error {
	q := store.Queries()
	for _, doc := range docs {
		params, err := db.APIDocumentToDBDocument(doc)
		if err != nil {
			return err
		}
		if err := q.UpsertDocument(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// PopulateProject inserts a project into the SQLite store.
func PopulateProject(ctx context.Context, store *db.Store, project api.Project, teamID string) error {
	q := store.Queries()
	params, err := db.APIProjectToDBProject(project)
	if err != nil {
		return err
	}
	if err := q.UpsertProject(ctx, params); err != nil {
		return err
	}
	// Link project to team
	if err := q.UpsertProjectTeam(ctx, db.UpsertProjectTeamParams{
		ProjectID: project.ID,
		TeamID:    teamID,
	}); err != nil {
		return err
	}
	return nil
}

// PopulateProjectLabels inserts workspace project labels into the SQLite store.
func PopulateProjectLabels(ctx context.Context, store *db.Store, labels []api.ProjectLabel) error {
	q := store.Queries()
	for _, label := range labels {
		params, err := db.APIProjectLabelToDBProjectLabel(label)
		if err != nil {
			return err
		}
		if err := q.UpsertProjectLabel(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// PopulateCycle inserts a cycle into the SQLite store.
func PopulateCycle(ctx context.Context, store *db.Store, cycle api.Cycle, teamID string) error {
	q := store.Queries()
	params, err := db.APICycleToDBCycle(cycle, teamID)
	if err != nil {
		return err
	}
	return q.UpsertCycle(ctx, params)
}

// PopulateInitiative inserts an initiative into the SQLite store.
func PopulateInitiative(ctx context.Context, store *db.Store, initiative api.Initiative) error {
	q := store.Queries()
	params, err := db.APIInitiativeToDBInitiative(initiative)
	if err != nil {
		return err
	}
	if err := q.UpsertInitiative(ctx, params); err != nil {
		return err
	}
	// Link projects to initiative
	for _, proj := range initiative.Projects.Nodes {
		if err := q.UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
			InitiativeID: initiative.ID,
			ProjectID:    proj.ID,
			SyncedAt:     time.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// PopulateParentChildIssues sets up a parent-child relationship between issues.
func PopulateParentChildIssues(ctx context.Context, store *db.Store, parentID, childID string) error {
	q := store.Queries()
	// Update child issue to have parent reference
	return q.SetIssueParent(ctx, db.SetIssueParentParams{
		ID:       childID,
		ParentID: sql.NullString{String: parentID, Valid: true},
	})
}

// TestConfig returns a config suitable for testing.
func TestConfig() *config.Config {
	return &config.Config{
		APIKey: "test-key",
		Cache: config.CacheConfig{
			TTL:        100 * time.Millisecond,
			MaxEntries: 100,
		},
	}
}

// PopulateEmbeddedFiles inserts embedded files for an issue into the SQLite store.
func PopulateEmbeddedFiles(ctx context.Context, store *db.Store, issueID string, files []api.EmbeddedFile) error {
	q := store.Queries()
	for _, file := range files {
		params := db.UpsertEmbeddedFileParams{
			ID:        file.ID,
			IssueID:   issueID,
			Url:       file.URL,
			Filename:  file.Filename,
			MimeType:  sql.NullString{String: file.MimeType, Valid: file.MimeType != ""},
			Source:    file.Source,
			CreatedAt: time.Now(),
			SyncedAt:  time.Now(),
		}
		if err := q.UpsertEmbeddedFile(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// PopulateIssueRelations inserts outgoing relations for an issue into the
// issue_relations table (the store behind the relations/ directory). Each
// relation must carry RelatedIssue; the row is stored from the owner's
// perspective, so the target issue's relations/ shows the inverse file.
func PopulateIssueRelations(ctx context.Context, store *db.Store, issueID string, rels []api.IssueRelation) error {
	q := store.Queries()
	for _, rel := range rels {
		if err := q.UpsertIssueRelation(ctx, db.IssueRelationUpsertParams(rel, issueID, rel.RelatedIssue.ID)); err != nil {
			return err
		}
	}
	return nil
}

// PopulateProjectMilestones inserts milestones for a project into the SQLite store.
func PopulateProjectMilestones(ctx context.Context, store *db.Store, projectID string, milestones []api.ProjectMilestone) error {
	q := store.Queries()
	for _, m := range milestones {
		params, err := db.APIProjectMilestoneToDBMilestone(m, projectID)
		if err != nil {
			return err
		}
		if err := q.UpsertProjectMilestone(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// PopulateProjectUpdates inserts status updates for a project into the SQLite store.
func PopulateProjectUpdates(ctx context.Context, store *db.Store, projectID string, updates []api.ProjectUpdate) error {
	q := store.Queries()
	for _, u := range updates {
		params, err := db.APIProjectUpdateToDBUpdate(u, projectID)
		if err != nil {
			return err
		}
		if err := q.UpsertProjectUpdate(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// PopulateInitiativeUpdates inserts status updates for an initiative into the SQLite store.
func PopulateInitiativeUpdates(ctx context.Context, store *db.Store, initiativeID string, updates []api.InitiativeUpdate) error {
	q := store.Queries()
	for _, u := range updates {
		params, err := db.APIInitiativeUpdateToDBUpdate(u, initiativeID)
		if err != nil {
			return err
		}
		if err := q.UpsertInitiativeUpdate(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// PopulateAttachments inserts external URL attachments for an issue into the
// SQLite store (rendered as *.link files in attachments/).
func PopulateAttachments(ctx context.Context, store *db.Store, issueID string, attachments []api.Attachment) error {
	q := store.Queries()
	for _, a := range attachments {
		params, err := db.APIAttachmentToDBAttachment(a, issueID)
		if err != nil {
			return err
		}
		if err := q.UpsertAttachment(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// PopulateIssueHistory caches history entries for an issue (the store behind
// the history.md render).
func PopulateIssueHistory(ctx context.Context, store *db.Store, issueID string, entries []api.IssueHistoryEntry) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return store.Queries().UpsertIssueHistoryCache(ctx, db.UpsertIssueHistoryCacheParams{
		IssueID:  issueID,
		SyncedAt: time.Now(),
		Data:     data,
	})
}

// PopulateTeamMembers inserts team membership rows (the store behind the
// by/assignee value listing).
func PopulateTeamMembers(ctx context.Context, store *db.Store, teamID string, userIDs []string) error {
	q := store.Queries()
	for _, uid := range userIDs {
		if err := q.UpsertTeamMember(ctx, db.UpsertTeamMemberParams{
			TeamID:   teamID,
			UserID:   uid,
			SyncedAt: time.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// PopulateViewer records the viewer identity (the store behind the my/ views;
// the user must also be populated via PopulateUsers so the identity resolves).
func PopulateViewer(ctx context.Context, store *db.Store, userID string) error {
	return store.Queries().SetViewerUserID(ctx, db.SetViewerUserIDParams{
		UserID:   userID,
		SyncedAt: time.Now(),
	})
}

// FixtureAPIEmbeddedFiles returns a set of embedded files for testing.
func FixtureAPIEmbeddedFiles() []api.EmbeddedFile {
	return []api.EmbeddedFile{
		{
			ID:       "file-1",
			URL:      "https://uploads.linear.app/workspace1/file1/screenshot.png",
			Filename: "screenshot.png",
			MimeType: "image/png",
			Source:   "description",
		},
		{
			ID:       "file-2",
			URL:      "https://uploads.linear.app/workspace1/file2/design.pdf",
			Filename: "design.pdf",
			MimeType: "application/pdf",
			Source:   "description",
		},
	}
}
