package repo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// The recheck seam (#477), from the write path's side: a mutation Linear
// rejected as "Entity not found" hands the repo a hint, and the repo — not the
// fs layer — decides what the cache does about it. These tests drive the three
// entry points against a server that answers the way Linear does when the
// entity is gone, and assert on the rows that survive.
//
// The pairing is the point. A recheck must prune on Linear's not-found and must
// NOT prune on anything else: the prune is reached only through
// orphanOnNotFound, so an ordinary backend fault (or a rejection that merely
// mentions the phrase) leaves the cache alone. Deleting on the fs layer's own
// verdict would skip that re-ask entirely.

// rejectingServer answers every GraphQL request with one error message, and
// counts the requests so a test can tell "the recheck fired and Linear said no"
// from "the recheck never fired at all".
func rejectingServer(t *testing.T, calls *atomic.Int32, message string) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": message}},
		})
	}))
	t.Cleanup(srv.Close)

	client := api.NewClient("test-key")
	client.SetAPIURL(srv.URL)
	return client
}

// Linear's own wording for an entity that is gone, and a backend fault that is
// not. The first must prune; the second must not. gone() names the entity type
// the way Linear does, since that suffix is what the anchored matcher tolerates
// after the phrase.
func gone(entity string) string {
	return "Entity not found: " + entity + " - Could not find referenced " + entity + "."
}

const backendFaultMessage = "Internal server error"

// waitFor polls a condition the background refresh satisfies asynchronously.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// settle waits for the background refresh to have reached the API and then
// some. Asserting a row still exists immediately would pass even if the prune
// were merely slow, which is the one way this test could lie.
func settle(calls *atomic.Int32) {
	waitFor(func() bool { return calls.Load() > 0 })
	time.Sleep(200 * time.Millisecond)
}

