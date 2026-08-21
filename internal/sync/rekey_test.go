package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// The scenario every test here shares: a team cached under key TST, renamed
// server-side to QA. Linear re-keys the issues immediately and bumps nothing,
// so the incremental cursor cannot see it (#427).

const renameFixedTime = "2026-08-01T12:00:00Z"

func rekeyTime(t *testing.T) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, renameFixedTime)
	if err != nil {
		t.Fatalf("parse fixed time: %v", err)
	}
	return ts
}

// seedCachedIssue writes an issue row exactly as a pre-rename sync left it:
// the identifier and the team key inside the data blob both spell the OLD key,
// while team_id points at the team that was renamed.
func seedCachedIssue(t *testing.T, store *db.Store, teamID, teamKey, issueID, identifier string, updatedAt time.Time) {
	t.Helper()
	issue := api.Issue{
		ID:         issueID,
		Identifier: identifier,
		Title:      identifier,
		Team:       &api.Team{ID: teamID, Key: teamKey},
		CreatedAt:  updatedAt,
		UpdatedAt:  updatedAt,
	}
	data, err := db.APIIssueToDBIssue(issue)
	if err != nil {
		t.Fatalf("convert %s: %v", identifier, err)
	}
	if err := store.Queries().UpsertIssue(context.Background(), data.ToUpsertParams()); err != nil {
		t.Fatalf("seed %s: %v", identifier, err)
	}
}

// seedTeamRow writes a teams row. Every cached issue has one in practice:
// LinearFS never prunes teams, so a team that drops out of the API's team list
// keeps its row (and its key) in the cache indefinitely.
func seedTeamRow(t *testing.T, store *db.Store, teamID, key, name string) {
	t.Helper()
	team := api.Team{ID: teamID, Key: key, Name: name}
	if err := store.Queries().UpsertTeam(context.Background(), db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team %s: %v", teamID, err)
	}
}

func seedWatermark(t *testing.T, store *db.Store, teamID string, at time.Time) {
	t.Helper()
	err := store.Queries().UpsertSyncMeta(context.Background(), db.UpsertSyncMetaParams{
		TeamID:             teamID,
		LastSyncedAt:       at,
		LastIssueUpdatedAt: db.ToNullTime(at),
		IssueCount:         db.ToNullInt64(0),
	})
	if err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
}

// serverIssue is what Linear returns after the rename: same UUIDs, same
// updatedAt, new identifiers.
//
// The nested team carries its KEY, not just its id, because the IssueFields
// fragment selects team { id key name } — and that nested key is what lands in
// the stored blob, which is the only thing the mount renders (DBIssueToAPIIssue
// is a bare unmarshal with no team join). A mock returning a keyless team would
// let a rebuild "pass" while leaving every symlink target and every issue.md
// frontmatter line blank. The key is the identifier's prefix by construction,
// so the two cannot drift apart here the way they do in a stale cache.
func serverIssue(id, identifier, teamID string, updatedAt time.Time) api.Issue {
	key, _, _ := strings.Cut(identifier, "-")
	return api.Issue{
		ID:         id,
		Identifier: identifier,
		Title:      identifier,
		Team:       &api.Team{ID: teamID, Key: key},
		CreatedAt:  updatedAt,
		UpdatedAt:  updatedAt,
	}
}

// blobIdentity reads the identifier and team key back out of the stored data
// blob. These are the values the mount actually renders: DBIssueToAPIIssue is
// a bare unmarshal of the blob with no team join, so every symlink target and
// every issue.md frontmatter line comes from here, not from the column.
func blobIdentity(t *testing.T, store *db.Store, issueID string) (identifier, teamKey string) {
	t.Helper()
	row, err := store.Queries().GetIssueByID(context.Background(), issueID)
	if err != nil {
		t.Fatalf("get issue %s: %v", issueID, err)
	}
	var blob struct {
		Identifier string `json:"identifier"`
		Team       struct {
			Key string `json:"key"`
		} `json:"team"`
	}
	if err := json.Unmarshal(row.Data, &blob); err != nil {
		t.Fatalf("unmarshal issue %s data: %v", issueID, err)
	}
	return blob.Identifier, blob.Team.Key
}

