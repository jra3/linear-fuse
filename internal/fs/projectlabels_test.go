package fs

import (
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func testCatalog() []api.ProjectLabel {
	retired := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	group := api.ProjectLabel{ID: "id-area", Name: "Area", IsGroup: true,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	return []api.ProjectLabel{
		group,
		{ID: "id-backend", Name: "Backend", Color: "#5e6ad2",
			Parent:    &api.ProjectLabel{ID: "id-area", Name: "Area"},
			CreatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "id-frontend", Name: "Frontend",
			Parent:    &api.ProjectLabel{ID: "id-area", Name: "Area"},
			CreatedAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)},
		{ID: "id-bug", Name: "Bug", Description: "bug work",
			CreatedAt: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)},
		{ID: "id-legacy", Name: "Legacy", RetiredAt: &retired,
			CreatedAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
	}
}

func TestProjectLabelsMarkdown(t *testing.T) {
	t.Parallel()
	got := string(projectLabelsMarkdown(testCatalog()))

	for _, want := range []string{
		"group: true",                       // group flagged in frontmatter
		"parent: Area",                      // child carries parent name
		"retired: true",                     // retired flagged, not hidden
		"group (assign a child)",            // table flag spells out the rule
		"| Legacy | — | — | retired |",      // retired row flag
		"At most ONE child from each group", // rules prose lives in-file
		"a raw label ID is also accepted",   // ID-passthrough documented
		"description: \"bug work\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog render missing %q:\n%s", want, got)
		}
	}
	// Frontmatter top key matches the labels.md idiom.
	if !strings.HasPrefix(got, "---\nlabels:\n") {
		t.Errorf("catalog frontmatter key is off-idiom:\n%s", got[:40])
	}
}

func TestProjectLabelsMarkdownEmpty(t *testing.T) {
	t.Parallel()
	got := string(projectLabelsMarkdown(nil))
	// Stable, never-ENOENT surface: header + rules + explicit emptiness.
	for _, want := range []string{"# Project Labels", "Rules:", "No project labels defined."} {
		if !strings.Contains(got, want) {
			t.Errorf("empty catalog render missing %q:\n%s", want, got)
		}
	}
}

func TestResolveProjectLabels(t *testing.T) {
	t.Parallel()
	catalog := testCatalog()
	none := map[string]bool{}

	t.Run("case-insensitive name hit", func(t *testing.T) {
		t.Parallel()
		ids, selected, ferr := resolveProjectLabels(catalog, []string{"backend", "BUG"}, none)
		if ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		if len(ids) != 2 || ids[0] != "id-backend" || ids[1] != "id-bug" {
			t.Errorf("ids = %v", ids)
		}
		if len(selected) != 2 || selected[0].Name != "Backend" {
			t.Errorf("selected = %+v", selected)
		}
	})

	t.Run("unknown name errors with catalog pointer", func(t *testing.T) {
		t.Parallel()
		_, _, ferr := resolveProjectLabels(catalog, []string{"Nope"}, none)
		if ferr == nil || !strings.Contains(ferr.Detail(), "project-labels.md") {
			t.Errorf("want unknown-name error pointing at the catalog, got %v", ferr)
		}
	})

	t.Run("catalog-ID passthrough", func(t *testing.T) {
		t.Parallel()
		ids, _, ferr := resolveProjectLabels(catalog, []string{"id-bug"}, none)
		if ferr != nil || len(ids) != 1 || ids[0] != "id-bug" {
			t.Errorf("ids = %v, err = %v", ids, ferr)
		}
	})

	t.Run("current-member ID passthrough survives a cold catalog", func(t *testing.T) {
		t.Parallel()
		// The round-trip invariant: render emitted a verbatim ID the catalog
		// does not know; re-saving the untouched file must resolve, not EINVAL.
		current := map[string]bool{"id-ghost": true}
		ids, _, ferr := resolveProjectLabels(nil, []string{"id-ghost"}, current)
		if ferr != nil || len(ids) != 1 || ids[0] != "id-ghost" {
			t.Errorf("ids = %v, err = %v", ids, ferr)
		}
	})

	t.Run("duplicates dedup to a set", func(t *testing.T) {
		t.Parallel()
		ids, _, ferr := resolveProjectLabels(catalog, []string{"Bug", "bug", "id-bug"}, none)
		if ferr != nil || len(ids) != 1 {
			t.Errorf("ids = %v, err = %v", ids, ferr)
		}
	})

	dupCatalog := func(retiredSecond bool) []api.ProjectLabel {
		twin := api.ProjectLabel{ID: "id-twin-2", Name: "Platform"}
		if retiredSecond {
			retired := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
			twin.RetiredAt = &retired
		}
		return []api.ProjectLabel{{ID: "id-twin-1", Name: "Platform"}, twin}
	}

	t.Run("duplicate name prefers the label already applied", func(t *testing.T) {
		t.Parallel()
		current := map[string]bool{"id-twin-2": true}
		ids, _, ferr := resolveProjectLabels(dupCatalog(false), []string{"Platform"}, current)
		if ferr != nil || len(ids) != 1 || ids[0] != "id-twin-2" {
			t.Errorf("ids = %v, err = %v — want the current member to win", ids, ferr)
		}
	})

	t.Run("duplicate name prefers the single active candidate", func(t *testing.T) {
		t.Parallel()
		ids, _, ferr := resolveProjectLabels(dupCatalog(true), []string{"Platform"}, none)
		if ferr != nil || len(ids) != 1 || ids[0] != "id-twin-1" {
			t.Errorf("ids = %v, err = %v — want active over retired", ids, ferr)
		}
	})

	t.Run("unresolvable duplicate errors loudly listing IDs", func(t *testing.T) {
		t.Parallel()
		_, _, ferr := resolveProjectLabels(dupCatalog(false), []string{"Platform"}, none)
		if ferr == nil {
			t.Fatal("want ambiguity error, got silent pick")
		}
		if d := ferr.Detail(); !strings.Contains(d, "id-twin-1") || !strings.Contains(d, "id-twin-2") {
			t.Errorf("ambiguity error must list candidate IDs: %s", d)
		}
	})

	t.Run("empty catalog with a name errors", func(t *testing.T) {
		t.Parallel()
		_, _, ferr := resolveProjectLabels(nil, []string{"Platform"}, none)
		if ferr == nil {
			t.Fatal("want unknown-name error on empty catalog")
		}
	})
}

