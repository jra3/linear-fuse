package linear

import (
	"time"
)

// Issue represents a Linear issue
type Issue struct {
	ID          string          `json:"id"`
	Identifier  string          `json:"identifier"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Priority    int             `json:"priority"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	State       State           `json:"state"`
	Assignee    *User           `json:"assignee"`
	Creator     User            `json:"creator"`
	Team        Team            `json:"team"`
	Labels      LabelConnection `json:"labels"`
}

// State represents an issue state
type State struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// User represents a Linear user
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Team represents a Linear team
type Team struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Label represents a Linear label
type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LabelConnection represents a paginated list of labels
type LabelConnection struct {
	Nodes []Label `json:"nodes"`
}

// IssuesResponse represents the response from the issues query
type IssuesResponse struct {
	Issues IssueConnection `json:"issues"`
}

// IssueConnection represents a paginated list of issues
type IssueConnection struct {
	Nodes    []Issue  `json:"nodes"`
	PageInfo PageInfo `json:"pageInfo"`
}

// PageInfo represents pagination information
type PageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// IssueResponse represents the response from a single issue query
type IssueResponse struct {
	Issue Issue `json:"issue"`
}

// UpdateIssueResponse represents the response from an issue update mutation
type UpdateIssueResponse struct {
	IssueUpdate IssueUpdatePayload `json:"issueUpdate"`
}

// IssueUpdatePayload represents the payload from an issue update
type IssueUpdatePayload struct {
	Success bool  `json:"success"`
	Issue   Issue `json:"issue"`
}

// CreateIssueResponse represents the response from an issue creation mutation
type CreateIssueResponse struct {
	IssueCreate IssueCreatePayload `json:"issueCreate"`
}

// IssueCreatePayload represents the payload from an issue creation
type IssueCreatePayload struct {
	Success bool  `json:"success"`
	Issue   Issue `json:"issue"`
}
