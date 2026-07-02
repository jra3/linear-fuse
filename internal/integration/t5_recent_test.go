package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
)

// TestT5_RecentViewOrdered is the T5/#153 receipt: teams/{KEY}/recent/ lists the
// team's issues newest-first by updatedAt (a shell-flag-independent alternative
// to `ls -t`). It seeds three issues with distinct, non-insertion-order
// timestamps so a missing fs-layer sort would fail the ordering assertion.
func TestT5_RecentViewOrdered(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode ordering check; seeds issues directly into the store")
	}
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// Non-insertion-order timestamps: expected newest-first is 9001, 9003, 9002.
	seed := []struct {
		id, ident string
		updated   time.Time
	}{
		{"recent-issue-9001", "TST-9001", base.Add(3 * time.Hour)},
		{"recent-issue-9002", "TST-9002", base.Add(1 * time.Hour)},
		{"recent-issue-9003", "TST-9003", base.Add(2 * time.Hour)},
	}
	for _, s := range seed {
		issue := fixtures.FixtureAPIIssue(
			fixtures.WithIssueID(s.id, s.ident),
			fixtures.WithTitle("Recent "+s.ident),
			fixtures.WithTeam(&api.Team{ID: testTeamID, Key: testTeamKey, Name: "Test Team"}),
			fixtures.WithCreatedAt(base),
			fixtures.WithUpdatedAt(s.updated),
		)
		if err := lfs.UpsertIssue(ctx, issue); err != nil {
			t.Fatalf("seed %s: %v", s.ident, err)
		}
	}

	// recent/ appears in the team listing.
	if !dirContains(teamPath(testTeamKey), "recent") {
		t.Fatal("recent/ not listed in team directory")
	}

	// Read raw directory order (os.ReadDir sorts by name; Readdirnames preserves
	// the order the FUSE readdir returned — i.e. newest-first).
	f, err := os.Open(recentPath(testTeamKey))
	if err != nil {
		t.Fatalf("open recent/: %v", err)
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		t.Fatalf("readdirnames recent/: %v", err)
	}

	// Filter to our seeded identifiers, preserving directory order.
	var got []string
	want := map[string]bool{"TST-9001": true, "TST-9002": true, "TST-9003": true}
	for _, n := range names {
		if want[n] {
			got = append(got, n)
		}
	}
	expected := []string{"TST-9001", "TST-9003", "TST-9002"}
	if strings.Join(got, ",") != strings.Join(expected, ",") {
		t.Fatalf("recent/ order = %v, want newest-first %v (fs-layer sort missing?)", got, expected)
	}

	// Each entry is a symlink to ../issues/{id} and resolves to a readable issue.
	target, err := os.Readlink(filepath.Join(recentPath(testTeamKey), "TST-9001"))
	if err != nil {
		t.Fatalf("readlink recent/TST-9001: %v", err)
	}
	if !strings.HasPrefix(target, "../issues/") {
		t.Errorf("symlink target %q should start with ../issues/", target)
	}
	if _, err := os.ReadFile(filepath.Join(recentPath(testTeamKey), "TST-9001", "issue.md")); err != nil {
		t.Errorf("recent/TST-9001/issue.md not readable: %v", err)
	}
}

// TestT5_FreshCreateAppearsInRecent: a just-created issue is visible in recent/
// immediately — the create tail re-cohers the view rather than leaving it stale
// until the dir cache TTL (the #148 design's known staleness bound, now closed).
func TestT5_FreshCreateAppearsInRecent(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode behavioral check; uses the mock mutator")
	}
	enableMockMutations(t)

	// Prime the kernel's view of recent/ so a stale cached listing would be caught.
	if _, err := os.ReadDir(recentPath(testTeamKey)); err != nil {
		t.Fatalf("prime recent/: %v", err)
	}

	if err := os.Mkdir(filepath.Join(issuesPath(testTeamKey), "Recent Visibility Probe"), 0755); err != nil {
		t.Fatalf("mkdir create: %v", err)
	}

	// Recover the created identifier from issues/.last (typed name != identifier).
	data, err := os.ReadFile(issuesLastPath(testTeamKey))
	if err != nil {
		t.Fatalf("read issues/.last: %v", err)
	}
	var entries []map[string]string
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("issues/.last not a YAML list: %v\n%s", err, data)
	}
	ident := ""
	for _, e := range entries {
		if e["title"] == "Recent Visibility Probe" {
			ident = e["path"]
		}
	}
	if ident == "" {
		t.Fatalf("issues/.last has no entry for our create; got: %s", data)
	}

	names, err := os.ReadDir(recentPath(testTeamKey))
	if err != nil {
		t.Fatalf("re-read recent/: %v", err)
	}
	for _, n := range names {
		if n.Name() == ident {
			return
		}
	}
	t.Fatalf("fresh create %s missing from recent/ (stale listing?)", ident)
}

// TestT5_RecentIsReadOnly: the recent/ view rejects mutation.
func TestT5_RecentIsReadOnly(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode only")
	}
	if err := os.Mkdir(filepath.Join(recentPath(testTeamKey), "Nope"), 0755); err == nil {
		t.Error("recent/ should be read-only, but mkdir succeeded")
	}
}
