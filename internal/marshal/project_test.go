package marshal

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func frontmatterKeys(t *testing.T, content []byte) ([]string, *Document) {
	t.Helper()
	doc, err := Parse(content)
	if err != nil {
		t.Fatalf("rendered content does not parse back: %v", err)
	}
	keys := make([]string, 0, len(doc.Frontmatter))
	for k := range doc.Frontmatter {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, doc
}

// TestProjectToMarkdown pins the editable-only contract for project.md: name,
// the initiatives list, the labels list, and the description body — and
// nothing server-managed (id/url/status live in project.meta), so a successful
// write never rewrites the bytes the writer wrote.
func TestProjectToMarkdown(t *testing.T) {
	t.Parallel()
	project := &api.Project{
		ID:          "proj-1",
		Name:        "API Gateway",
		Slug:        "api-gateway",
		URL:         "https://linear.app/projects/api-gateway",
		Description: "The gateway project.",
		Initiatives: &api.ProjectInitiatives{Nodes: []api.ProjectInitiative{{Name: "Platform"}, {Name: "Modernization"}}},
	}

	content, err := ProjectToMarkdown(project, []string{"Backend", "Q3-Bet"})
	if err != nil {
		t.Fatalf("ProjectToMarkdown: %v", err)
	}
	keys, doc := frontmatterKeys(t, content)
	if want := []string{"initiatives", "labels", "name"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("project.md frontmatter keys = %v, want %v (editable-only)", keys, want)
	}
	if doc.Body != project.Description {
		t.Errorf("body = %q, want the description", doc.Body)
	}
	if got := StringSliceFromYAML(doc.Frontmatter["labels"]); !reflect.DeepEqual(got, []string{"Backend", "Q3-Bet"}) {
		t.Errorf("labels = %v, want the caller-resolved names", got)
	}

	// Labels but no initiatives.
	content, err = ProjectToMarkdown(&api.Project{Name: "Labeled"}, []string{"Bug"})
	if err != nil {
		t.Fatalf("ProjectToMarkdown(labeled): %v", err)
	}
	if keys, _ := frontmatterKeys(t, content); !reflect.DeepEqual(keys, []string{"labels", "name"}) {
		t.Errorf("labeled project frontmatter keys = %v, want [labels name]", keys)
	}

	// No initiatives and no labels → neither key at all (deleting the line
	// clears; an empty list must not render).
	bare := &api.Project{Name: "Bare"}
	content, err = ProjectToMarkdown(bare, nil)
	if err != nil {
		t.Fatalf("ProjectToMarkdown(bare): %v", err)
	}
	if keys, _ := frontmatterKeys(t, content); !reflect.DeepEqual(keys, []string{"name"}) {
		t.Errorf("bare project frontmatter keys = %v, want [name]", keys)
	}
}

// TestProjectMetaToMarkdown pins the server-managed half.
func TestProjectMetaToMarkdown(t *testing.T) {
	t.Parallel()
	start, target := "2026-01-01", "2026-06-30"
	project := &api.Project{
		ID:         "proj-1",
		Name:       "API Gateway",
		Slug:       "api-gateway",
		URL:        "https://linear.app/projects/api-gateway",
		Status:     &api.Status{Name: "In Progress"},
		Lead:       &api.User{ID: "u1", Name: "Ada", Email: "ada@example.com"},
		StartDate:  &start,
		TargetDate: &target,
		CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}

	content, err := ProjectMetaToMarkdown(project)
	if err != nil {
		t.Fatalf("ProjectMetaToMarkdown: %v", err)
	}
	keys, doc := frontmatterKeys(t, content)
	want := []string{"created", "id", "lead", "slug", "startDate", "status", "targetDate", "updated", "url"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("project.meta frontmatter keys = %v, want %v", keys, want)
	}
	if doc.Frontmatter["status"] != "In Progress" {
		t.Errorf("status = %v, want In Progress", doc.Frontmatter["status"])
	}
	if doc.Body != "" {
		t.Errorf("meta must be frontmatter-only, got body %q", doc.Body)
	}

	// A nil status renders as the explicit "unknown", never a missing key.
	project.Status = nil
	content, err = ProjectMetaToMarkdown(project)
	if err != nil {
		t.Fatalf("ProjectMetaToMarkdown(nil status): %v", err)
	}
	if _, doc = frontmatterKeys(t, content); doc.Frontmatter["status"] != "unknown" {
		t.Errorf("nil status = %v, want unknown", doc.Frontmatter["status"])
	}
}

// TestInitiativeToMarkdown pins the editable-only contract for initiative.md:
// name, the linked project slugs, and the description body.
func TestInitiativeToMarkdown(t *testing.T) {
	t.Parallel()
	initiative := &api.Initiative{
		ID:          "init-1",
		Name:        "Platform Modernization",
		Slug:        "platform-modernization",
		Description: "Modernize all the things.",
	}
	initiative.Projects.Nodes = []api.InitiativeProject{{ID: "p1", Slug: "api-gateway"}, {ID: "p2", Slug: "auth-service"}}

	content, err := InitiativeToMarkdown(initiative)
	if err != nil {
		t.Fatalf("InitiativeToMarkdown: %v", err)
	}
	keys, doc := frontmatterKeys(t, content)
	if want := []string{"name", "projects"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("initiative.md frontmatter keys = %v, want %v (editable-only)", keys, want)
	}
	if doc.Body != initiative.Description {
		t.Errorf("body = %q, want the description", doc.Body)
	}
}

// TestInitiativeMetaToMarkdown pins the server-managed half.
func TestInitiativeMetaToMarkdown(t *testing.T) {
	t.Parallel()
	target := "2026-12-31"
	initiative := &api.Initiative{
		ID:         "init-1",
		Name:       "Platform Modernization",
		Slug:       "platform-modernization",
		URL:        "https://linear.app/initiatives/platform-modernization",
		Status:     "Active",
		Color:      "#00ff00",
		Icon:       "Rocket",
		Owner:      &api.User{ID: "u1", Name: "Ada", Email: "ada@example.com"},
		TargetDate: &target,
		CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}

	content, err := InitiativeMetaToMarkdown(initiative)
	if err != nil {
		t.Fatalf("InitiativeMetaToMarkdown: %v", err)
	}
	keys, doc := frontmatterKeys(t, content)
	want := []string{"color", "created", "icon", "id", "owner", "slug", "status", "targetDate", "updated", "url"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("initiative.meta frontmatter keys = %v, want %v", keys, want)
	}
	if doc.Body != "" {
		t.Errorf("meta must be frontmatter-only, got body %q", doc.Body)
	}
}
