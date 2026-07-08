package marshal

import (
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// ProjectToMarkdown renders the editable-only project.md: name, initiatives,
// labels, and the description body. Server-managed fields live in project.meta
// (see ProjectMetaToMarkdown), so a successful write never rewrites the bytes
// the writer wrote. The parse side is scalarEdit (name/description) plus
// reconcileLinks (the initiatives list) plus resolveProjectLabels (the labels
// list) in internal/fs. labelNames is the project's labelIds mapped to catalog
// names by the caller — an unknown ID arrives verbatim (round-trip invariant);
// the key is omitted when empty (delete-the-line clears).
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

	return Render(&Document{Frontmatter: fm, Body: project.Description})
}

// ProjectMetaToMarkdown renders the read-only project.meta: server-managed
// identity, status, lead, dates, and timestamps as a frontmatter-only block.
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
