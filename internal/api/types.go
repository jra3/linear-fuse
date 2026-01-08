package api

import (
	"fmt"
	"time"
)

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
	BranchName       string            `json:"branchName"`
	State            State             `json:"state"`
	Assignee         *User             `json:"assignee"`
	Creator          *User             `json:"creator"`
	Priority         int               `json:"priority"`
	Labels           Labels            `json:"labels"`
	DueDate          *string           `json:"dueDate"`
	Estimate         *float64          `json:"estimate"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	StartedAt        *time.Time        `json:"startedAt"`
	CompletedAt      *time.Time        `json:"completedAt"`
	CanceledAt       *time.Time        `json:"canceledAt"`
	ArchivedAt       *time.Time        `json:"archivedAt"`
	URL              string            `json:"url"`
	Team             *Team             `json:"team"`
	Project          *Project          `json:"project"`
	ProjectMilestone *ProjectMilestone `json:"projectMilestone"`
	Parent           *ParentIssue      `json:"parent"`
	Children         ChildIssues       `json:"children"`
	Cycle            *IssueCycle       `json:"cycle"`
	Relations        IssueRelations    `json:"relations"`
	InverseRelations IssueRelations    `json:"inverseRelations"`
}

// IssueRelations is a collection of issue relations
type IssueRelations struct {
	Nodes []IssueRelation `json:"nodes"`
}

// IssueRelation represents a relationship between two issues
type IssueRelation struct {
	ID           string       `json:"id"`
	Type         string       `json:"type"` // blocks, duplicate, related, similar
	RelatedIssue *ParentIssue `json:"relatedIssue,omitempty"`
	Issue        *ParentIssue `json:"issue,omitempty"` // For inverse relations
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
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
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
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Team       *Team     `json:"team"`
}

// CycleIssue is a minimal issue representation for cycle listings
type CycleIssue struct {
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"createdAt"`
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

// PriorityValue converts string priority to numeric (silently defaults to 0)
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

// ValidatePriority validates and converts string priority to numeric, returning error for invalid values
func ValidatePriority(name string) (int, error) {
	switch name {
	case "urgent":
		return 1, nil
	case "high":
		return 2, nil
	case "medium":
		return 3, nil
	case "low":
		return 4, nil
	case "none", "":
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid priority %q: must be none, low, medium, high, or urgent", name)
	}
}

// Attachment represents an external link attachment (GitHub PR, Slack message, etc.)
type Attachment struct {
	ID         string                 `json:"id"`
	Title      string                 `json:"title"`
	Subtitle   string                 `json:"subtitle"`
	URL        string                 `json:"url"`
	SourceType string                 `json:"sourceType"`
	Metadata   map[string]interface{} `json:"metadata"`
	Creator    *User                  `json:"creator"`
	CreatedAt  time.Time              `json:"createdAt"`
	UpdatedAt  time.Time              `json:"updatedAt"`
}

// EmbeddedFile represents a file uploaded to Linear's CDN (image, PDF, etc.)
type EmbeddedFile struct {
	ID        string    // SHA256 hash of URL
	IssueID   string    // Issue this file belongs to
	URL       string    // Linear CDN URL
	Filename  string    // Derived filename
	MimeType  string    // MIME type (e.g., "image/png")
	FileSize  int64     // File size in bytes (0 if unknown)
	CachePath string    // Local cache path (empty if not cached)
	Source    string    // Where found: "description" or "comment:{id}"
	SyncedAt  time.Time // When metadata was synced
}

// IssueHistoryEntry represents a single change in an issue's history
type IssueHistoryEntry struct {
	ID                 string     `json:"id"`
	CreatedAt          time.Time  `json:"createdAt"`
	Actor              *User      `json:"actor"`
	FromAssignee       *User      `json:"fromAssignee"`
	ToAssignee         *User      `json:"toAssignee"`
	FromState          *State     `json:"fromState"`
	ToState            *State     `json:"toState"`
	FromPriority       *int       `json:"fromPriority"`
	ToPriority         *int       `json:"toPriority"`
	FromTitle          *string    `json:"fromTitle"`
	ToTitle            *string    `json:"toTitle"`
	FromDueDate        *string    `json:"fromDueDate"`
	ToDueDate          *string    `json:"toDueDate"`
	FromEstimate       *float64   `json:"fromEstimate"`
	ToEstimate         *float64   `json:"toEstimate"`
	FromParent         *ParentRef `json:"fromParent"`
	ToParent           *ParentRef `json:"toParent"`
	FromProject        *NamedRef  `json:"fromProject"`
	ToProject          *NamedRef  `json:"toProject"`
	FromCycle          *NamedRef  `json:"fromCycle"`
	ToCycle            *NamedRef  `json:"toCycle"`
	AddedLabels        []Label    `json:"addedLabels"`
	RemovedLabels      []Label    `json:"removedLabels"`
	UpdatedDescription bool       `json:"updatedDescription"`
}

// ParentRef is a minimal issue reference for history entries
type ParentRef struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
}

// NamedRef is a minimal reference with just ID and name
type NamedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// IssueHistory is a collection of history entries
type IssueHistory struct {
	Nodes []IssueHistoryEntry `json:"nodes"`
}
