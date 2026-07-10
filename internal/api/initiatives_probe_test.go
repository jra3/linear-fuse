package api

// Wire tests for the initiatives change-detection probe (#244): the exact
// request shape matters here — the probe's whole point is costing a few
// hundred complexity instead of the workspace query's ~7.2K, and the
// cost difference lives in the query text (small first:, no nested
// projects connection). Scripted through the mock server like the drain
// e2e tests.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/testutil"
)

// TestGetInitiativesProbeShape asserts the probe query's load-bearing
// shape: a single small newest-first page (first: 5, orderBy: updatedAt),
// projected through the InitiativeFields fragment, with NO nested projects
// connection and NO pageInfo (the probe never paginates).
func TestGetInitiativesProbeShape(t *testing.T) {
	t.Parallel()
	mock := testutil.NewMockLinearServer()
	defer mock.Close()

	mock.SetResponse("InitiativesProbe", map[string]any{
		"initiatives": map[string]any{
			"nodes": []map[string]any{
				{"id": "init-1", "name": "Alpha", "slugId": "alpha", "updatedAt": "2026-07-10T12:00:00.000Z"},
				{"id": "init-2", "name": "Beta", "slugId": "beta", "updatedAt": "2026-07-09T08:30:00.000Z"},
			},
		},
	})

	c := NewClient("test")
	c.SetAPIURL(mock.URL())

	initiatives, err := c.GetInitiativesProbe(context.Background())
	if err != nil {
		t.Fatalf("GetInitiativesProbe: %v", err)
	}
	if len(initiatives) != 2 {
		t.Fatalf("initiatives = %d, want 2", len(initiatives))
	}
	if initiatives[0].ID != "init-1" || initiatives[0].Name != "Alpha" {
		t.Errorf("initiatives[0] = %+v, want init-1/Alpha", initiatives[0])
	}
	want := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if !initiatives[0].UpdatedAt.Equal(want) {
		t.Errorf("initiatives[0].UpdatedAt = %v, want %v", initiatives[0].UpdatedAt, want)
	}
	if len(initiatives[0].Projects.Nodes) != 0 || initiatives[0].Projects.PageInfo != nil {
		t.Errorf("probe initiative carries projects %+v, want none (scalars only)", initiatives[0].Projects)
	}

	calls := mock.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want exactly 1 (the probe never paginates)", len(calls))
	}
	query := calls[0].Query

	// The cheap shape: a small newest-first page.
	if !strings.Contains(query, "first: 5") {
		t.Errorf("query missing 'first: 5':\n%s", query)
	}
	if !strings.Contains(query, "orderBy: updatedAt") {
		t.Errorf("query missing 'orderBy: updatedAt':\n%s", query)
	}

	// The cost guarantee: NO nested projects connection — the nested
	// first: arguments are what make the workspace query cost ~7.2K.
	if strings.Contains(query, "projects") {
		t.Errorf("probe query selects a projects connection — that is the ~7.2K cost the probe exists to avoid:\n%s", query)
	}

	// Fragment rule: scalars project through InitiativeFields, no inline copy.
	if !strings.Contains(query, "...InitiativeFields") || !strings.Contains(query, "fragment InitiativeFields on Initiative") {
		t.Errorf("query must project through the InitiativeFields fragment:\n%s", query)
	}

	// No pageInfo: single-page by design (selecting it would arm the
	// fetchNodes truncation tripwire on a 6th initiative).
	if strings.Contains(query, "pageInfo") {
		t.Errorf("probe query selects pageInfo — it must stay single-page:\n%s", query)
	}
}