func seedIssueRow(t *testing.T, store *db.Store, issueID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: now, UpdatedAt: now}
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	issue := api.Issue{
		ID: issueID, Identifier: "TST-1", Title: "Gone upstream", Team: &team,
		State:     api.State{ID: "s1", Name: "Todo", Type: "unstarted"},
		CreatedAt: now, UpdatedAt: now,
	}
	row, err := db.APIIssueToDBIssue(issue)
	if err != nil {
		t.Fatalf("convert issue: %v", err)
	}
	if err := store.Queries().UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	// A sub-resource, so the assertion covers the cascade and not just the row.
	if err := store.Queries().UpsertComment(ctx, db.UpsertCommentParams{
		ID: "c-" + issueID, IssueID: issueID, Body: "hi",
		CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
}

func seedProjectRow(t *testing.T, store *db.Store, projectID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	params, err := db.APIProjectToDBProject(api.Project{
		ID: projectID, Name: "Gone Project", Slug: "gone-project", State: "started",
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("convert project: %v", err)
	}
	if err := store.Queries().UpsertProject(ctx, params); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

func seedInitiativeRow(t *testing.T, store *db.Store, initiativeID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	params, err := db.APIInitiativeToDBInitiative(api.Initiative{
		ID: initiativeID, Name: "Gone Initiative", Slug: "gone-initiative",
		Status: "active", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("convert initiative: %v", err)
	}
	if err := store.Queries().UpsertInitiative(ctx, params); err != nil {
		t.Fatalf("seed initiative: %v", err)
	}
}

func issueRowExists(t *testing.T, store *db.Store, issueID string) bool {
	t.Helper()
	_, err := store.Queries().GetIssueByID(context.Background(), issueID)
	return err == nil
}

func projectRowExists(t *testing.T, store *db.Store, projectID string) bool {
	t.Helper()
	_, err := store.Queries().GetProject(context.Background(), projectID)
	return err == nil
}

func initiativeRowExists(t *testing.T, store *db.Store, initiativeID string) bool {
	t.Helper()
	_, err := store.Queries().GetInitiative(context.Background(), initiativeID)
	return err == nil
}

// TestRecheckPrunesOnLinearNotFound: each recheck re-asks Linear, and when
// Linear says the entity is gone the row (and its cascade) leaves the cache.
// Without it the mount keeps listing an entity that no longer exists, keeps
// opening it, and keeps failing the same write until an unrelated read or a
// sync cycle rediscovers the truth.
func TestRecheckPrunesOnLinearNotFound(t *testing.T) {
	t.Parallel()

	t.Run("issue", func(t *testing.T) {
		t.Parallel()
		store, cleanup := setupTestDB(t)
		defer cleanup()
		var calls atomic.Int32
		repo := NewSQLiteRepository(store, rejectingServer(t, &calls, gone("Issue")))
		defer repo.Close()
		seedIssueRow(t, store, "issue-gone")

		repo.RecheckIssue("issue-gone")

		if !waitFor(func() bool { return !issueRowExists(t, store, "issue-gone") }) {
			t.Errorf("issue row survived Linear's not-found (api calls=%d)", calls.Load())
		}
		if got, _ := store.Queries().ListIssueComments(context.Background(), "issue-gone"); len(got) != 0 {
			t.Errorf("orphan cascade left %d comment(s) behind", len(got))
		}
	})

	t.Run("project", func(t *testing.T) {
		t.Parallel()
		store, cleanup := setupTestDB(t)
		defer cleanup()
		var calls atomic.Int32
		repo := NewSQLiteRepository(store, rejectingServer(t, &calls, gone("Project")))
		defer repo.Close()
		seedProjectRow(t, store, "project-gone")

		repo.RecheckProject("project-gone")

		if !waitFor(func() bool { return !projectRowExists(t, store, "project-gone") }) {
			t.Errorf("project row survived Linear's not-found (api calls=%d)", calls.Load())
		}
	})

	t.Run("initiative", func(t *testing.T) {
		t.Parallel()
		store, cleanup := setupTestDB(t)
		defer cleanup()
		var calls atomic.Int32
		repo := NewSQLiteRepository(store, rejectingServer(t, &calls, gone("Initiative")))
		defer repo.Close()
		seedInitiativeRow(t, store, "initiative-gone")

		repo.RecheckInitiative("initiative-gone")

		if !waitFor(func() bool { return !initiativeRowExists(t, store, "initiative-gone") }) {
			t.Errorf("initiative row survived Linear's not-found (api calls=%d)", calls.Load())
		}
	})
}

// TestRecheckKeepsRowsOnOtherFailures is the half that makes the indirection
// worth its cost. The recheck is a hint, not a verdict: a backend fault, or any
// rejection that is not Linear's not-found, must leave the cache exactly as it
// was. Pruning off the fs layer's own classification — which answers on message
// TEXT — would delete a live entity's rows here.
func TestRecheckKeepsRowsOnOtherFailures(t *testing.T) {
	t.Parallel()

	t.Run("issue", func(t *testing.T) {
		t.Parallel()
		store, cleanup := setupTestDB(t)
		defer cleanup()
		var calls atomic.Int32
		repo := NewSQLiteRepository(store, rejectingServer(t, &calls, backendFaultMessage))
		defer repo.Close()
		seedIssueRow(t, store, "issue-live")

		repo.RecheckIssue("issue-live")
		settle(&calls)

		if calls.Load() == 0 {
			t.Fatal("the recheck never reached the API; the assertion below would pass vacuously")
		}
		if !issueRowExists(t, store, "issue-live") {
			t.Error("a backend fault pruned a live issue's cache")
		}
	})

	t.Run("project", func(t *testing.T) {
		t.Parallel()
		store, cleanup := setupTestDB(t)
		defer cleanup()
		var calls atomic.Int32
		repo := NewSQLiteRepository(store, rejectingServer(t, &calls, backendFaultMessage))
		defer repo.Close()
		seedProjectRow(t, store, "project-live")

		repo.RecheckProject("project-live")
		settle(&calls)

		if calls.Load() == 0 {
			t.Fatal("the recheck never reached the API; the assertion below would pass vacuously")
		}
		if !projectRowExists(t, store, "project-live") {
			t.Error("a backend fault pruned a live project's cache")
		}
	})

	t.Run("initiative", func(t *testing.T) {
		t.Parallel()
		store, cleanup := setupTestDB(t)
		defer cleanup()
		var calls atomic.Int32
		repo := NewSQLiteRepository(store, rejectingServer(t, &calls, backendFaultMessage))
		defer repo.Close()
		seedInitiativeRow(t, store, "initiative-live")

		repo.RecheckInitiative("initiative-live")
		settle(&calls)

		if calls.Load() == 0 {
			t.Fatal("the recheck never reached the API; the assertion below would pass vacuously")
		}
		if !initiativeRowExists(t, store, "initiative-live") {
			t.Error("a backend fault pruned a live initiative's cache")
		}
	})
}

// TestRecheckWithoutClientIsInert: fixture mode (and any repo built without an
// API client) must not panic or prune. maybeRefreshSWR returns before it even
// queries, so a recheck there is a no-op by construction.
func TestRecheckWithoutClientIsInert(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewSQLiteRepository(store, nil)
	defer repo.Close()
	seedIssueRow(t, store, "issue-offline")

	repo.RecheckIssue("issue-offline")
	repo.RecheckProject("project-offline")
	repo.RecheckInitiative("initiative-offline")

	if !issueRowExists(t, store, "issue-offline") {
		t.Error("a clientless recheck pruned a row")
	}
}
