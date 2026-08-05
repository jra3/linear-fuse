package fs

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// TestTeamCatalogHostileNames pins the injection fix for the team catalogs:
// the hand-built frontmatter emitted `name: Q3: Triage` unquoted (invalid
// YAML) in states.md/labels.md — the reference files agents machine-parse to
// find valid values after a validation .error. Renders must stay parseable
// YAML and recover hostile names byte-exactly.
func TestTeamCatalogHostileNames(t *testing.T) {
	t.Parallel()
	team := api.Team{ID: "team-1", Key: "ENG", Name: `Team "Core": Platform`,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}

	t.Run("states.md", func(t *testing.T) {
		t.Parallel()
		states := []api.State{{ID: "s1", Name: "Q3: Triage", Type: "triage"}}
		doc, err := marshal.Parse(statesMarkdown(team, states))
		if err != nil {
			t.Fatalf("states.md render is not parseable YAML frontmatter: %v", err)
		}
		if doc.Frontmatter["team"] != "ENG" {
			t.Errorf("team key = %v, want ENG", doc.Frontmatter["team"])
		}
		entries, _ := doc.Frontmatter["states"].([]any)
		if len(entries) != 1 {
			t.Fatalf("states = %v, want 1 entry", doc.Frontmatter["states"])
		}
		entry, _ := entries[0].(map[string]any)
		if got := entry["name"]; got != "Q3: Triage" {
			t.Errorf("state name round-tripped to %v, want %q", got, "Q3: Triage")
		}
	})

	t.Run("labels.md", func(t *testing.T) {
		t.Parallel()
		labels := []api.Label{{ID: "l1", Name: `He said "ship it"`, Color: "#5e6ad2",
			Description: "desc: with colon"}}
		doc, err := marshal.Parse(labelsMarkdown(team, labels))
		if err != nil {
			t.Fatalf("labels.md render is not parseable YAML frontmatter: %v", err)
		}
		entries, _ := doc.Frontmatter["labels"].([]any)
		if len(entries) != 1 {
			t.Fatalf("labels = %v, want 1 entry", doc.Frontmatter["labels"])
		}
		entry, _ := entries[0].(map[string]any)
		if got := entry["name"]; got != `He said "ship it"` {
			t.Errorf("label name round-tripped to %v, want %q", got, `He said "ship it"`)
		}
		if got := entry["color"]; got != "#5e6ad2" {
			t.Errorf("label color round-tripped to %v, want #5e6ad2 (a plain # starts a YAML comment)", got)
		}
	})

	t.Run("team.md", func(t *testing.T) {
		t.Parallel()
		content := teamMarkdown(team, nil, nil)
		doc, err := marshal.Parse(content)
		if err != nil {
			t.Fatalf("team.md render is not parseable YAML frontmatter: %v", err)
		}
		if got := doc.Frontmatter["name"]; got != team.Name {
			t.Errorf("team name round-tripped to %v, want %q", got, team.Name)
		}
		// The prose body survives untouched below the frontmatter.
		if !strings.Contains(string(content), "- **Key:** ENG") {
			t.Errorf("team.md body missing the key bullet:\n%s", content)
		}
		// A top-level, childless team names no hierarchy it doesn't have —
		// absence is the disclosure, so an agent never chases a `parent`
		// symlink that Readdir doesn't list.
		for _, key := range []string{"parent", "parent_id", "subteams"} {
			if _, ok := doc.Frontmatter[key]; ok {
				t.Errorf("team.md carries %q for a team with no parent and no sub-teams", key)
			}
		}
	})
}

