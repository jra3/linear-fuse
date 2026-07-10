package api

import (
	"context"
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
