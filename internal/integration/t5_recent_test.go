package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestT5_RecentIsReadOnly: the recent/ view rejects mutation.
func TestT5_RecentIsReadOnly(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode only")
	}
	if err := os.Mkdir(filepath.Join(recentPath(testTeamKey), "Nope"), 0755); err == nil {
		t.Error("recent/ should be read-only, but mkdir succeeded")
	}
}
