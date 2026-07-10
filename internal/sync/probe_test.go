package sync

// Tests for the lean cycle's team-projects change-detection probe (#243).
//
// Worker-seam tests script cycles against the op-recording mockAPIClient and
// the fake clock (the #242 lean/full taxonomy fixtures) and assert external
// behavior only: which ops each cycle issued, what landed in SQLite, and
// where the persisted watermark stands. The wire test drives the real
// api.Client against the scripted GraphQL mock server and asserts request
// shape (orderBy, first, after) and that resume pagination stops at the
// watermark instead of draining.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/testutil"
)

// countOp reports how many times op appears in ops.
func countOp(ops []string, op string) int {
	n := 0
	for _, o := range ops {
		if o == op {
			n++
		}
	}
	return n
}

// probeProject builds a test project with the given updatedAt.
func probeProject(id, name string, updatedAt time.Time) api.Project {
	return api.Project{
		ID:        id,
		Name:      name,
		Slug:      id,
		State:     "started",
		CreatedAt: updatedAt.Add(-24 * time.Hour),
		UpdatedAt: updatedAt,
	}
}

// TestLeanCyclesProbeProjectsUnchangedWorld: in an unchanged world, each lean
// cycle spends exactly one small probe page per team — no full projects drain
// (GetTeamMetadata) and no resume pages. The first lean cycle ever has no
// watermark and bootstraps by walking every page (upsert-only); once the
// watermark is seeded, steady state is one op per team per cycle.
func TestLeanCyclesProbeProjectsUnchangedWorld(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	worker, mock, clock := cycleTestWorker(t, store)
	base := clock.now().Add(-time.Hour)
	// 7 projects: more than the probe page (5), so the bootstrap walk needs a
	// resume page while steady state must still be a single op.
	for i := 0; i < 7; i++ {
		p := probeProject(fmt.Sprintf("proj-%d", i), fmt.Sprintf("Project %d", i), base.Add(time.Duration(i)*time.Minute))
		mock.projectsByTeam["team-1"] = append(mock.projectsByTeam["team-1"], p)
	}

	// Cycle 1: cold start — full. The metadata drain covers projects; no probe.
	ops := opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("cycle 1: %v", err)
		}
	})
	assertCycleOps(t, "cycle 1 (cold start)", ops, true)

	// Cycle 2: lean, no watermark yet — the bootstrap walk pages to the end
	// (7 projects = probe page of 5 + one resume page) and seeds the watermark.
	clock.advance(2 * time.Minute)
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("cycle 2: %v", err)
		}
	})
	assertCycleOps(t, "cycle 2 (bootstrap)", ops, false)
	if got := countOp(ops, "GetTeamProjectsNewestPage"); got != 2 {
		t.Errorf("bootstrap cycle probe pages = %d, want 2 (5-node probe page + resume page)", got)
	}

	// The bootstrap walk seeded the watermark at the newest updatedAt.
	wm, err := store.Queries().GetSyncSchedule(ctx, projectsProbeScheduleKey("team-1"))
	if err != nil {
		t.Fatalf("GetSyncSchedule watermark: %v", err)
	}
	if !wm.Equal(base.Add(6 * time.Minute)) {
		t.Errorf("watermark = %v, want %v (max project updatedAt)", wm, base.Add(6*time.Minute))
	}

	// Cycles 3-4: unchanged world — exactly one probe op per team per cycle.
	for i := 3; i <= 4; i++ {
		clock.advance(2 * time.Minute)
		ops = opsDuring(mock, func() {
			if err := worker.syncAllTeams(ctx); err != nil {
				t.Fatalf("cycle %d: %v", i, err)
			}
		})
		assertCycleOps(t, fmt.Sprintf("cycle %d (unchanged)", i), ops, false)
		if got := countOp(ops, "GetTeamProjectsNewestPage"); got != 1 {
			t.Errorf("cycle %d probe pages = %d, want exactly 1 in an unchanged world", i, got)
		}
	}
}

