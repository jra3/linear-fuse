package integration

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/fs"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
)

var (
	mountPoint  string
	server      *fuse.Server
	lfs         *fs.LinearFS
	apiClient   *api.Client
	testTeamID  string
	testTeamKey string

	// liveAPIMode indicates if tests are running against real Linear API
	liveAPIMode bool
)

func TestMain(m *testing.M) {
	apiKey := os.Getenv("LINEAR_API_KEY")
	liveAPIMode = os.Getenv("LINEARFS_LIVE_API") == "1" && apiKey != ""

	if liveAPIMode {
		// Live API mode: requires API key
		if apiKey == "" {
			log.Fatal("LINEAR_API_KEY required for live API tests")
		}
		if err := setupLiveAPI(apiKey); err != nil {
			log.Fatalf("Failed to setup live API: %v", err)
		}
	} else {
		// SQLite fixture mode: no API key needed
		if err := setupSQLiteFixtures(); err != nil {
			log.Fatalf("Failed to setup SQLite fixtures: %v", err)
		}
	}

	log.Printf("Integration tests using mount=%s team=%s (liveAPI=%v)", mountPoint, testTeamKey, liveAPIMode)

	code := m.Run()

	cleanup()
	os.Exit(code)
}

// setupLiveAPI configures tests to run against real Linear API
func setupLiveAPI(apiKey string) error {
	var err error
	mountPoint, err = os.MkdirTemp("", "linearfs-test-*")
	if err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}

	cfg := &config.Config{
		APIKey: apiKey,
		Cache: config.CacheConfig{
			TTL: 100 * time.Millisecond, // Short TTL for fast tests
		},
	}

	server, lfs, err = fs.Mount(mountPoint, cfg, false)
	if err != nil {
		os.RemoveAll(mountPoint)
		return fmt.Errorf("mount filesystem: %w", err)
	}

	// Enable SQLite cache for repository access
	ctx := context.Background()
	if err := lfs.EnableSQLiteCache(ctx, ""); err != nil {
		_ = server.Unmount()
		os.RemoveAll(mountPoint)
		return fmt.Errorf("enable sqlite cache: %w", err)
	}

	apiClient = api.NewClient(apiKey)

	if err := discoverTestTeam(); err != nil {
		cleanup()
		return fmt.Errorf("discover test team: %w", err)
	}

	return nil
}

// setupSQLiteFixtures configures tests to run with pre-populated SQLite data
func setupSQLiteFixtures() error {
	var err error
	mountPoint, err = os.MkdirTemp("", "linearfs-test-*")
	if err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}

	// Create temp database
	dbPath := filepath.Join(mountPoint, "test.db")
	store, err := db.Open(dbPath)
	if err != nil {
		os.RemoveAll(mountPoint)
		return fmt.Errorf("open db: %w", err)
	}

	// Populate with fixtures
	ctx := context.Background()
	if err := populateTestFixtures(ctx, store); err != nil {
		store.Close()
		os.RemoveAll(mountPoint)
		return fmt.Errorf("populate fixtures: %w", err)
	}

	// Create LinearFS with a dummy API key (won't be used for mutations in fixture mode)
	cfg := &config.Config{
		APIKey: "fixture-mode-key",
		Cache: config.CacheConfig{
			TTL: 100 * time.Millisecond,
		},
	}

	lfs, err = fs.NewLinearFS(cfg, false)
	if err != nil {
		store.Close()
		os.RemoveAll(mountPoint)
		return fmt.Errorf("create linearfs: %w", err)
	}

	// Inject the store and create repository (no API client for fetching)
	if err := lfs.InjectTestStore(store); err != nil {
		lfs.Close()
		store.Close()
		os.RemoveAll(mountPoint)
		return fmt.Errorf("inject store: %w", err)
	}

	// Mount the filesystem
	server, err = fs.MountFS(mountPoint, lfs, false)
	if err != nil {
		lfs.Close()
		store.Close()
		os.RemoveAll(mountPoint)
		return fmt.Errorf("mount filesystem: %w", err)
	}

	// Use fixture team
	testTeamID = "team-1"
	testTeamKey = "TST"

	return nil
}

