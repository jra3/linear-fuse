package api

import "time"

type Team struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
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
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slugId"`
}

type PageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
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
