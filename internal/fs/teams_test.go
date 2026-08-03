package fs

import (
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
		content := teamMarkdown(team, nil)
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

	doc, err := marshal.Parse(teamMarkdown(team, children))
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
	doc, err = marshal.Parse(teamMarkdown(orphan, nil))
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
}
