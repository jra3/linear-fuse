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
