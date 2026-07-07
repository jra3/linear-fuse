package fs

import (
	"context"
	"fmt"
	"strings"

	"github.com/jra3/linear-fuse/internal/api"
)

// resolveByName finds the id of the item whose name matches — exact first, then
// case-insensitive — or errors "unknown <label>: <name>". It is the one copy of
// the fetch-then-match tail the five single-name resolvers (state, project,
// milestone, cycle, initiative) each hand-rolled identically; the caller fetches
// the list and passes the two field accessors. Pure of the repo, so it is
// unit-tested on literal slices.
func resolveByName[T any](items []T, name, label string, nameOf, idOf func(T) string) (string, error) {
	for _, it := range items {
		if nameOf(it) == name {
			return idOf(it), nil
		}
	}
	lower := strings.ToLower(name)
	for _, it := range items {
		if strings.ToLower(nameOf(it)) == lower {
			return idOf(it), nil
		}
	}
	return "", fmt.Errorf("unknown %s: %s", label, name)
}

// Name→ID resolution for issue edits.
//
// marshal turns edited frontmatter into an update map whose relational fields
// hold human-friendly *names*: a state name, an assignee email, label names, a
// parent identifier, project / milestone / cycle names. Linear's API needs IDs.
//
// resolveIssueUpdate turns each name into its ID in place. It owns the field
// ordering (a milestone resolves against the project, which may itself be
// changing in the same edit, so project must resolve first), the label-clearing
// special case (Linear rejects an empty labelIds, so clearing must use
// removedLabelIds), and the per-field error messages. A bad value yields a
// *FieldError that the handler renders to .error and returns as EINVAL; with this
// the front half of issue Flush shrinks from ~125 lines to one call.
//
// It depends on an issueResolver seam rather than *LinearFS, so the whole
// resolution path is unit-tested with a fake resolver — no repo, SQLite, or API.

// FieldError describes a single field that could not be resolved. Detail renders
// the .error payload in the established "Field / Value / Error" format.
type FieldError struct {
	Field   string
	Value   string
	Message string
}

func (e *FieldError) Detail() string {
	if e.Value != "" {
		return fmt.Sprintf("Field: %s\nValue: %q\nError: %s", e.Field, e.Value, e.Message)
	}
	return fmt.Sprintf("Field: %s\nError: %s", e.Field, e.Message)
}

func (e *FieldError) Error() string { return e.Detail() }

// issueResolver is the minimal set of name→ID lookups resolveIssueUpdate needs.
// *LinearFS satisfies it through its existing Resolve* methods.
type issueResolver interface {
	ResolveStateID(ctx context.Context, teamID, stateName string) (string, error)
	ResolveUserID(ctx context.Context, identifier string) (string, error)
	ResolveLabelIDs(ctx context.Context, teamID string, labelNames []string) ([]string, []string, error)
	ResolveIssueID(ctx context.Context, identifier string) (string, error)
	ResolveProjectID(ctx context.Context, teamID, projectName string) (string, error)
	ResolveMilestoneID(ctx context.Context, projectID, milestoneName string) (string, error)
	ResolveCycleID(ctx context.Context, teamID, cycleName string) (string, error)
}

// resolveIssueUpdate resolves the name-bearing relational fields of a parsed
// issue update into IDs, mutating updates in place. It returns a *FieldError on
// the first field that fails to resolve, or nil on success.
func resolveIssueUpdate(ctx context.Context, r issueResolver, issue *api.Issue, updates map[string]any) *FieldError {
	teamID := ""
	if issue.Team != nil {
		teamID = issue.Team.ID
	}

	// status name -> state ID
	if stateName, ok := updates["stateId"].(string); ok {
		if teamID == "" {
			return &FieldError{Field: "status", Value: stateName, Message: "Cannot resolve state - issue has no team"}
		}
		stateID, err := r.ResolveStateID(ctx, teamID, stateName)
		if err != nil {
			return &FieldError{Field: "status", Value: stateName, Message: err.Error() + ". See states.md for valid workflow states."}
		}
		updates["stateId"] = stateID
	}

	// assignee email/name -> user ID
	if assignee, ok := updates["assigneeId"].(string); ok {
		userID, err := r.ResolveUserID(ctx, assignee)
		if err != nil {
			return &FieldError{Field: "assignee", Value: assignee, Message: err.Error() + ". Use email address or display name."}
		}
		updates["assigneeId"] = userID
	}

	// label names -> IDs; an empty list clears via removedLabelIds
	if labelNames, ok := updates["labelIds"].([]string); ok {
		if len(labelNames) == 0 {
			delete(updates, "labelIds")
			if len(issue.Labels.Nodes) > 0 {
				removedIDs := make([]string, len(issue.Labels.Nodes))
				for idx, l := range issue.Labels.Nodes {
					removedIDs[idx] = l.ID
				}
				updates["removedLabelIds"] = removedIDs
			}
		} else {
			if teamID == "" {
				return &FieldError{Field: "labels", Message: "Cannot resolve labels - issue has no team"}
			}
			labelIDs, notFound, err := r.ResolveLabelIDs(ctx, teamID, labelNames)
			if err != nil {
				return &FieldError{Field: "labels", Message: err.Error()}
			}
			if len(notFound) > 0 {
				return &FieldError{Field: "labels", Value: fmt.Sprintf("%v", notFound), Message: "Unknown labels. See labels.md for valid labels."}
			}
			updates["labelIds"] = labelIDs
		}
	}

	// parent identifier -> ID
	if parent, ok := updates["parentId"].(string); ok {
		issueID, err := r.ResolveIssueID(ctx, parent)
		if err != nil {
			return &FieldError{Field: "parent", Value: parent, Message: err.Error()}
		}
		updates["parentId"] = issueID
	}

	// project name -> ID
	if projectName, ok := updates["projectId"].(string); ok {
		if teamID == "" {
			return &FieldError{Field: "project", Value: projectName, Message: "Cannot resolve project - issue has no team"}
		}
		projectID, err := r.ResolveProjectID(ctx, teamID, projectName)
		if err != nil {
			return &FieldError{Field: "project", Value: projectName, Message: err.Error()}
		}
		updates["projectId"] = projectID
	}

	// milestone name -> ID, resolved against the new or existing project
	if milestoneName, ok := updates["projectMilestoneId"].(string); ok {
		var projectID string
		if newProjectID, ok := updates["projectId"].(string); ok {
			projectID = newProjectID
		} else if issue.Project != nil {
			projectID = issue.Project.ID
		} else {
			return &FieldError{Field: "milestone", Value: milestoneName, Message: "Cannot resolve milestone - issue has no project. Set project first."}
		}
		milestoneID, err := r.ResolveMilestoneID(ctx, projectID, milestoneName)
		if err != nil {
			return &FieldError{Field: "milestone", Value: milestoneName, Message: err.Error()}
		}
		updates["projectMilestoneId"] = milestoneID
	}

	// cycle name -> ID
	if cycleName, ok := updates["cycleId"].(string); ok {
		if teamID == "" {
			return &FieldError{Field: "cycle", Value: cycleName, Message: "Cannot resolve cycle - issue has no team"}
		}
		cycleID, err := r.ResolveCycleID(ctx, teamID, cycleName)
		if err != nil {
			return &FieldError{Field: "cycle", Value: cycleName, Message: err.Error()}
		}
		updates["cycleId"] = cycleID
	}

	return nil
}
