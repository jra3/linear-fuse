package marshal

import (
	"fmt"
	"strings"

	"github.com/jra3/linear-fuse/internal/api"
)

// LabelToMarkdown renders the editable-only label .md: name, color, and
// description — every field is editable, so the frontmatter is the whole
// contract and the body is empty. The server-managed id (which the old render
// leaked into the frontmatter AND re-printed in a generated prose body) lives
// in the sibling .meta (see LabelMetaToMarkdown). The parse side
// (MarkdownToLabelUpdate below) reads only the three frontmatter keys and
// ignores the body, so an empty body preserves the parse contract.
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

// parseLabelFrontmatter is the shared front half of the two label parsers:
// frontmatter is required (the label .md contract is frontmatter-only, so a
// body-only write is a malformed edit, not a no-op), and an unquoted hex color
// is rejected loudly. In YAML, `color: #FF0000` parses the value as a comment —
// the key arrives present with a nil value — so silently proceeding would drop
// the writer's edit; the guard names the fix instead.
func parseLabelFrontmatter(content []byte) (map[string]any, error) {
	if !strings.HasPrefix(string(content), frontmatterDelimiter) {
		return nil, fmt.Errorf("no YAML frontmatter found")
	}
	doc, err := Parse(content)
	if err != nil {
		return nil, err
	}
	if raw, ok := doc.Frontmatter["color"]; ok && raw == nil {
		return nil, &FieldError{Field: "color",
			Message: `value parsed as a YAML comment — quote hex colors: color: '#FF0000'`}
	}
	return doc.Frontmatter, nil
}

// MarkdownToLabelUpdate parses markdown and returns the fields that changed
// against the original label — name, color, description, each coerced via
// ScalarToString so a wrong-typed-but-meaningful value updates instead of
// being silently dropped. The body is ignored (see LabelToMarkdown).
func MarkdownToLabelUpdate(content []byte, original *api.Label) (map[string]any, error) {
	fm, err := parseLabelFrontmatter(content)
	if err != nil {
		return nil, err
	}

	update := make(map[string]any)

	if v, ok := fm["name"]; ok {
		if name := ScalarToString(v); name != original.Name {
			update["name"] = name
		}
	}
	if v, ok := fm["color"]; ok {
		if color := ScalarToString(v); color != original.Color {
			update["color"] = color
		}
	}
	if v, ok := fm["description"]; ok {
		if desc := ScalarToString(v); desc != original.Description {
			update["description"] = desc
		}
	}

	return update, nil
}

// ParseNewLabel parses markdown for creating a new label: the same three
// frontmatter keys as MarkdownToLabelUpdate, with no original to diff against.
// The caller enforces that name is non-empty.
func ParseNewLabel(content []byte) (name, color, description string, err error) {
	fm, err := parseLabelFrontmatter(content)
	if err != nil {
		return "", "", "", err
	}
	return ScalarToString(fm["name"]), ScalarToString(fm["color"]), ScalarToString(fm["description"]), nil
}
