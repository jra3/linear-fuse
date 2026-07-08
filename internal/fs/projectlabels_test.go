package fs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
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
		"description: bug work",
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

// TestProjectLabelsMarkdownHostileNames pins the injection fix: hand-built
// YAML emitted `name: Q3: Bets` unquoted, which is INVALID YAML — in exactly
// the file agents machine-parse after a validation .error. The render must
// stay parseable and recover hostile names byte-exactly.
func TestProjectLabelsMarkdownHostileNames(t *testing.T) {
	t.Parallel()
	hostile := []string{`Q3: Bets`, `He said "no"`, `#urgent`, `[wip] thing`}
	catalog := make([]api.ProjectLabel, 0, len(hostile))
	for i, name := range hostile {
		catalog = append(catalog, api.ProjectLabel{
			ID:          fmt.Sprintf("id-%d", i),
			Name:        name,
			Description: `desc with: colon and "quotes"`,
		})
	}

	doc, err := marshal.Parse(projectLabelsMarkdown(catalog))
	if err != nil {
		t.Fatalf("catalog render is not parseable YAML frontmatter: %v", err)
	}
	entries, ok := doc.Frontmatter["labels"].([]any)
	if !ok || len(entries) != len(hostile) {
		t.Fatalf("labels frontmatter = %T with %v entries, want list of %d", doc.Frontmatter["labels"], entries, len(hostile))
	}
	for i, want := range hostile {
		entry, ok := entries[i].(map[string]any)
		if !ok {
			t.Fatalf("entry %d = %T, want map", i, entries[i])
		}
		if got := entry["name"]; got != want {
			t.Errorf("entry %d name round-tripped to %v, want %q", i, got, want)
		}
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

// labelsEditHarness wires newLabelsEdit with a literal catalog and recording
// closures — no mount, no API. refreshed is what the guard fetch returns;
// catalogErr forces the cold-catalog degradation.
type labelsEditHarness struct {
	catalog      []api.ProjectLabel
	catalogErr   error
	catalogCalls int
	refreshed    []string
	refreshCalls int
}

func (h *labelsEditHarness) eval(t *testing.T, raw any, present bool, current []string) (labelsEdit, *FieldError) {
	t.Helper()
	return newLabelsEdit(context.Background(), raw, present, current,
		func(context.Context) ([]api.ProjectLabel, error) {
			h.catalogCalls++
			return h.catalog, h.catalogErr
		},
		func(context.Context) []string {
			h.refreshCalls++
			return h.refreshed
		})
}

func TestLabelsEdit(t *testing.T) {
	t.Parallel()

	t.Run("key absent + current empty = untouched", func(t *testing.T) {
		t.Parallel()
		h := &labelsEditHarness{catalog: testCatalog()}
		e, ferr := h.eval(t, nil, false, nil)
		if ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		if e.changed() {
			t.Error("changed() = true for an untouched label set")
		}
		var input api.ProjectUpdateInput
		e.applyTo(&input)
		if input.LabelIds != nil {
			t.Errorf("applyTo set LabelIds = %v, want nil omit", *input.LabelIds)
		}
		if h.refreshCalls != 0 {
			t.Errorf("guard fired %d times on an untouched empty set, want 0", h.refreshCalls)
		}
		if h.catalogCalls != 0 {
			t.Errorf("catalog fetched %d times with nothing to resolve, want 0", h.catalogCalls)
		}
	})

	t.Run("key absent + current non-empty = clear", func(t *testing.T) {
		t.Parallel()
		h := &labelsEditHarness{catalog: testCatalog()}
		e, ferr := h.eval(t, nil, false, []string{"id-bug"})
		if ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		if !e.changed() {
			t.Fatal("changed() = false for a delete-the-line clear")
		}
		var input api.ProjectUpdateInput
		e.applyTo(&input)
		if input.LabelIds == nil || len(*input.LabelIds) != 0 {
			t.Errorf("applyTo LabelIds = %v, want &[]string{} clear", input.LabelIds)
		}
	})

	t.Run("explicit empty list = clear", func(t *testing.T) {
		t.Parallel()
		h := &labelsEditHarness{catalog: testCatalog()}
		e, ferr := h.eval(t, []any{}, true, []string{"id-bug"})
		if ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		if !e.changed() {
			t.Fatal("changed() = false for an explicit empty list")
		}
		var input api.ProjectUpdateInput
		e.applyTo(&input)
		if input.LabelIds == nil || len(*input.LabelIds) != 0 {
			t.Errorf("applyTo LabelIds = %v, want &[]string{} clear", input.LabelIds)
		}
	})

	t.Run("apply resolves names to IDs", func(t *testing.T) {
		t.Parallel()
		h := &labelsEditHarness{catalog: testCatalog()}
		e, ferr := h.eval(t, []any{"bug"}, true, []string{"id-backend"})
		if ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		if !e.changed() {
			t.Fatal("changed() = false for a real set change")
		}
		var input api.ProjectUpdateInput
		e.applyTo(&input)
		if input.LabelIds == nil || len(*input.LabelIds) != 1 || (*input.LabelIds)[0] != "id-bug" {
			t.Errorf("applyTo LabelIds = %v, want [id-bug]", input.LabelIds)
		}
		if h.refreshCalls != 0 {
			t.Errorf("guard fired %d times with a non-empty current set, want 0", h.refreshCalls)
		}
	})

	t.Run("reordered list is not a change", func(t *testing.T) {
		t.Parallel()
		h := &labelsEditHarness{catalog: testCatalog()}
		e, ferr := h.eval(t, []any{"Bug", "Backend"}, true, []string{"id-backend", "id-bug"})
		if ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		if e.changed() {
			t.Error("changed() = true for an order-only difference")
		}
	})

	t.Run("guard fires only when current empty and applying", func(t *testing.T) {
		t.Parallel()
		// The blob reads empty but the project really carries id-bug: the
		// refreshed set feeds both resolution and the change decision, so
		// re-saving the same label is NOT a change (no wipe-shaped write).
		h := &labelsEditHarness{catalog: testCatalog(), refreshed: []string{"id-bug"}}
		e, ferr := h.eval(t, []any{"Bug"}, true, nil)
		if ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		if h.refreshCalls != 1 {
			t.Fatalf("guard fired %d times, want exactly 1", h.refreshCalls)
		}
		if e.changed() {
			t.Error("changed() = true after refresh proved the set identical")
		}
	})

	t.Run("guard silent when clearing", func(t *testing.T) {
		t.Parallel()
		// Key absent, current empty: nothing to apply, guard must not fire.
		h := &labelsEditHarness{catalog: testCatalog(), refreshed: []string{"id-bug"}}
		if _, ferr := h.eval(t, nil, true, nil); ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		if h.refreshCalls != 0 {
			t.Errorf("guard fired %d times with no labels to apply, want 0", h.refreshCalls)
		}
	})

	t.Run("validation error passthrough", func(t *testing.T) {
		t.Parallel()
		h := &labelsEditHarness{catalog: testCatalog()}
		_, ferr := h.eval(t, []any{"Area"}, true, nil) // group label
		if ferr == nil {
			t.Fatal("expected a FieldError for a group-label assignment")
		}
		if ferr.Field != "labels" || !strings.Contains(ferr.Message, "label group") {
			t.Errorf("FieldError = %+v, want labels/group message", ferr)
		}
	})

	t.Run("ID passthrough on cold catalog", func(t *testing.T) {
		t.Parallel()
		h := &labelsEditHarness{catalogErr: errors.New("catalog unavailable")}
		e, ferr := h.eval(t, []any{"id-bug"}, true, []string{"id-bug"})
		if ferr != nil {
			t.Fatalf("cold catalog broke the round-trip invariant: %v", ferr.Detail())
		}
		if e.changed() {
			t.Error("changed() = true re-saving the current set via ID passthrough")
		}
	})

	t.Run("divergences nil when unchanged", func(t *testing.T) {
		t.Parallel()
		h := &labelsEditHarness{catalog: testCatalog()}
		e, ferr := h.eval(t, nil, false, nil)
		if ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		// A concurrent writer changed labels; an untouched edit must not EIO.
		if got := e.divergences([]string{"id-frontend"}); got != nil {
			t.Errorf("divergences = %v for an unchanged set, want nil", got)
		}
	})

	t.Run("divergence fatal on set mismatch", func(t *testing.T) {
		t.Parallel()
		h := &labelsEditHarness{catalog: testCatalog()}
		e, ferr := h.eval(t, []any{"Bug"}, true, nil)
		if ferr != nil {
			t.Fatalf("unexpected error: %v", ferr.Detail())
		}
		if got := e.divergences([]string{"id-bug"}); got != nil {
			t.Errorf("divergences = %v for a faithful persist, want nil", got)
		}
		got := e.divergences([]string{"id-bug", "id-frontend"})
		if len(got) != 1 || !got[0].fatal {
			t.Fatalf("divergences = %+v for a set mismatch, want one fatal result", got)
		}
		if !strings.Contains(got[0].message, "Field: labels") {
			t.Errorf("divergence message %q missing Field: labels", got[0].message)
		}
	})
}