// populateTestFixtures inserts test data into the SQLite database
func populateTestFixtures(ctx context.Context, store *db.Store) error {
	team := fixtures.FixtureAPITeam()
	states := fixtures.FixtureAPIStates()
	labels := fixtures.FixtureAPILabels()
	users := fixtures.FixtureAPIUsers()

	// Create a project
	project := fixtures.FixtureAPIProject()

	// Create issues with various configurations
	issues := []api.Issue{
		fixtures.FixtureAPIIssue(
			fixtures.WithIssueID("issue-1", "TST-1"),
			fixtures.WithTitle("Test Issue 1"),
			fixtures.WithDescription("This is test issue 1"),
			fixtures.WithState(fixtures.FixtureAPIState("started")),
			fixtures.WithPriority(2),
		),
		fixtures.FixtureAPIIssue(
			fixtures.WithIssueID("issue-2", "TST-2"),
			fixtures.WithTitle("Test Issue 2"),
			fixtures.WithDescription("This is test issue 2"),
			fixtures.WithState(fixtures.FixtureAPIState("unstarted")),
			fixtures.WithPriority(1),
		),
		fixtures.FixtureAPIIssue(
			fixtures.WithIssueID("issue-3", "TST-3"),
			fixtures.WithTitle("Test Issue 3 - High Priority"),
			fixtures.WithDescription("This is a high priority issue"),
			fixtures.WithState(fixtures.FixtureAPIState("backlog")),
			fixtures.WithPriority(4),
		),
		fixtures.FixtureAPIIssue(
			fixtures.WithIssueID("issue-4", "TST-4"),
			fixtures.WithTitle("Test Issue 4 - With Labels"),
			fixtures.WithDescription("This issue has labels"),
			fixtures.WithState(fixtures.FixtureAPIState("started")),
			fixtures.WithLabels(fixtures.FixtureAPILabels()...),
		),
		fixtures.FixtureAPIIssue(
			fixtures.WithIssueID("issue-5", "TST-5"),
			fixtures.WithTitle("Test Issue 5 - Completed"),
			fixtures.WithDescription("This issue is completed"),
			fixtures.WithState(fixtures.FixtureAPIState("completed")),
		),
		// Issue with project assignment
		fixtures.FixtureAPIIssue(
			fixtures.WithIssueID("issue-6", "TST-6"),
			fixtures.WithTitle("Test Issue 6 - In Project"),
			fixtures.WithDescription("This issue is assigned to a project"),
			fixtures.WithState(fixtures.FixtureAPIState("started")),
			fixtures.WithProject(&project),
		),
		// Issue without assignee
		fixtures.FixtureAPIIssue(
			fixtures.WithIssueID("issue-7", "TST-7"),
			fixtures.WithTitle("Test Issue 7 - Unassigned"),
			fixtures.WithDescription("This issue has no assignee"),
			fixtures.WithState(fixtures.FixtureAPIState("unstarted")),
			fixtures.WithAssignee(nil),
		),
		// Issue with cycle assignment
		fixtures.FixtureAPIIssue(
			fixtures.WithIssueID("issue-8", "TST-8"),
			fixtures.WithTitle("Test Issue 8 - In Sprint"),
			fixtures.WithDescription("This issue is in a sprint/cycle"),
			fixtures.WithState(fixtures.FixtureAPIState("started")),
			fixtures.WithCycle(&api.IssueCycle{ID: "cycle-1", Name: "Sprint 42", Number: 42}),
		),
	}

	// Populate team with issues
	if err := fixtures.PopulateTeam(ctx, store, team, states, labels, issues); err != nil {
		return err
	}

	// Populate users
	if err := fixtures.PopulateUsers(ctx, store, users); err != nil {
		return err
	}

	// Populate project
	if err := fixtures.PopulateProject(ctx, store, project, team.ID); err != nil {
		return err
	}

	// Populate comments for issue-1
	comments := fixtures.FixtureAPIComments(3)
	if err := fixtures.PopulateComments(ctx, store, "issue-1", comments); err != nil {
		return err
	}

	// Populate documents for issue-1
	issueDocs := []api.Document{
		fixtures.FixtureAPIIssueDocument("issue-1", 1),
		fixtures.FixtureAPIIssueDocument("issue-1", 2),
	}
	if err := fixtures.PopulateDocuments(ctx, store, issueDocs); err != nil {
		return err
	}

	// Populate documents for project
	projectDocs := []api.Document{
		fixtures.FixtureAPIProjectDocument(project.ID, 1),
	}
	if err := fixtures.PopulateDocuments(ctx, store, projectDocs); err != nil {
		return err
	}

	// Populate cycle
	cycle := fixtures.FixtureAPICycle()
	if err := fixtures.PopulateCycle(ctx, store, cycle, team.ID); err != nil {
		return err
	}

	// Populate initiative (links to the project)
	initiative := fixtures.FixtureAPIInitiative()
	if err := fixtures.PopulateInitiative(ctx, store, initiative); err != nil {
		return err
	}

	// Set up parent-child relationship: TST-1 is parent of TST-2
	if err := fixtures.PopulateParentChildIssues(ctx, store, "issue-1", "issue-2"); err != nil {
		return err
	}

	// Populate embedded files for issue-1
	embeddedFiles := fixtures.FixtureAPIEmbeddedFiles()
	if err := fixtures.PopulateEmbeddedFiles(ctx, store, "issue-1", embeddedFiles); err != nil {
		return err
	}

	return nil
}

func discoverTestTeam() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	teams, err := apiClient.GetTeams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get teams: %w", err)
	}
	if len(teams) == 0 {
		return fmt.Errorf("no teams found in workspace")
	}

	// Prefer TST team for tests, fall back to first team
	for _, team := range teams {
		if team.Key == "TST" {
			testTeamID = team.ID
			testTeamKey = team.Key
			return nil
		}
	}

	// Fallback to first team if TST not found
	testTeamID = teams[0].ID
	testTeamKey = teams[0].Key
	return nil
}

func cleanup() {
	if server != nil {
		if err := server.Unmount(); err != nil {
			log.Printf("Warning: failed to unmount: %v", err)
		}
	}
	if lfs != nil {
		lfs.Close()
	}
	if mountPoint != "" {
		os.RemoveAll(mountPoint)
	}
}