// TestTeamHierarchyRender pins the two directions of the sub-team edge in
// team.md: the frontmatter values must be the DIRECTORY NAMES the `parent` and
// `subteams/` symlinks point at, so an agent that reads the frontmatter and an
// agent that reads the listing traverse to the same place.
func TestTeamHierarchyRender(t *testing.T) {
	t.Parallel()
	parent := &api.Team{ID: "team-plat", Key: "PLAT", Name: "Platform"}
	team := api.Team{ID: "team-1", Key: "ENG", Name: "Engineering", Parent: parent}
	children := []api.Team{
		{ID: "team-fe", Key: "FE", Name: "Frontend"},
		{ID: "team-be", Key: "BE", Name: "Backend"},
	}

	doc, err := marshal.Parse(teamMarkdown(team, children, nil))
	if err != nil {
		t.Fatalf("team.md render is not parseable YAML frontmatter: %v", err)
	}
	if got := doc.Frontmatter["parent"]; got != "PLAT" {
		t.Errorf("parent = %v, want PLAT", got)
	}
	if got := doc.Frontmatter["parent_id"]; got != "team-plat" {
		t.Errorf("parent_id = %v, want team-plat", got)
	}
	subteams, _ := doc.Frontmatter["subteams"].([]any)
	if len(subteams) != 2 || subteams[0] != "FE" || subteams[1] != "BE" {
		t.Errorf("subteams = %v, want [FE BE]", doc.Frontmatter["subteams"])
	}

	// The frontmatter key must equal the last component of the symlink target:
	// one is derived from the other, and a divergence sends `cd $(parent)` to
	// a directory that does not exist.
	if got, want := parentLinkTarget(team), "../PLAT"; got != want {
		t.Errorf("parent link target = %q, want %q", got, want)
	}

	// An edge whose team is absent from the local copy: the raw ID is all
	// there is, so no key is invented and no symlink is listed.
	orphan := api.Team{ID: "team-2", Key: "OPS", Name: "Ops", Parent: &api.Team{ID: "team-ghost"}}
	doc, err = marshal.Parse(teamMarkdown(orphan, nil, nil))
	if err != nil {
		t.Fatalf("team.md render for an unresolvable parent: %v", err)
	}
	if _, ok := doc.Frontmatter["parent"]; ok {
		t.Errorf("team.md named a parent key for a parent it cannot resolve: %v", doc.Frontmatter["parent"])
	}
	if got := doc.Frontmatter["parent_id"]; got != "team-ghost" {
		t.Errorf("parent_id = %v, want team-ghost (the edge is known even when the team is not)", got)
	}
	if got := parentLinkTarget(orphan); got != "" {
		t.Errorf("parent link target = %q, want \"\" (Readdir must not list a dangling parent)", got)
	}

	// Every listed sub-team resolves through the same builder the entry name
	// comes from, so the frontmatter value IS the last component of the target.
	for _, c := range children {
		if got, want := subteamLinkTarget(c), "../../"+teamDirName(c); got != want {
			t.Errorf("subteam link target for %s = %q, want %q", c.Key, got, want)
		}
	}
}

// TestTeamHierarchyChildrenLoadFailure pins the difference between "no
// sub-teams" and "could not find out". subteams/ Readdir returns EIO when the
// children load fails; team.md must not answer the same question with a
// confident "none", or the two surfaces contradict each other.
func TestTeamHierarchyChildrenLoadFailure(t *testing.T) {
	t.Parallel()
	team := api.Team{ID: "team-1", Key: "ENG", Name: "Engineering"}

	content := teamMarkdown(team, nil, errors.New("database is locked"))
	doc, err := marshal.Parse(content)
	if err != nil {
		t.Fatalf("team.md render on a children load failure is not parseable: %v", err)
	}
	if v, ok := doc.Frontmatter["subteams"]; ok {
		t.Errorf("team.md carries subteams: %v after a load failure; absence must mean unknown, not empty", v)
	}
	if !strings.Contains(string(content), "error loading sub-teams") {
		t.Errorf("team.md is silent about the failed sub-team load:\n%s", content)
	}
}

