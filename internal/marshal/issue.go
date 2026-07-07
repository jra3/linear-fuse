package marshal

import (
	"fmt"
	"math"
	"strconv"
	"strings"
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

	// team is read-only (an issue's team is fixed) — it lives in issue.meta, not
	// here, so issue.md contains no editable-looking-but-ignored fields (#148).

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
		body = placeholderBody(issue.Title)
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
	if issue.Team != nil {
		fm["team"] = issue.Team.Key
	}
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
	fm := doc.Frontmatter

	// Every editable field is coerced to its scalar form (ScalarToString) before
	// comparison so a wrong-typed-but-meaningful value — an unquoted `due:` that
	// parsed as time.Time, a numeric `title:`/`cycle:`, a quoted `estimate: "3"` —
	// is applied rather than silently ignored (the #148 no-op-write failure mode,
	// which the create path already avoids). "Present but empty" is treated as
	// "not set"; explicit removal is keyed on the field being absent entirely.

	if v, present := fm["title"]; present {
		if title := ScalarToString(v); title != "" && title != original.Title {
			update["title"] = title
		}
	}

	if v, present := fm["status"]; present {
		if status := ScalarToString(v); status != "" {
			origStatus := ""
			if original.State.ID != "" {
				origStatus = original.State.Name
			}
			if status != origStatus {
				update["stateId"] = status // Will need to resolve to actual state ID
			}
		}
	}

	if v, present := fm["priority"]; present {
		newPriority, set, err := coercePriority(v)
		if err != nil {
			return nil, fmt.Errorf("priority: %w", err)
		}
		if set && newPriority != original.Priority {
			update["priority"] = newPriority
		}
	}

	// Assignee
	if v, present := fm["assignee"]; present {
		if assignee := ScalarToString(v); assignee != "" {
			origAssignee := ""
			if original.Assignee != nil {
				origAssignee = original.Assignee.Email
			}
			if assignee != origAssignee {
				update["assigneeId"] = assignee // Will need to resolve to actual user ID
			}
		}
	} else if original.Assignee != nil {
		update["assigneeId"] = nil // removed
	}

	// Due date
	if v, present := fm["due"]; present {
		if due := ScalarToString(v); due != "" {
			origDue := ""
			if original.DueDate != nil {
				origDue = *original.DueDate
			}
			if due != origDue {
				update["dueDate"] = due
			}
		}
	} else if original.DueDate != nil {
		update["dueDate"] = nil // removed
	}

	// Estimate — accepts int, float (truncated), or numeric string. An
	// unrecognized type leaves the field untouched (never coerces to 0).
	if v, present := fm["estimate"]; present {
		if newEstimate, valid := coerceEstimate(v); valid {
			origEstimate := 0
			if original.Estimate != nil {
				origEstimate = int(*original.Estimate)
			}
			if newEstimate != origEstimate {
				update["estimate"] = newEstimate
			}
		}
	} else if original.Estimate != nil {
		update["estimate"] = nil // removed
	}

	// Labels
	if labelsRaw, present := fm["labels"]; present {
		newLabels := StringSliceFromYAML(labelsRaw)

		origLabels := make([]string, len(original.Labels.Nodes))
		for i, l := range original.Labels.Nodes {
			origLabels[i] = l.Name
		}

		// Order-independent comparison
		if !stringSlicesEqual(newLabels, origLabels) {
			update["labelIds"] = newLabels // Will need to resolve to actual label IDs
		}
	} else if len(original.Labels.Nodes) > 0 {
		update["labelIds"] = []string{} // removed
	}

	// Parent
	if v, present := fm["parent"]; present {
		if parent := ScalarToString(v); parent != "" {
			origParent := ""
			if original.Parent != nil {
				origParent = original.Parent.Identifier
			}
			if parent != origParent {
				update["parentId"] = parent // Will need to resolve to actual issue ID
			}
		}
	} else if original.Parent != nil {
		update["parentId"] = nil // removed
	}

	// Project
	if v, present := fm["project"]; present {
		if project := ScalarToString(v); project != "" {
			origProject := ""
			if original.Project != nil {
				origProject = original.Project.Name
			}
			if project != origProject {
				update["projectId"] = project // Will need to resolve to actual project ID
			}
		}
	} else if original.Project != nil {
		update["projectId"] = nil // removed
	}

	// Milestone
	if v, present := fm["milestone"]; present {
		if milestone := ScalarToString(v); milestone != "" {
			origMilestone := ""
			if original.ProjectMilestone != nil {
				origMilestone = original.ProjectMilestone.Name
			}
			if milestone != origMilestone {
				update["projectMilestoneId"] = milestone // Will need to resolve to actual milestone ID
			}
		}
	} else if original.ProjectMilestone != nil {
		update["projectMilestoneId"] = nil // removed
	}

	// Cycle
	if v, present := fm["cycle"]; present {
		if cycle := ScalarToString(v); cycle != "" {
			origCycle := ""
			if original.Cycle != nil {
				origCycle = original.Cycle.Name
			}
			if cycle != origCycle {
				update["cycleId"] = cycle // Will need to resolve to actual cycle ID
			}
		}
	} else if original.Cycle != nil {
		update["cycleId"] = nil // removed
	}

	// Description (body). IssueToMarkdown renders a `# <Title>` placeholder for an
	// empty description; a no-op rewrite of such an issue must not push that
	// placeholder back as a real description (the byte-stable-write contract).
	if doc.Body != original.Description && !isPlaceholderNoop(doc.Body, original.Description, original.Title) {
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
		if s := ScalarToString(v); s != "" {
			create["title"] = s
		}
	}
	// Relational fields are coerced to their string name (via ScalarToString)
	// rather than bare `.(string)` — a numeric name (`cycle: 42`) or other
	// non-string scalar must not be silently dropped (#148).
	if s := ScalarToString(fm["status"]); s != "" {
		create["stateId"] = s // resolved to state ID
	}
	if v, ok := fm["priority"]; ok {
		n, set, err := coercePriority(v)
		if err != nil {
			return nil, fmt.Errorf("priority: %w", err)
		}
		if set {
			create["priority"] = n
		}
	}
	if s := ScalarToString(fm["assignee"]); s != "" {
		create["assigneeId"] = s // resolved to user ID
	}
	if labels := StringSliceFromYAML(fm["labels"]); len(labels) > 0 {
		create["labelIds"] = labels // resolved to label IDs
	}
	if v, ok := fm["due"]; ok {
		if s := dueToString(v); s != "" {
			create["dueDate"] = s
		}
	}
	if v, ok := fm["estimate"]; ok {
		if n, valid := coerceEstimate(v); valid {
			create["estimate"] = n // Linear estimate is an integer
		}
	}
	if s := ScalarToString(fm["project"]); s != "" {
		create["projectId"] = s // resolved to project ID
	}
	if s := ScalarToString(fm["milestone"]); s != "" {
		create["projectMilestoneId"] = s // resolved to milestone ID
	}
	if s := ScalarToString(fm["cycle"]); s != "" {
		create["cycleId"] = s // resolved to cycle ID
	}
	if s := ScalarToString(fm["parent"]); s != "" {
		create["parentId"] = s // resolved to issue ID
	}
	if body := doc.Body; body != "" {
		create["description"] = body
	}

	return create, nil
}

