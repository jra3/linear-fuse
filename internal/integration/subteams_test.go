package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jra3/linear-fuse/internal/marshal"
)

// The team hierarchy is exposed twice — as team.md frontmatter and as symlinks
// (`parent`, `subteams/`) — and the two must agree, because an agent will
// traverse whichever one it read. These tests pin that agreement from the
// mount, where the fixture render tests (internal/fs) cannot see it.

// TestSubteamSymlinksResolve walks the seeded TST → SUB edge end to end: both
// directions, both representations, and the round trip back to the same
// directory. It names fixture rows, so it is fixture-mode only.
func TestSubteamSymlinksResolve(t *testing.T) {
	skipIfLiveAPI(t, fixtureSeededData)

	// Parent side: subteams/SUB is a symlink to the sibling team directory.
	link := filepath.Join(teamPath(testTeamKey), "subteams", "SUB")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink %s: %v", link, err)
	}
	if target != "../../SUB" {
		t.Errorf("subteams/SUB -> %q, want ../../SUB", target)
	}
	// Resolving it must land on the real team directory, not a dangling path.
	if _, err := os.Stat(filepath.Join(link, "team.md")); err != nil {
		t.Errorf("subteams/SUB does not resolve to a team directory: %v", err)
	}

	// Child side: SUB/parent points back at TST, and the round trip returns to
	// the directory it started from.
	parentLink := filepath.Join(teamPath("SUB"), "parent")
	target, err = os.Readlink(parentLink)
	if err != nil {
		t.Fatalf("readlink %s: %v", parentLink, err)
	}
	if want := "../" + testTeamKey; target != want {
		t.Errorf("SUB/parent -> %q, want %q", target, want)
	}
	roundTrip, err := filepath.EvalSymlinks(filepath.Join(parentLink, "subteams", "SUB"))
	if err != nil {
		t.Fatalf("round trip SUB/parent/subteams/SUB: %v", err)
	}
	want, err := filepath.EvalSymlinks(teamPath("SUB"))
	if err != nil {
		t.Fatalf("resolve teams/SUB: %v", err)
	}
	if roundTrip != want {
		t.Errorf("round trip landed on %q, want %q", roundTrip, want)
	}

	// The hierarchy is set in Linear, so rm of a link must fail loud rather
	// than silently no-op and resurrect on the next readdir (#286/#287).
	if err := os.Remove(link); err == nil {
		t.Error("rm subteams/SUB succeeded, want EPERM (the edge is owned by Linear)")
	}
	if err := os.Remove(filepath.Join(teamPath("SUB"), "parent")); err == nil {
		t.Error("rm SUB/parent succeeded, want EPERM")
	}

	// A top-level team lists no parent entry at all — absence is how the
	// filesystem says "no parent", so the entry must not exist as a dangler.
	if _, err := os.Lstat(filepath.Join(teamPath(testTeamKey), "parent")); !os.IsNotExist(err) {
		t.Errorf("teams/%s/parent exists for a top-level team (err=%v)", testTeamKey, err)
	}
	names := dirNames(t, teamPath(testTeamKey))
	if names["parent"] {
		t.Errorf("teams/%s lists a parent entry for a top-level team: %v", testTeamKey, names)
	}
	if !names["subteams"] {
		t.Errorf("teams/%s does not list subteams/: %v", testTeamKey, names)
	}
}

// TestTeamHierarchyFrontmatterMatchesLinks cross-checks the two
// representations for EVERY team the workspace shows: team.md's parent/subteams
// must name exactly the entries the symlinks provide, and every name must
// resolve. It runs in every mode — offline the fixture's TST/SUB edge gives it
// substance, live it audits the real hierarchy.
func TestTeamHierarchyFrontmatterMatchesLinks(t *testing.T) {
	entries, err := os.ReadDir(teamsPath())
	if err != nil {
		t.Fatalf("read teams: %v", err)
	}

	for _, e := range entries {
		if !e.IsDir() || isControlFile(e.Name()) {
			continue
		}
		teamKey := e.Name()

		data, err := os.ReadFile(teamInfoPath(teamKey))
		if err != nil {
			t.Errorf("read %s/team.md: %v", teamKey, err)
			continue
		}
		doc, err := marshal.Parse(data)
		if err != nil {
			t.Errorf("%s/team.md is not parseable: %v", teamKey, err)
			continue
		}

		// parent: frontmatter and the symlink agree on presence and on target.
		fmParent, _ := doc.Frontmatter["parent"].(string)
		linkTarget, linkErr := os.Readlink(filepath.Join(teamPath(teamKey), "parent"))
		switch {
		case fmParent != "" && linkErr != nil:
			t.Errorf("%s/team.md names parent %q but there is no parent symlink: %v", teamKey, fmParent, linkErr)
		case fmParent == "" && linkErr == nil:
			t.Errorf("%s has a parent symlink (-> %s) but team.md names no parent", teamKey, linkTarget)
		case fmParent != "" && linkErr == nil:
			if want := "../" + fmParent; linkTarget != want {
				t.Errorf("%s/parent -> %q, but team.md says parent is %q (want %q)", teamKey, linkTarget, fmParent, want)
			}
			if _, err := os.Stat(filepath.Join(teamPath(teamKey), "parent", "team.md")); err != nil {
				t.Errorf("%s/parent does not resolve to a team directory: %v", teamKey, err)
			}
		}

		// subteams: the frontmatter list is exactly the directory listing.
		listed := dirNames(t, filepath.Join(teamPath(teamKey), "subteams"))
		declared := map[string]bool{}
		fmSubteams, _ := doc.Frontmatter["subteams"].([]any)
		for _, v := range fmSubteams {
			key, ok := v.(string)
			if !ok {
				t.Errorf("%s/team.md subteams entry %v is not a string", teamKey, v)
				continue
			}
			declared[key] = true
			if !listed[key] {
				t.Errorf("%s/team.md names sub-team %q, but subteams/ does not list it (%v)", teamKey, key, listed)
			}
			if _, err := os.Stat(filepath.Join(teamPath(teamKey), "subteams", key, "team.md")); err != nil {
				t.Errorf("%s/subteams/%s does not resolve to a team directory: %v", teamKey, key, err)
			}
		}
		for key := range listed {
			if !declared[key] {
				t.Errorf("%s/subteams/ lists %q, but team.md does not name it", teamKey, key)
			}
		}
	}
}
