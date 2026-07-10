package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Fixture parity: the fixture set IS the fixture-mode test surface.
//
// Every table schema.sql creates must either be populated by the shared
// fixture population (populateTestFixtures) or sit on the explicit exclusion
// list below with a reason. A new table that lands in schema.sql without a
// fixture (or an exclusion entry) fails TestSchemaFixtureCoverage — so a new
// read surface can't silently become live-mode-only, which is exactly how
// render drift escapes CI.
// =============================================================================

// fixtureExcludedTables are schema tables intentionally NOT populated by the
// fixture set. Each entry needs a reason: either the table has no mount-visible
// render, or it is written only by machinery the fixture harness bypasses.
var fixtureExcludedTables = map[string]string{
	"sync_meta":           "sync-worker bookkeeping (last-sync watermarks); no mount-visible render",
	"sync_schedule":       "sync-worker bookkeeping (persisted schedule timestamps, e.g. last full cycle); no mount-visible render",
	"pending_detail_sync": "sync-worker retry ledger for failed detail fetches; no mount-visible render",
}

// TestSchemaFixtureCoverage asserts fixture coverage tracks the schema's table
// list: every table in the live fixture store (sqlite_master, i.e. schema.sql
// as applied) is either populated with at least one row or explicitly excluded.
func TestSchemaFixtureCoverage(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode conformance: audits the seeded fixture store")
	}

	ctx := context.Background()
	rows, err := testStore.DB().QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables found — schema not applied?")
	}

	seen := make(map[string]bool, len(tables))
	for _, table := range tables {
		seen[table] = true
		if reason, ok := fixtureExcludedTables[table]; ok {
			t.Logf("excluded: %s (%s)", table, reason)
			continue
		}
		var count int
		// Table names come from sqlite_master, not user input.
		q := fmt.Sprintf("SELECT COUNT(*) FROM %q", table)
		if err := testStore.DB().QueryRowContext(ctx, q).Scan(&count); err != nil {
			t.Errorf("count %s: %v", table, err)
			continue
		}
		if count == 0 {
			t.Errorf("table %q has no fixture rows: add a Populate call to populateTestFixtures "+
				"(internal/testutil/fixtures) or an entry in fixtureExcludedTables with a reason", table)
		}
	}

	// The exclusion list must not carry stale entries for dropped tables.
	for excluded := range fixtureExcludedTables {
		if !seen[excluded] {
			t.Errorf("fixtureExcludedTables lists %q, which is not in the schema anymore", excluded)
		}
	}
}

// =============================================================================
// Newly fixture-testable surfaces. Minimal by design: the point is that these
// surfaces exercise at all in fixture mode.
// =============================================================================

// TestFixtureRelationFiles: the seeded relation (TST-1 blocks TST-3) surfaces
// as a .rel file on both endpoints, and its content names the other end.
func TestFixtureRelationFiles(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic relation")
	}

	// Outgoing side: TST-1 blocks TST-3
	outPath := filepath.Join(issueDirPath(testTeamKey, "TST-1"), "relations", "blocks-TST-3.rel")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read %s: %v", outPath, err)
	}
	content := string(data)
	for _, want := range []string{"type: blocks", "to: TST-3"} {
		if !strings.Contains(content, want) {
			t.Errorf("outgoing .rel missing %q:\n%s", want, content)
		}
	}

	// Inverse side: TST-3 is blocked by TST-1 (same stored row, other end)
	inPath := filepath.Join(issueDirPath(testTeamKey, "TST-3"), "relations", "blocked-by-TST-1.rel")
	data, err = os.ReadFile(inPath)
	if err != nil {
		t.Fatalf("read %s: %v", inPath, err)
	}
	content = string(data)
	for _, want := range []string{"type: blocks", "from: TST-1"} {
		if !strings.Contains(content, want) {
			t.Errorf("inverse .rel missing %q:\n%s", want, content)
		}
	}
}

// TestFixtureIssueMetaRelations: issue.meta renders the relations block for
// both directions (outgoing "blocks", inverse rendered as "blocked-by").
func TestFixtureIssueMetaRelations(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic relation")
	}

	data, err := os.ReadFile(issueMetaPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Fatalf("read TST-1 issue.meta: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "relations:") || !strings.Contains(content, "TST-3") {
		t.Errorf("TST-1 issue.meta missing relations render:\n%s", content)
	}

	data, err = os.ReadFile(issueMetaPath(testTeamKey, "TST-3"))
	if err != nil {
		t.Fatalf("read TST-3 issue.meta: %v", err)
	}
	content = string(data)
	if !strings.Contains(content, "relations:") || !strings.Contains(content, "blocked-by") {
		t.Errorf("TST-3 issue.meta missing inverse relations render:\n%s", content)
	}
}

