package marshal

import (
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// ProjectToMarkdown renders the editable-only project.md: name, initiatives,
// labels, and the content body. The body maps to Linear's long `content`
// field (uncapped markdown), NOT the ≤255 short `description`, which is
// server-owned and rendered read-only in project.meta (see
// ProjectMetaToMarkdown), so a successful write never rewrites the bytes
// the writer wrote. The parse side is MarkdownToProjectEdit below; the diffs
// stay with internal/fs's scalarEdit (name/content), reconcileLinks (the
// initiatives list), and labelsEdit (the labels list). labelNames is the
// project's labelIds mapped to catalog names by the caller — an unknown ID
// arrives verbatim (round-trip invariant); the key is omitted when empty
// (delete-the-line clears).
func ProjectToMarkdown(project *api.Project, labelNames []string) ([]byte, error) {
	fm := map[string]any{"name": project.Name}

	if project.Initiatives != nil && len(project.Initiatives.Nodes) > 0 {
		names := make([]string, len(project.Initiatives.Nodes))
		for i, init := range project.Initiatives.Nodes {
			names[i] = init.Name
		}
		fm["initiatives"] = names
	}
	if len(labelNames) > 0 {
		fm["labels"] = labelNames
	}

	return Render(&Document{Frontmatter: fm, Body: project.Content})
}

// ProjectMetaToMarkdown renders the read-only project.meta: server-managed
// identity, the short description, status, lead, dates, and timestamps as a
// frontmatter-only block. (description is the ≤255 summary field, distinct
// from the editable content body in project.md.)
func ProjectMetaToMarkdown(project *api.Project) ([]byte, error) {
	status := "unknown"
	if project.Status != nil {
		status = project.Status.Name
	}
	fm := map[string]any{
		"id":      project.ID,
		"slug":    project.Slug,
		"url":     project.URL,
		"status":  status,
		"created": project.CreatedAt.Format(time.RFC3339),
		"updated": project.UpdatedAt.Format(time.RFC3339),
	}
	if project.Description != "" {
		fm["description"] = project.Description
	}
	if project.Lead != nil {
		fm["lead"] = map[string]any{
			"id":    project.Lead.ID,
			"name":  project.Lead.Name,
			"email": project.Lead.Email,
		}
	}
	if project.StartDate != nil {
		fm["startDate"] = *project.StartDate
	}
	if project.TargetDate != nil {
		fm["targetDate"] = *project.TargetDate
	}
	return Render(&Document{Frontmatter: fm})
}

// ProjectEdit is what an edited project.md says — extraction and coercion
// only, no diffing (the diff has owners: scalarEdit for name/body, labelsEdit
// for labels, reconcileLinks for initiatives). Labels keep their raw
// value + presence pair because labelsEdit downstream owns the label coercion
// (ID passthrough, ambiguity); initiatives collapse to a plain slice where
// absent ⇒ empty, today's unlink-all semantics.
type ProjectEdit struct {
	Name          string
	Body          string
	LabelsRaw     any
	LabelsPresent bool
	Initiatives   []string
}

// MarkdownToProjectEdit parses an edited project.md into its editable field
// set. The name is coerced via ScalarToString (a numeric/bare-scalar name
// arrives as its string form, not a silent drop); the body passes through
// verbatim for scalarEdit's trim-aware diff.
func MarkdownToProjectEdit(content []byte) (*ProjectEdit, error) {
	doc, err := Parse(content)
	if err != nil {
		return nil, err
	}
	rawLabels, labelsPresent := doc.Frontmatter["labels"]
	return &ProjectEdit{
		Name:          ScalarToString(doc.Frontmatter["name"]),
		Body:          doc.Body,
		LabelsRaw:     rawLabels,
		LabelsPresent: labelsPresent,
		Initiatives:   StringSliceFromYAML(doc.Frontmatter["initiatives"]),
	}, nil
}