// TestTeamDescriptionRender pins the team description on both surfaces it is
// disclosed through — frontmatter (machine-read) and the body prose under the
// H1 (where Linear's own UI puts it) — and the absent case, which must OMIT
// the key rather than emit an empty one: "" would read as "set to nothing",
// where Linear's Team.description is nullable precisely to say "never set".
//
// The description is remote free text, so it is also the frontmatter injection
// surface the catalogs already guard: a colon, a quote, or a leading `-` must
// survive as a value, not restructure the YAML.
func TestTeamDescriptionRender(t *testing.T) {
	t.Parallel()

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		team := api.Team{ID: "team-1", Key: "ENG", Name: "Engineering",
			Description: "Onboarding Pod: https://example.invalid/p?a=1&b=2"}

		content := teamMarkdown(team, nil, nil)
		doc, err := marshal.Parse(content)
		if err != nil {
			t.Fatalf("team.md render is not parseable YAML frontmatter: %v", err)
		}
		if got := doc.Frontmatter["description"]; got != team.Description {
			t.Errorf("description = %v, want %q", got, team.Description)
		}
		if !strings.Contains(doc.Body, team.Description) {
			t.Errorf("team.md body does not carry the description:\n%s", doc.Body)
		}
		// The H1 stays the team name — the description is prose beneath it,
		// not a replacement for the heading.
		if !strings.Contains(doc.Body, "# Engineering") {
			t.Errorf("team.md body lost its H1:\n%s", doc.Body)
		}
	})

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		team := api.Team{ID: "team-1", Key: "ENG", Name: "Engineering"}

		doc, err := marshal.Parse(teamMarkdown(team, nil, nil))
		if err != nil {
			t.Fatalf("team.md render is not parseable YAML frontmatter: %v", err)
		}
		if got, ok := doc.Frontmatter["description"]; ok {
			t.Errorf("description key present as %#v for a team that never set one; want omitted", got)
		}
	})

	t.Run("hostile", func(t *testing.T) {
		t.Parallel()
		hostile := "desc: \"quoted\"\nkey: injected"
		team := api.Team{ID: "team-1", Key: "ENG", Name: "Engineering", Description: hostile}

		doc, err := marshal.Parse(teamMarkdown(team, nil, nil))
		if err != nil {
			t.Fatalf("hostile description broke the frontmatter: %v", err)
		}
		if got := doc.Frontmatter["description"]; got != hostile {
			t.Errorf("description round-tripped to %#v, want %#v", got, hostile)
		}
		if got := doc.Frontmatter["key"]; got != "ENG" {
			t.Errorf("description injected a frontmatter key: key = %v, want ENG", got)
		}
	})
}

