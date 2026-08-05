package integration

import (
	"os"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/marshal"
)

// The team description is disclosed twice — frontmatter and body prose — and
// these pin it from the mount, where the render tests (internal/fs) cannot see
// whether the value actually survived the API → SQLite → repo round trip.

// TestTeamDescriptionReachesMount proves the description survives the whole
// path, not just the renderer. It names the seeded value, so it is
// fixture-mode only.
func TestTeamDescriptionReachesMount(t *testing.T) {
	skipIfLiveAPI(t, fixtureSeededData)

	data, err := os.ReadFile(teamInfoPath(testTeamKey))
	if err != nil {
		t.Fatalf("read team.md: %v", err)
	}
	doc, err := marshal.Parse(data)
	if err != nil {
		t.Fatalf("team.md is not parseable: %v", err)
	}
	const want = "The test team: for fixtures"
	if got := doc.Frontmatter["description"]; got != want {
		t.Errorf("description = %v, want %q", got, want)
	}
	if !strings.Contains(doc.Body, want) {
		t.Errorf("team.md body does not carry the description:\n%s", doc.Body)
	}
}

// TestTeamDescriptionFrontmatterMatchesBody audits both disclosures for EVERY
// team the workspace shows: whatever frontmatter claims must appear in the
// prose an agent reads. It runs in every mode — offline the fixture's TST
// description gives it substance, live it audits the real workspace, where
// SUB's absent description also pins the omit-when-unset half.
func TestTeamDescriptionFrontmatterMatchesBody(t *testing.T) {
	entries, err := os.ReadDir(teamsPath())
	if err != nil {
		t.Fatalf("read teams: %v", err)
	}

	checked := 0
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

		desc, ok := doc.Frontmatter["description"]
		if !ok {
			continue
		}
		// Present means non-empty: the renderer omits the key for a team that
		// never set one, so an empty value here means the two spellings of
		// "unset" have diverged.
		text, isString := desc.(string)
		if !isString || text == "" {
			t.Errorf("%s/team.md has description %#v; want the key omitted when unset", teamKey, desc)
			continue
		}
		if !strings.Contains(doc.Body, text) {
			t.Errorf("%s/team.md frontmatter describes the team but the body does not:\n%s", teamKey, doc.Body)
		}
		checked++
	}
	t.Logf("checked %d team description(s)", checked)
}

// TestTeamDefaultsReachMount proves the issue-creation defaults survive the
// whole path — API → data blob → SQLite → repo → render — and not just the
// renderer. It names the seeded values, so it is fixture-mode only.
func TestTeamDefaultsReachMount(t *testing.T) {
	skipIfLiveAPI(t, fixtureSeededData)

	data, err := os.ReadFile(teamInfoPath(testTeamKey))
	if err != nil {
		t.Fatalf("read team.md: %v", err)
	}
	doc, err := marshal.Parse(data)
	if err != nil {
		t.Fatalf("team.md is not parseable: %v", err)
	}

	defaults, ok := doc.Frontmatter["defaults"].(map[string]any)
	if !ok {
		t.Fatalf("defaults = %#v, want a map", doc.Frontmatter["defaults"])
	}
	for key, want := range map[string]any{
		"issue_state":  "Todo",
		"triage":       true,
		"triage_state": "Triage",
		"estimation":   "fibonacci",
	} {
		if got := defaults[key]; got != want {
			t.Errorf("defaults[%q] = %#v, want %#v", key, got, want)
		}
	}

	templates, ok := doc.Frontmatter["templates"].(map[string]any)
	if !ok {
		t.Fatalf("templates = %#v, want a map", doc.Frontmatter["templates"])
	}
	issue, _ := templates["issue"].(map[string]any)
	if got := issue["name"]; got != "Bug report" {
		t.Errorf("templates.issue.name = %#v, want %q", got, "Bug report")
	}
}

// TestTeamDefaultsAreKnownOrAbsent audits the sentinel across EVERY team the
// workspace shows, in every mode: a `defaults` block must name a real
// estimation scale, never the empty string. An empty one would mean the
// unknown-settings gate leaked, and every reader downstream would take
// "nobody has synced this yet" for "this team estimates in nothing".
func TestTeamDefaultsAreKnownOrAbsent(t *testing.T) {
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

		raw, ok := doc.Frontmatter["defaults"]
		if !ok {
			continue
		}
		defaults, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("%s/team.md defaults = %#v, want a map", teamKey, raw)
			continue
		}
		if scale, _ := defaults["estimation"].(string); scale == "" {
			t.Errorf("%s/team.md published a defaults block with no estimation scale (%#v); "+
				"the unknown-settings gate must omit the block instead", teamKey, defaults)
		}
	}
}
