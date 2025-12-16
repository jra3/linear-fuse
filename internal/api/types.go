package api

import "time"

type Team struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Icon      string    `json:"icon"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Issue struct {
	ID          string     `json:"id"`
	Identifier  string     `json:"identifier"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       State      `json:"state"`
	Assignee    *User      `json:"assignee"`
	Priority    int        `json:"priority"`
	Labels      Labels     `json:"labels"`
	DueDate     *string    `json:"dueDate"`
	Estimate    *float64   `json:"estimate"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	URL         string     `json:"url"`
	Team        *Team      `json:"team"`
	Project     *Project   `json:"project"`
}

type State struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // backlog, unstarted, started, completed, canceled
}

type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`
}

type Labels struct {
	Nodes []Label `json:"nodes"`
}

type Label struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type Project struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Slug        string     `json:"slugId"`
	Description string     `json:"description"`
	URL         string     `json:"url"`
	State       string     `json:"state"`
	StartDate   *string    `json:"startDate"`
	TargetDate  *string    `json:"targetDate"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	Lead        *User      `json:"lead"`
	Status      *Status    `json:"status"`
}

type Status struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// ProjectIssue is a minimal issue representation for project listings
type ProjectIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Team       *Team  `json:"team"`
}

type Cycle struct {
	ID                         string    `json:"id"`
	Number                     int       `json:"number"`
	Name                       string    `json:"name"`
	StartsAt                   time.Time `json:"startsAt"`
	EndsAt                     time.Time `json:"endsAt"`
	CompletedIssueCountHistory []int     `json:"completedIssueCountHistory"`
	IssueCountHistory          []int     `json:"issueCountHistory"`
}

type Comment struct {
	ID        string     `json:"id"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	EditedAt  *time.Time `json:"editedAt"`
	User      *User      `json:"user"`
}

type Document struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	SlugID    string    `json:"slugId"`
	URL       string    `json:"url"`
	Icon      string    `json:"icon"`
	Color     string    `json:"color"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Creator   *User     `json:"creator"`
	Issue     *Issue    `json:"issue"`
	Project   *Project  `json:"project"`
	Team      *Team     `json:"team"`
}

// PriorityName converts numeric priority to string
func PriorityName(p int) string {
	switch p {
	case 1:
		return "urgent"
	case 2:
		return "high"
	case 3:
		return "medium"
	case 4:
		return "low"
	default:
		return "none"
	}
}

// PriorityValue converts string priority to numeric
func PriorityValue(name string) int {
	switch name {
	case "urgent":
		return 1
	case "high":
		return 2
	case "medium":
		return 3
	case "low":
		return 4
	default:
		return 0
	}
}