// TestKeyRenameRebuildsTeam is the headline case: a rename with no updatedAt
// movement, on a cache that a previous cycle already filled. One full cycle
// must leave the column, the blob's identifier and the blob's team key all
// correct.
func TestKeyRenameRebuildsTeam(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	at := rekeyTime(t)

	// A cache as a pre-rename cycle left it, watermark included: without the
	// repair the walk below stops on the first unchanged page and nothing is
	// ever re-fetched.
	seedCachedIssue(t, store, "team-1", "TST", "issue-1", "TST-1", at)
	seedCachedIssue(t, store, "team-1", "TST", "issue-2", "TST-2", at)
	seedWatermark(t, store, "team-1", at)

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "QA", Name: "Quality"}}
	mock.issuesByTeam["team-1"] = []api.Issue{
		serverIssue("issue-1", "QA-1", "team-1", at),
		serverIssue("issue-2", "QA-2", "team-1", at),
	}

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	if err := worker.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}

	for _, want := range []struct{ id, identifier string }{{"issue-1", "QA-1"}, {"issue-2", "QA-2"}} {
		row, err := store.Queries().GetIssueByID(ctx, want.id)
		if err != nil {
			t.Fatalf("get %s: %v", want.id, err)
		}
		if row.Identifier != want.identifier {
			t.Errorf("%s identifier column = %q, want %q", want.id, row.Identifier, want.identifier)
		}
		ident, key := blobIdentity(t, store, want.id)
		if ident != want.identifier {
			t.Errorf("%s blob identifier = %q, want %q", want.id, ident, want.identifier)
		}
		if key != "QA" {
			t.Errorf("%s blob team key = %q, want %q", want.id, key, "QA")
		}
	}

	if _, err := store.Queries().GetIssueByIdentifier(ctx, "QA-1"); err != nil {
		t.Errorf("QA-1 should resolve: %v", err)
	}
	if _, err := store.Queries().GetIssueByIdentifier(ctx, "TST-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("TST-1 should be gone, got err=%v", err)
	}
}

