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
		content := teamMarkdown(team)
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
	})
}
