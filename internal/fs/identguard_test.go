package fs

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
)

func TestIdentifierMatchesTeamKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		identifier string
		teamKey    string
		want       bool
	}{
		{"agrees", "QA-1", "QA", true},
		// The #427 rejection: the row still spells the pre-rename key, so the
		// name resolves to an issue the requesting path does not describe.
		{"stale prefix after a rename", "TST-1", "QA", false},
		// A shorter key must not swallow a longer one, or a genuine rename
		// from TST to TS would read as healthy.
		{"shorter key is not a prefix match", "TST-1", "TS", false},
		{"longer key is not a prefix match", "TS-1", "TST", false},
		// Nothing authoritative to disagree with: the owning team is not
		// cached, so a verdict would be invented from missing data.
		{"unknown owning team admits", "TST-1", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := identifierMatchesTeamKey(tt.identifier, tt.teamKey); got != tt.want {
				t.Errorf("identifierMatchesTeamKey(%q, %q) = %v, want %v", tt.identifier, tt.teamKey, got, tt.want)
			}
		})
	}
}

// seedTeamWithIssue puts one team and one issue in the store, with the
// identifier spelled independently of the team's key so a post-rename cache
// can be modelled.
func seedTeamWithIssue(t *testing.T, store *db.Store, teamID, teamKey, issueID, identifier string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	team := api.Team{ID: teamID, Key: teamKey, Name: teamKey, CreatedAt: now, UpdatedAt: now}
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team %s: %v", teamKey, err)
	}
	issue := api.Issue{
		ID:         issueID,
		Identifier: identifier,
		Title:      identifier,
		Team:       &api.Team{ID: teamID, Key: teamKey},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	data, err := db.APIIssueToDBIssue(issue)
	if err != nil {
		t.Fatalf("convert issue %s: %v", identifier, err)
	}
	if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
		t.Fatalf("seed issue %s: %v", identifier, err)
	}
}

// TestOwningTeamKeyReadsTheTeamsRow pins the half of the guard that decides
// what it compares against. A team-key rename leaves the identifier and the
// team key inside the issue's data blob stale together, so the key must come
// from the teams table via the issue's team_id column; reading the blob would
// make the guard agree with itself and never fire.
func TestOwningTeamKeyReadsTheTeamsRow(t *testing.T) {
	t.Parallel()
	sqliteRepo, store := fixtures.NewTestSQLiteRepository(t)
	lfs := &LinearFS{repo: sqliteRepo, store: store}
	ctx := context.Background()

	// A cache damaged by a rename: the team row carries the new key QA, the
	// issue row (and its blob) still spells the old one.
	seedTeamWithIssue(t, store, "team-renamed", "QA", "issue-1", "TST-1")

	if got := lfs.owningTeamKey(ctx, "issue-1"); got != "QA" {
		t.Fatalf("owningTeamKey = %q, want the teams row's current key %q", got, "QA")
	}
	if identifierMatchesTeamKey("TST-1", lfs.owningTeamKey(ctx, "issue-1")) {
		t.Error("stale identifier TST-1 accepted for a team now keyed QA")
	}

	t.Run("uncached issue resolves to no key", func(t *testing.T) {
		if got := lfs.owningTeamKey(ctx, "nope"); got != "" {
			t.Errorf("owningTeamKey for an uncached issue = %q, want \"\"", got)
		}
	})
}

// TestOwningTeamKeyAdmitsCrossTeamIssues is the non-regression twin. Project
// and sub-issue symlinks put issues from other teams under a containing team's
// issues/ directory, and those names resolve only because the lookup is not
// team-scoped. The guard checks the identifier's own consistency, so a healthy
// cross-team issue still resolves no matter which team's directory asked.
func TestOwningTeamKeyAdmitsCrossTeamIssues(t *testing.T) {
	t.Parallel()
	sqliteRepo, store := fixtures.NewTestSQLiteRepository(t)
	lfs := &LinearFS{repo: sqliteRepo, store: store}
	ctx := context.Background()

	seedTeamWithIssue(t, store, "team-qa", "QA", "issue-qa", "QA-7")
	seedTeamWithIssue(t, store, "team-eng", "ENG", "issue-eng", "ENG-3")

	// ENG-3 reached through team QA's issues/ directory (how a cross-team
	// project member or sub-issue symlink lands) stays resolvable.
	if !identifierMatchesTeamKey("ENG-3", lfs.owningTeamKey(ctx, "issue-eng")) {
		t.Error("healthy cross-team issue ENG-3 rejected")
	}
	if !identifierMatchesTeamKey("QA-7", lfs.owningTeamKey(ctx, "issue-qa")) {
		t.Error("healthy same-team issue QA-7 rejected")
	}
}

