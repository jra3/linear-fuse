package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// The label catalog's SWR surface (#475). Before it, a label reached SQLite
// only on the sync worker's FULL cycle, so an out-of-band description edit was
// invisible to every label reader for FullSyncInterval + Interval — ~12 minutes
// measured against a live mount, with no marker saying so.

// labelServer stands in for Linear's team.labels connection: it answers the
// drained query with the given labels and counts the calls, so a test can
// assert both what landed in SQLite and whether the network was touched at all.
func labelServer(t *testing.T, calls *atomic.Int32, labels ...api.Label) *api.Client {
	t.Helper()
	nodes := make([]map[string]any, 0, len(labels))
	for _, l := range labels {
		node := map[string]any{"id": l.ID, "name": l.Name, "color": l.Color, "description": l.Description}
		if l.Team != nil {
			node["team"] = map[string]any{"id": l.Team.ID}
		}
		nodes = append(nodes, node)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"team": map[string]any{
					"labels": map[string]any{
						"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
						"nodes":    nodes,
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client := api.NewClient("test-key")
	client.SetAPIURL(srv.URL)
	return client
}

// seedLabel writes a label row with an explicit synced_at. Times come from
// db.Now() (UTC), never time.Now(): synced_at strings keep their zone verbatim
// and the prune compares them textually, so a local-zone seed sorts by its
// wall-clock face and a positive-offset TZ would read an hour-old row as newer
// than the cutoff (internal/sync/prune_test.go states the invariant).
func seedLabel(t *testing.T, store *db.Store, label api.Label, syncedAt time.Time) {
	t.Helper()
	params, err := db.APILabelToDBLabel(label)
	if err != nil {
		t.Fatalf("convert %s: %v", label.ID, err)
	}
	params.SyncedAt = syncedAt
	if err := store.Queries().UpsertLabel(context.Background(), params); err != nil {
		t.Fatalf("seed %s: %v", label.ID, err)
	}
}

// seedCatalogStamp places a team's label-catalog freshness stamp, the signal
// the SWR trigger actually reads. It is deliberately independent of the label
// rows — see db.TeamLabelsScheduleKey.
func seedCatalogStamp(t *testing.T, store *db.Store, teamID string, at time.Time) {
	t.Helper()
	if err := store.Queries().UpsertSyncSchedule(context.Background(), db.UpsertSyncScheduleParams{
		Key:     db.TeamLabelsScheduleKey(teamID),
		LastRun: at,
	}); err != nil {
		t.Fatalf("seed catalog stamp %s: %v", teamID, err)
	}
}

// catalogStamp reads a team's stamp back; ok=false means no row (never
// stamped), which is what a never-synced catalog and a failed refresh both
// look like.
func catalogStamp(t *testing.T, store *db.Store, teamID string) (time.Time, bool) {
	t.Helper()
	at, err := store.Queries().GetSyncSchedule(context.Background(), db.TeamLabelsScheduleKey(teamID))
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// waitForCatalogStamp polls for the background refresh's stamp, the same
// "lands within a bound" shape as waitForDescription.
func waitForCatalogStamp(t *testing.T, store *db.Store, teamID string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := catalogStamp(t, store, teamID); ok {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func labelByID(t *testing.T, labels []api.Label, id string) *api.Label {
	t.Helper()
	for i := range labels {
		if labels[i].ID == id {
			return &labels[i]
		}
	}
	return nil
}

// waitForDescription polls SQLite for a background refresh's effect. The
// refresh is asynchronous by design (that is what the "while-revalidate" half
// means), so the assertion is "lands within a bound", never "landed already".
func waitForDescription(t *testing.T, store *db.Store, id, want string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, err := store.Queries().GetLabel(context.Background(), id)
		if err == nil && db.NullStringValue(row.Description) == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestGetTeamLabelsTriggersRefreshWhenStale is the wiring test for the hook's
// placement: the trigger lives on the repository read, not on the FUSE
// directory node, because a bare Lookup — `cat labels/@home.md`, the reported
// access pattern — never runs Readdir. It also pins the SWR contract itself:
// the triggering read still answers from cache, and the fresh value arrives
// behind it.
func TestGetTeamLabelsTriggersRefreshWhenStale(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	stale := db.Now().Add(-defaultStalenessThreshold - time.Minute)
	seedLabel(t, store, api.Label{
		ID: "l-home", Name: "@home", Color: "#95A2B3",
		Description: "STALE-DESCRIPTION", Team: &api.Team{ID: "team-1"},
	}, stale)
	seedCatalogStamp(t, store, "team-1", stale)

	var calls atomic.Int32
	client := labelServer(t, &calls, api.Label{
		ID: "l-home", Name: "@home", Color: "#95A2B3",
		Description: "FRESH-DESCRIPTION", Team: &api.Team{ID: "team-1"},
	})
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	// The read that finds the catalog stale still answers from SQLite: SWR
	// never blocks a FUSE read on the network.
	got, err := repo.GetTeamLabels(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeamLabels: %v", err)
	}
	if l := labelByID(t, got, "l-home"); l == nil || l.Description != "STALE-DESCRIPTION" {
		t.Fatalf("triggering read should serve cached bytes, got %+v", got)
	}

	if !waitForDescription(t, store, "l-home", "FRESH-DESCRIPTION") {
		t.Fatal("background refresh never landed the fresh description in SQLite")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly one labels fetch, got %d", n)
	}

	// And the next read serves it — the converged state the mount reaches
	// within a kernel revalidation.
	got, err = repo.GetTeamLabels(ctx, "team-1")
	if err != nil {
		t.Fatalf("second GetTeamLabels: %v", err)
	}
	if l := labelByID(t, got, "l-home"); l == nil || l.Description != "FRESH-DESCRIPTION" {
		t.Errorf("second read still stale: %+v", got)
	}
}

// TestGetTeamLabelsFreshCatalogTouchesNothing is the other half of the TTL
// contract: a catalog synced inside the threshold must not fetch. Without it
// every label browse would be an API call, which is how a read path turns into
// a per-browse loop.
func TestGetTeamLabelsFreshCatalogTouchesNothing(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	seedLabel(t, store, api.Label{
		ID: "l-fresh", Name: "quick", Team: &api.Team{ID: "team-1"},
	}, db.Now())
	seedCatalogStamp(t, store, "team-1", db.Now())

	var calls atomic.Int32
	repo := NewSQLiteRepository(store, labelServer(t, &calls))
	defer repo.Close()

	for i := 0; i < 3; i++ {
		if _, err := repo.GetTeamLabels(context.Background(), "team-1"); err != nil {
			t.Fatalf("GetTeamLabels: %v", err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Errorf("fresh catalog fetched %d times; want 0", n)
	}
}

// TestGetTeamLabelsEmptyCatalogRefreshesOnceThenQuiets pins why freshness is a
// stamp and not an aggregate over the label rows. A team with NO labels — its
// own or workspace-scoped — has no row to carry a synced_at, so a row-derived
// signal reads "never synced" forever: the refresh fetches an empty catalog,
// upserts nothing, and every following browse re-fires it. That is the
// permanent per-browse API loop detail_synced_at was introduced to escape, and
// this path is hot (the labels/ listing, each label file's Lookup, labels.md,
// the by/label views, the write path's resolver all land here). The stamp is
// written by the refresh itself, so the empty catalog converges: one fetch.
func TestGetTeamLabelsEmptyCatalogRefreshesOnceThenQuiets(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	var calls atomic.Int32
	repo := NewSQLiteRepository(store, labelServer(t, &calls))
	defer repo.Close()

	got, err := repo.GetTeamLabels(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("GetTeamLabels: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected an empty catalog, got %+v", got)
	}
	if !waitForCatalogStamp(t, store, "team-1") {
		t.Fatal("an empty catalog never recorded that it was checked")
	}

	for i := 0; i < 3; i++ {
		if _, err := repo.GetTeamLabels(context.Background(), "team-1"); err != nil {
			t.Fatalf("GetTeamLabels: %v", err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("label-less team fetched %d times; want exactly 1", n)
	}
}

// TestGetTeamLabelsStampIsPerTeam pins the other hole a row-derived signal
// has. A team's catalog is its own labels PLUS the workspace labels (team_id
// NULL), and those rows are SHARED: an aggregate over that union lets team-1's
// refresh re-stamp the workspace rows and so declare team-2 fresh, suppressing
// exactly the refresh #475 exists to fire. Each team's stamp is its own, so
// team-2's first read still goes to the network.
func TestGetTeamLabelsStampIsPerTeam(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// The shared row both teams' catalogs serve, plus one label each.
	seedLabel(t, store, api.Label{ID: "l-ws", Name: "Bug"}, db.Now())
	seedLabel(t, store, api.Label{ID: "l-one", Name: "one", Team: &api.Team{ID: "team-1"}}, db.Now())
	seedLabel(t, store, api.Label{ID: "l-two", Name: "two", Team: &api.Team{ID: "team-2"}}, db.Now())

	var calls atomic.Int32
	repo := NewSQLiteRepository(store, labelServer(t, &calls,
		api.Label{ID: "l-ws", Name: "Bug"},
		api.Label{ID: "l-one", Name: "one", Team: &api.Team{ID: "team-1"}}))
	defer repo.Close()

	if _, err := repo.GetTeamLabels(context.Background(), "team-1"); err != nil {
		t.Fatalf("team-1 GetTeamLabels: %v", err)
	}
	if !waitForCatalogStamp(t, store, "team-1") {
		t.Fatal("team-1's refresh never stamped its catalog")
	}
	if _, ok := catalogStamp(t, store, "team-2"); ok {
		t.Fatal("team-1's refresh stamped team-2's catalog")
	}

	// team-2 has never been refreshed, so its own read must still fire.
	if _, err := repo.GetTeamLabels(context.Background(), "team-2"); err != nil {
		t.Fatalf("team-2 GetTeamLabels: %v", err)
	}
	if !waitForCatalogStamp(t, store, "team-2") {
		t.Fatal("team-2 read as fresh off team-1's refresh: no refresh fired")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("expected one fetch per team, got %d", n)
	}
}

// TestRefreshTeamLabelsPrunes pins what the completeness license buys: the
// drained fetch is the whole team catalog, so a label deleted in Linear leaves
// SQLite on the refresh rather than lingering to the next full sync cycle.
// Workspace labels are NOT the fetch's to remove — PruneTeamLabels matches
// team_id = ?, and a NULL team_id never matches — so one must survive a pass
// that did not return it.
func TestRefreshTeamLabelsPrunes(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	old := db.Now().Add(-time.Hour)
	seedLabel(t, store, api.Label{ID: "l-keep", Name: "keep", Team: &api.Team{ID: "team-1"}}, old)
	seedLabel(t, store, api.Label{ID: "l-deleted", Name: "gone", Team: &api.Team{ID: "team-1"}}, old)
	seedLabel(t, store, api.Label{ID: "l-other-team", Name: "theirs", Team: &api.Team{ID: "team-2"}}, old)
	seedLabel(t, store, api.Label{ID: "l-workspace", Name: "Bug"}, old)

	client := labelServer(t, nil, api.Label{ID: "l-keep", Name: "keep", Team: &api.Team{ID: "team-1"}})
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	if err := repo.refreshTeamLabels(ctx, "team-1"); err != nil {
		t.Fatalf("refreshTeamLabels: %v", err)
	}

	for _, tc := range []struct {
		id       string
		wantGone bool
	}{
		{"l-keep", false},
		{"l-deleted", true},
		{"l-other-team", false},
		{"l-workspace", false},
	} {
		_, err := store.Queries().GetLabel(ctx, tc.id)
		gone := err == sql.ErrNoRows
		if gone != tc.wantGone {
			t.Errorf("%s: gone=%v want %v (err=%v)", tc.id, gone, tc.wantGone, err)
		}
	}

	if _, ok := catalogStamp(t, store, "team-1"); !ok {
		t.Error("a clean drained pass left the catalog unstamped")
	}
}

// TestRefreshTeamLabelsFailureKeepsRows: a failed fetch must leave the cache
// exactly as it was. An empty result read as "everything was deleted" would
// blank the catalog the mount serves.
func TestRefreshTeamLabelsFailureKeepsRows(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	seedLabel(t, store, api.Label{ID: "l-keep", Name: "keep", Team: &api.Team{ID: "team-1"}}, db.Now().Add(-time.Hour))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	client := api.NewClient("test-key")
	client.SetAPIURL(srv.URL)
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	if err := repo.refreshTeamLabels(ctx, "team-1"); err == nil {
		t.Fatal("expected the failed fetch to return an error")
	}
	if _, err := store.Queries().GetLabel(ctx, "l-keep"); err != nil {
		t.Errorf("failed refresh removed a cached label: %v", err)
	}
}

// TestRefreshTeamLabelsFailureLeavesStampAlone: a failed fetch must not record
// that the catalog was checked. Stamping one would suppress the next read's
// refresh for the whole threshold, converting a transient API failure into
// five more minutes of the staleness #475 is about — so the previous stamp
// stands untouched and the next read retries.
func TestRefreshTeamLabelsFailureLeavesStampAlone(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	stale := db.Now().Add(-defaultStalenessThreshold - time.Minute)
	seedCatalogStamp(t, store, "team-1", stale)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	client := api.NewClient("test-key")
	client.SetAPIURL(srv.URL)
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	if err := repo.refreshTeamLabels(ctx, "team-1"); err == nil {
		t.Fatal("expected the failed fetch to return an error")
	}
	at, ok := catalogStamp(t, store, "team-1")
	if !ok {
		t.Fatal("failed refresh deleted the previous stamp")
	}
	if at.After(stale) {
		t.Fatalf("failed refresh advanced the stamp to %s (was %s)", at, stale)
	}

	// Still stale, so the read path tries again rather than going quiet.
	before := calls.Load()
	if _, err := repo.GetTeamLabels(ctx, "team-1"); err != nil {
		t.Fatalf("GetTeamLabels: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && calls.Load() == before {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() == before {
		t.Error("the read after a failed refresh did not retry")
	}
}