func TestValidateProjectLabelSelection(t *testing.T) {
	t.Parallel()
	catalog := testCatalog()
	byName := make(map[string]api.ProjectLabel)
	for _, l := range catalog {
		byName[l.Name] = l
	}
	none := map[string]bool{}

	t.Run("clean selection passes", func(t *testing.T) {
		t.Parallel()
		sel := []api.ProjectLabel{byName["Backend"], byName["Bug"]}
		if ferr := validateProjectLabelSelection(sel, none, catalog); ferr != nil {
			t.Errorf("unexpected error: %v", ferr.Detail())
		}
	})

	t.Run("group rejected, error names assignable children", func(t *testing.T) {
		t.Parallel()
		ferr := validateProjectLabelSelection([]api.ProjectLabel{byName["Area"]}, none, catalog)
		if ferr == nil {
			t.Fatal("want group rejection")
		}
		d := ferr.Detail()
		if !strings.Contains(d, "Backend") || !strings.Contains(d, "Frontend") {
			t.Errorf("group error must name the assignable children: %s", d)
		}
	})

	t.Run("retired newly applied rejected", func(t *testing.T) {
		t.Parallel()
		ferr := validateProjectLabelSelection([]api.ProjectLabel{byName["Legacy"]}, none, catalog)
		if ferr == nil || !strings.Contains(ferr.Detail(), "retired") {
			t.Errorf("want retired-new rejection, got %v", ferr)
		}
	})

	t.Run("retired carried through passes", func(t *testing.T) {
		t.Parallel()
		current := map[string]bool{"id-legacy": true}
		if ferr := validateProjectLabelSelection([]api.ProjectLabel{byName["Legacy"]}, current, catalog); ferr != nil {
			t.Errorf("carried-through retired label must pass: %v", ferr.Detail())
		}
	})

	t.Run("two children of one group rejected naming all parties", func(t *testing.T) {
		t.Parallel()
		sel := []api.ProjectLabel{byName["Backend"], byName["Frontend"]}
		ferr := validateProjectLabelSelection(sel, none, catalog)
		if ferr == nil {
			t.Fatal("want one-child-per-group rejection")
		}
		d := ferr.Detail()
		for _, want := range []string{"Backend", "Frontend", "Area"} {
			if !strings.Contains(d, want) {
				t.Errorf("group-exclusivity error missing %q: %s", want, d)
			}
		}
	})

	t.Run("children of different groups pass", func(t *testing.T) {
		t.Parallel()
		other := api.ProjectLabel{ID: "id-x", Name: "X", Parent: &api.ProjectLabel{ID: "id-other-group", Name: "Other"}}
		sel := []api.ProjectLabel{byName["Backend"], other}
		if ferr := validateProjectLabelSelection(sel, none, append(catalog, other)); ferr != nil {
			t.Errorf("children of distinct groups must pass: %v", ferr.Detail())
		}
	})
}

func TestSameIDSet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{[]string{"a"}, nil, false},
		{[]string{"a", "b"}, []string{"b", "a"}, true}, // order-insensitive
		{[]string{"a", "b"}, []string{"a", "c"}, false},
	}
	for _, c := range cases {
		if got := sameIDSet(c.a, c.b); got != c.want {
			t.Errorf("sameIDSet(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestProjectLabelCatalogTimes(t *testing.T) {
	t.Parallel()
	mtime, ctime := projectLabelCatalogTimes(testCatalog())
	if want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC); !mtime.Equal(want) {
		t.Errorf("mtime = %v, want newest UpdatedAt %v", mtime, want)
	}
	if want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); !ctime.Equal(want) {
		t.Errorf("ctime = %v, want oldest CreatedAt %v", ctime, want)
	}

	// Empty catalog: zero times (never fabricate now()).
	mtime, ctime = projectLabelCatalogTimes(nil)
	if !mtime.IsZero() || !ctime.IsZero() {
		t.Errorf("empty catalog times = %v/%v, want zero", mtime, ctime)
	}
}