// TestFixtureMilestoneFile: the seeded milestone lists and reads under
// projects/<slug>/milestones/.
func TestFixtureMilestoneFile(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic milestone")
	}

	path := filepath.Join(projectsPath(testTeamKey), "test-project", "milestones", "Alpha Release.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read milestone: %v", err)
	}
	content := string(data)
	for _, want := range []string{"2024-03-31", "First alpha release"} {
		if !strings.Contains(content, want) {
			t.Errorf("milestone file missing %q:\n%s", want, content)
		}
	}
}

// TestFixtureProjectUpdateFile: the seeded project status update lists under
// projects/<slug>/updates/ and renders the health frontmatter.
func TestFixtureProjectUpdateFile(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic update")
	}
	assertUpdateDirHasHealthyUpdate(t, filepath.Join(projectsPath(testTeamKey), "test-project", "updates"))
}

// TestFixtureInitiativeUpdateFile: same for initiatives/<slug>/updates/.
func TestFixtureInitiativeUpdateFile(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic update")
	}
	assertUpdateDirHasHealthyUpdate(t, filepath.Join(initiativesPath(), "test-initiative", "updates"))
}

func assertUpdateDirHasHealthyUpdate(t *testing.T, updatesDir string) {
	t.Helper()
	entries, err := os.ReadDir(updatesDir)
	if err != nil {
		t.Fatalf("read %s: %v", updatesDir, err)
	}
	var updateFile string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") && !strings.HasPrefix(e.Name(), "_") {
			updateFile = e.Name()
			break
		}
	}
	if updateFile == "" {
		t.Fatalf("no update .md file in %s (fixture seeded one)", updatesDir)
	}
	data, err := os.ReadFile(filepath.Join(updatesDir, updateFile))
	if err != nil {
		t.Fatalf("read %s: %v", updateFile, err)
	}
	if !strings.Contains(string(data), "health: onTrack") {
		t.Errorf("update %s missing health frontmatter:\n%s", updateFile, data)
	}
}

// TestFixtureAttachmentLinkFile: the seeded external URL attachment surfaces
// as a .link file alongside the embedded files.
func TestFixtureAttachmentLinkFile(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic attachment")
	}

	path := filepath.Join(attachmentsPath(testTeamKey, "TST-1"), "Design Spec.link")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .link: %v", err)
	}
	content := string(data)
	for _, want := range []string{"title: Design Spec", "url: https://example.com/design-spec"} {
		if !strings.Contains(content, want) {
			t.Errorf(".link missing %q:\n%s", want, content)
		}
	}
}

// TestFixtureIssueHistoryRendered: the seeded history cache renders a real
// change in history.md (not the empty-history placeholder).
func TestFixtureIssueHistoryRendered(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic history")
	}

	path := filepath.Join(issueDirPath(testTeamKey, "TST-1"), "history.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read history.md: %v", err)
	}
	content := string(data)
	for _, want := range []string{"Status Changed", "Todo → In Progress", "test@example.com"} {
		if !strings.Contains(content, want) {
			t.Errorf("history.md missing %q:\n%s", want, content)
		}
	}
}

// TestFixtureByAssigneeMembers: with team_members populated, by/assignee lists
// the members' handles alongside "unassigned".
func TestFixtureByAssigneeMembers(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic membership")
	}

	entries, err := os.ReadDir(byAssigneePath(testTeamKey))
	if err != nil {
		t.Fatalf("read by/assignee: %v", err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	// assigneeHandle prefers DisplayName; see FixtureAPIUsers.
	for _, want := range []string{"unassigned", "Test User", "Jane", "Bob"} {
		if !names[want] {
			t.Errorf("by/assignee missing %q (got %v)", want, names)
		}
	}
}

// TestFixtureMyViewsPopulated: with viewer_cache populated (viewer = user-1,
// the default fixture assignee), the my/ views resolve offline and are
// non-empty.
func TestFixtureMyViewsPopulated(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded synthetic viewer")
	}

	entries, err := os.ReadDir(myAssignedPath())
	if err != nil {
		t.Fatalf("read my/assigned: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("my/assigned is empty; viewer fixture (viewer_cache -> user-1) did not resolve")
	}
	found := false
	for _, e := range entries {
		if e.Name() == "TST-1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("my/assigned missing TST-1 (assigned to the fixture viewer); got %d entries", len(entries))
	}
}
