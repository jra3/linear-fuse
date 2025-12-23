package fixtures

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/repo"
)

// LinearFSForTest is a minimal interface for fs.LinearFS that can be used in tests.
// This avoids circular imports with the fs package.
type LinearFSForTest interface {
	Close()
}

// TestLinearFSConfig holds configuration for creating a test LinearFS.
type TestLinearFSConfig struct {
	// WithIssues pre-populates the repository with test issues
	WithIssues []api.Issue
	// WithTeams pre-populates the repository with test teams
	WithTeams []api.Team
	// WithStates pre-populates the repository with test states (keyed by team ID)
	WithStates map[string][]api.State
	// WithLabels pre-populates the repository with test labels (keyed by team ID)
	WithLabels map[string][]api.Label
	// WithUsers pre-populates the repository with test users
	WithUsers []api.User
	// CurrentUser sets the current user for "my" issue queries
	CurrentUser *api.User
}

// NewTestMockRepository creates a MockRepository with optional pre-populated data.
// Use this for fast, in-memory tests that don't need SQLite.
func NewTestMockRepository(t *testing.T, cfg *TestLinearFSConfig) *repo.MockRepository {
	t.Helper()

	mockRepo := repo.NewMockRepository()

	if cfg == nil {
		return mockRepo
	}

	// Populate teams
	mockRepo.Teams = cfg.WithTeams

	// Populate states
	if cfg.WithStates != nil {
		mockRepo.States = cfg.WithStates
	}

	// Populate labels
	if cfg.WithLabels != nil {
		mockRepo.Labels = cfg.WithLabels
	}

	// Populate users
	mockRepo.Users = cfg.WithUsers
	mockRepo.CurrentUser = cfg.CurrentUser

	// Populate issues
	for _, issue := range cfg.WithIssues {
		mockRepo.AddIssue(issue)
	}

	return mockRepo
}

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
		labelParams, err := db.APILabelToDBLabel(label, team.ID)
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
		labelParams, err := db.APILabelToDBLabel(label, team.ID)
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