// issuesNodeFor builds an IssuesNode standing in for teams/<key>/issues/,
// wired to a store but not to a mounted tree. resolveIssue is Lookup's whole
// resolution policy (the rest is inode plumbing, which needs a live bridge),
// so this is the seam where the guard's verdict is observable.
func issuesNodeFor(lfs *LinearFS, teamID, teamKey string) *IssuesNode {
	return &IssuesNode{
		attrNode:   attrNode{BaseNode: BaseNode{lfs: lfs}},
		entityCell: entityCell[api.Team]{val: api.Team{ID: teamID, Key: teamKey, Name: teamKey}},
	}
}

// TestIssuesLookupRejectsStaleIdentifier is the wrong-issue case, end to end
// through the node. Team-1 was renamed TST -> QA and still holds a row named
// TST-1; team-2 has since taken the freed key TST. Asking team-2's issues/
// directory for TST-1 resolves — workspace-wide — to team-1's issue, and the
// entity that comes back is what a later Flush would mutate, so this is a
// wrong-issue WRITE waiting to happen, not just a bad read.
//
// This fails on pre-fix code, where resolveIssue returns team-1's issue with
// errno 0. TestIdentifierMatchesTeamKey and TestOwningTeamKey* pin the two
// halves; only this pins that Lookup actually consults them.
func TestIssuesLookupRejectsStaleIdentifier(t *testing.T) {
	t.Parallel()
	sqliteRepo, store := fixtures.NewTestSQLiteRepository(t)
	lfs := &LinearFS{repo: sqliteRepo, store: store}
	ctx := context.Background()

	seedTeamWithIssue(t, store, "team-1", "QA", "issue-renamed", "TST-1")
	seedTeamWithIssue(t, store, "team-2", "TST", "issue-taker", "TST-9")

	// Sanity: the unguarded resolution really does cross teams, so the
	// rejection below is not passing for want of anything to resolve.
	if got, err := lfs.FetchIssueByIdentifier(ctx, "TST-1"); err != nil || got.ID != "issue-renamed" {
		t.Fatalf("FetchIssueByIdentifier(TST-1) = %+v, %v; want the renamed team's issue", got, err)
	}

	issue, errno := issuesNodeFor(lfs, "team-2", "TST").resolveIssue(ctx, "TST-1")
	if errno != syscall.ENOENT {
		t.Errorf("resolveIssue(TST-1) errno = %v, want ENOENT; it resolved to %+v", errno, issue)
	}
}

// TestIssuesLookupAllowsCrossTeamIssue is the non-regression twin, and the
// reason the guard is identifier-consistency rather than parent-team equality.
// ProjectNode.Lookup and ChildrenNode.Lookup build ../../issues/<IDENT> from
// listings scoped by project_id and parent_id, not by team, so a cross-team
// project member or sub-issue is routinely reached through a containing team's
// issues/ directory. Scoping to the parent team would dangle those symlinks.
func TestIssuesLookupAllowsCrossTeamIssue(t *testing.T) {
	t.Parallel()
	sqliteRepo, store := fixtures.NewTestSQLiteRepository(t)
	lfs := &LinearFS{repo: sqliteRepo, store: store}
	ctx := context.Background()

	seedTeamWithIssue(t, store, "team-qa", "QA", "issue-qa", "QA-7")
	seedTeamWithIssue(t, store, "team-eng", "ENG", "issue-eng", "ENG-3")

	qaIssues := issuesNodeFor(lfs, "team-qa", "QA")

	// ENG-3 asked for through team QA's directory: healthy, so it resolves.
	issue, errno := qaIssues.resolveIssue(ctx, "ENG-3")
	if errno != 0 {
		t.Fatalf("resolveIssue(ENG-3) through team QA = errno %v, want it to resolve", errno)
	}
	if issue.ID != "issue-eng" {
		t.Errorf("resolveIssue(ENG-3) = %q, want issue-eng", issue.ID)
	}

	// The team's own issue still resolves too.
	if _, errno := qaIssues.resolveIssue(ctx, "QA-7"); errno != 0 {
		t.Errorf("resolveIssue(QA-7) = errno %v, want it to resolve", errno)
	}

	// A name that is not an identifier never reaches the store.
	if _, errno := qaIssues.resolveIssue(ctx, "not-an-identifier"); errno != syscall.ENOENT {
		t.Errorf("resolveIssue(not-an-identifier) = errno %v, want ENOENT", errno)
	}
}
