package marshal

import (
	"fmt"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// AttachmentLink represents an external link attachment for frontmatter
type AttachmentLink struct {
	Type  string `yaml:"type"`
	Title string `yaml:"title"`
	URL   string `yaml:"url"`
}

// IssueRelationLink represents an issue relation for frontmatter
type IssueRelationLink struct {
	Type  string `yaml:"type"`
	Issue string `yaml:"issue"`
}

// invertRelationType returns the inverse relation type
// blocks -> blocked-by, duplicate -> duplicate-of, etc.
func invertRelationType(relType string) string {
	switch relType {
	case "blocks":
		return "blocked-by"
	case "duplicate":
		return "duplicate-of"
	default:
		return relType // related, similar stay the same
	}
}

// IssueToMarkdown converts a Linear issue to the editable-only markdown surface
// (issue.md): the fields a writer may set, plus the description body. Server-
// managed and write-volatile fields (id, url, updated, …) live in the read-only
// issue.meta sibling produced by IssueMetaToMarkdown — keeping them out of this
// file means a successful write never rewrites the bytes the writer wrote (the
// "editable in, server-managed out" write contract, #150).
func IssueToMarkdown(issue *api.Issue) ([]byte, error) {
	fm := make(map[string]any)

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

// IssueMetaToMarkdown renders the read-only issue.meta sibling: the server-
// managed, write-volatile fields (identity, timestamps, branch, external links,
// and relations) as a YAML frontmatter block with no body. These are the fields
// deliberately excluded from IssueToMarkdown so that editing issue.md never
// races a server-written `updated:`.
func IssueMetaToMarkdown(issue *api.Issue, attachments ...api.Attachment) ([]byte, error) {
	fm := make(map[string]any)

	// Identity + timestamps (read-only)
	fm["id"] = issue.ID
	fm["identifier"] = issue.Identifier
	fm["url"] = issue.URL
	fm["created"] = issue.CreatedAt.Format(time.RFC3339)
	fm["updated"] = issue.UpdatedAt.Format(time.RFC3339)
	if issue.Creator != nil {
		fm["creator"] = issue.Creator.Email
	}
	if issue.BranchName != "" {
		fm["branch"] = issue.BranchName
	}

	// Workflow timestamps (read-only)
	if issue.StartedAt != nil {
		fm["started"] = issue.StartedAt.Format(time.RFC3339)
	}
	if issue.CompletedAt != nil {
		fm["completed"] = issue.CompletedAt.Format(time.RFC3339)
	}
	if issue.CanceledAt != nil {
		fm["canceled"] = issue.CanceledAt.Format(time.RFC3339)
	}
	if issue.ArchivedAt != nil {
		fm["archived"] = issue.ArchivedAt.Format(time.RFC3339)
	}

	// External link attachments (read-only)
	if len(attachments) > 0 {
		links := make([]AttachmentLink, 0, len(attachments))
		for _, a := range attachments {
			links = append(links, AttachmentLink{
				Type:  a.SourceType,
				Title: a.Title,
				URL:   a.URL,
			})
		}
		fm["links"] = links
	}

	// Issue relations (read-only)
	var relations []IssueRelationLink
	for _, rel := range issue.Relations.Nodes {
		if rel.RelatedIssue != nil {
			relations = append(relations, IssueRelationLink{
				Type:  rel.Type,
				Issue: rel.RelatedIssue.Identifier,
			})
		}
	}
	for _, rel := range issue.InverseRelations.Nodes {
		if rel.Issue != nil {
			relations = append(relations, IssueRelationLink{
				Type:  invertRelationType(rel.Type),
				Issue: rel.Issue.Identifier,
			})
		}
	}
	if len(relations) > 0 {
		fm["relations"] = relations
	}

	// Meta is a frontmatter-only document (no body).
	return Render(&Document{Frontmatter: fm})
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
		newPriority, err := api.ValidatePriority(priority)
		if err != nil {
			return nil, fmt.Errorf("priority: %w", err)
		}
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

// MarkdownToIssueCreate parses a full issue spec (frontmatter + body) into a
// create-input map for a brand-new issue. Unlike MarkdownToIssueUpdate it emits
// every present editable field (there is no "original" to diff against), with
// relational fields as human names for resolveIssueUpdate to turn into IDs. The
// body becomes the description. Unknown / read-only keys are ignored tolerantly.
// teamId is added by the caller. Returns an error only for an invalid priority.
func MarkdownToIssueCreate(content []byte) (map[string]any, error) {
	doc, err := Parse(content)
	if err != nil {
		return nil, err
	}
	fm := doc.Frontmatter
	create := make(map[string]any)

	// Coerce scalars rather than silently dropping wrong-typed values — a bare
	// `due: 2026-02-01` parses as time.Time, `priority: 2` as int, etc. Silent
	// field-drops are exactly the #148 failure mode this surface exists to kill.
	if v, ok := fm["title"]; ok {
		if s := scalarToString(v); s != "" {
			create["title"] = s
		}
	}
	if status, ok := fm["status"].(string); ok && status != "" {
		create["stateId"] = status // resolved to state ID
	}
	if v, ok := fm["priority"]; ok {
		switch p := v.(type) {
		case string:
			if p != "" {
				n, err := api.ValidatePriority(p)
				if err != nil {
					return nil, fmt.Errorf("priority: %w", err)
				}
				create["priority"] = n
			}
		case int:
			create["priority"] = p // numeric Linear priority (0-4)
		case float64:
			create["priority"] = int(p)
		default:
			return nil, fmt.Errorf("priority: must be a name (none|low|medium|high|urgent) or 0-4")
		}
	}
	if assignee, ok := fm["assignee"].(string); ok && assignee != "" {
		create["assigneeId"] = assignee // resolved to user ID
	}
	if labels := stringSliceFromYAML(fm["labels"]); len(labels) > 0 {
		create["labelIds"] = labels // resolved to label IDs
	}
	if v, ok := fm["due"]; ok {
		if s := dueToString(v); s != "" {
			create["dueDate"] = s
		}
	}
	if estimate, ok := fm["estimate"]; ok {
		switch v := estimate.(type) {
		case int:
			create["estimate"] = v
		case float64:
			create["estimate"] = int(v) // Linear estimate is an integer
		}
	}
	if project, ok := fm["project"].(string); ok && project != "" {
		create["projectId"] = project // resolved to project ID
	}
	if milestone, ok := fm["milestone"].(string); ok && milestone != "" {
		create["projectMilestoneId"] = milestone // resolved to milestone ID
	}
	if cycle, ok := fm["cycle"].(string); ok && cycle != "" {
		create["cycleId"] = cycle // resolved to cycle ID
	}
	if parent, ok := fm["parent"].(string); ok && parent != "" {
		create["parentId"] = parent // resolved to issue ID
	}
	if body := doc.Body; body != "" {
		create["description"] = body
	}

	return create, nil
}

// scalarToString coerces a YAML scalar (string, number, bool) to its string
// form so a wrong-typed-but-meaningful value isn't silently dropped.
func scalarToString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	case time.Time:
		return s.Format("2006-01-02")
	default:
		return fmt.Sprint(s)
	}
}

// dueToString coerces a due-date value to YYYY-MM-DD. Unquoted YAML dates parse
// as time.Time; quoted ones as string.
func dueToString(v any) string {
	if t, ok := v.(time.Time); ok {
		return t.Format("2006-01-02")
	}
	return scalarToString(v)
}

// stringSliceFromYAML coerces a YAML value (parsed as []any or []string) into a
// []string, dropping non-string elements.
func stringSliceFromYAML(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
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
