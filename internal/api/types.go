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
	ID               string            `json:"id"`
	Identifier       string            `json:"identifier"`
	Title            string            `json:"title"`
	Description      string            `json:"description"`
	State            State             `json:"state"`
	Assignee         *User             `json:"assignee"`
	Priority         int               `json:"priority"`
	Labels           Labels            `json:"labels"`
	DueDate          *string           `json:"dueDate"`
	Estimate         *float64          `json:"estimate"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	URL              string            `json:"url"`
	Team             *Team             `json:"team"`
	Project          *Project          `json:"project"`
	ProjectMilestone *ProjectMilestone `json:"projectMilestone"`
	Parent           *ParentIssue      `json:"parent"`
	Children         ChildIssues       `json:"children"`
	Cycle            *IssueCycle       `json:"cycle"`
}

// IssueCycle is a minimal cycle representation for issue references
type IssueCycle struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Number int    `json:"number"`
}

// ParentIssue is a minimal issue representation for parent references
type ParentIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
}

// ChildIssues is a collection of child/sub-issues
type ChildIssues struct {
	Nodes []ChildIssue `json:"nodes"`
}

// ChildIssue is a minimal issue representation for child listings
type ChildIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
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
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Slug        string              `json:"slugId"`
	Description string              `json:"description"`
	URL         string              `json:"url"`
	State       string              `json:"state"`
	StartDate   *string             `json:"startDate"`
	TargetDate  *string             `json:"targetDate"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt"`
	Lead        *User               `json:"lead"`
	Status      *Status             `json:"status"`
	Initiatives *ProjectInitiatives `json:"initiatives"`
}

// ProjectInitiatives is a collection of initiatives a project belongs to
type ProjectInitiatives struct {
	Nodes []ProjectInitiative `json:"nodes"`
}

// ProjectInitiative is a minimal initiative representation for project listings
type ProjectInitiative struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Status struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProjectMilestone represents a milestone within a project
type ProjectMilestone struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	TargetDate  *string `json:"targetDate"`
	SortOrder   float64 `json:"sortOrder"`
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

// CycleIssue is a minimal issue representation for cycle listings
type CycleIssue struct {
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	Title      string    `json:"title"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Team       *Team     `json:"team"`
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

// ProjectUpdate represents a status update on a project
type ProjectUpdate struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	Health    string    `json:"health"` // onTrack, atRisk, offTrack
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	User      *User     `json:"user"`
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

type Initiative struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Slug        string             `json:"slugId"`
	Description string             `json:"description"`
	Status      string             `json:"status"`
	Color       string             `json:"color"`
	Icon        string             `json:"icon"`
	TargetDate  *string            `json:"targetDate"`
	URL         string             `json:"url"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	Owner       *User              `json:"owner"`
	Projects    InitiativeProjects `json:"projects"`
}

type InitiativeProjects struct {
	Nodes []InitiativeProject `json:"nodes"`
}

type InitiativeProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slugId"`
}

// InitiativeUpdate represents a status update on an initiative
type InitiativeUpdate struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	Health    string    `json:"health"` // onTrack, atRisk, offTrack
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	User      *User     `json:"user"`
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
