package fs

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
)

// TestSymlinkNodeReportsConstructedState pins the module's whole contract:
// Readlink and Getattr only report what construction fixed — target, size,
// entity times — so a view cannot grow per-call behaviour (the drift that
// produced the initiative symlink's fabricated size-64/now() metadata).
func TestSymlinkNodeReportsConstructedState(t *testing.T) {
	t.Parallel()
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
	node := &symlinkNode{
		target:     "../../issues/TST-1",
		createdAt:  created,
		updatedAt:  updated,
		accessedAt: updated,
	}

	link, errno := node.Readlink(context.Background())
	if errno != 0 {
		t.Fatalf("Readlink errno = %v", errno)
	}
	if string(link) != "../../issues/TST-1" {
		t.Errorf("Readlink = %q, want %q", link, "../../issues/TST-1")
	}

	var out fuse.AttrOut
	if errno := node.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr errno = %v", errno)
	}
	if out.Mode != 0777|syscall.S_IFLNK {
		t.Errorf("Mode = %o, want %o", out.Mode, 0777|syscall.S_IFLNK)
	}
	if out.Size != uint64(len(node.target)) {
		t.Errorf("Size = %d, want %d", out.Size, len(node.target))
	}
	if got := time.Unix(int64(out.Mtime), 0).UTC(); !got.Equal(updated) {
		t.Errorf("Mtime = %v, want updatedAt %v", got, updated)
	}
	if got := time.Unix(int64(out.Ctime), 0).UTC(); !got.Equal(created) {
		t.Errorf("Ctime = %v, want createdAt %v", got, created)
	}
}

// TestSymlinkNodeZeroTimesStayEpoch pins the zero-time guard: a row whose
// timestamps failed to parse must stat as epoch, not wrap uint64(negative)
// into a year-584-billion mtime that sorts first in `ls -lt`.
func TestSymlinkNodeZeroTimesStayEpoch(t *testing.T) {
	t.Parallel()
	node := &symlinkNode{target: "../../issues/TST-1"}

	var out fuse.AttrOut
	if errno := node.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr errno = %v", errno)
	}
	if out.Mtime != 0 || out.Ctime != 0 || out.Atime != 0 {
		t.Errorf("zero times must stay 0, got atime=%d mtime=%d ctime=%d", out.Atime, out.Mtime, out.Ctime)
	}
}

// TestSymlinkNodeSeparateAtime pins the cycles convention: atime may carry a
// distinct date (cycle end) while mtime/ctime carry start.
func TestSymlinkNodeSeparateAtime(t *testing.T) {
	t.Parallel()
	starts := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	ends := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)
	node := &symlinkNode{target: "cycle-7", createdAt: starts, updatedAt: starts, accessedAt: ends}

	var out fuse.AttrOut
	if errno := node.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr errno = %v", errno)
	}
	if got := time.Unix(int64(out.Atime), 0).UTC(); !got.Equal(ends) {
		t.Errorf("Atime = %v, want EndsAt %v", got, ends)
	}
	if got := time.Unix(int64(out.Mtime), 0).UTC(); !got.Equal(starts) {
		t.Errorf("Mtime = %v, want StartsAt %v", got, starts)
	}
}

// TestSymlinkNodeGetattrMatchesFillAttr pins Lookup/stat parity: the attrs
// newSymlinkInode puts in a Lookup's EntryOut come from the same fillAttr that
// answers a later stat, so the two can never disagree (CurrentCycleSymlink's
// Lookup and Getattr had drifted apart before consolidation).
func TestSymlinkNodeGetattrMatchesFillAttr(t *testing.T) {
	t.Parallel()
	node := &symlinkNode{
		target:     "../../../teams/TST/projects/test-project",
		createdAt:  time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		updatedAt:  time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC),
		accessedAt: time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC),
	}

	var lookupAttr fuse.Attr
	node.fillAttr(&lookupAttr)

	var statOut fuse.AttrOut
	if errno := node.Getattr(context.Background(), nil, &statOut); errno != 0 {
		t.Fatalf("Getattr errno = %v", errno)
	}
	if statOut.Attr != lookupAttr {
		t.Errorf("Getattr attr diverges from Lookup attr:\n stat: %+v\nlookup: %+v", statOut.Attr, lookupAttr)
	}
}

// TestTeamIssueTargetUnsyncedTeamIsENOENT pins the shared my/-and-users/
// target helper: an issue without team data is unresolvable -> ENOENT, never
// a dangling "../../teams//issues/X" placeholder.
func TestTeamIssueTargetUnsyncedTeamIsENOENT(t *testing.T) {
	t.Parallel()
	issue := api.Issue{Identifier: "TST-1", Team: &api.Team{Key: "TST"}}
	target, errno := teamIssueTarget(issue)
	if errno != 0 || target != "../../teams/TST/issues/TST-1" {
		t.Errorf("resolvable issue: target=%q errno=%v", target, errno)
	}

	for name, i := range map[string]api.Issue{
		"nil team":       {Identifier: "TST-2"},
		"empty team key": {Identifier: "TST-3", Team: &api.Team{}},
	} {
		if _, errno := teamIssueTarget(i); errno != syscall.ENOENT {
			t.Errorf("%s: errno = %v, want ENOENT", name, errno)
		}
	}
}

