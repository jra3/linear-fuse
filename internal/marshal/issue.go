package marshal

import (
	"fmt"
	"math"
	"sort"
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
	// team moves the issue to another team (#429). Non-removable, like title and
	// status: an issue always belongs to exactly one team, so an absent key means
	// "unchanged", never "clear it". The rendered value is the team KEY — the same
	// string that names the directory the issue lives under — so what a reader
	// sees is what a writer may write back. Linear re-numbers a moved issue
	// (AGT-15 → SPY-20), which is why the write-back compare checks it and the
	// old path simply disappears.
	{"team", "teamId", func(i *api.Issue) (string, bool) {
		if i.Team != nil {
			return i.Team.Key, true /* safename:ok resolution key */
		}
		return "", false
	}, false},
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

// issueEditableKeys is every frontmatter key issue.md accepts, in the order the
// README documents them. It is derived from the one field table plus the three
// bespoke fields, so a field added to issueScalarFields is accepted here without
// a second edit — the key-set guard below must never be the reason a newly
// editable field is rejected.
var issueEditableKeys = func() []string {
	keys := make([]string, 0, len(issueScalarFields)+3)
	for _, f := range issueScalarFields {
		keys = append(keys, f.yamlKey)
	}
	return append(keys, "priority", "labels", "estimate")
}()

// issueEditableKeySet is issueEditableKeys as a lookup, built once: the guard
// runs on every issue write, and the list is fixed at init.
var issueEditableKeySet = func() map[string]bool {
	set := make(map[string]bool, len(issueEditableKeys))
	for _, k := range issueEditableKeys {
		set[k] = true
	}
	return set
}()

// issueMetaKeys are the server-managed keys IssueMetaToMarkdown renders into
// issue.meta. They are not editable anywhere, but they are RECOGNIZED: a writer
// who names one gets told where the field actually lives instead of "unknown
// field", and the create path ignores them outright (see checkIssueFrontmatter).
var issueMetaKeys = map[string]bool{
	"id": true, "identifier": true, "url": true, "created": true,
	"updated": true, "creator": true, "branch": true, "started": true,
	"completed": true, "canceled": true, "archived": true, "links": true,
	"relations": true,
}

// checkIssueFrontmatter rejects frontmatter keys issue.md does not accept (#426).
// Before this guard, an unrecognized key took a third path the failure model does
// not have: the write was accepted, no mutation was sent, and the key vanished on
// the next fresh render — so a typo (`assigne:`, `teem:`) read as a successful
// edit. Every key must now be applied or named in .error; nothing is dropped.
//
// ignoreMeta splits the two callers. On an update, naming a server-managed field
// is a mistake worth reporting — the writer thinks they are editing something
// they are not. On create, it is not: a spec assembled from a rendered issue.md
// plus its issue.meta carries fields the server assigns at birth, and ignoring
// them is the meaningful reading (pinned by TestMarkdownToIssueCreate). A key
// that is in neither set has no meaning on either path.
func checkIssueFrontmatter(fm map[string]any, ignoreMeta bool) *FieldError {
	var unknown, readOnly []string
	for k := range fm {
		switch {
		case issueEditableKeySet[k]:
		case issueMetaKeys[k]:
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

	accepts := "issue.md accepts: " + strings.Join(issueEditableKeys, ", ") + "."
	switch {
	case len(unknown) > 0:
		return &FieldError{
			Field:   unknown[0],
			Message: "unknown field. " + accepts + alsoNamed("unrecognized", unknown[1:]) + alsoNamed("read-only", readOnly),
		}
	case len(readOnly) > 0:
		return &FieldError{
			Field:   readOnly[0],
			Message: "read-only field: it is server-managed and lives in the issue.meta sidecar. " + accepts + alsoNamed("read-only", readOnly[1:]),
		}
	}
	return nil
}

// alsoNamed appends the remaining bad keys to a field error's message, so one
// rejected write names every key a writer has to fix rather than one per retry.
func alsoNamed(kind string, keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return ` (also ` + kind + `: "` + strings.Join(keys, `", "`) + `")`
}

// IssueToMarkdown converts a Linear issue to the editable-only markdown surface
// (issue.md): the fields a writer may set, plus the description body. Server-
// managed and write-volatile fields (id, url, updated, …) live in the read-only
// issue.meta sibling produced by IssueMetaToMarkdown — keeping them out of this
// file means a successful write never rewrites the bytes the writer wrote (the
// "editable in, server-managed out" write contract, #150).
func IssueToMarkdown(issue *api.Issue) ([]byte, error) {
	fm := make(map[string]any)

	// Editable scalar fields, table-driven (title, team, status, assignee, due,
	// parent, project, milestone, cycle). team used to be read-only in issue.meta
	// on the reasoning that an issue's team is fixed; it is not — moving an issue
	// between teams is a normal Linear operation, and #429 made it editable here.
	// It lives in exactly one file, this one, so issue.md still carries no
	// editable-looking-but-ignored fields (#148).
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

	if hasEstimate(issue.Estimate) {
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
	// team is NOT here: it became editable in issue.md (#429), and a field lives
	// in exactly one file. Duplicating it would give a writer two places to edit
	// one value, only one of which is read.
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

	// Key-set guard first: a document naming a field issue.md does not accept is
	// rejected whole, before any field is applied. Half-applying it would be the
	// worse failure — the writer's other edits would land while the one they
	// misspelled silently did not (#426).
	if ferr := checkIssueFrontmatter(fm, false); ferr != nil {
		return nil, ferr
	}

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
	} else if hasEstimate(original.Estimate) {
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
// body becomes the description. Read-only (issue.meta) keys are ignored
// tolerantly; an unrecognized key is a *FieldError (#426).
// teamId is added by the caller. Returns an error for an invalid priority or a
// key issue.md does not accept.
func MarkdownToIssueCreate(content []byte) (map[string]any, error) {
	doc, err := Parse(content)
	if err != nil {
		return nil, err
	}
	fm := doc.Frontmatter
	create := make(map[string]any)

	// Key-set guard: server-managed keys are ignored here (a spec pasted from a
	// rendered issue carries them), but a key in neither set is a typo and is
	// rejected — an issue created with `assigne:` would otherwise be born
	// unassigned with nothing said about it (#426).
	if ferr := checkIssueFrontmatter(fm, true); ferr != nil {
		return nil, ferr
	}

	// Scalar fields, table-driven. There is no original to diff against, so every
	// present, non-empty value is emitted under its apiKey (relational names
	// resolved to IDs downstream). Values are coerced via ScalarToString so a
	// wrong-typed-but-meaningful value — a bare `due: 2026-02-01` (time.Time), a
	// numeric name (`cycle: 42`) — is applied, not silently dropped (#148); a
	// missing key coerces to "" and is skipped, and the read-only keys the guard
	// above let through are not in the table, so they never reach the input.
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

// hasEstimate reports whether an issue carries an estimate at all. Zero is NOT
// an estimate: Linear stores a cleared estimate as 0 rather than null, so a
// pointer to 0 means "unestimated" exactly as nil does.
//
// One predicate for both directions is the point. When the render asked
// `!= nil` and the diff asked `!= nil` too, clearing an estimate never
// converged: the clear stored 0, and every later save of a document written
// before it — which the serve-your-own-writes pin makes an ordinary thing to
// hold — saw "no key here, a value there" and re-sent the same clear. A write
// that keeps re-issuing itself is a write that never settles, and offline it
// surfaced as an editor's no-op re-save emitting a mutation (#415).
func hasEstimate(e *float64) bool { return e != nil && *e != 0 }

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
