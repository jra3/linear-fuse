package fixtures

import (
	"context"
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
