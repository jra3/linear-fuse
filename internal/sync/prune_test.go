package sync

import (
	"context"
	"database/sql"
	"encoding/json"
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

func seedLabel(t *testing.T, store *db.Store, id, teamID string, syncedAt time.Time) {
	t.Helper()
	if err := store.Queries().UpsertLabel(context.Background(), db.UpsertLabelParams{
		ID:       id,
		TeamID:   sql.NullString{String: teamID, Valid: true},
		Name:     id,
		SyncedAt: syncedAt,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("seed label %s: %v", id, err)
	}
}

func seedCycle(t *testing.T, store *db.Store, id, teamID string, syncedAt time.Time) {
	t.Helper()
	if err := store.Queries().UpsertCycle(context.Background(), db.UpsertCycleParams{
		ID:       id,
		TeamID:   teamID,
		Number:   1,
		Name:     sql.NullString{String: id, Valid: true},
		SyncedAt: syncedAt,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("seed cycle %s: %v", id, err)
	}
}

// seedMember writes both the workspace user row and the team_members junction
// row (ListTeamMembers joins users), so the seeded membership is observable
// before the prune runs.
func seedMember(t *testing.T, store *db.Store, teamID, userID string, syncedAt time.Time) {
	t.Helper()
	if err := store.Queries().UpsertUser(context.Background(), db.UpsertUserParams{
		ID:       userID,
		Email:    userID + "@test.com",
		Name:     userID,
		SyncedAt: syncedAt,
		Data:     json.RawMessage("{}"),
	}); err != nil {
		t.Fatalf("seed user %s: %v", userID, err)
	}
	if err := store.Queries().UpsertTeamMember(context.Background(), db.UpsertTeamMemberParams{
		TeamID:   teamID,
		UserID:   userID,
		SyncedAt: syncedAt,
	}); err != nil {
		t.Fatalf("seed team_member %s-%s: %v", teamID, userID, err)
	}
}

func teamLabelIDs(t *testing.T, store *db.Store, teamID string) []string {
	t.Helper()
	rows, err := store.Queries().ListTeamLabels(context.Background(), sql.NullString{String: teamID, Valid: true})
	if err != nil {
		t.Fatalf("list team labels: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func teamCycleIDs(t *testing.T, store *db.Store, teamID string) []string {
	t.Helper()
	rows, err := store.Queries().ListTeamCycles(context.Background(), teamID)
	if err != nil {
		t.Fatalf("list team cycles: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func teamMemberIDs(t *testing.T, store *db.Store, teamID string) []string {
	t.Helper()
	rows, err := store.Queries().ListTeamMembers(context.Background(), teamID)
	if err != nil {
		t.Fatalf("list team members: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// contains reports whether ids includes want.
func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestTeamMetadataSyncPrunesStaleLabels: a label the drained labels fetch no
// longer returns — renamed or deleted in Linear — must go; the returned label
// stays. (Only team-scoped labels are seeded; workspace labels re-arrive on
// every team fetch and so are always refreshed above the cutoff.)
func TestTeamMetadataSyncPrunesStaleLabels(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	old := db.Now().Add(-time.Minute)
	seedLabel(t, store, "label-live", "team-1", old)
	seedLabel(t, store, "label-stale", "team-1", old)

	mock := newMockAPIClient()
	mock.labelsByTeam["team-1"] = []api.Label{{ID: "label-live", Name: "Live"}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncTeamMetadata(ctx, api.Team{ID: "team-1", Key: "T1"}); err != nil {
		t.Fatalf("syncTeamMetadata: %v", err)
	}

	got := teamLabelIDs(t, store, "team-1")
	if !contains(got, "label-live") {
		t.Errorf("labels = %v, want label-live retained (returned row re-stamped)", got)
	}
	if contains(got, "label-stale") {
		t.Errorf("labels = %v, want label-stale pruned", got)
	}
}

// TestTeamMetadataSyncPrunesStaleCycles: a cycle absent from the drained fetch
// is pruned; a returned cycle survives.
func TestTeamMetadataSyncPrunesStaleCycles(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	old := db.Now().Add(-time.Minute)
	seedCycle(t, store, "cycle-live", "team-1", old)
	seedCycle(t, store, "cycle-stale", "team-1", old)
	seedCycle(t, store, "cycle-elsewhere", "team-2", old) // other team: out of scope

	mock := newMockAPIClient()
	mock.cyclesByTeam["team-1"] = []api.Cycle{{ID: "cycle-live", Number: 1, Name: "Live"}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncTeamMetadata(ctx, api.Team{ID: "team-1", Key: "T1"}); err != nil {
		t.Fatalf("syncTeamMetadata: %v", err)
	}

	got := teamCycleIDs(t, store, "team-1")
	if !contains(got, "cycle-live") || contains(got, "cycle-stale") {
		t.Errorf("team-1 cycles = %v, want [cycle-live] (stale pruned)", got)
	}
	if other := teamCycleIDs(t, store, "team-2"); !contains(other, "cycle-elsewhere") {
		t.Errorf("team-2 cycles = %v, want cycle-elsewhere untouched", other)
	}
}

// TestTeamMetadataSyncPrunesStaleMembers: a departed member is pruned from the
// team_members junction; the returned member stays and another team's roster is
// untouched. The workspace users table is not pruned.
func TestTeamMetadataSyncPrunesStaleMembers(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	old := db.Now().Add(-time.Minute)
	seedMember(t, store, "team-1", "user-live", old)
	seedMember(t, store, "team-1", "user-gone", old)
	seedMember(t, store, "team-2", "user-elsewhere", old) // other team: out of scope

	mock := newMockAPIClient()
	mock.membersByTeam["team-1"] = []api.User{{ID: "user-live", Email: "user-live@test.com", Name: "Live"}}
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncTeamMetadata(ctx, api.Team{ID: "team-1", Key: "T1"}); err != nil {
		t.Fatalf("syncTeamMetadata: %v", err)
	}

	got := teamMemberIDs(t, store, "team-1")
	if !contains(got, "user-live") || contains(got, "user-gone") {
		t.Errorf("team-1 members = %v, want [user-live] (departed member pruned)", got)
	}
	if other := teamMemberIDs(t, store, "team-2"); !contains(other, "user-elsewhere") {
		t.Errorf("team-2 members = %v, want user-elsewhere untouched", other)
	}
}

// TestTeamMetadataFetchErrorSparesMetadata: a failed metadata fetch prunes
// nothing — labels, cycles, and members all survive.
func TestTeamMetadataFetchErrorSparesMetadata(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	old := db.Now().Add(-time.Minute)
	seedLabel(t, store, "label-stale", "team-1", old)
	seedCycle(t, store, "cycle-stale", "team-1", old)
	seedMember(t, store, "team-1", "user-stale", old)

	mock := newMockAPIClient()
	mock.simulateError = errors.New("api down")
	worker := NewWorker(mock, store, Config{Interval: time.Hour})

	if err := worker.syncTeamMetadata(ctx, api.Team{ID: "team-1", Key: "T1"}); err == nil {
		t.Fatal("syncTeamMetadata should surface the fetch error")
	}

	if !contains(teamLabelIDs(t, store, "team-1"), "label-stale") {
		t.Error("label-stale pruned after failed fetch, want untouched")
	}
	if !contains(teamCycleIDs(t, store, "team-1"), "cycle-stale") {
		t.Error("cycle-stale pruned after failed fetch, want untouched")
	}
	if !contains(teamMemberIDs(t, store, "team-1"), "user-stale") {
		t.Error("user-stale pruned after failed fetch, want untouched")
	}
}

// TestDeferDetailIssues: every issue handed to the shared defer helper lands in
// pending_detail_sync so a later cycle can retry it.
func TestDeferDetailIssues(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	worker := NewWorker(newMockAPIClient(), store, Config{Interval: time.Hour})
	worker.deferDetailIssues(ctx, []struct {
		ID         string
		Identifier string
	}{
		{ID: "issue-1", Identifier: "TST-1"},
		{ID: "issue-2", Identifier: "TST-2"},
	})

	rows, err := store.Queries().ListPendingDetailSync(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("pending rows = %d, want 2", len(rows))
	}
	seen := map[string]string{}
	for _, r := range rows {
		seen[r.IssueID] = r.Identifier
	}
	if seen["issue-1"] != "TST-1" || seen["issue-2"] != "TST-2" {
		t.Errorf("pending rows = %v, want issue-1/TST-1 and issue-2/TST-2", seen)
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