// =============================================================================
// Initiative project target resolution
// =============================================================================

// TestResolveProjectTargetResolvesTeamAndTimes pins the fix for the drifted
// initiative symlink: the target comes from the canonical-team query (not a
// teams-by-projects scan), climbs three levels (the symlink lives at
// initiatives/{slug}/projects/{name}), and the timestamps are the project's
// real ones.
func TestResolveProjectTargetResolvesTeamAndTimes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fixtures.NewTestSQLiteStore(t)

	created := time.Date(2025, 2, 3, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	project := api.Project{
		ID:        "project-1",
		Name:      "Test Project",
		Slug:      "test-project",
		CreatedAt: created,
		UpdatedAt: updated,
	}

	if err := fixtures.PopulateTeam(ctx, store, api.Team{ID: "team-1", Key: "TST", Name: "Test"}, nil, nil, nil); err != nil {
		t.Fatalf("populate team: %v", err)
	}
	if err := fixtures.PopulateProject(ctx, store, project, "team-1"); err != nil {
		t.Fatalf("populate project: %v", err)
	}

	lfs := &LinearFS{}
	if err := lfs.InjectTestStore(store); err != nil {
		t.Fatalf("inject store: %v", err)
	}
	node := &InitiativeProjectsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}}

	target, gotCreated, gotUpdated, errno := node.resolveProjectTarget(ctx, "project-1")
	if errno != 0 {
		t.Fatalf("resolveProjectTarget errno = %v", errno)
	}
	if want := "../../../teams/TST/projects/test-project"; target != want {
		t.Errorf("target = %q, want %q", target, want)
	}
	if !gotCreated.Equal(created) {
		t.Errorf("createdAt = %v, want %v", gotCreated, created)
	}
	if !gotUpdated.Equal(updated) {
		t.Errorf("updatedAt = %v, want %v", gotUpdated, updated)
	}
}

// TestResolveProjectTargetMultiTeamIsFirstByKey pins the canonical-team
// contract (ORDER BY t.key LIMIT 1) that makes multi-team symlink targets
// deterministic.
func TestResolveProjectTargetMultiTeamIsFirstByKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fixtures.NewTestSQLiteStore(t)

	project := api.Project{ID: "project-shared", Name: "Shared", Slug: "shared"}
	if err := fixtures.PopulateTeam(ctx, store, api.Team{ID: "team-z", Key: "ZZZ", Name: "Zulu"}, nil, nil, nil); err != nil {
		t.Fatalf("populate team-z: %v", err)
	}
	if err := fixtures.PopulateTeam(ctx, store, api.Team{ID: "team-a", Key: "AAA", Name: "Alpha"}, nil, nil, nil); err != nil {
		t.Fatalf("populate team-a: %v", err)
	}
	// Link to the later-sorting team first so insertion order can't fake a pass.
	if err := fixtures.PopulateProject(ctx, store, project, "team-z"); err != nil {
		t.Fatalf("populate project (team-z): %v", err)
	}
	if err := fixtures.PopulateProject(ctx, store, project, "team-a"); err != nil {
		t.Fatalf("populate project (team-a): %v", err)
	}

	lfs := &LinearFS{}
	if err := lfs.InjectTestStore(store); err != nil {
		t.Fatalf("inject store: %v", err)
	}
	node := &InitiativeProjectsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}}

	target, _, _, errno := node.resolveProjectTarget(ctx, "project-shared")
	if errno != 0 {
		t.Fatalf("resolveProjectTarget errno = %v", errno)
	}
	if want := "../../../teams/AAA/projects/shared"; target != want {
		t.Errorf("target = %q, want first-by-key %q", target, want)
	}
}

// TestResolveProjectTargetUnsyncedIsENOENT pins the failure model: until sync
// has both the project row and its team association, the name references
// something that doesn't exist yet -> ENOENT (no dangling "broken-link").
func TestResolveProjectTargetUnsyncedIsENOENT(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fixtures.NewTestSQLiteStore(t)
	q := store.Queries()

	lfs := &LinearFS{}
	if err := lfs.InjectTestStore(store); err != nil {
		t.Fatalf("inject store: %v", err)
	}
	node := &InitiativeProjectsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}}

	// Project row missing entirely.
	if _, _, _, errno := node.resolveProjectTarget(ctx, "project-absent"); errno != syscall.ENOENT {
		t.Errorf("missing project: errno = %v, want ENOENT", errno)
	}

	// Project row present but no project_teams association yet (deliberately
	// not fixtures.PopulateProject, which always writes the junction row).
	params, err := db.APIProjectToDBProject(api.Project{ID: "project-orphan", Name: "Orphan", Slug: "orphan"})
	if err != nil {
		t.Fatalf("convert project: %v", err)
	}
	if err := q.UpsertProject(ctx, params); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	if _, _, _, errno := node.resolveProjectTarget(ctx, "project-orphan"); errno != syscall.ENOENT {
		t.Errorf("teamless project: errno = %v, want ENOENT", errno)
	}
}
