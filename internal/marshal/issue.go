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

// issueScalarField declares one editable scalar issue frontmatter field once, so
// the three parallel field walks — render (IssueToMarkdown), diff-update
// (MarkdownToIssueUpdate), and create (MarkdownToIssueCreate) — share one row per
// field instead of three hand-maintained blocks that drift (#227). Only the
// homogeneous scalar fields live here; priority, estimate, and labels keep their
// bespoke coercion/compare below.
//
// current returns the field's present value on an issue and whether it is set —
// the SAME nil/ID check backs both the render source and the update diff's
// "original", so each field states that condition exactly once.
type issueScalarField struct {
	yamlKey string // frontmatter key, e.g. "status"
	apiKey  string // update/create map key, e.g. "stateId"
	current func(*api.Issue) (value string, present bool)
	// removable: an absent key on a field that was set clears it (apiKey: nil).
	// Off for title/status, which have no removal semantics.
	removable bool
}

var issueScalarFields = []issueScalarField{
	{"title", "title", func(i *api.Issue) (string, bool) { return i.Title, true }, false},
	{"status", "stateId", func(i *api.Issue) (string, bool) { return i.State.Name, i.State.ID != "" }, false},
	{"assignee", "assigneeId", func(i *api.Issue) (string, bool) {
		if i.Assignee != nil {
			return i.Assignee.Email, true
		}
		return "", false
	}, true},
	{"due", "dueDate", func(i *api.Issue) (string, bool) {
		if i.DueDate != nil {
			return *i.DueDate, true
		}
		return "", false
	}, true},
	{"parent", "parentId", func(i *api.Issue) (string, bool) {
		if i.Parent != nil {
			return i.Parent.Identifier, true
		}
		return "", false
	}, true},
	{"project", "projectId", func(i *api.Issue) (string, bool) {
		if i.Project != nil {
			return i.Project.Name, true
		}
		return "", false
	}, true},
	{"milestone", "projectMilestoneId", func(i *api.Issue) (string, bool) {
		if i.ProjectMilestone != nil {
			return i.ProjectMilestone.Name, true
		}
		return "", false
	}, true},
	{"cycle", "cycleId", func(i *api.Issue) (string, bool) {
		if i.Cycle != nil {
			return i.Cycle.Name, true
		}
		return "", false
	}, true},
}

// IssueToMarkdown converts a Linear issue to the editable-only markdown surface
// (issue.md): the fields a writer may set, plus the description body. Server-
// managed and write-volatile fields (id, url, updated, …) live in the read-only
// issue.meta sibling produced by IssueMetaToMarkdown — keeping them out of this
// file means a successful write never rewrites the bytes the writer wrote (the
// "editable in, server-managed out" write contract, #150).
func IssueToMarkdown(issue *api.Issue) ([]byte, error) {
	fm := make(map[string]any)

	// Editable scalar fields, table-driven (title, status, assignee, due, parent,
	// project, milestone, cycle). team is read-only (an issue's team is fixed) — it
	// lives in issue.meta, not here, so issue.md carries no editable-looking-but-
	// ignored fields (#148).
	for _, f := range issueScalarFields {
		if v, present := f.current(issue); present {
			fm[f.yamlKey] = v
		}
	}

	// Priority always renders (it has no unset state — 0 is "none").
	fm["priority"] = api.PriorityName(issue.Priority)

	if len(issue.Labels.Nodes) > 0 {
		labels := make([]string, len(issue.Labels.Nodes))
		for i, l := range issue.Labels.Nodes {
			labels[i] = l.Name
		}
		fm["labels"] = labels
	}

	if issue.Estimate != nil {
		fm["estimate"] = *issue.Estimate
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

	// Scalar fields (title, status, assignee, due, parent, project, milestone,
	// cycle), table-driven: a present, non-empty value that differs from the
	// current one is applied under the field's apiKey; a removable field absent
	// from the frontmatter clears a value that was set. The apiKey names carry
	// human values here — resolveIssueUpdate turns the relational ones into IDs.
	for _, f := range issueScalarFields {
		origVal, origPresent := f.current(original)
		if v, present := fm[f.yamlKey]; present {
			if s := ScalarToString(v); s != "" && s != origVal {
				update[f.apiKey] = s
			}
		} else if f.removable && origPresent {
			update[f.apiKey] = nil // removed
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

	// Scalar fields, table-driven. There is no original to diff against, so every
	// present, non-empty value is emitted under its apiKey (relational names
	// resolved to IDs downstream). Values are coerced via ScalarToString so a
	// wrong-typed-but-meaningful value — a bare `due: 2026-02-01` (time.Time), a
	// numeric name (`cycle: 42`) — is applied, not silently dropped (#148); a
	// missing key coerces to "" and is skipped, and unknown keys are ignored.
	for _, f := range issueScalarFields {
		if s := ScalarToString(fm[f.yamlKey]); s != "" {
			create[f.apiKey] = s
		}
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
	if labels := StringSliceFromYAML(fm["labels"]); len(labels) > 0 {
		create["labelIds"] = labels // resolved to label IDs
	}
	if v, ok := fm["estimate"]; ok {
		if n, valid := coerceEstimate(v); valid {
			create["estimate"] = n // Linear estimate is an integer
		}
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