// TestRebuildDeletesDependentRows: the rebuild deletes issues, and everything
// keyed off an issue has to go with them or it is unreachable forever.
func TestRebuildDeletesDependentRows(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	q := store.Queries()
	at := rekeyTime(t)

	seedCachedIssue(t, store, "team-1", "TST", "issue-1", "TST-1", at)
	seedWatermark(t, store, "team-1", at)

	if err := q.UpsertComment(ctx, db.UpsertCommentParams{
		ID: "c-1", IssueID: "issue-1", Body: "hi", CreatedAt: at, UpdatedAt: at, SyncedAt: at, Data: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	doc, err := db.APIDocumentToDBDocument(api.Document{
		ID: "d-1", Title: "Spec", Issue: &api.Issue{ID: "issue-1"}, CreatedAt: at, UpdatedAt: at,
	})
	if err != nil {
		t.Fatalf("convert document: %v", err)
	}
	if err := q.UpsertDocument(ctx, doc); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	att, err := db.APIAttachmentToDBAttachment(api.Attachment{
		ID: "a-1", Title: "PR", URL: "https://example.invalid/pr", CreatedAt: at, UpdatedAt: at,
	}, "issue-1")
	if err != nil {
		t.Fatalf("convert attachment: %v", err)
	}
	if err := q.UpsertAttachment(ctx, att); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	if err := q.UpsertEmbeddedFile(ctx, db.UpsertEmbeddedFileParams{
		ID: "f-1", IssueID: "issue-1", Url: "https://uploads.invalid/x.png", Filename: "x.png",
		Source: "description", CreatedAt: at, SyncedAt: at,
	}); err != nil {
		t.Fatalf("seed embedded file: %v", err)
	}
	if err := q.UpsertIssueRelation(ctx, db.IssueRelationUpsertParams(api.IssueRelation{
		ID: "r-1", Type: "blocks", RelatedIssue: &api.ParentIssue{ID: "issue-other"}, CreatedAt: at, UpdatedAt: at,
	}, "issue-1", "issue-other")); err != nil {
		t.Fatalf("seed relation: %v", err)
	}
	if err := q.UpsertIssueHistoryCache(ctx, db.UpsertIssueHistoryCacheParams{
		IssueID: "issue-1", SyncedAt: at, Data: json.RawMessage(`[]`),
	}); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	if err := q.UpsertPendingDetailSync(ctx, db.UpsertPendingDetailSyncParams{
		IssueID: "issue-1", Identifier: "TST-1", QueuedAt: at,
	}); err != nil {
		t.Fatalf("seed pending detail sync: %v", err)
	}

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "QA", Name: "Quality"}}
	mock.issuesByTeam["team-1"] = []api.Issue{serverIssue("issue-new", "QA-1", "team-1", at)}

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	if err := worker.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}

	if comments, err := q.ListIssueComments(ctx, "issue-1"); err != nil || len(comments) != 0 {
		t.Errorf("comments for the deleted issue: n=%d err=%v", len(comments), err)
	}
	if docs, err := q.ListIssueDocuments(ctx, sql.NullString{String: "issue-1", Valid: true}); err != nil || len(docs) != 0 {
		t.Errorf("documents for the deleted issue: n=%d err=%v", len(docs), err)
	}
	if atts, err := q.ListIssueAttachments(ctx, "issue-1"); err != nil || len(atts) != 0 {
		t.Errorf("attachments for the deleted issue: n=%d err=%v", len(atts), err)
	}
	if files, err := q.ListIssueEmbeddedFiles(ctx, "issue-1"); err != nil || len(files) != 0 {
		t.Errorf("embedded files for the deleted issue: n=%d err=%v", len(files), err)
	}
	if rels, err := q.ListIssueRelations(ctx, "issue-1"); err != nil || len(rels) != 0 {
		t.Errorf("relations for the deleted issue: n=%d err=%v", len(rels), err)
	}
	if _, err := q.GetIssueHistoryCache(ctx, "issue-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("history cache for the deleted issue survived: err=%v", err)
	}
	for _, p := range mustListPending(t, store) {
		if p.IssueID == "issue-1" {
			t.Error("pending detail sync for the deleted issue survived")
		}
	}
}

func mustListPending(t *testing.T, store *db.Store) []db.ListPendingDetailSyncRow {
	t.Helper()
	rows, err := store.Queries().ListPendingDetailSync(context.Background())
	if err != nil {
		t.Fatalf("list pending detail sync: %v", err)
	}
	return rows
}

// TestHealthyCycleDoesNoDriftWork: with the key unchanged the drift count is
// zero, so the cycle keeps its ordinary incremental shape — one page, stopping
// on the unchanged issues — and nothing is deleted.
func TestHealthyCycleDoesNoDriftWork(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	at := rekeyTime(t)

	seedCachedIssue(t, store, "team-1", "TST", "issue-1", "TST-1", at)
	seedWatermark(t, store, "team-1", at)

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}
	mock.issuesByTeam["team-1"] = []api.Issue{serverIssue("issue-1", "TST-1", "team-1", at)}

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	if err := worker.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}

	if got := mock.getIssuesCalls; got != 1 {
		t.Errorf("issue pages fetched = %d, want 1 (the incremental shape)", got)
	}
	meta, err := store.Queries().GetSyncMeta(ctx, "team-1")
	if err != nil {
		t.Fatalf("watermark should survive a healthy cycle: %v", err)
	}
	if !meta.LastIssueUpdatedAt.Valid {
		t.Error("watermark cleared on a healthy cycle")
	}
	if _, err := store.Queries().GetIssueByIdentifier(ctx, "TST-1"); err != nil {
		t.Errorf("TST-1 should still be cached: %v", err)
	}
}

