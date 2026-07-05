package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// Association prunes: the metadata and workspace syncs fetch provably
// complete sets (every connection drained), which is what makes deleting
// rows absent from the response safe. These tests pin the three properties
// the cutoff pattern guarantees: stale rows go, rows written mid-sync
// survive, and a failed fetch prunes nothing.

// Seeds must pass db.Now()-derived (UTC) times: synced_at strings carry
// their zone verbatim and compare textually, so the codebase-wide invariant
// is that every synced_at write goes through db.Now(). A local-zone seed
// here would order against the UTC cutoff by wall-clock face, not instant.
func seedProjectTeam(t *testing.T, store *db.Store, projectID, teamID string, syncedAt time.Time) {
	t.Helper()
	if err := store.Queries().UpsertProjectTeam(context.Background(), db.UpsertProjectTeamParams{
		ProjectID: projectID,
		TeamID:    teamID,
		SyncedAt:  syncedAt,
	}); err != nil {
		t.Fatalf("seed project_team %s-%s: %v", projectID, teamID, err)
	}
}

func seedInitiativeProject(t *testing.T, store *db.Store, initiativeID, projectID string, syncedAt time.Time) {
	t.Helper()
	if err := store.Queries().UpsertInitiativeProject(context.Background(), db.UpsertInitiativeProjectParams{
		InitiativeID: initiativeID,
		ProjectID:    projectID,
		SyncedAt:     syncedAt,
	}); err != nil {
		t.Fatalf("seed initiative_project %s-%s: %v", initiativeID, projectID, err)
	}
}

func projectTeamIDs(t *testing.T, store *db.Store, projectID string) []string {
	t.Helper()
	ids, err := store.Queries().ListProjectTeamIDs(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list project teams: %v", err)
	}
	return ids
}

func initiativeProjectIDs(t *testing.T, store *db.Store, initiativeID string) []string {
	t.Helper()
	ids, err := store.Queries().ListInitiativeProjectIDs(context.Background(), initiativeID)
	if err != nil {
		t.Fatalf("list initiative projects: %v", err)
	}
	return ids
}

