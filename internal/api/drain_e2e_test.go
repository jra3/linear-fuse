package api

import (
	"context"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/testutil"
)

// End-to-end drain tests: real queries through the mock server, multi-page
// sequences scripted with SetResponseSequence (a single static SetResponse
// can never terminate a pagination loop whose first page says hasNextPage).

func pf(hasNext bool, cursor string) map[string]any {
	return map[string]any{"hasNextPage": hasNext, "endCursor": cursor}
}

func connOf(pageInfo map[string]any, nodes ...map[string]any) map[string]any {
	if nodes == nil {
		nodes = []map[string]any{}
	}
	return map[string]any{"pageInfo": pageInfo, "nodes": nodes}
}

func TestGetTeamProjectsPaginates(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	pageA := map[string]any{"team": map[string]any{"projects": connOf(pf(true, "cursor-1"),
		map[string]any{"id": "proj-a", "name": "Alpha", "slugId": "alpha"})}}
	pageB := map[string]any{"team": map[string]any{"projects": connOf(pf(false, ""),
		map[string]any{"id": "proj-b", "name": "Beta", "slugId": "beta"})}}
	mock.SetResponseSequence("TeamProjects", pageA, pageB)

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	projects, err := c.GetTeamProjects(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("GetTeamProjects: %v", err)
	}
	if len(projects) != 2 || projects[0].Name != "Alpha" || projects[1].Name != "Beta" {
		t.Fatalf("projects = %+v, want [Alpha Beta]", projects)
	}

	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].Variables["after"] != nil {
		t.Errorf("first page sent after=%v, want omitted", calls[0].Variables["after"])
	}
	if calls[1].Variables["after"] != "cursor-1" {
		t.Errorf("second page after = %v, want cursor-1", calls[1].Variables["after"])
	}
}

