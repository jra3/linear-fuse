package marshal

import (
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// InitiativeToMarkdown renders the editable-only initiative.md: name, linked
// project slugs, and the description body. Server-managed fields live in
// initiative.meta (see InitiativeMetaToMarkdown), so a successful write never
// rewrites the bytes the writer wrote. The parse side is
// MarkdownToInitiativeEdit below; the diffs stay with internal/fs's scalarEdit
// (name/description) and reconcileLinks (the projects list).
func InitiativeToMarkdown(initiative *api.Initiative) ([]byte, error) {
	fm := map[string]any{"name": initiative.Name}

	if len(initiative.Projects.Nodes) > 0 {
		slugs := make([]string, len(initiative.Projects.Nodes))
		for i, p := range initiative.Projects.Nodes {
			slugs[i] = p.Slug
		}
		fm["projects"] = slugs
	}

	return Render(&Document{Frontmatter: fm, Body: initiative.Description})
}

// InitiativeMetaToMarkdown renders the read-only initiative.meta:
// server-managed identity, status, owner, appearance, and timestamps as a
// frontmatter-only block.
func InitiativeMetaToMarkdown(initiative *api.Initiative) ([]byte, error) {
	fm := map[string]any{
		"id":      initiative.ID,
		"slug":    initiative.Slug,
		"url":     initiative.URL,
		"status":  initiative.Status,
		"created": initiative.CreatedAt.Format(time.RFC3339),
		"updated": initiative.UpdatedAt.Format(time.RFC3339),
	}
	if initiative.Color != "" {
		fm["color"] = initiative.Color
	}
	if initiative.Icon != "" {
		fm["icon"] = initiative.Icon
	}
	if initiative.Owner != nil {
		fm["owner"] = map[string]any{
			"id":    initiative.Owner.ID,
			"name":  initiative.Owner.Name,
			"email": initiative.Owner.Email,
		}
	}
	if initiative.TargetDate != nil {
		fm["targetDate"] = *initiative.TargetDate
	}
	return Render(&Document{Frontmatter: fm})
}

// InitiativeEdit is what an edited initiative.md says — extraction and
// coercion only, no diffing (scalarEdit owns the name/body diff,
// reconcileLinks the projects list). Projects is a plain slice where
// absent ⇒ empty, today's unlink-all semantics.
type InitiativeEdit struct {
	Name     string
	Body     string
	Projects []string
}

// MarkdownToInitiativeEdit parses an edited initiative.md into its editable
// field set. The name is coerced via ScalarToString (a numeric/bare-scalar
// name arrives as its string form, not a silent drop); the body passes through
// verbatim for scalarEdit's trim-aware diff.
func MarkdownToInitiativeEdit(content []byte) (*InitiativeEdit, error) {
	doc, err := Parse(content)
	if err != nil {
		return nil, err
	}
	return &InitiativeEdit{
		Name:     ScalarToString(doc.Frontmatter["name"]),
		Body:     doc.Body,
		Projects: StringSliceFromYAML(doc.Frontmatter["projects"]),
	}, nil
}