// TestLeanCycleProbeDetectsChangedProject: a remote project change is picked
// up within one lean cycle — the probe detects the newer updatedAt, the
// resume upserts the project (with its milestones and project_teams junction
// row) through the full drain's persist path, and the watermark advances so
// the NEXT lean cycle is back to a single unchanged probe. The changed
// timestamp carries sub-second precision to pin the watermark's SQLite
// round-trip fidelity: a truncated watermark would read "changed" forever.
func TestLeanCycleProbeDetectsChangedProject(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	worker, mock, clock := cycleTestWorker(t, store)
	base := clock.now().Add(-time.Hour)
	mock.projectsByTeam["team-1"] = []api.Project{
		probeProject("proj-a", "Alpha", base),
		probeProject("proj-b", "Beta", base.Add(time.Minute)),
	}

	// Cold-start full cycle, then a lean cycle to seed the watermark.
	if err := worker.syncAllTeams(ctx); err != nil {
		t.Fatalf("full cycle: %v", err)
	}
	clock.advance(2 * time.Minute)
	if err := worker.syncAllTeams(ctx); err != nil {
		t.Fatalf("seeding lean cycle: %v", err)
	}

	// Remote change: Alpha is renamed and gains a milestone; its updatedAt
	// jumps past the watermark (with a millisecond fraction).
	changedAt := clock.now().Add(17 * time.Millisecond)
	changed := probeProject("proj-a", "Alpha Renamed", changedAt)
	changed.Milestones = &api.ProjectMilestones{Nodes: []api.ProjectMilestone{
		{ID: "ms-1", Name: "Phase 1", SortOrder: 1},
	}}
	mock.projectsByTeam["team-1"][0] = changed

	// Next lean cycle: probe detects the change and upserts.
	clock.advance(2 * time.Minute)
	ops := opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("changed lean cycle: %v", err)
		}
	})
	assertCycleOps(t, "changed lean cycle", ops, false)

	// The changed project is visible in the store, junction row included
	// (ListTeamProjects joins through project_teams).
	teamProjects, err := store.Queries().ListTeamProjects(ctx, "team-1")
	if err != nil {
		t.Fatalf("ListTeamProjects: %v", err)
	}
	var found bool
	for _, p := range teamProjects {
		if p.ID == "proj-a" {
			found = true
			if p.Name != "Alpha Renamed" {
				t.Errorf("probed project name = %q, want %q", p.Name, "Alpha Renamed")
			}
		}
	}
	if !found {
		t.Fatalf("probed project proj-a not in team projects (junction row missing?): %+v", teamProjects)
	}

	// Its nested milestone landed through the shared persist path.
	milestones, err := store.Queries().ListProjectMilestones(ctx, "proj-a")
	if err != nil {
		t.Fatalf("ListProjectMilestones: %v", err)
	}
	if len(milestones) != 1 || milestones[0].Name != "Phase 1" {
		t.Errorf("milestones = %+v, want the probed project's Phase 1", milestones)
	}

	// The watermark advanced to the changed updatedAt.
	wm, err := store.Queries().GetSyncSchedule(ctx, projectsProbeScheduleKey("team-1"))
	if err != nil {
		t.Fatalf("GetSyncSchedule watermark: %v", err)
	}
	if !wm.Equal(changedAt) {
		t.Errorf("watermark = %v, want %v (advanced to the change)", wm, changedAt)
	}

	// Steady state again: the following lean cycle is one unchanged probe —
	// which also proves the millisecond watermark survived its SQLite
	// round-trip (a lossy read would re-detect the same change every cycle).
	clock.advance(2 * time.Minute)
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("post-change lean cycle: %v", err)
		}
	})
	if got := countOp(ops, "GetTeamProjectsNewestPage"); got != 1 {
		t.Errorf("post-change probe pages = %d, want exactly 1 (watermark round-trip lost precision?)", got)
	}
}

// TestLeanCycleProbeErrorContinuesAndRetries: a probe failure must not take
// the cycle down — the issues sync still runs, the watermark stays put, and
// the next lean cycle probes again.
func TestLeanCycleProbeErrorContinuesAndRetries(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	worker, mock, clock := cycleTestWorker(t, store)
	mock.projectsByTeam["team-1"] = []api.Project{
		probeProject("proj-a", "Alpha", clock.now().Add(-time.Hour)),
	}

	if err := worker.syncAllTeams(ctx); err != nil {
		t.Fatalf("full cycle: %v", err)
	}

	// Lean cycle with the probe failing. A remote issue arrives in the same
	// window: the failed probe must not stop the issues sync from seeing it.
	mock.projectsProbeErr = errors.New("boom")
	clock.advance(2 * time.Minute)
	mock.issuesByTeam["team-1"] = []api.Issue{
		{ID: "issue-1", Identifier: "TST-1", Title: "One", Team: &api.Team{ID: "team-1"}, UpdatedAt: clock.now()},
	}
	ops := opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("probe-error cycle must not fail the cycle: %v", err)
		}
	})
	if got := countOp(ops, "GetTeamProjectsNewestPage"); got != 1 {
		t.Errorf("probe-error cycle probe attempts = %d, want 1", got)
	}
	// The cycle continued past the failed probe: the issues sync still ran.
	issues, err := store.Queries().ListTeamIssues(ctx, "team-1")
	if err != nil || len(issues) != 1 {
		t.Errorf("issues after probe-error cycle = %d (err %v), want 1 — cycle must continue past a failed probe", len(issues), err)
	}
	// No watermark was persisted by the failed walk.
	if _, err := store.Queries().GetSyncSchedule(ctx, projectsProbeScheduleKey("team-1")); err == nil {
		t.Error("watermark persisted despite probe failure, want none")
	}

	// Recovery: the next lean cycle probes again and succeeds.
	mock.projectsProbeErr = nil
	clock.advance(2 * time.Minute)
	ops = opsDuring(mock, func() {
		if err := worker.syncAllTeams(ctx); err != nil {
			t.Fatalf("recovery cycle: %v", err)
		}
	})
	if got := countOp(ops, "GetTeamProjectsNewestPage"); got != 1 {
		t.Errorf("recovery cycle probe attempts = %d, want 1", got)
	}
	if _, err := store.Queries().GetSyncSchedule(ctx, projectsProbeScheduleKey("team-1")); err != nil {
		t.Errorf("watermark after recovery cycle: %v, want seeded", err)
	}
}

