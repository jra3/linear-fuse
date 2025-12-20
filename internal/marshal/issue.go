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
	if issue.State.ID != "" {
		fm["status"] = issue.State.Name
	}
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

	if issue.ProjectMilestone != nil {
		fm["milestone"] = issue.ProjectMilestone.Name
	}

	if issue.Parent != nil {
		fm["parent"] = issue.Parent.Identifier
	}

	if issue.Cycle != nil {
		fm["cycle"] = issue.Cycle.Name
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

	if status, ok := doc.Frontmatter["status"].(string); ok {
		origStatus := ""
		if original.State.ID != "" {
			origStatus = original.State.Name
		}
		if status != origStatus {
			update["stateId"] = status // Will need to resolve to actual state ID
		}
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
	} else if _, exists := doc.Frontmatter["assignee"]; !exists && original.Assignee != nil {
		// Assignee was removed - set to null
		update["assigneeId"] = nil
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

	// Check labels
	if labelsRaw, ok := doc.Frontmatter["labels"]; ok {
		var newLabels []string
		switch v := labelsRaw.(type) {
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					newLabels = append(newLabels, s)
				}
			}
		case []string:
			newLabels = v
		}

		// Get original label names
		origLabels := make([]string, len(original.Labels.Nodes))
		for i, l := range original.Labels.Nodes {
			origLabels[i] = l.Name
		}

		// Check if labels changed (order-independent comparison)
		if !stringSlicesEqual(newLabels, origLabels) {
			update["labelIds"] = newLabels // Will need to resolve to actual label IDs
		}
	} else if _, exists := doc.Frontmatter["labels"]; !exists && len(original.Labels.Nodes) > 0 {
		// Labels were removed - set to empty
		update["labelIds"] = []string{}
	}

	// Check parent
	if parent, ok := doc.Frontmatter["parent"].(string); ok {
		origParent := ""
		if original.Parent != nil {
			origParent = original.Parent.Identifier
		}
		if parent != origParent {
			update["parentId"] = parent // Will need to resolve to actual issue ID
		}
	} else if _, exists := doc.Frontmatter["parent"]; !exists && original.Parent != nil {
		// Parent was removed - set to null
		update["parentId"] = nil
	}

	// Check project
	if project, ok := doc.Frontmatter["project"].(string); ok {
		origProject := ""
		if original.Project != nil {
			origProject = original.Project.Name
		}
		if project != origProject {
			update["projectId"] = project // Will need to resolve to actual project ID
		}
	} else if _, exists := doc.Frontmatter["project"]; !exists && original.Project != nil {
		// Project was removed - set to null
		update["projectId"] = nil
	}

	// Check milestone
	if milestone, ok := doc.Frontmatter["milestone"].(string); ok {
		origMilestone := ""
		if original.ProjectMilestone != nil {
			origMilestone = original.ProjectMilestone.Name
		}
		if milestone != origMilestone {
			update["projectMilestoneId"] = milestone // Will need to resolve to actual milestone ID
		}
	} else if _, exists := doc.Frontmatter["milestone"]; !exists && original.ProjectMilestone != nil {
		// Milestone was removed - set to null
		update["projectMilestoneId"] = nil
	}

	// Check cycle
	if cycle, ok := doc.Frontmatter["cycle"].(string); ok {
		origCycle := ""
		if original.Cycle != nil {
			origCycle = original.Cycle.Name
		}
		if cycle != origCycle {
			update["cycleId"] = cycle // Will need to resolve to actual cycle ID
		}
	} else if _, exists := doc.Frontmatter["cycle"]; !exists && original.Cycle != nil {
		// Cycle was removed - set to null
		update["cycleId"] = nil
	}

	// Check description (body)
	if doc.Body != original.Description {
		update["description"] = doc.Body
	}

	return update, nil
}

// stringSlicesEqual checks if two string slices contain the same elements (order-independent)
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aMap := make(map[string]int)
	for _, s := range a {
		aMap[s]++
	}
	for _, s := range b {
		aMap[s]--
		if aMap[s] < 0 {
			return false
		}
	}
	return true
}
