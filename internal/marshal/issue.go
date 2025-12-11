package marshal

import (
	"fmt"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// IssueToMarkdown converts a Linear issue to markdown with YAML frontmatter
func IssueToMarkdown(issue *api.Issue) ([]byte, error) {
	fm := make(map[string]any)

	// Read-only fields
	fm["id"] = issue.ID
	fm["identifier"] = issue.Identifier
	fm["url"] = issue.URL
	fm["created"] = issue.CreatedAt.Format(time.RFC3339)
	fm["updated"] = issue.UpdatedAt.Format(time.RFC3339)

	// Editable fields
	fm["title"] = issue.Title
	fm["status"] = issue.State.Name
	fm["priority"] = api.PriorityName(issue.Priority)

	if issue.Assignee != nil {
		fm["assignee"] = issue.Assignee.Email
	}

	if len(issue.Labels.Nodes) > 0 {
		labels := make([]string, len(issue.Labels.Nodes))
		for i, l := range issue.Labels.Nodes {
			labels[i] = l.Name
		}
		fm["labels"] = labels
	}

	if issue.DueDate != nil {
		fm["due"] = *issue.DueDate
	}

	if issue.Estimate != nil {
		fm["estimate"] = *issue.Estimate
	}

	if issue.Team != nil {
		fm["team"] = issue.Team.Key
	}

	if issue.Project != nil {
		fm["project"] = issue.Project.Name
	}

	// Body is just the description
	body := issue.Description
	if body == "" {
		body = fmt.Sprintf("# %s\n", issue.Title)
	}

	doc := &Document{
		Frontmatter: fm,
		Body:        body,
	}

	return Render(doc)
}

// MarkdownToIssueUpdate parses markdown and returns fields that changed
func MarkdownToIssueUpdate(content []byte, original *api.Issue) (map[string]any, error) {
	doc, err := Parse(content)
	if err != nil {
		return nil, err
	}

	update := make(map[string]any)

	// Check each editable field for changes
	if title, ok := doc.Frontmatter["title"].(string); ok && title != original.Title {
		update["title"] = title
	}

	if status, ok := doc.Frontmatter["status"].(string); ok && status != original.State.Name {
		update["stateId"] = status // Will need to resolve to actual state ID
	}

	if priority, ok := doc.Frontmatter["priority"].(string); ok {
		newPriority := api.PriorityValue(priority)
		if newPriority != original.Priority {
			update["priority"] = newPriority
		}
	}

	// Check assignee
	if assignee, ok := doc.Frontmatter["assignee"].(string); ok {
		origAssignee := ""
		if original.Assignee != nil {
			origAssignee = original.Assignee.Email
		}
		if assignee != origAssignee {
			update["assigneeId"] = assignee // Will need to resolve to actual user ID
		}
	}

	// Check due date
	if due, ok := doc.Frontmatter["due"].(string); ok {
		origDue := ""
		if original.DueDate != nil {
			origDue = *original.DueDate
		}
		if due != origDue {
			update["dueDate"] = due
		}
	} else if _, exists := doc.Frontmatter["due"]; !exists && original.DueDate != nil {
		// Due date was removed - set to null
		update["dueDate"] = nil
	}

	// Check estimate - YAML may parse as int or float64
	if estimate, ok := doc.Frontmatter["estimate"]; ok {
		var newEstimate float64
		switch v := estimate.(type) {
		case int:
			newEstimate = float64(v)
		case float64:
			newEstimate = v
		}
		origEstimate := float64(0)
		if original.Estimate != nil {
			origEstimate = *original.Estimate
		}
		if newEstimate != origEstimate {
			update["estimate"] = int(newEstimate)
		}
	} else if _, exists := doc.Frontmatter["estimate"]; !exists && original.Estimate != nil {
		// Estimate was removed - set to null
		update["estimate"] = nil
	}

	// Check description (body)
	if doc.Body != original.Description {
		update["description"] = doc.Body
	}

	return update, nil
}
