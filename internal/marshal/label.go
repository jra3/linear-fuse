package marshal

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jra3/linear-fuse/internal/api"
)

// LabelToMarkdown renders the editable-only label .md: name, color, and
// description — every field is editable, so the frontmatter is the whole
// contract and the body is empty. The server-managed id (which the old render
// leaked into the frontmatter AND re-printed in a generated prose body) lives
// in the sibling .meta (see LabelMetaToMarkdown). The parse side
// (MarkdownToLabelUpdate below) accepts only those three frontmatter keys and
// rejects a body outright, so this render is a fixpoint of its own parser.
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

// labelEditableKeys is every frontmatter key a label .md accepts — exactly the
// set LabelToMarkdown renders, which is what makes render -> parse a fixpoint.
var labelEditableKeys = []string{"name", "color", "description"}

// labelEditableKeySet is labelEditableKeys as a lookup, built once: the guard
// runs on every label write, and the list is fixed at init.
var labelEditableKeySet = func() map[string]bool {
	set := make(map[string]bool, len(labelEditableKeys))
	for _, k := range labelEditableKeys {
		set[k] = true
	}
	return set
}()

// labelMetaKeys are the server-managed keys LabelMetaToMarkdown renders into
// the label .meta sidecar. They are not editable, but they are RECOGNIZED, so a
// writer who names one is told where the field actually lives (see
// checkLabelFrontmatter).
var labelMetaKeys = map[string]bool{"id": true, "team": true}

// checkLabelFrontmatter rejects frontmatter keys a label .md does not accept
// (#476), the guard issue.md got in #426. Without it an unrecognized key took a
// path the failure model does not have: the write was accepted, no mutation was
// sent, and the key vanished on the next fresh render — so `parent: Context`
// (label groups, which this surface cannot express at all), a typo
// (`descriptoin:`), or a .meta key pasted back into the .md all read as a
// successful edit. Every key must now be applied or named in .error.
//
// ignoreMeta splits the two callers exactly as it does for issues: on an update,
// naming a server-managed field is a mistake worth reporting, while on create a
// spec assembled from a rendered {name}.md plus its {name}.meta is meaningfully
// read by ignoring the fields the server assigns at birth.
func checkLabelFrontmatter(fm map[string]any, ignoreMeta bool) *FieldError {
	var unknown, readOnly []string
	for k := range fm {
		switch {
		case labelEditableKeySet[k]:
		case labelMetaKeys[k]:
			if !ignoreMeta {
				readOnly = append(readOnly, k)
			}
		default:
			unknown = append(unknown, k)
		}
	}
	// Sorted so a document with several bad keys reports the same field every
	// time — a .error that changes between identical writes is not a contract.
	sort.Strings(unknown)
	sort.Strings(readOnly)

	accepts := "labels/<name>.md accepts: " + strings.Join(labelEditableKeys, ", ") + "."
	switch {
	case len(unknown) > 0:
		return &FieldError{
			Field:   unknown[0],
			Message: "unknown field. " + accepts + alsoNamed("unrecognized", unknown[1:]) + alsoNamed("read-only", readOnly),
		}
	case len(readOnly) > 0:
		return &FieldError{
			Field:   readOnly[0],
			Message: "read-only field: it is server-managed and lives in the label .meta sidecar. " + accepts + alsoNamed("read-only", readOnly[1:]),
		}
	}
	return nil
}

// parseLabelFrontmatter is the shared front half of the two label parsers:
// frontmatter is required (the label .md contract is frontmatter-only, so a
// body-only write is a malformed edit, not a no-op), and an unquoted hex color
// is rejected loudly. In YAML, `color: #FF0000` parses the value as a comment —
// the key arrives present with a nil value — so silently proceeding would drop
// the writer's edit; the guard names the fix instead.
//
// Text below the closing delimiter is rejected too: a label has no content
// field, so a body is prose the mount would accept and never send anywhere. The
// guards run in this order because each is a strictly better explanation than
// the next — a body-only write is "no frontmatter", not "unexpected body".
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
	if strings.TrimSpace(doc.Body) != "" {
		return nil, &FieldError{Field: "body",
			Message: "labels have no body: <name>.md is frontmatter-only (" + strings.Join(labelEditableKeys, ", ") +
				"). Remove the text below the closing --- and write the document back."}
	}
	return doc.Frontmatter, nil
}

// MarkdownToLabelUpdate parses markdown and returns the fields that changed
// against the original label — name, color, description, each coerced via
// ScalarToString so a wrong-typed-but-meaningful value updates instead of
// being silently dropped. Any other key, and any body, rejects the document
// whole (checkLabelFrontmatter). An ABSENT key means "leave that field alone",
// not "clear it": the clearing idiom on this surface is `description: ""`.
func MarkdownToLabelUpdate(content []byte, original *api.Label) (map[string]any, error) {
	fm, err := parseLabelFrontmatter(content)
	if err != nil {
		return nil, err
	}
	// Whole-document rejection, before any field is diffed: a document carrying
	// one bad key and one good change applies neither.
	if ferr := checkLabelFrontmatter(fm, false); ferr != nil {
		return nil, ferr
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
// The caller enforces that name is non-empty. Unlike the update path it IGNORES
// the .meta keys, so a spec pasted from a rendered {name}.md + {name}.meta still
// creates; a misspelled key or a body is rejected here too.
func ParseNewLabel(content []byte) (name, color, description string, err error) {
	fm, err := parseLabelFrontmatter(content)
	if err != nil {
		return "", "", "", err
	}
	if ferr := checkLabelFrontmatter(fm, true); ferr != nil {
		return "", "", "", ferr
	}
	return ScalarToString(fm["name"]), ScalarToString(fm["color"]), ScalarToString(fm["description"]), nil
}
