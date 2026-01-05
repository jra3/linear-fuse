package marshal

import (
	"fmt"
	"strings"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// HistoryToMarkdown converts issue history entries to a human-readable markdown format
func HistoryToMarkdown(identifier string, entries []api.IssueHistoryEntry) []byte {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# History for %s\n\n", identifier))

	if len(entries) == 0 {
		sb.WriteString("*No history available*\n")
		return []byte(sb.String())
	}

	// Entries come from API in reverse chronological order (newest first)
	for _, entry := range entries {
		sb.WriteString(formatHistoryEntry(&entry))
		sb.WriteString("\n")
	}

	return []byte(sb.String())
}

// formatHistoryEntry formats a single history entry as markdown
func formatHistoryEntry(entry *api.IssueHistoryEntry) string {
	var sb strings.Builder

	// Timestamp and actor
	timestamp := entry.CreatedAt.Format(time.RFC3339)
	actor := "System"
	if entry.Actor != nil {
		if entry.Actor.Email != "" {
			actor = entry.Actor.Email
		} else {
			actor = entry.Actor.Name
		}
	}

	// Determine what changed
	changes := describeChanges(entry)
	if len(changes) == 0 {
		return "" // Skip entries with no describable changes
	}

	changeType := changes[0].changeType
	sb.WriteString(fmt.Sprintf("## %s - %s\n", timestamp, changeType))
	sb.WriteString(fmt.Sprintf("- **By:** %s\n", actor))

	for _, change := range changes {
		if change.from != "" || change.to != "" {
			if change.from == "" {
				sb.WriteString(fmt.Sprintf("- **%s:** %s\n", change.field, change.to))
			} else if change.to == "" {
				sb.WriteString(fmt.Sprintf("- **%s:** ~~%s~~ (cleared)\n", change.field, change.from))
			} else {
				sb.WriteString(fmt.Sprintf("- **%s:** %s → %s\n", change.field, change.from, change.to))
			}
		}
	}

	return sb.String()
}

type changeDescription struct {
	changeType string
	field      string
	from       string
	to         string
}

// describeChanges extracts human-readable descriptions from a history entry
func describeChanges(entry *api.IssueHistoryEntry) []changeDescription {
	var changes []changeDescription

	// Status change
	if entry.FromState != nil || entry.ToState != nil {
		fromName := ""
		toName := ""
		if entry.FromState != nil {
			fromName = entry.FromState.Name
		}
		if entry.ToState != nil {
			toName = entry.ToState.Name
		}
		changes = append(changes, changeDescription{
			changeType: "Status Changed",
			field:      "Status",
			from:       fromName,
			to:         toName,
		})
	}

	// Assignee change
	if entry.FromAssignee != nil || entry.ToAssignee != nil {
		fromName := "(unassigned)"
		toName := "(unassigned)"
		if entry.FromAssignee != nil {
			if entry.FromAssignee.Email != "" {
				fromName = entry.FromAssignee.Email
			} else {
				fromName = entry.FromAssignee.Name
			}
		}
		if entry.ToAssignee != nil {
			if entry.ToAssignee.Email != "" {
				toName = entry.ToAssignee.Email
			} else {
				toName = entry.ToAssignee.Name
			}
		}
		changes = append(changes, changeDescription{
			changeType: "Assignee Changed",
			field:      "Assignee",
			from:       fromName,
			to:         toName,
		})
	}

	// Priority change
	if entry.FromPriority != nil || entry.ToPriority != nil {
		fromPriority := "none"
		toPriority := "none"
		if entry.FromPriority != nil {
			fromPriority = api.PriorityName(*entry.FromPriority)
		}
		if entry.ToPriority != nil {
			toPriority = api.PriorityName(*entry.ToPriority)
		}
		changes = append(changes, changeDescription{
			changeType: "Priority Changed",
			field:      "Priority",
			from:       fromPriority,
			to:         toPriority,
		})
	}

	// Title change
	if entry.FromTitle != nil || entry.ToTitle != nil {
		fromTitle := ""
		toTitle := ""
		if entry.FromTitle != nil {
			fromTitle = *entry.FromTitle
		}
		if entry.ToTitle != nil {
			toTitle = *entry.ToTitle
		}
		changes = append(changes, changeDescription{
			changeType: "Title Changed",
			field:      "Title",
			from:       fromTitle,
			to:         toTitle,
		})
	}

	// Due date change
	if entry.FromDueDate != nil || entry.ToDueDate != nil {
		fromDue := ""
		toDue := ""
		if entry.FromDueDate != nil {
			fromDue = *entry.FromDueDate
		}
		if entry.ToDueDate != nil {
			toDue = *entry.ToDueDate
		}
		changes = append(changes, changeDescription{
			changeType: "Due Date Changed",
			field:      "Due Date",
			from:       fromDue,
			to:         toDue,
		})
	}

	// Estimate change
	if entry.FromEstimate != nil || entry.ToEstimate != nil {
		fromEst := ""
		toEst := ""
		if entry.FromEstimate != nil {
			fromEst = fmt.Sprintf("%.0f", *entry.FromEstimate)
		}
		if entry.ToEstimate != nil {
			toEst = fmt.Sprintf("%.0f", *entry.ToEstimate)
		}
		changes = append(changes, changeDescription{
			changeType: "Estimate Changed",
			field:      "Estimate",
			from:       fromEst,
			to:         toEst,
		})
	}

	// Parent change
	if entry.FromParent != nil || entry.ToParent != nil {
		fromParent := ""
		toParent := ""
		if entry.FromParent != nil {
			fromParent = entry.FromParent.Identifier
		}
		if entry.ToParent != nil {
			toParent = entry.ToParent.Identifier
		}
		changes = append(changes, changeDescription{
			changeType: "Parent Changed",
			field:      "Parent",
			from:       fromParent,
			to:         toParent,
		})
	}

	// Project change
	if entry.FromProject != nil || entry.ToProject != nil {
		fromProject := ""
		toProject := ""
		if entry.FromProject != nil {
			fromProject = entry.FromProject.Name
		}
		if entry.ToProject != nil {
			toProject = entry.ToProject.Name
		}
		changes = append(changes, changeDescription{
			changeType: "Project Changed",
			field:      "Project",
			from:       fromProject,
			to:         toProject,
		})
	}

	// Cycle change
	if entry.FromCycle != nil || entry.ToCycle != nil {
		fromCycle := ""
		toCycle := ""
		if entry.FromCycle != nil {
			fromCycle = entry.FromCycle.Name
		}
		if entry.ToCycle != nil {
			toCycle = entry.ToCycle.Name
		}
		changes = append(changes, changeDescription{
			changeType: "Cycle Changed",
			field:      "Cycle",
			from:       fromCycle,
			to:         toCycle,
		})
	}

	// Labels added
	if len(entry.AddedLabels) > 0 {
		var labelNames []string
		for _, l := range entry.AddedLabels {
			labelNames = append(labelNames, l.Name)
		}
		changes = append(changes, changeDescription{
			changeType: "Labels Added",
			field:      "Added Labels",
			to:         strings.Join(labelNames, ", "),
		})
	}

	// Labels removed
	if len(entry.RemovedLabels) > 0 {
		var labelNames []string
		for _, l := range entry.RemovedLabels {
			labelNames = append(labelNames, l.Name)
		}
		changes = append(changes, changeDescription{
			changeType: "Labels Removed",
			field:      "Removed Labels",
			from:       strings.Join(labelNames, ", "),
		})
	}

	// Description updated
	if entry.UpdatedDescription {
		changes = append(changes, changeDescription{
			changeType: "Description Updated",
			field:      "Description",
			to:         "(content changed)",
		})
	}

	return changes
}
