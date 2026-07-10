package marshal

import (
	"reflect"
	"sort"
	"strings"
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
// the initiatives list, the labels list, and the content body — and nothing
// server-managed (id/url/status live in project.meta), so a successful write
// never rewrites the bytes the writer wrote.
func TestProjectToMarkdown(t *testing.T) {
	t.Parallel()
	project := &api.Project{
		ID:          "proj-1",
		Name:        "API Gateway",
		Slug:        "api-gateway",
		URL:         "https://linear.app/projects/api-gateway",
		Description: "Short summary (read-only, in .meta).",
		Content:     "The gateway project.",
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
	// The body maps to the long content field, NOT the ≤255 description (#5).
	if doc.Body != project.Content {
		t.Errorf("body = %q, want the content", doc.Body)
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
		ID:          "proj-1",
		Name:        "API Gateway",
		Slug:        "api-gateway",
		URL:         "https://linear.app/projects/api-gateway",
		Description: "Short summary (read-only here, distinct from content).",
		Status:      &api.Status{Name: "In Progress"},
		Lead:        &api.User{ID: "u1", Name: "Ada", Email: "ada@example.com"},
		StartDate:   &start,
		TargetDate:  &target,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}

	content, err := ProjectMetaToMarkdown(project)
	if err != nil {
		t.Fatalf("ProjectMetaToMarkdown: %v", err)
	}
	keys, doc := frontmatterKeys(t, content)
	// The short description is read-only here (#5); content is the editable body.
	want := []string{"created", "description", "id", "lead", "slug", "startDate", "status", "targetDate", "updated", "url"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("project.meta frontmatter keys = %v, want %v", keys, want)
	}
	if doc.Frontmatter["description"] != project.Description {
		t.Errorf("description = %v, want %q", doc.Frontmatter["description"], project.Description)
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
// name, the linked project slugs, and the content body.
func TestInitiativeToMarkdown(t *testing.T) {
	t.Parallel()
	initiative := &api.Initiative{
		ID:          "init-1",
		Name:        "Platform Modernization",
		Slug:        "platform-modernization",
		Description: "Short summary (read-only, in .meta).",
		Content:     "Modernize all the things.",
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
	// The body maps to the long content field, NOT the ≤255 description (#5).
	if doc.Body != initiative.Content {
		t.Errorf("body = %q, want the content", doc.Body)
	}
}

// TestInitiativeMetaToMarkdown pins the server-managed half.
func TestInitiativeMetaToMarkdown(t *testing.T) {
	t.Parallel()
	target := "2026-12-31"
	initiative := &api.Initiative{
		ID:          "init-1",
		Name:        "Platform Modernization",
		Slug:        "platform-modernization",
		URL:         "https://linear.app/initiatives/platform-modernization",
		Description: "Short summary (read-only here, distinct from content).",
		Status:      "Active",
		Color:       "#00ff00",
		Icon:        "Rocket",
		Owner:       &api.User{ID: "u1", Name: "Ada", Email: "ada@example.com"},
		TargetDate:  &target,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}

	content, err := InitiativeMetaToMarkdown(initiative)
	if err != nil {
		t.Fatalf("InitiativeMetaToMarkdown: %v", err)
	}
	keys, doc := frontmatterKeys(t, content)
	// The short description is read-only here (#5); content is the editable body.
	want := []string{"color", "created", "description", "icon", "id", "owner", "slug", "status", "targetDate", "updated", "url"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("initiative.meta frontmatter keys = %v, want %v", keys, want)
	}
	if doc.Frontmatter["description"] != initiative.Description {
		t.Errorf("description = %v, want %q", doc.Frontmatter["description"], initiative.Description)
	}
	if doc.Body != "" {
		t.Errorf("meta must be frontmatter-only, got body %q", doc.Body)
	}
}

// TestMarkdownToProjectEditRoundTrip pins render → parse as the identity on
// the editable field set: name, body, labels presence + raw values, and the
// initiatives list. The parse is extraction-only — the diff owners downstream
// (scalarEdit/labelsEdit/reconcileLinks) must see exactly what the render said.
func TestMarkdownToProjectEditRoundTrip(t *testing.T) {
	t.Parallel()
	project := &api.Project{
		Name:        "API Gateway",
		Content:     "The gateway project.",
		Initiatives: &api.ProjectInitiatives{Nodes: []api.ProjectInitiative{{Name: "Platform"}, {Name: "Modernization"}}},
	}
	content, err := ProjectToMarkdown(project, []string{"Backend", "Q3-Bet"})
	if err != nil {
		t.Fatalf("ProjectToMarkdown: %v", err)
	}
	edit, err := MarkdownToProjectEdit(content)
	if err != nil {
		t.Fatalf("MarkdownToProjectEdit: %v", err)
	}
	if edit.Name != project.Name {
		t.Errorf("Name = %q, want %q", edit.Name, project.Name)
	}
	if edit.Body != project.Content {
		t.Errorf("Body = %q, want %q", edit.Body, project.Content)
	}
	if !edit.LabelsPresent {
		t.Error("LabelsPresent = false, want true (labels were rendered)")
	}
	if got := StringSliceFromYAML(edit.LabelsRaw); !reflect.DeepEqual(got, []string{"Backend", "Q3-Bet"}) {
		t.Errorf("LabelsRaw = %v, want the rendered names", got)
	}
	if !reflect.DeepEqual(edit.Initiatives, []string{"Platform", "Modernization"}) {
		t.Errorf("Initiatives = %v, want the rendered names", edit.Initiatives)
	}

	// Bare project: labels key absent ⇒ LabelsPresent false (delete-the-line
	// clears via labelsEdit); initiatives absent ⇒ empty (unlink-all).
	content, err = ProjectToMarkdown(&api.Project{Name: "Bare"}, nil)
	if err != nil {
		t.Fatalf("ProjectToMarkdown(bare): %v", err)
	}
	edit, err = MarkdownToProjectEdit(content)
	if err != nil {
		t.Fatalf("MarkdownToProjectEdit(bare): %v", err)
	}
	if edit.LabelsPresent || edit.LabelsRaw != nil {
		t.Errorf("bare project LabelsPresent = %v raw = %v, want absent", edit.LabelsPresent, edit.LabelsRaw)
	}
	if len(edit.Initiatives) != 0 {
		t.Errorf("bare project Initiatives = %v, want empty", edit.Initiatives)
	}
}

// TestProjectContentBodyIsNotLengthCapped pins KNOWN_ISSUES #5: the project
// body maps to Linear's uncapped `content`, so a multi-paragraph write-up far
// longer than the ≤255 `description` limit round-trips through render→parse
// intact (previously it was routed to `description` and rejected by the API).
// The same guarantee holds for initiatives (symmetric marshal code).
func TestProjectContentBodyIsNotLengthCapped(t *testing.T) {
	t.Parallel()
	longBody := strings.Repeat("A real project write-up paragraph. ", 40) // ~1400 chars, >> 255
	project := &api.Project{Name: "Big Writeup", Content: longBody}

	content, err := ProjectToMarkdown(project, nil)
	if err != nil {
		t.Fatalf("ProjectToMarkdown: %v", err)
	}
	edit, err := MarkdownToProjectEdit(content)
	if err != nil {
		t.Fatalf("MarkdownToProjectEdit: %v", err)
	}
	if len(edit.Body) <= 255 {
		t.Fatalf("body length = %d, want > 255 (the #5 regression)", len(edit.Body))
	}
	if strings.TrimSpace(edit.Body) != strings.TrimSpace(longBody) {
		t.Errorf("long body did not round-trip intact:\n got %d chars\nwant %d chars", len(edit.Body), len(longBody))
	}
}

// TestMarkdownToProjectEditCoercion pins the ScalarToString name coercion (a
// numeric name arrives as its string form, not a silent drop) and that an
// unclosed frontmatter surfaces as a parse error.
func TestMarkdownToProjectEditCoercion(t *testing.T) {
	t.Parallel()
	edit, err := MarkdownToProjectEdit([]byte("---\nname: 2026\n---\nbody"))
	if err != nil {
		t.Fatalf("MarkdownToProjectEdit: %v", err)
	}
	if edit.Name != "2026" {
		t.Errorf("numeric name = %q, want coerced \"2026\"", edit.Name)
	}

	if _, err := MarkdownToProjectEdit([]byte("---\nname: x\nno closing")); err == nil {
		t.Error("unclosed frontmatter should error, got nil")
	}
}

// TestMarkdownToInitiativeEditRoundTrip pins render → parse as the identity on
// the editable field set: name, body, and the project-slug list.
func TestMarkdownToInitiativeEditRoundTrip(t *testing.T) {
	t.Parallel()
	initiative := &api.Initiative{
		Name:    "Platform Modernization",
		Content: "Modernize all the things.",
	}
	initiative.Projects.Nodes = []api.InitiativeProject{{ID: "p1", Slug: "api-gateway"}, {ID: "p2", Slug: "auth-service"}}

	content, err := InitiativeToMarkdown(initiative)
	if err != nil {
		t.Fatalf("InitiativeToMarkdown: %v", err)
	}
	edit, err := MarkdownToInitiativeEdit(content)
	if err != nil {
		t.Fatalf("MarkdownToInitiativeEdit: %v", err)
	}
	if edit.Name != initiative.Name {
		t.Errorf("Name = %q, want %q", edit.Name, initiative.Name)
	}
	if edit.Body != initiative.Content {
		t.Errorf("Body = %q, want %q", edit.Body, initiative.Content)
	}
	if !reflect.DeepEqual(edit.Projects, []string{"api-gateway", "auth-service"}) {
		t.Errorf("Projects = %v, want the rendered slugs", edit.Projects)
	}

	// No linked projects: key absent ⇒ empty (unlink-all semantics downstream).
	content, err = InitiativeToMarkdown(&api.Initiative{Name: "Bare"})
	if err != nil {
		t.Fatalf("InitiativeToMarkdown(bare): %v", err)
	}
	edit, err = MarkdownToInitiativeEdit(content)
	if err != nil {
		t.Fatalf("MarkdownToInitiativeEdit(bare): %v", err)
	}
	if len(edit.Projects) != 0 {
		t.Errorf("bare initiative Projects = %v, want empty", edit.Projects)
	}
}
