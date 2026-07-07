package fs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// fakeResolver resolves names via simple maps and errors on anything unknown. It
// satisfies issueResolver, so resolveIssueUpdate is tested with no repo or API.
type fakeResolver struct {
	states     map[string]string
	users      map[string]string
	labels     map[string]string // name -> id; absence means "not found"
	issues     map[string]string
	projects   map[string]string
	milestones map[string]string
	cycles     map[string]string
}

func (f fakeResolver) ResolveStateID(_ context.Context, _, name string) (string, error) {
	if id, ok := f.states[name]; ok {
		return id, nil
	}
	return "", errors.New("unknown state " + name)
}
func (f fakeResolver) ResolveUserID(_ context.Context, id string) (string, error) {
	if uid, ok := f.users[id]; ok {
		return uid, nil
	}
	return "", errors.New("unknown user " + id)
}
func (f fakeResolver) ResolveLabelIDs(_ context.Context, _ string, names []string) ([]string, []string, error) {
	var ids, notFound []string
	for _, n := range names {
		if id, ok := f.labels[n]; ok {
			ids = append(ids, id)
		} else {
			notFound = append(notFound, n)
		}
	}
	return ids, notFound, nil
}
func (f fakeResolver) ResolveIssueID(_ context.Context, id string) (string, error) {
	if x, ok := f.issues[id]; ok {
		return x, nil
	}
	return "", errors.New("unknown issue " + id)
}
func (f fakeResolver) ResolveProjectID(_ context.Context, _, name string) (string, error) {
	if id, ok := f.projects[name]; ok {
		return id, nil
	}
	return "", errors.New("unknown project " + name)
}
func (f fakeResolver) ResolveMilestoneID(_ context.Context, _, name string) (string, error) {
	if id, ok := f.milestones[name]; ok {
		return id, nil
	}
	return "", errors.New("unknown milestone " + name)
}
func (f fakeResolver) ResolveCycleID(_ context.Context, _, name string) (string, error) {
	if id, ok := f.cycles[name]; ok {
		return id, nil
	}
	return "", errors.New("unknown cycle " + name)
}

func teamedIssue() *api.Issue {
	return &api.Issue{Team: &api.Team{ID: "team-1"}}
}

func fullResolver() fakeResolver {
	return fakeResolver{
		states:     map[string]string{"In Progress": "state-1"},
		users:      map[string]string{"a@b.com": "user-1"},
		labels:     map[string]string{"Bug": "label-1", "Backend": "label-2"},
		issues:     map[string]string{"ENG-100": "issue-100"},
		projects:   map[string]string{"Apollo": "proj-1"},
		milestones: map[string]string{"Phase 1": "ms-1"},
		cycles:     map[string]string{"Sprint 42": "cycle-1"},
	}
}

func TestResolveIssueUpdate_ResolvesEveryField(t *testing.T) {
	updates := map[string]any{
		"stateId":            "In Progress",
		"assigneeId":         "a@b.com",
		"labelIds":           []string{"Bug", "Backend"},
		"parentId":           "ENG-100",
		"projectId":          "Apollo",
		"projectMilestoneId": "Phase 1",
		"cycleId":            "Sprint 42",
		"title":              "untouched", // non-relational fields pass through
	}
	if ferr := resolveIssueUpdate(context.Background(), fullResolver(), teamedIssue(), updates); ferr != nil {
		t.Fatalf("unexpected FieldError: %v", ferr)
	}
	want := map[string]any{
		"stateId":            "state-1",
		"assigneeId":         "user-1",
		"labelIds":           []string{"label-1", "label-2"},
		"parentId":           "issue-100",
		"projectId":          "proj-1",
		"projectMilestoneId": "ms-1",
		"cycleId":            "cycle-1",
		"title":              "untouched",
	}
	if !reflect.DeepEqual(updates, want) {
		t.Errorf("resolved = %#v\nwant %#v", updates, want)
	}
}