// TestTeamDefaultsRender pins the issue-creation defaults, and above all the
// difference between "this team does not estimate" and "nobody has asked yet".
// A team row written before the settings sync has zero-valued settings, and
// rendering those verbatim would tell a writer that triage is off and the
// estimate scale is "" — a confident answer to a question never asked.
func TestTeamDefaultsRender(t *testing.T) {
	t.Parallel()
	base := api.Team{ID: "team-1", Key: "ENG", Name: "Engineering"}

	t.Run("known", func(t *testing.T) {
		t.Parallel()
		team := base
		team.IssueEstimationType = "fibonacci"
		team.DefaultIssueEstimate = 2
		team.IssueEstimationAllowZero = true
		team.TriageEnabled = true
		team.TriageIssueState = &api.State{ID: "s-tri", Name: "Triage", Type: "triage"}
		team.RequirePriorityToLeaveTriage = true
		team.DefaultIssueState = &api.State{ID: "s-todo", Name: "Todo", Type: "unstarted"}

		doc, err := marshal.Parse(teamMarkdown(team, nil, nil))
		if err != nil {
			t.Fatalf("team.md render is not parseable YAML frontmatter: %v", err)
		}
		defaults, ok := doc.Frontmatter["defaults"].(map[string]any)
		if !ok {
			t.Fatalf("defaults = %#v, want a map", doc.Frontmatter["defaults"])
		}
		for key, want := range map[string]any{
			"issue_state":              "Todo",
			"triage":                   true,
			"triage_state":             "Triage",
			"triage_requires_priority": true,
			"estimation":               "fibonacci",
			"estimate_allow_zero":      true,
		} {
			if got := defaults[key]; got != want {
				t.Errorf("defaults[%q] = %#v, want %#v", key, got, want)
			}
		}
		// The estimate scale is the whole point: a writer that cannot read it
		// discovers the team's units by guessing and reading .error.
		if !strings.Contains(doc.Body, "fibonacci") {
			t.Errorf("body does not name the estimate scale:\n%s", doc.Body)
		}
		if !strings.Contains(doc.Body, "Triage") {
			t.Errorf("body does not name the triage state:\n%s", doc.Body)
		}
	})

	t.Run("unknown settings omit the block", func(t *testing.T) {
		t.Parallel()
		doc, err := marshal.Parse(teamMarkdown(base, nil, nil))
		if err != nil {
			t.Fatalf("team.md render is not parseable YAML frontmatter: %v", err)
		}
		if got, ok := doc.Frontmatter["defaults"]; ok {
			t.Errorf("defaults = %#v for an unsynced team; want the key omitted", got)
		}
		if strings.Contains(doc.Body, "Issue defaults") {
			t.Errorf("body claims defaults it does not know:\n%s", doc.Body)
		}
	})

	t.Run("estimates not used", func(t *testing.T) {
		t.Parallel()
		team := base
		team.IssueEstimationType = "notUsed"

		doc, err := marshal.Parse(teamMarkdown(team, nil, nil))
		if err != nil {
			t.Fatalf("team.md render is not parseable YAML frontmatter: %v", err)
		}
		// Known-and-off still renders: the block is present, and says so.
		defaults, ok := doc.Frontmatter["defaults"].(map[string]any)
		if !ok {
			t.Fatalf("defaults = %#v, want a map for a synced team", doc.Frontmatter["defaults"])
		}
		if got := defaults["estimation"]; got != "notUsed" {
			t.Errorf("estimation = %#v, want notUsed", got)
		}
		if !strings.Contains(doc.Body, "not used by this team") {
			t.Errorf("body does not say estimates are unused:\n%s", doc.Body)
		}
		if got := defaults["triage"]; got != false {
			t.Errorf("triage = %#v, want false (known-off, not omitted)", got)
		}
	})
}

// TestTeamTemplatesRender pins the default templates: name and id, no
// templateData, and the key omitted entirely for a team that sets none.
func TestTeamTemplatesRender(t *testing.T) {
	t.Parallel()
	team := api.Team{ID: "team-1", Key: "ENG", Name: "Engineering",
		IssueEstimationType:       "linear",
		DefaultTemplateForMembers: &api.Template{ID: "t1", Name: "Bug: report", Description: "For defects"},
		DefaultProjectTemplate:    &api.Template{ID: "t2", Name: "Standard project"},
	}

	doc, err := marshal.Parse(teamMarkdown(team, nil, nil))
	if err != nil {
		t.Fatalf("team.md render is not parseable YAML frontmatter: %v", err)
	}
	templates, ok := doc.Frontmatter["templates"].(map[string]any)
	if !ok {
		t.Fatalf("templates = %#v, want a map", doc.Frontmatter["templates"])
	}
	issue, _ := templates["issue"].(map[string]any)
	if got := issue["name"]; got != "Bug: report" {
		t.Errorf("issue template name = %#v, want %q (a colon must survive as a value)", got, "Bug: report")
	}
	if got := issue["id"]; got != "t1" {
		t.Errorf("issue template id = %#v, want t1", got)
	}
	if got := issue["description"]; got != "For defects" {
		t.Errorf("issue template description = %#v, want %q", got, "For defects")
	}
	// A template the team does not set is absent, not empty.
	if got, ok := templates["issue_non_member"]; ok {
		t.Errorf("issue_non_member = %#v for a team that sets none; want omitted", got)
	}

	bare := api.Team{ID: "team-2", Key: "OPS", Name: "Ops", IssueEstimationType: "linear"}
	doc, err = marshal.Parse(teamMarkdown(bare, nil, nil))
	if err != nil {
		t.Fatalf("team.md render for a team with no templates: %v", err)
	}
	if got, ok := doc.Frontmatter["templates"]; ok {
		t.Errorf("templates = %#v for a team that sets none; want the key omitted", got)
	}
}
