package marshal

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// MilestoneToMarkdown renders the editable-only milestone .md: name,
// targetDate, sortOrder, and the description body. The server-managed id lives
// in the sibling .meta (see MilestoneMetaToMarkdown), so an edit to it is
// unrepresentable instead of a silent no-op.
func MilestoneToMarkdown(m *api.ProjectMilestone) ([]byte, error) {
	fm := make(map[string]any)

	// Editable fields
	fm["name"] = m.Name

	if m.TargetDate != nil && *m.TargetDate != "" {
		fm["targetDate"] = *m.TargetDate
	}

	if m.SortOrder != 0 {
		fm["sortOrder"] = m.SortOrder
	}

	// Description is the body
	body := m.Description

	mdDoc := &Document{
		Frontmatter: fm,
		Body:        body,
	}

	return Render(mdDoc)
}

// MilestoneMetaToMarkdown renders the read-only milestone .meta sidecar.
// api.ProjectMilestone carries no other server-managed fields (no timestamps,
// no url), so the sidecar is the identity alone.
func MilestoneMetaToMarkdown(m *api.ProjectMilestone) ([]byte, error) {
	return Render(&Document{Frontmatter: map[string]any{"id": m.ID}})
}

// MarkdownToMilestoneUpdate parses markdown and returns fields that changed
func MarkdownToMilestoneUpdate(content []byte, original *api.ProjectMilestone) (api.ProjectMilestoneUpdateInput, error) {
	doc, err := Parse(content)
	if err != nil {
		return api.ProjectMilestoneUpdateInput{}, err
	}

	input := api.ProjectMilestoneUpdateInput{}

	// Check name
	if name, ok := doc.Frontmatter["name"].(string); ok && name != original.Name {
		input.Name = &name
	}

	// Check targetDate - YAML may parse dates as time.Time or string
	if tdVal, ok := doc.Frontmatter["targetDate"]; ok {
		var td string
		switch v := tdVal.(type) {
		case string:
			td = v
		case time.Time:
			td = v.Format("2006-01-02")
		default:
			// Unknown type, skip
			td = ""
		}
		if td != "" {
			origDate := ""
			if original.TargetDate != nil {
				origDate = *original.TargetDate
			}
			if td != origDate {
				input.TargetDate = &td
			}
		}
	} else if original.TargetDate != nil {
		// targetDate was removed - set to empty string
		empty := ""
		input.TargetDate = &empty
	}

	// Check sortOrder - can be float or int in YAML
	if so, ok := parseSortOrder(doc.Frontmatter["sortOrder"]); ok && so != original.SortOrder {
		input.SortOrder = &so
	}

	// Check description (body)
	if doc.Body != original.Description {
		input.Description = &doc.Body
	}

	return input, nil
}

// parseSortOrder parses sortOrder from YAML which may be int or float
func parseSortOrder(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case string:
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// ParseNewMilestone parses content for creating a new milestone
// Format: "name\ndescription" or just "name"
func ParseNewMilestone(content []byte) (name string, description string) {
	text := strings.TrimSpace(string(content))
	lines := strings.SplitN(text, "\n", 2)

	name = strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		description = strings.TrimSpace(lines[1])
	}

	return
}

// ValidateMilestoneUpdate validates milestone update fields
func ValidateMilestoneUpdate(input api.ProjectMilestoneUpdateInput) error {
	if input.Name != nil && *input.Name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	return nil
}