// TestDriftCheckDisarmsAfterRebuild guards against the failure mode where the
// repair never converges and every cycle walks the whole team forever.
func TestDriftCheckDisarmsAfterRebuild(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	at := rekeyTime(t)

	seedCachedIssue(t, store, "team-1", "TST", "issue-1", "TST-1", at)
	seedWatermark(t, store, "team-1", at)

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "QA", Name: "Quality"}}
	mock.issuesByTeam["team-1"] = []api.Issue{serverIssue("issue-1", "QA-1", "team-1", at)}

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	if err := worker.SyncNow(ctx); err != nil {
		t.Fatalf("rebuild cycle: %v", err)
	}
	afterRebuild := mock.getIssuesCalls

	for i := 0; i < 3; i++ {
		if err := worker.SyncNow(ctx); err != nil {
			t.Fatalf("steady-state cycle %d: %v", i, err)
		}
	}
	// One page per cycle, the incremental shape: the walk stops on the first
	// unchanged page instead of re-walking a team it already repaired.
	if got := mock.getIssuesCalls - afterRebuild; got != 3 {
		t.Errorf("issue pages over 3 steady-state cycles = %d, want 3", got)
	}
}

// TestRebuildFailureRetriesNextCycle pins the ordering property in the rebuild:
// the watermark is dropped BEFORE the refill, so a rebuild that dies partway
// leaves no watermark and the next cycle walks the team again. The drift check
// cannot provide this — after the delete there are no stale rows left to count.
func TestRebuildFailureRetriesNextCycle(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	at := rekeyTime(t)

	for i := 1; i <= 3; i++ {
		seedCachedIssue(t, store, "team-1", "TST", fmt.Sprintf("issue-%d", i), fmt.Sprintf("TST-%d", i), at)
	}
	seedWatermark(t, store, "team-1", at)

	mock := newMockAPIClient()
	mock.pageSize = 2
	mock.teams = []api.Team{{ID: "team-1", Key: "QA", Name: "Quality"}}
	mock.issuesByTeam["team-1"] = []api.Issue{
		serverIssue("issue-1", "QA-1", "team-1", at),
		serverIssue("issue-2", "QA-2", "team-1", at),
		serverIssue("issue-3", "QA-3", "team-1", at),
	}
	// Fail the second page of the refill.
	mock.issuesPageErr = func(_ string, cursor string) error {
		if cursor != "" {
			return errors.New("network went away mid-rebuild")
		}
		return nil
	}

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	if err := worker.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow should log-and-continue past a team failure: %v", err)
	}

	if _, err := store.Queries().GetSyncMeta(ctx, "team-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a failed rebuild must leave no watermark, got err=%v", err)
	}
	// The drift check is already disarmed here: the stale rows are gone.
	if _, err := store.Queries().GetIssueByIdentifier(ctx, "TST-3"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("stale rows should have been deleted, got err=%v", err)
	}

	mock.issuesPageErr = nil
	if err := worker.SyncNow(ctx); err != nil {
		t.Fatalf("retry cycle: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := store.Queries().GetIssueByIdentifier(ctx, fmt.Sprintf("QA-%d", i)); err != nil {
			t.Errorf("QA-%d missing after the retry cycle: %v", i, err)
		}
	}
}

// TestRebuildCancelMidwayLeavesCacheIntact pins the atomicity the repair rests
// on. The rebuild drops a team's watermark and deletes its issues in ONE
// transaction, so a cancel mid-way (shutdown, unmount) must roll the whole
// thing back rather than commit what it got through.
//
// Rows-gone-and-watermark-gone is fine and rows-and-watermark-both-standing is
// fine — both re-arm, one through the missing watermark and one through the
// stale prefixes the drift check still counts. The state that must not exist
// is the partial delete: rows gone with the watermark still standing leaves a
// half-emptied team the incremental cursor believes is up to date and the
// drift check can no longer see.
func TestRebuildCancelMidwayLeavesCacheIntact(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	at := rekeyTime(t)

	seedTeamRow(t, store, "team-1", "QA", "Quality")
	for i := 1; i <= 3; i++ {
		seedCachedIssue(t, store, "team-1", "TST", fmt.Sprintf("issue-%d", i), fmt.Sprintf("TST-%d", i), at)
	}
	seedWatermark(t, store, "team-1", at)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	worker := NewWorker(newMockAPIClient(), store, Config{Interval: time.Hour})
	worker.rebuildTeamIssues(cancelled, api.Team{ID: "team-1", Key: "QA", Name: "Quality"})

	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, err := store.Queries().GetIssueByIdentifier(ctx, fmt.Sprintf("TST-%d", i)); err != nil {
			t.Errorf("TST-%d deleted by a cancelled rebuild: %v", i, err)
		}
	}
	if _, err := store.Queries().GetSyncMeta(ctx, "team-1"); err != nil {
		t.Errorf("watermark dropped by a cancelled rebuild: %v", err)
	}

	// And the cache it left is exactly the one the next cycle repairs.
	stale, err := store.Queries().CountTeamIssuesWithForeignIdentifier(ctx, db.CountTeamIssuesWithForeignIdentifierParams{
		TeamID:    "team-1",
		KeyPrefix: "QA-",
	})
	if err != nil {
		t.Fatalf("drift check after a cancelled rebuild: %v", err)
	}
	if stale != 3 {
		t.Errorf("drift count after a cancelled rebuild = %d, want 3 (the check must still re-arm)", stale)
	}
}