// ScalarToString coerces a YAML scalar (string, number, bool) to its string
// form so a wrong-typed-but-meaningful value isn't silently dropped.
func ScalarToString(v any) string {
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
	return ScalarToString(v)
}

// StringSliceFromYAML coerces a YAML value into a []string. It accepts a list
// (`labels: [Bug, Backend]`) or a bare scalar (`labels: Bug`), and coerces each
// element via ScalarToString so a numeric-looking name (`2026`) isn't silently
// dropped — silent element-drops are the #148 failure mode this surface kills.
func StringSliceFromYAML(v any) []string {
	switch s := v.(type) {
	case nil:
		return nil
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str := ScalarToString(item); str != "" {
				out = append(out, str)
			}
		}
		return out
	default:
		// A bare scalar (`labels: Bug`, or a number) — a single-element list.
		if str := ScalarToString(v); str != "" {
			return []string{str}
		}
		return nil
	}
}

// coercePriority normalizes a priority frontmatter value to Linear's 0-4 scale.
// It accepts a name (none|low|medium|high|urgent) or a number, range-checking
// numeric input so out-of-range values fail loudly (EINVAL via .error) instead
// of reaching the API. ok is false when there is nothing to set (empty string).
func coercePriority(v any) (n int, ok bool, err error) {
	switch p := v.(type) {
	case string:
		if p == "" {
			return 0, false, nil
		}
		n, err := api.ValidatePriority(p)
		if err != nil {
			return 0, false, err
		}
		return n, true, nil
	case int:
		if p < 0 || p > 4 {
			return 0, false, fmt.Errorf("invalid priority %d: must be 0-4 or a name (none|low|medium|high|urgent)", p)
		}
		return p, true, nil
	case float64:
		if p != math.Trunc(p) || p < 0 || p > 4 {
			return 0, false, fmt.Errorf("invalid priority %v: must be an integer 0-4 or a name (none|low|medium|high|urgent)", p)
		}
		return int(p), true, nil
	default:
		return 0, false, fmt.Errorf("must be a name (none|low|medium|high|urgent) or a number 0-4")
	}
}

// coerceEstimate normalizes an estimate frontmatter value to an int. It accepts
// int, float (truncated), or a numeric string (`estimate: "3"`). ok is false for
// an unrecognized type — callers must leave the field untouched rather than
// coercing to 0, which would zero the estimate on Linear.
func coerceEstimate(v any) (int, bool) {
	switch e := v.(type) {
	case int:
		return e, true
	case float64:
		return int(e), true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(e)); err == nil {
			return n, true
		}
	}
	return 0, false
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