// TestTeamMetadataSyncPrunesStaleProjectTeams: an association the (complete)
// projects fetch no longer returns — the project moved off the team or was
// deleted — must go, while returned associations and other teams' rows stay.
func TestTeamMetadataSyncPrunesStaleProjectTeams(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	old := db.Now().Add(-time.Minute)
	seedProjectTeam(t, store, "proj-live", "team-1", old)
	seedProjectTeam(t, store, "proj-stale", "team-1", old)
	seedProjectTeam(t, store, "proj-elsewhere", "team-2", old) // other team: out of scope

	mock := newMockAPIClient()
	mock.projectsByTeam["team-1"] = []api.Project{{ID: "proj-live", Name: "Live", Slug: "live"}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncTeamMetadata(ctx, api.Team{ID: "team-1", Key: "T1"}); err != nil {
		t.Fatalf("syncTeamMetadata: %v", err)
	}

	if got := projectTeamIDs(t, store, "proj-live"); len(got) != 1 || got[0] != "team-1" {
		t.Errorf("proj-live teams = %v, want [team-1] (returned row re-stamped)", got)
	}
	if got := projectTeamIDs(t, store, "proj-stale"); len(got) != 0 {
		t.Errorf("proj-stale teams = %v, want [] (stale association pruned)", got)
	}
	if got := projectTeamIDs(t, store, "proj-elsewhere"); len(got) != 1 || got[0] != "team-2" {
		t.Errorf("proj-elsewhere teams = %v, want [team-2] (other team untouched)", got)
	}
}

// TestTeamMetadataPruneSparesMidSyncAssociation: an association written
// while the metadata fetch is in flight postdates the pre-fetch cutoff and
// must survive, even though the fetch response doesn't contain it.
func TestTeamMetadataPruneSparesMidSyncAssociation(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	mock.projectsByTeam["team-1"] = []api.Project{} // fetch returns nothing…
	mock.onTeamMetadata = func() {
		// …but mid-fetch, an association lands (e.g. a user linking a
		// project through the FUSE write path).
		seedProjectTeam(t, store, "proj-raced", "team-1", db.Now())
	}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncTeamMetadata(ctx, api.Team{ID: "team-1", Key: "T1"}); err != nil {
		t.Fatalf("syncTeamMetadata: %v", err)
	}

	if got := projectTeamIDs(t, store, "proj-raced"); len(got) != 1 {
		t.Errorf("proj-raced teams = %v, want the mid-sync association to survive pruning", got)
	}
}

// TestTeamMetadataFetchErrorPrunesNothing: no fetch, no prune — a failed
// metadata call must leave every association untouched.
func TestTeamMetadataFetchErrorPrunesNothing(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	seedProjectTeam(t, store, "proj-stale", "team-1", db.Now().Add(-time.Minute))

	mock := newMockAPIClient()
	mock.simulateError = errors.New("api down")
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncTeamMetadata(ctx, api.Team{ID: "team-1", Key: "T1"}); err == nil {
		t.Fatal("syncTeamMetadata should surface the fetch error")
	}

	if got := projectTeamIDs(t, store, "proj-stale"); len(got) != 1 {
		t.Errorf("proj-stale teams = %v, want untouched after failed fetch", got)
	}
}

// TestWorkspaceSyncPrunesStaleInitiativeProjects: a junction row the drained
// initiative projects list no longer contains — the project was unlinked in
// Linear — must go; returned links and initiatives absent from the response
// stay untouched.
func TestWorkspaceSyncPrunesStaleInitiativeProjects(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	old := db.Now().Add(-time.Minute)
	seedInitiativeProject(t, store, "init-1", "proj-live", old)
	seedInitiativeProject(t, store, "init-1", "proj-unlinked", old)
	seedInitiativeProject(t, store, "init-2", "proj-keep", old) // not in response: out of scope

	mock := newMockAPIClient()
	mock.initiatives = []api.Initiative{{
		ID: "init-1", Name: "One", Slug: "one",
		Projects: api.InitiativeProjects{Nodes: []api.InitiativeProject{{ID: "proj-live", Name: "Live", Slug: "live"}}},
	}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncWorkspace(ctx); err != nil {
		t.Fatalf("syncWorkspace: %v", err)
	}

	if got := initiativeProjectIDs(t, store, "init-1"); len(got) != 1 || got[0] != "proj-live" {
		t.Errorf("init-1 projects = %v, want [proj-live] (unlinked row pruned)", got)
	}
	if got := initiativeProjectIDs(t, store, "init-2"); len(got) != 1 || got[0] != "proj-keep" {
		t.Errorf("init-2 projects = %v, want [proj-keep] (absent initiative untouched)", got)
	}
}

// TestWorkspaceSyncPruneSparesMidSyncLink: a link the user creates while the
// workspace fetch is in flight (persistInitiativeProjectLink stamps a fresh
// synced_at) postdates the cutoff and must survive.
func TestWorkspaceSyncPruneSparesMidSyncLink(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	mock := newMockAPIClient()
	mock.initiatives = []api.Initiative{{ID: "init-1", Name: "One", Slug: "one"}}
	mock.onWorkspace = func() {
		seedInitiativeProject(t, store, "init-1", "proj-raced", db.Now())
	}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncWorkspace(ctx); err != nil {
		t.Fatalf("syncWorkspace: %v", err)
	}

	if got := initiativeProjectIDs(t, store, "init-1"); len(got) != 1 || got[0] != "proj-raced" {
		t.Errorf("init-1 projects = %v, want the mid-sync link to survive pruning", got)
	}
}

// TestWorkspaceFetchErrorPrunesNothing: a failed workspace fetch must leave
// every junction row untouched.
func TestWorkspaceFetchErrorPrunesNothing(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	seedInitiativeProject(t, store, "init-1", "proj-stale", db.Now().Add(-time.Minute))

	mock := newMockAPIClient()
	mock.simulateError = errors.New("api down")
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncWorkspace(ctx); err == nil {
		t.Fatal("syncWorkspace should surface the fetch error")
	}

	if got := initiativeProjectIDs(t, store, "init-1"); len(got) != 1 {
		t.Errorf("init-1 projects = %v, want untouched after failed fetch", got)
	}
}