// TestWatermarkWithheldOnUpsertFailure covers the silent mode of key reuse: a
// team takes a key another team's stale rows still hold, its colliding issue
// is NOT the team's newest, and MAX(updated_at) over the rows that landed
// would step the cursor straight over it — dropping the issue from the mount
// permanently after one log line.
func TestWatermarkWithheldOnUpsertFailure(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	at := rekeyTime(t)

	// The blocking row: a stale TST-1 left behind on the team that was
	// renamed TST -> QA. Its teams row already carries the new key (the team
	// loop applies that immediately); only the issue's identifier and blob
	// still spell TST. team-ghost is deliberately absent from mock.teams, so
	// no cycle visits it and nothing rebuilds it away — the collision is the
	// only thing under test here.
	seedTeamRow(t, store, "team-ghost", "QA", "Quality")
	seedCachedIssue(t, store, "team-ghost", "TST", "issue-ghost", "TST-1", at)

	mock := newMockAPIClient()
	mock.teams = []api.Team{{ID: "team-1", Key: "TST", Name: "Test"}}
	mock.issuesByTeam["team-1"] = []api.Issue{
		// Newest first, as Linear orders them: the collided issue is second.
		serverIssue("issue-new-2", "TST-2", "team-1", at.Add(time.Hour)),
		serverIssue("issue-new-1", "TST-1", "team-1", at),
	}

	worker := NewWorker(mock, store, Config{Interval: time.Hour})
	if err := worker.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}

	meta, err := store.Queries().GetSyncMeta(ctx, "team-1")
	if err != nil {
		t.Fatalf("sync meta: %v", err)
	}
	if meta.LastIssueUpdatedAt.Valid && !meta.LastIssueUpdatedAt.Time.Before(at.Add(time.Hour)) {
		t.Errorf("watermark advanced to %v over a failed upsert; the collided issue would never be offered again",
			meta.LastIssueUpdatedAt.Time)
	}

	// The holder is named with its OWN team's current key, which is what
	// makes the log line diagnosable: "TST-1 is held by an issue on team QA"
	// points straight at the rename. Echoing the incoming team's key back
	// would name TST twice and say nothing.
	if got := worker.identifierHolder(ctx, serverIssue("issue-new-1", "TST-1", "team-1", at)); got != "issue issue-ghost (team QA)" {
		t.Errorf("identifierHolder = %q, want the blocking row named with its own team's key", got)
	}

	// Freeing the identifier lets the next cycle land the collided issue,
	// which is only possible because the cursor never stepped over it.
	if err := store.Queries().DeleteIssue(ctx, "issue-ghost"); err != nil {
		t.Fatalf("free the identifier: %v", err)
	}
	if err := worker.SyncNow(ctx); err != nil {
		t.Fatalf("retry cycle: %v", err)
	}
	row, err := store.Queries().GetIssueByIdentifier(ctx, "TST-1")
	if err != nil {
		t.Fatalf("TST-1 should have landed on the retry: %v", err)
	}
	if row.ID != "issue-new-1" {
		t.Errorf("TST-1 resolves to %q, want the taker team's issue", row.ID)
	}
}

