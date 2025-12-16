package marshal

import (
	"strings"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// DocumentToMarkdown converts a Linear document to markdown with YAML frontmatter
func DocumentToMarkdown(doc *api.Document) ([]byte, error) {
	fm := make(map[string]any)

	// Read-only fields
	fm["id"] = doc.ID
	fm["title"] = doc.Title
	fm["url"] = doc.URL
	fm["created"] = doc.CreatedAt.Format(time.RFC3339)
	fm["updated"] = doc.UpdatedAt.Format(time.RFC3339)

	if doc.Creator != nil {
		fm["creator"] = doc.Creator.Email
	}

	if doc.SlugID != "" {
		fm["slug"] = doc.SlugID
	}

	if doc.Icon != "" {
		fm["icon"] = doc.Icon
	}

	if doc.Color != "" {
		fm["color"] = doc.Color
	}

	// Body is the document content
	body := doc.Content
	if body == "" {
		body = "# " + doc.Title + "\n"
	}

	mdDoc := &Document{
		Frontmatter: fm,
		Body:        body,
	}

	return Render(mdDoc)
}

// MarkdownToDocumentUpdate parses markdown and returns fields that changed
func MarkdownToDocumentUpdate(content []byte, original *api.Document) (map[string]any, error) {
	doc, err := Parse(content)
	if err != nil {
		return nil, err
	}

	update := make(map[string]any)

	// Check title
	if title, ok := doc.Frontmatter["title"].(string); ok && title != original.Title {
		update["title"] = title
	}

	// Check content (body)
	if doc.Body != original.Content {
		update["content"] = doc.Body
	}

	// Icon and color are editable
	if icon, ok := doc.Frontmatter["icon"].(string); ok && icon != original.Icon {
		update["icon"] = icon
	}

	if color, ok := doc.Frontmatter["color"].(string); ok && color != original.Color {
		update["color"] = color
	}

	return update, nil
}

// ParseNewDocument parses markdown for creating a new document
// Returns title and body. Title is extracted from frontmatter or first heading.
func ParseNewDocument(content []byte) (title string, body string, err error) {
	doc, err := Parse(content)
	if err != nil {
		return "", "", err
	}

	// Try to get title from frontmatter
	if t, ok := doc.Frontmatter["title"].(string); ok && t != "" {
		title = t
		body = doc.Body
		return
	}

	// Try to extract title from first markdown heading
	lines := strings.Split(doc.Body, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimPrefix(line, "# ")
			// Body is everything after the heading
			if i+1 < len(lines) {
				body = strings.TrimLeft(strings.Join(lines[i+1:], "\n"), "\n")
			}
			return
		}
	}

	// No title found, use first line or "Untitled"
	body = doc.Body
	if len(lines) > 0 && lines[0] != "" {
		title = lines[0]
		if len(title) > 50 {
			title = title[:50] + "..."
		}
	} else {
		title = "Untitled"
	}

	return
}