// TestProbeNeverPrunes: the CRITICAL prune rule — a probe walk (bootstrap or
// resume) is upsert-only. A project present locally but absent from the
// probe's pages (deleted remotely, or simply below the watermark) must
// survive every lean cycle; only the full cycle's complete drain may prune.
func TestProbeNeverPrunes(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	worker, mock, clock := cycleTestWorker(t, store)
	base := clock.now().Add(-time.Hour)
	mock.projectsByTeam["team-1"] = []api.Project{
		probeProject("proj-a", "Alpha", base),
		probeProject("proj-b", "Beta", base.Add(time.Minute)),
	}

	// Full cycle populates both projects; a lean cycle seeds the watermark.
	if err := worker.syncAllTeams(ctx); err != nil {
		t.Fatalf("full cycle: %v", err)
	}
	clock.advance(2 * time.Minute)
	if err := worker.syncAllTeams(ctx); err != nil {
		t.Fatalf("seeding lean cycle: %v", err)
	}

	// proj-a is deleted remotely AND proj-b changes (so the probe runs a real
	// resume walk that no longer sees proj-a).
	changed := probeProject("proj-b", "Beta v2", clock.now())
	mock.projectsByTeam["team-1"] = []api.Project{changed}

	clock.advance(2 * time.Minute)
	if err := worker.syncAllTeams(ctx); err != nil {
		t.Fatalf("lean cycle after deletion: %v", err)
	}

	// The deleted project survives the lean cycle: probes never license a
	// prune. (The full cycle's complete drain deletes it, by design.)
	if _, err := store.Queries().GetProject(ctx, "proj-a"); err != nil {
		t.Errorf("GetProject(proj-a) after lean cycle: %v — the probe must never prune", err)
	}
	teamProjects, err := store.Queries().ListTeamProjects(ctx, "team-1")
	if err != nil {
		t.Fatalf("ListTeamProjects: %v", err)
	}
	if len(teamProjects) != 2 {
		t.Errorf("team projects after lean cycle = %d, want 2 (junction rows must survive probes too)", len(teamProjects))
	}
}

