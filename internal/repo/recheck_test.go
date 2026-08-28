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

// TestRecheckIgnoresStaleness is the case the SWR gate suppressed. A recheck
// does not ask "has this fallen behind?" — it asks "does this still exist?",
// and an entity deleted upstream looks FRESH forever by every staleness measure
// the specs have: its updated_at stops moving, its doc rows stop arriving, and
// the very browse that walked into the collection stamps the sync instant. Route
// the recheck through maybeRefreshSWR and the hint is dropped without a single
// request, so the mount keeps listing the dead row and keeps failing the same
// write — exactly #477's symptom, on the surfaces #477 is about.
//
// Each subtest seeds the "fresh" shape for its spec's own flavor: the issue's
// detail_synced_at AFTER its updated_at (event-driven), and a just-synced
// document row for the project and initiative (TTL).
func TestRecheckIgnoresStaleness(t *testing.T) {
	t.Parallel()

	t.Run("issue detail-synced after its last change", func(t *testing.T) {
		t.Parallel()
		store, cleanup := setupTestDB(t)
		defer cleanup()
		var calls atomic.Int32
		repo := NewSQLiteRepository(store, rejectingServer(t, &calls, gone("Issue")))
		defer repo.Close()
		seedIssueRow(t, store, "issue-fresh")
		stampIssueDetailsFresh(t, store, "issue-fresh")

		repo.RecheckIssue("issue-fresh")

		if !waitFor(func() bool { return !issueRowExists(t, store, "issue-fresh") }) {
			t.Errorf("a detail-fresh issue survived Linear's not-found: the staleness gate ate the recheck (api calls=%d)", calls.Load())
		}
	})

	t.Run("project with just-synced docs", func(t *testing.T) {
		t.Parallel()
		store, cleanup := setupTestDB(t)
		defer cleanup()
		var calls atomic.Int32
		repo := NewSQLiteRepository(store, rejectingServer(t, &calls, gone("Project")))
		defer repo.Close()
		seedProjectRow(t, store, "project-fresh")
		seedSyncedDocument(t, store, "doc-project-fresh", "project_id", "project-fresh")

		repo.RecheckProject("project-fresh")

		if !waitFor(func() bool { return !projectRowExists(t, store, "project-fresh") }) {
			t.Errorf("a docs-fresh project survived Linear's not-found: the staleness gate ate the recheck (api calls=%d)", calls.Load())
		}
	})

	t.Run("initiative with just-synced docs", func(t *testing.T) {
		t.Parallel()
		store, cleanup := setupTestDB(t)
		defer cleanup()
		var calls atomic.Int32
		repo := NewSQLiteRepository(store, rejectingServer(t, &calls, gone("Initiative")))
		defer repo.Close()
		seedInitiativeRow(t, store, "initiative-fresh")
		seedSyncedDocument(t, store, "doc-initiative-fresh", "initiative_id", "initiative-fresh")

		repo.RecheckInitiative("initiative-fresh")

		if !waitFor(func() bool { return !initiativeRowExists(t, store, "initiative-fresh") }) {
			t.Errorf("a docs-fresh initiative survived Linear's not-found: the staleness gate ate the recheck (api calls=%d)", calls.Load())
		}
	})
}

// stampIssueDetailsFresh makes an issue look fully detail-synced: the stamp
// lands AFTER updated_at, which is what swrStale's event-driven flavor reads as
// fresh. Any prior browse of comments/, docs/ or attachments/ leaves exactly
// this shape behind.
func stampIssueDetailsFresh(t *testing.T, store *db.Store, issueID string) {
	t.Helper()
	if err := store.Queries().StampIssueDetailSynced(context.Background(), db.StampIssueDetailSyncedParams{
		DetailSyncedAt: db.ToNullTime(time.Now().Add(time.Minute)), ID: issueID,
	}); err != nil {
		t.Fatalf("stamp issue detail synced: %v", err)
	}
}

// seedSyncedDocument gives a project or initiative a document row synced just
// now, which is what the TTL flavor reads as fresh (the docs specs derive their
// staleness from MAX(synced_at) over these rows).
func seedSyncedDocument(t *testing.T, store *db.Store, docID, ownerColumn, ownerID string) {
	t.Helper()
	now := time.Now()
	params := db.UpsertDocumentParams{
		ID: docID, SlugID: docID, Title: "Doc",
		CreatedAt: sql.NullTime{Time: now, Valid: true},
		UpdatedAt: sql.NullTime{Time: now, Valid: true},
		SyncedAt:  now, Data: []byte("{}"),
	}
	owner := sql.NullString{String: ownerID, Valid: true}
	switch ownerColumn {
	case "project_id":
		params.ProjectID = owner
	case "initiative_id":
		params.InitiativeID = owner
	default:
		t.Fatalf("unknown owner column %q", ownerColumn)
	}
	if err := store.Queries().UpsertDocument(context.Background(), params); err != nil {
		t.Fatalf("seed document: %v", err)
	}
}

// TestRecheckWithoutClientIsInert: fixture mode (and any repo built without an
// API client) must not panic or prune. Bypassing the staleness gate does not
// bypass this — triggerBackgroundRefresh returns on a nil client before it
// starts anything, so a recheck there is still a no-op by construction.
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