func TestResolveIssueUpdate_FieldErrors(t *testing.T) {
	cases := []struct {
		name      string
		issue     *api.Issue
		updates   map[string]any
		wantField string
		wantValue string
	}{
		{
			name:      "unknown state",
			issue:     teamedIssue(),
			updates:   map[string]any{"stateId": "Bogus"},
			wantField: "status", wantValue: "Bogus",
		},
		{
			name:      "state with no team",
			issue:     &api.Issue{}, // no team
			updates:   map[string]any{"stateId": "In Progress"},
			wantField: "status", wantValue: "In Progress",
		},
		{
			name:      "unknown label reported as not-found",
			issue:     teamedIssue(),
			updates:   map[string]any{"labelIds": []string{"Bug", "Nope"}},
			wantField: "labels", wantValue: "[Nope]",
		},
		{
			name:      "milestone without a project",
			issue:     teamedIssue(), // no Project, no projectId in updates
			updates:   map[string]any{"projectMilestoneId": "Phase 1"},
			wantField: "milestone", wantValue: "Phase 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ferr := resolveIssueUpdate(context.Background(), fullResolver(), tc.issue, tc.updates)
			if ferr == nil {
				t.Fatal("expected a FieldError, got nil")
			}
			if ferr.Field != tc.wantField || ferr.Value != tc.wantValue {
				t.Errorf("FieldError{Field:%q, Value:%q}, want Field:%q Value:%q", ferr.Field, ferr.Value, tc.wantField, tc.wantValue)
			}
		})
	}
}

// TestResolveIssueUpdate_ClearLabels confirms an empty labels list clears via
// removedLabelIds (Linear rejects an empty labelIds).
func TestResolveIssueUpdate_ClearLabels(t *testing.T) {
	issue := teamedIssue()
	issue.Labels.Nodes = []api.Label{{ID: "label-1"}, {ID: "label-2"}}
	updates := map[string]any{"labelIds": []string{}}

	if ferr := resolveIssueUpdate(context.Background(), fullResolver(), issue, updates); ferr != nil {
		t.Fatalf("unexpected FieldError: %v", ferr)
	}
	if _, present := updates["labelIds"]; present {
		t.Error("labelIds should be removed when clearing")
	}
	got, _ := updates["removedLabelIds"].([]string)
	if !reflect.DeepEqual(got, []string{"label-1", "label-2"}) {
		t.Errorf("removedLabelIds = %v, want [label-1 label-2]", got)
	}
}

// TestResolveIssueUpdate_MilestoneUsesNewProject confirms a milestone set in the
// same edit as a project resolves against the newly-resolved project.
func TestResolveIssueUpdate_MilestoneUsesNewProject(t *testing.T) {
	updates := map[string]any{"projectId": "Apollo", "projectMilestoneId": "Phase 1"}
	if ferr := resolveIssueUpdate(context.Background(), fullResolver(), teamedIssue(), updates); ferr != nil {
		t.Fatalf("unexpected FieldError: %v", ferr)
	}
	if updates["projectId"] != "proj-1" || updates["projectMilestoneId"] != "ms-1" {
		t.Errorf("got projectId=%v milestone=%v, want proj-1 / ms-1", updates["projectId"], updates["projectMilestoneId"])
	}
}

// TestResolveByName covers the shared fetch-then-match tail: exact match wins,
// case-insensitive is the fallback (and exact is preferred over a differing-case
// entry), and an unknown name errors with the label.
func TestResolveByName(t *testing.T) {
	t.Parallel()
	type ent struct{ name, id string }
	nameOf := func(e ent) string { return e.name }
	idOf := func(e ent) string { return e.id }
	items := []ent{{"Backlog", "s1"}, {"In Progress", "s2"}, {"done", "s3"}}

	cases := []struct {
		name, query, wantID string
		wantErr             bool
	}{
		{"exact", "In Progress", "s2", false},
		{"case-insensitive", "in progress", "s2", false},
		{"exact-preferred-over-ci", "done", "s3", false}, // exact "done" over any CI collision
		{"unknown", "Nope", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveByName(items, tc.query, "state", nameOf, idOf)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got id %q", tc.query, got)
				}
				if got := err.Error(); got != "unknown state: "+tc.query {
					t.Errorf("error = %q, want unknown state: %s", got, tc.query)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveByName(%q): %v", tc.query, err)
			}
			if got != tc.wantID {
				t.Errorf("id = %q, want %q", got, tc.wantID)
			}
		})
	}

	// Exact match must win even when an earlier entry differs only by case.
	shadow := []ent{{"Bug", "L1"}, {"bug", "L2"}}
	if got, _ := resolveByName(shadow, "bug", "label", nameOf, idOf); got != "L2" {
		t.Errorf("exact %q resolved to %q, want L2 (exact beats the earlier case-variant)", "bug", got)
	}
}