// TestProbeWireResumeStopsAtWatermark drives probeTeamProjects through the
// real api.Client against the scripted GraphQL mock server: the probe page
// must carry orderBy: updatedAt with the small first, the resume page must
// carry the cursor and the full-drain first, and pagination must STOP at the
// watermark — no third request even though the second page reports
// hasNextPage (the whole point: never drain on a lean cycle).
func TestProbeWireResumeStopsAtWatermark(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	watermark := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	newer := func(d time.Duration) string { return watermark.Add(d).Format(time.RFC3339Nano) }
	node := func(id string, updatedAt string) map[string]any {
		return map[string]any{"id": id, "name": id, "slugId": id, "updatedAt": updatedAt}
	}
	connPage := func(hasNext bool, cursor string, nodes ...map[string]any) map[string]any {
		return map[string]any{"team": map[string]any{"projects": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
			"nodes":    nodes,
		}}}
	}
	// Probe page: 5 nodes, all newer than the watermark, more behind.
	// Resume page: one more newer node, then a node AT the watermark — the
	// stop signal — while hasNextPage still says true.
	mock.SetResponseSequence("TeamProjectsByUpdatedAt",
		connPage(true, "cursor-1",
			node("p1", newer(50*time.Minute)), node("p2", newer(40*time.Minute)),
			node("p3", newer(30*time.Minute)), node("p4", newer(20*time.Minute)),
			node("p5", newer(10*time.Minute))),
		connPage(true, "cursor-2",
			node("p6", newer(5*time.Minute)),
			node("p7", watermark.Format(time.RFC3339Nano))),
	)

	client := api.NewClient("test")
	client.SetAPIURL(mock.URL())

	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.Queries().UpsertSyncSchedule(ctx, db.UpsertSyncScheduleParams{
		Key:     projectsProbeScheduleKey("team-1"),
		LastRun: watermark,
	}); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	worker := NewWorker(client, store, Config{Interval: time.Hour})
	if err := worker.probeTeamProjects(ctx, api.Team{ID: "team-1", Key: "TST"}); err != nil {
		t.Fatalf("probeTeamProjects: %v", err)
	}

	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("wire calls = %d, want exactly 2 (probe + one resume, stopping at the watermark)", len(calls))
	}

	probe := calls[0]
	if probe.Operation != "TeamProjectsByUpdatedAt" {
		t.Errorf("probe operation = %q, want TeamProjectsByUpdatedAt", probe.Operation)
	}
	if want := "orderBy: updatedAt"; !strings.Contains(probe.Query, want) {
		t.Errorf("probe query missing %q:\n%s", want, probe.Query)
	}
	if want := "...ProjectFields"; !strings.Contains(probe.Query, want) {
		t.Errorf("probe query must project through the shared fragment, missing %q:\n%s", want, probe.Query)
	}
	if got := probe.Variables["first"]; got != float64(probeProjectsPageSize) {
		t.Errorf("probe first = %v, want %d", got, probeProjectsPageSize)
	}
	if probe.Variables["after"] != nil {
		t.Errorf("probe after = %v, want omitted", probe.Variables["after"])
	}

	resume := calls[1]
	if got := resume.Variables["after"]; got != "cursor-1" {
		t.Errorf("resume after = %v, want cursor-1", got)
	}
	if got := resume.Variables["first"]; got != float64(probeProjectsResumePageSize) {
		t.Errorf("resume first = %v, want %d", got, probeProjectsResumePageSize)
	}

	// Everything newer than the watermark was upserted; the at-watermark node
	// was not refetched into a store write (p7 predates the watermark's walk).
	for _, id := range []string{"p1", "p2", "p3", "p4", "p5", "p6"} {
		if _, err := store.Queries().GetProject(ctx, id); err != nil {
			t.Errorf("GetProject(%s): %v, want upserted by the resume walk", id, err)
		}
	}

	// The watermark advanced to the newest updatedAt seen.
	wm, err := store.Queries().GetSyncSchedule(ctx, projectsProbeScheduleKey("team-1"))
	if err != nil {
		t.Fatalf("GetSyncSchedule: %v", err)
	}
	if !wm.Equal(watermark.Add(50 * time.Minute)) {
		t.Errorf("watermark = %v, want %v", wm, watermark.Add(50*time.Minute))
	}
}

// TestProbeWireUnchangedIsSinglePage: the steady-state wire shape — a probe
// whose newest node is not newer than the watermark issues exactly one
// request, even when the page reports more behind it.
func TestProbeWireUnchangedIsSinglePage(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	watermark := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	mock.SetResponse("TeamProjectsByUpdatedAt", map[string]any{
		"team": map[string]any{"projects": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "cursor-1"},
			"nodes": []map[string]any{
				{"id": "p1", "name": "p1", "slugId": "p1", "updatedAt": watermark.Format(time.RFC3339Nano)},
			},
		}},
	})

	client := api.NewClient("test")
	client.SetAPIURL(mock.URL())

	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.Queries().UpsertSyncSchedule(ctx, db.UpsertSyncScheduleParams{
		Key:     projectsProbeScheduleKey("team-1"),
		LastRun: watermark,
	}); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	worker := NewWorker(client, store, Config{Interval: time.Hour})
	if err := worker.probeTeamProjects(ctx, api.Team{ID: "team-1", Key: "TST"}); err != nil {
		t.Fatalf("probeTeamProjects: %v", err)
	}

	if calls := mock.Calls(); len(calls) != 1 {
		t.Fatalf("wire calls = %d, want exactly 1 for an unchanged world", len(calls))
	}
	// The watermark did not move.
	wm, err := store.Queries().GetSyncSchedule(ctx, projectsProbeScheduleKey("team-1"))
	if err != nil {
		t.Fatalf("GetSyncSchedule: %v", err)
	}
	if !wm.Equal(watermark) {
		t.Errorf("watermark = %v, want unchanged %v", wm, watermark)
	}
}