// TestGetTeamProjectsNewestPageQueryShape: the lean cycle's projects probe
// query (#243) — newest-first ordering, caller-chosen page size, cursor
// passed only on resume pages, nodes projected through ProjectFields (so a
// probed project carries the full drain's field set, nested milestones
// included).
func TestGetTeamProjectsNewestPageQueryShape(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponseSequence("TeamProjectsByUpdatedAt",
		map[string]any{"team": map[string]any{"projects": connOf(pf(true, "cursor-1"),
			map[string]any{"id": "proj-a", "name": "Alpha", "slugId": "alpha",
				"updatedAt": "2026-07-09T12:34:56.017Z",
				"projectMilestones": map[string]any{"nodes": []map[string]any{
					{"id": "ms-1", "name": "Phase 1", "sortOrder": 1.0},
				}}})}},
		map[string]any{"team": map[string]any{"projects": connOf(pf(false, ""),
			map[string]any{"id": "proj-b", "name": "Beta", "slugId": "beta"})}},
	)

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	// Probe page: small first, no cursor.
	projects, pageInfo, err := c.GetTeamProjectsNewestPage(context.Background(), "team-1", "", 5)
	if err != nil {
		t.Fatalf("GetTeamProjectsNewestPage: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "Alpha" {
		t.Fatalf("projects = %+v, want [Alpha]", projects)
	}
	if projects[0].Milestones == nil || len(projects[0].Milestones.Nodes) != 1 {
		t.Errorf("probed project milestones = %+v, want the fragment's nested nodes", projects[0].Milestones)
	}
	if !pageInfo.HasNextPage || pageInfo.EndCursor != "cursor-1" {
		t.Errorf("pageInfo = %+v, want hasNextPage cursor-1", pageInfo)
	}

	// Resume page: the caller passes the cursor and a larger page.
	if _, _, err := c.GetTeamProjectsNewestPage(context.Background(), "team-1", "cursor-1", 50); err != nil {
		t.Fatalf("resume page: %v", err)
	}

	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if !strings.Contains(calls[0].Query, "orderBy: updatedAt") {
		t.Errorf("query missing newest-first ordering:\n%s", calls[0].Query)
	}
	if !strings.Contains(calls[0].Query, "...ProjectFields") {
		t.Errorf("query must project through ProjectFields, not an inline copy:\n%s", calls[0].Query)
	}
	if got := calls[0].Variables["first"]; got != float64(5) {
		t.Errorf("probe first = %v, want 5", got)
	}
	if calls[0].Variables["after"] != nil {
		t.Errorf("probe after = %v, want omitted", calls[0].Variables["after"])
	}
	if got := calls[1].Variables["first"]; got != float64(50) {
		t.Errorf("resume first = %v, want 50", got)
	}
	if got := calls[1].Variables["after"]; got != "cursor-1" {
		t.Errorf("resume after = %v, want cursor-1", got)
	}
}

func TestGetTeamMetadataDrainsOverflowingConnections(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	// Combined query: labels overflow (hasNextPage), everything else fits.
	mock.SetResponse("TeamMetadata", map[string]any{
		"team": map[string]any{
			"states": map[string]any{"nodes": []map[string]any{{"id": "s1", "name": "Todo", "type": "unstarted"}}},
			"labels": connOf(pf(true, "lab-cursor"),
				map[string]any{"id": "l1", "name": "bug", "color": "#f00"}),
			"cycles":  connOf(pf(false, "")),
			"members": connOf(pf(false, "")),
		},
		"issueLabels": connOf(pf(false, ""),
			map[string]any{"id": "l3", "name": "workspace", "color": "#00f"}),
	})
	// The labels drain resumes from lab-cursor.
	mock.SetResponse("TeamLabelsPage", map[string]any{
		"team": map[string]any{
			"labels": connOf(pf(false, ""),
				map[string]any{"id": "l2", "name": "perf", "color": "#0f0"}),
		},
	})
	mock.SetResponse("TeamProjects", testutil.TeamProjectsResponse())

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	meta, err := c.GetTeamMetadata(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("GetTeamMetadata: %v", err)
	}
	if len(meta.Labels) != 3 {
		t.Fatalf("labels = %+v, want 3 (combined page + drained page + workspace)", meta.Labels)
	}
	if meta.Labels[0].ID != "l1" || meta.Labels[1].ID != "l2" || meta.Labels[2].ID != "l3" {
		t.Errorf("label order = %v %v %v, want l1 l2 l3 (drained tail before workspace merge)",
			meta.Labels[0].ID, meta.Labels[1].ID, meta.Labels[2].ID)
	}

	// The drain must have resumed from the combined query's cursor.
	var drainCall *testutil.GraphQLCall
	for i, call := range mock.Calls() {
		if call.Operation == "TeamLabelsPage" {
			drainCall = &mock.Calls()[i]
		}
	}
	if drainCall == nil {
		t.Fatal("no TeamLabelsPage drain call recorded")
	}
	if drainCall.Variables["after"] != "lab-cursor" {
		t.Errorf("drain after = %v, want lab-cursor", drainCall.Variables["after"])
	}
}

// The combined metadata queries decode their first page through the read
// envelope's walkPath descent (#263): a null parent object or connection is an
// error, never a silently empty TeamMetadata / workspace that a sync prune
// would read as "everything was removed".

func TestGetTeamMetadataNullTeamFails(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	// team is null (nonexistent id); the old anonymous-struct decode returned
	// an empty TeamMetadata with a nil error.
	mock.SetResponse("TeamMetadata", map[string]any{
		"team":        nil,
		"issueLabels": connOf(pf(false, "")),
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	if _, err := c.GetTeamMetadata(context.Background(), "team-1"); err == nil {
		t.Fatal("expected error for null team, got nil")
	} else if !strings.Contains(err.Error(), "team") {
		t.Errorf("error = %q, want it to name the team path", err)
	}
}

func TestGetWorkspaceNullConnectionFails(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	// initiatives is null; the drained set feeds a junction-row prune, so a
	// silent empty must not pass as an authoritative "no initiatives".
	mock.SetResponse("Workspace", map[string]any{
		"users":       connOf(pf(false, ""), map[string]any{"id": "u1", "name": "User", "email": "u@example.com"}),
		"initiatives": nil,
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	if _, err := c.GetWorkspace(context.Background()); err == nil {
		t.Fatal("expected error for null initiatives connection, got nil")
	} else if !strings.Contains(err.Error(), "initiatives") {
		t.Errorf("error = %q, want it to name the initiatives path", err)
	}
}

func TestGetWorkspaceDrainsNestedInitiativeProjects(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("Workspace", map[string]any{
		"users": connOf(pf(false, ""),
			map[string]any{"id": "u1", "name": "User", "email": "u@example.com"}),
		"initiatives": connOf(pf(false, ""),
			map[string]any{
				"id": "init-1", "name": "Big Initiative", "slugId": "big",
				"projects": connOf(pf(true, "proj-cursor"),
					map[string]any{"id": "p1", "name": "One", "slugId": "one"}),
			}),
	})
	mock.SetResponse("InitiativeProjectsPage", map[string]any{
		"initiative": map[string]any{
			"projects": connOf(pf(false, ""),
				map[string]any{"id": "p2", "name": "Two", "slugId": "two"}),
		},
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	ws, err := c.GetWorkspace(context.Background())
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if len(ws.Initiatives) != 1 {
		t.Fatalf("initiatives = %d, want 1", len(ws.Initiatives))
	}
	init := ws.Initiatives[0]
	if len(init.Projects.Nodes) != 2 || init.Projects.Nodes[0].ID != "p1" || init.Projects.Nodes[1].ID != "p2" {
		t.Fatalf("initiative projects = %+v, want [p1 p2]", init.Projects.Nodes)
	}
	if init.Projects.PageInfo != nil {
		t.Error("initiative Projects.PageInfo should be cleared after drain")
	}
}

func TestGetInitiativeDrainsNestedProjects(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("Initiative", map[string]any{
		"initiative": map[string]any{
			"id": "init-1", "name": "Big Initiative", "slugId": "big",
			"projects": connOf(pf(true, "proj-cursor"),
				map[string]any{"id": "p1", "name": "One", "slugId": "one"}),
		},
	})
	mock.SetResponse("InitiativeProjectsPage", map[string]any{
		"initiative": map[string]any{
			"projects": connOf(pf(false, ""),
				map[string]any{"id": "p2", "name": "Two", "slugId": "two"}),
		},
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	init, err := c.GetInitiative(context.Background(), "init-1")
	if err != nil {
		t.Fatalf("GetInitiative: %v", err)
	}
	if len(init.Projects.Nodes) != 2 || init.Projects.Nodes[0].ID != "p1" || init.Projects.Nodes[1].ID != "p2" {
		t.Fatalf("initiative projects = %+v, want [p1 p2]", init.Projects.Nodes)
	}
	if init.Projects.PageInfo != nil {
		t.Error("initiative Projects.PageInfo should be cleared after drain")
	}

	// The drain must resume from the first page's cursor.
	var drainCall *testutil.GraphQLCall
	for i, call := range mock.Calls() {
		if call.Operation == "InitiativeProjectsPage" {
			drainCall = &mock.Calls()[i]
		}
	}
	if drainCall == nil {
		t.Fatal("no InitiativeProjectsPage drain call recorded")
	}
	if drainCall.Variables["after"] != "proj-cursor" {
		t.Errorf("drain after = %v, want proj-cursor", drainCall.Variables["after"])
	}
}

func TestGetWorkspaceProjectIDsPaginates(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponseSequence("WorkspaceProjectIDs",
		map[string]any{"projects": connOf(pf(true, "id-cursor"), map[string]any{"id": "p1"})},
		map[string]any{"projects": connOf(pf(false, ""), map[string]any{"id": "p2"})},
	)

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	ids, err := c.GetWorkspaceProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("GetWorkspaceProjectIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "p1" || ids[1] != "p2" {
		t.Fatalf("ids = %v, want [p1 p2]", ids)
	}
}