// TestKeyReuseConvergesInBothTeamOrders. The team loop rotates its starting
// team each cycle, so whether the renamed team or the team that inherited its
// key is visited first is not something the fix may depend on.
func TestKeyReuseConvergesInBothTeamOrders(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cycle int64 // seeds the rotation: start = cycle % len(teams)
	}{
		{"renamed team first", 0},
		{"taker team first", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t)
			defer store.Close()
			ctx := context.Background()
			at := rekeyTime(t)

			// team-renamed was TST, is now QA; team-taker has taken TST.
			seedCachedIssue(t, store, "team-renamed", "TST", "issue-1", "TST-1", at)
			seedWatermark(t, store, "team-renamed", at)

			mock := newMockAPIClient()
			mock.teams = []api.Team{
				{ID: "team-renamed", Key: "QA", Name: "Quality"},
				{ID: "team-taker", Key: "TST", Name: "Test"},
			}
			mock.issuesByTeam["team-renamed"] = []api.Issue{serverIssue("issue-1", "QA-1", "team-renamed", at)}
			mock.issuesByTeam["team-taker"] = []api.Issue{serverIssue("issue-taker", "TST-1", "team-taker", at)}

			worker := NewWorker(mock, store, Config{Interval: time.Hour})
			worker.cycle.Store(tc.cycle)

			for i := 0; i < 2; i++ {
				if err := worker.SyncNow(ctx); err != nil {
					t.Fatalf("cycle %d: %v", i, err)
				}
			}

			renamed, err := store.Queries().GetIssueByIdentifier(ctx, "QA-1")
			if err != nil {
				t.Fatalf("QA-1 missing: %v", err)
			}
			if renamed.ID != "issue-1" {
				t.Errorf("QA-1 resolves to %q, want issue-1", renamed.ID)
			}
			taker, err := store.Queries().GetIssueByIdentifier(ctx, "TST-1")
			if err != nil {
				t.Fatalf("TST-1 missing after two cycles: %v", err)
			}
			if taker.ID != "issue-taker" {
				t.Errorf("TST-1 resolves to %q, want the taker team's issue", taker.ID)
			}

			// A third cycle must be quiet: nothing drifts, nothing collides.
			if err := worker.SyncNow(ctx); err != nil {
				t.Fatalf("settled cycle: %v", err)
			}
			for _, team := range mock.teams {
				prefix := team.Key + "-"
				n, err := store.Queries().CountTeamIssuesWithForeignIdentifier(ctx, db.CountTeamIssuesWithForeignIdentifierParams{
					TeamID: team.ID, KeyPrefix: prefix,
				})
				if err != nil {
					t.Fatalf("drift count for %s: %v", team.Key, err)
				}
				if n != 0 {
					t.Errorf("team %s still carries %d foreign identifiers after convergence", team.Key, n)
				}
			}
		})
	}
}

// TestTeamKeyHolderNamesTheSquatter: a genuinely new team taking a departed
// team's key cannot be inserted at all (teams.key is UNIQUE, the upsert
// conflicts on id, and there is no team eviction). Evicting the old team is a
// separate ticket; what this one owes is a log line that names the holder.
func TestTeamKeyHolderNamesTheSquatter(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	worker := NewWorker(newMockAPIClient(), store, Config{Interval: time.Hour})
	old := api.Team{ID: "team-old", Key: "SPY", Name: "Spycraft"}
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(old)); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	incoming := api.Team{ID: "team-new", Key: "SPY", Name: "Agent State"}
	if got := worker.teamKeyHolder(ctx, incoming); got != "team team-old (Spycraft)" {
		t.Errorf("teamKeyHolder = %q, want the holding team named", got)
	}
	// A team upserting over its own row is not a collision.
	if got := worker.teamKeyHolder(ctx, old); got != "" {
		t.Errorf("teamKeyHolder for the holder itself = %q, want \"\"", got)
	}
	if got := worker.teamKeyHolder(ctx, api.Team{ID: "team-x", Key: "NONE"}); got != "" {
		t.Errorf("teamKeyHolder for an unheld key = %q, want \"\"", got)
	}
}
