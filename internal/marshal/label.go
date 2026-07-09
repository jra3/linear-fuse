package marshal

import (
	"github.com/jra3/linear-fuse/internal/api"
)

// LabelToMarkdown renders the editable-only label .md: name, color, and
// description — every field is editable, so the frontmatter is the whole
// contract and the body is empty. The server-managed id (which the old render
// leaked into the frontmatter AND re-printed in a generated prose body) lives
// in the sibling .meta (see LabelMetaToMarkdown). The parse side
// (fs's parseLabelMarkdown) reads only the three frontmatter keys and ignores
// the body, so an empty body preserves the parse contract.
func LabelToMarkdown(label *api.Label) ([]byte, error) {
	fm := map[string]any{
		"name":        label.Name,
		"color":       label.Color,
		"description": label.Description,
	}
	return Render(&Document{Frontmatter: fm})
}

// LabelMetaToMarkdown renders the read-only label .meta sidecar: the identity,
// plus the owning team's id for a team-scoped label (omitted for a
// workspace-level label — api.Label carries no other server fields, and no
// timestamps).
func LabelMetaToMarkdown(label *api.Label) ([]byte, error) {
	fm := map[string]any{"id": label.ID}
	if label.Team != nil {
		fm["team"] = label.Team.ID
	}
	return Render(&Document{Frontmatter: fm})
}
