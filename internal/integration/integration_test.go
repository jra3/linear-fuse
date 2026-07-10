package integration

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	stateDir    string // fixture mode: holds the SQLite db, OUTSIDE the mount (see setupSQLiteFixtures)
	server      *fuse.Server
	lfs         *fs.LinearFS
	testStore   *db.Store // fixture mode: the store behind the mount, for tests simulating sync-side writes
	apiClient   *api.Client
	testTeamID  string
	testTeamKey string

	// liveAPIMode indicates if tests are running against real Linear API
	liveAPIMode bool
)

func TestMain(m *testing.M) {
	// Preflight stale mounts from a killed prior run: their dead FUSE
	// connections make this run's kernel I/O fail with roaming EIO errors —
	// the whole-suite flakiness this exists to prevent. The product's
	// fs.PreflightMountpoint carries the policy now: dead test mounts are
	// self-healed (lazy unmount), a healthy one (concurrent test run) fails
	// loud rather than getting yanked out from under the other run.
	if mounts, err := os.ReadFile("/proc/self/mounts"); err == nil {
		for _, line := range strings.Split(string(mounts), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || !strings.Contains(fields[1], "linearfs-test-") {
				continue
			}
			if err := fs.PreflightMountpoint(fields[1]); err != nil {
				log.Fatalf("stale linearfs-test mount at %s: %v", fields[1], err)
			}
			_ = os.RemoveAll(fields[1])
		}
	}

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

	lfs, err = fs.NewLinearFS(cfg, false)
	if err != nil {
		os.RemoveAll(mountPoint)
		return fmt.Errorf("create filesystem: %w", err)
	}
	server, err = fs.MountFS(mountPoint, lfs, false)
	if err != nil {
		os.RemoveAll(mountPoint)
		return fmt.Errorf("mount filesystem: %w", err)
	}
	// Readiness gate: don't let tests touch the mount before the kernel has it.
	if err := server.WaitMount(); err != nil {
		_ = server.Unmount()
		os.RemoveAll(mountPoint)
		return fmt.Errorf("wait mount: %w", err)
	}

	// Enable SQLite cache for repository access
	if err := lfs.EnableSQLiteCache(""); err != nil {
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

	// The database lives in its OWN temp dir, never inside the mountpoint:
	// db.Open ran before the mount so SQLite's held fds kept working, but any
	// post-mount file open (a WAL checkpoint, a journal) would route through
	// our own FUSE layer and fail — poisoning the suite with roaming EIO.
	stateDir, err = os.MkdirTemp("", "linearfs-test-state-*")
	if err != nil {
		os.RemoveAll(mountPoint)
		return fmt.Errorf("create state dir: %w", err)
	}
	dbPath := filepath.Join(stateDir, "test.db")
	store, err := db.Open(dbPath)
	if err != nil {
		os.RemoveAll(mountPoint)
		os.RemoveAll(stateDir)
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

	testStore = store

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
		os.RemoveAll(stateDir)
		return fmt.Errorf("mount filesystem: %w", err)
	}
	// Readiness gate: don't let tests touch the mount before the kernel has it.
	if err := server.WaitMount(); err != nil {
		cleanup()
		return fmt.Errorf("wait mount: %w", err)
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

	// Create a project, pre-labeled with a group child + a retired label (the
	// carried-through case: labelIds is a full-set write, so a save that keeps
	// Legacy must re-send it and pass validation).
	project := fixtures.FixtureAPIProject()
	project.LabelIds = []string{"plabel-backend", "plabel-legacy"}

	// One relation both ways: TST-1 blocks TST-3. The issue-embedded copies
	// back the issue.meta relations render; the issue_relations rows (below)
	// back the relations/ directory on both endpoints.
	relation := fixtures.FixtureAPIIssueRelation()
	inverseRelation := api.IssueRelation{
		ID:        relation.ID,
		Type:      relation.Type,
		Issue:     &api.ParentIssue{ID: "issue-1", Identifier: "TST-1", Title: "Test Issue 1"},
		CreatedAt: relation.CreatedAt,
		UpdatedAt: relation.UpdatedAt,
	}

	// Create issues with various configurations
	issues := []api.Issue{
		fixtures.FixtureAPIIssue(
			fixtures.WithIssueID("issue-1", "TST-1"),
			fixtures.WithTitle("Test Issue 1"),
			fixtures.WithDescription("This is test issue 1"),
			fixtures.WithState(fixtures.FixtureAPIState("started")),
			fixtures.WithPriority(2),
			fixtures.WithRelations(relation),
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
			fixtures.WithInverseRelations(inverseRelation),
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

	// Populate the workspace project-label catalog
	if err := fixtures.PopulateProjectLabels(ctx, store, fixtures.FixtureAPIProjectLabels()); err != nil {
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

	// Populate the relation row: TST-1 blocks TST-3 (backs relations/ on both ends)
	if err := fixtures.PopulateIssueRelations(ctx, store, "issue-1", []api.IssueRelation{relation}); err != nil {
		return err
	}

	// Populate milestones and status updates for the project
	if err := fixtures.PopulateProjectMilestones(ctx, store, project.ID, []api.ProjectMilestone{fixtures.FixtureAPIProjectMilestone()}); err != nil {
		return err
	}
	if err := fixtures.PopulateProjectUpdates(ctx, store, project.ID, []api.ProjectUpdate{fixtures.FixtureAPIProjectUpdate()}); err != nil {
		return err
	}

	// Populate a status update for the initiative
	if err := fixtures.PopulateInitiativeUpdates(ctx, store, initiative.ID, []api.InitiativeUpdate{fixtures.FixtureAPIInitiativeUpdate()}); err != nil {
		return err
	}

	// Populate an external URL attachment for issue-1 (a .link file)
	if err := fixtures.PopulateAttachments(ctx, store, "issue-1", []api.Attachment{fixtures.FixtureAPIAttachment()}); err != nil {
		return err
	}

	// Populate external links for the project and initiative (links/ *.link
	// files). Distinct IDs: the two share a primary key otherwise, and the
	// second upsert would clobber the first (ON CONFLICT(id)).
	projLink := fixtures.FixtureAPIEntityExternalLink()
	if err := fixtures.PopulateProjectLinks(ctx, store, project.ID, []api.EntityExternalLink{projLink}); err != nil {
		return err
	}
	initLink := fixtures.FixtureAPIEntityExternalLink()
	initLink.ID = "extlink-2"
	if err := fixtures.PopulateInitiativeLinks(ctx, store, initiative.ID, []api.EntityExternalLink{initLink}); err != nil {
		return err
	}

	// Populate cached history for issue-1 (backs the history.md render)
	if err := fixtures.PopulateIssueHistory(ctx, store, "issue-1", fixtures.FixtureAPIHistoryEntries()); err != nil {
		return err
	}

	// Populate team membership (backs the by/assignee value listing)
	userIDs := make([]string, len(users))
	for i, u := range users {
		userIDs[i] = u.ID
	}
	if err := fixtures.PopulateTeamMembers(ctx, store, team.ID, userIDs); err != nil {
		return err
	}

	// Populate the viewer identity (backs the my/ views; user-1 is the default
	// fixture assignee, so my/assigned is non-empty)
	if err := fixtures.PopulateViewer(ctx, store, "user-1"); err != nil {
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
			// EBUSY (a straggling fd some test leaked) is the common failure —
			// and the chronic orphan source: the process exits, the fd closes,
			// but nothing ever unmounts. Retry once, then LAZY-detach: the
			// kernel completes the unmount the moment the last fd closes (at
			// process exit, right after this), so no stale mount survives to
			// trip the next run's preflight.
			time.Sleep(200 * time.Millisecond)
			if err := server.Unmount(); err != nil {
				// Name the leak: any of our fds still pointing into the mount
				// is the test that forgot to close a file.
				if fds, derr := os.ReadDir("/proc/self/fd"); derr == nil {
					for _, fd := range fds {
						if target, lerr := os.Readlink("/proc/self/fd/" + fd.Name()); lerr == nil && strings.HasPrefix(target, mountPoint) {
							log.Printf("Warning: leaked fd %s -> %s held the mount busy", fd.Name(), target)
						}
					}
				}
				// A plain umount2 is not permitted for unprivileged users on
				// FUSE; the setuid fusermount3 helper with -z lazy-detaches,
				// and the kernel completes the unmount when the leaked fd
				// closes at process exit — no stale mount survives to trip
				// the next run's preflight.
				if out, lerr := exec.Command("fusermount3", "-uz", mountPoint).CombinedOutput(); lerr != nil {
					log.Printf("Warning: unmount %s failed (%v), fusermount3 -uz failed too (%v: %s); clean it manually", mountPoint, err, lerr, out)
				} else {
					log.Printf("Note: %s was busy at exit (leaked fd); lazy-detached", mountPoint)
				}
			}
		}
	}
	if lfs != nil {
		lfs.Close()
	}
	if mountPoint != "" {
		os.RemoveAll(mountPoint)
	}
	if stateDir != "" {
		os.RemoveAll(stateDir)
	}
}
