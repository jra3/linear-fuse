package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/time/rate"
)

var debugRateLimit = os.Getenv("LINEARFS_DEBUG_RATE") != ""
var debugAPI = os.Getenv("LINEARFS_DEBUG_API") != ""

const defaultAPIURL = "https://api.linear.app/graphql"

type Client struct {
	apiKey     string
	apiURL     string
	httpClient *http.Client
	limiter    *rate.Limiter
}

func NewClient(apiKey string) *Client {
	// Linear allows 1,500 requests/hour (25/min).
	// Burst of 50 handles cold cache scenarios; rate of 2/sec for sustained use.
	limiter := rate.NewLimiter(rate.Limit(2), 50)

	return &Client{
		apiKey:     apiKey,
		apiURL:     defaultAPIURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		limiter:    limiter,
	}
}

// SetAPIURL overrides the API URL (for testing).
func (c *Client) SetAPIURL(url string) {
	c.apiURL = url
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

func (c *Client) query(ctx context.Context, query string, variables map[string]any, result any) error {
	// Extract operation name for logging (first word after "query" or "mutation")
	var opName string
	if debugAPI {
		// Simple extraction: find first { and take word before it
		for i, ch := range query {
			if ch == '{' || ch == '(' {
				// Walk backwards to find operation name
				end := i - 1
				for end > 0 && (query[end] == ' ' || query[end] == '\n') {
					end--
				}
				start := end
				for start > 0 && query[start-1] != ' ' && query[start-1] != '\n' {
					start--
				}
				if start < end {
					opName = query[start : end+1]
				}
				break
			}
		}
		log.Printf("[API] Calling %s vars=%v", opName, variables)
	}

	// Wait for rate limit token before making request
	if debugRateLimit {
		reservation := c.limiter.Reserve()
		delay := reservation.Delay()
		if delay > 0 {
			log.Printf("[RATE] Waiting %v for rate limit token", delay)
		}
		reservation.Cancel() // Cancel and use Wait() instead for proper blocking
	}
	start := time.Now()
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait cancelled: %w", err)
	}
	if debugRateLimit && time.Since(start) > 100*time.Millisecond {
		log.Printf("[RATE] Waited %v for rate limit", time.Since(start))
	}

	reqBody := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var gqlResp graphQLResponse
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return fmt.Errorf("GraphQL error: %s", gqlResp.Errors[0].Message)
	}

	if err := json.Unmarshal(gqlResp.Data, result); err != nil {
		return fmt.Errorf("failed to parse data: %w", err)
	}

	return nil
}

// GetTeams fetches all teams the user has access to
func (c *Client) GetTeams(ctx context.Context) ([]Team, error) {
	var result struct {
		Teams struct {
			Nodes []Team `json:"nodes"`
		} `json:"teams"`
	}

	err := c.query(ctx, queryTeams, nil, &result)
	if err != nil {
		return nil, err
	}

	return result.Teams.Nodes, nil
}

// GetTeamIssues fetches all issues for a team
func (c *Client) GetTeamIssues(ctx context.Context, teamID string) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			Team struct {
				Issues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []Issue  `json:"nodes"`
				} `json:"issues"`
			} `json:"team"`
		}

		vars := map[string]any{
			"teamId": teamID,
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryTeamIssues, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Team.Issues.Nodes...)

		if !result.Team.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Team.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetTeamIssuesPage fetches a single page of issues ordered by updatedAt DESC.
// Returns the issues, page info, and any error.
// Use cursor="" for the first page.
func (c *Client) GetTeamIssuesPage(ctx context.Context, teamID string, cursor string, pageSize int) ([]Issue, PageInfo, error) {
	var result struct {
		Team struct {
			Issues struct {
				PageInfo PageInfo `json:"pageInfo"`
				Nodes    []Issue  `json:"nodes"`
			} `json:"issues"`
		} `json:"team"`
	}

	vars := map[string]any{
		"teamId": teamID,
		"first":  pageSize,
	}
	if cursor != "" {
		vars["after"] = cursor
	}

	err := c.query(ctx, queryTeamIssuesByUpdatedAt, vars, &result)
	if err != nil {
		return nil, PageInfo{}, err
	}

	return result.Team.Issues.Nodes, result.Team.Issues.PageInfo, nil
}

// GetTeamIssuesByStatus fetches issues filtered by status name
func (c *Client) GetTeamIssuesByStatus(ctx context.Context, teamID, statusName string) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			Team struct {
				Issues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []Issue  `json:"nodes"`
				} `json:"issues"`
			} `json:"team"`
		}

		vars := map[string]any{
			"teamId":     teamID,
			"statusName": statusName,
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryTeamIssuesByStatus, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Team.Issues.Nodes...)

		if !result.Team.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Team.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetTeamIssuesByPriority fetches issues filtered by priority (0=none, 1=urgent, 2=high, 3=medium, 4=low)
func (c *Client) GetTeamIssuesByPriority(ctx context.Context, teamID string, priority int) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			Issues struct {
				PageInfo PageInfo `json:"pageInfo"`
				Nodes    []Issue  `json:"nodes"`
			} `json:"issues"`
		}

		vars := map[string]any{
			"teamId":   teamID,
			"priority": priority,
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryTeamIssuesByPriority, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Issues.Nodes...)

		if !result.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetTeamIssuesByLabel fetches issues filtered by label name
func (c *Client) GetTeamIssuesByLabel(ctx context.Context, teamID, labelName string) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			Team struct {
				Issues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []Issue  `json:"nodes"`
				} `json:"issues"`
			} `json:"team"`
		}

		vars := map[string]any{
			"teamId":    teamID,
			"labelName": labelName,
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryTeamIssuesByLabel, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Team.Issues.Nodes...)

		if !result.Team.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Team.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetTeamIssuesByAssignee fetches issues filtered by assignee ID
func (c *Client) GetTeamIssuesByAssignee(ctx context.Context, teamID, assigneeID string) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			Team struct {
				Issues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []Issue  `json:"nodes"`
				} `json:"issues"`
			} `json:"team"`
		}

		vars := map[string]any{
			"teamId":     teamID,
			"assigneeId": assigneeID,
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryTeamIssuesByAssignee, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Team.Issues.Nodes...)

		if !result.Team.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Team.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetTeamIssuesUnassigned fetches issues with no assignee
func (c *Client) GetTeamIssuesUnassigned(ctx context.Context, teamID string) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			Team struct {
				Issues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []Issue  `json:"nodes"`
				} `json:"issues"`
			} `json:"team"`
		}

		vars := map[string]any{
			"teamId": teamID,
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryTeamIssuesUnassigned, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Team.Issues.Nodes...)

		if !result.Team.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Team.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetIssue fetches a single issue by ID
func (c *Client) GetIssue(ctx context.Context, issueID string) (*Issue, error) {
	var result struct {
		Issue Issue `json:"issue"`
	}

	vars := map[string]any{
		"id": issueID,
	}

	err := c.query(ctx, queryIssue, vars, &result)
	if err != nil {
		return nil, err
	}

	return &result.Issue, nil
}

// GetMyIssues fetches issues assigned to the current user
func (c *Client) GetMyIssues(ctx context.Context) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			Viewer struct {
				AssignedIssues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []Issue  `json:"nodes"`
				} `json:"assignedIssues"`
			} `json:"viewer"`
		}

		vars := map[string]any{}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryMyIssues, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Viewer.AssignedIssues.Nodes...)

		if !result.Viewer.AssignedIssues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Viewer.AssignedIssues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetMyCreatedIssues fetches issues created by the current user
func (c *Client) GetMyCreatedIssues(ctx context.Context) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			Viewer struct {
				CreatedIssues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []Issue  `json:"nodes"`
				} `json:"createdIssues"`
			} `json:"viewer"`
		}

		vars := map[string]any{}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryMyCreatedIssues, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Viewer.CreatedIssues.Nodes...)

		if !result.Viewer.CreatedIssues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Viewer.CreatedIssues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetMyActiveIssues fetches active (not completed/canceled) issues assigned to the current user
func (c *Client) GetMyActiveIssues(ctx context.Context) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			Viewer struct {
				AssignedIssues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []Issue  `json:"nodes"`
				} `json:"assignedIssues"`
			} `json:"viewer"`
		}

		vars := map[string]any{}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryMyActiveIssues, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Viewer.AssignedIssues.Nodes...)

		if !result.Viewer.AssignedIssues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Viewer.AssignedIssues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// UpdateIssue updates an existing issue
func (c *Client) UpdateIssue(ctx context.Context, issueID string, input map[string]any) error {
	var result struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}

	vars := map[string]any{
		"id":    issueID,
		"input": input,
	}

	err := c.query(ctx, mutationUpdateIssue, vars, &result)
	if err != nil {
		return err
	}

	if !result.IssueUpdate.Success {
		return fmt.Errorf("issue update failed")
	}

	return nil
}

// ArchiveIssue archives an issue (soft delete)
func (c *Client) ArchiveIssue(ctx context.Context, issueID string) error {
	var result struct {
		IssueArchive struct {
			Success bool `json:"success"`
		} `json:"issueArchive"`
	}

	vars := map[string]any{
		"id": issueID,
	}

	err := c.query(ctx, mutationArchiveIssue, vars, &result)
	if err != nil {
		return err
	}

	if !result.IssueArchive.Success {
		return fmt.Errorf("issue archive failed")
	}

	return nil
}

// GetTeamStates fetches workflow states for a team
func (c *Client) GetTeamStates(ctx context.Context, teamID string) ([]State, error) {
	var result struct {
		Team struct {
			States struct {
				Nodes []State `json:"nodes"`
			} `json:"states"`
		} `json:"team"`
	}

	vars := map[string]any{
		"teamId": teamID,
	}

	err := c.query(ctx, queryTeamStates, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Team.States.Nodes, nil
}

// GetTeamProjects fetches projects for a team
func (c *Client) GetTeamProjects(ctx context.Context, teamID string) ([]Project, error) {
	var result struct {
		Team struct {
			Projects struct {
				Nodes []Project `json:"nodes"`
			} `json:"projects"`
		} `json:"team"`
	}

	vars := map[string]any{
		"teamId": teamID,
	}

	err := c.query(ctx, queryTeamProjects, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Team.Projects.Nodes, nil
}

// GetProjectIssues fetches issues for a project
func (c *Client) GetProjectIssues(ctx context.Context, projectID string) ([]ProjectIssue, error) {
	var allIssues []ProjectIssue
	var cursor *string

	for {
		var result struct {
			Project struct {
				Issues struct {
					PageInfo PageInfo       `json:"pageInfo"`
					Nodes    []ProjectIssue `json:"nodes"`
				} `json:"issues"`
			} `json:"project"`
		}

		vars := map[string]any{
			"projectId": projectID,
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryProjectIssues, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Project.Issues.Nodes...)

		if !result.Project.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Project.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetProjectMilestones fetches milestones for a project
func (c *Client) GetProjectMilestones(ctx context.Context, projectID string) ([]ProjectMilestone, error) {
	var result struct {
		Project struct {
			ProjectMilestones struct {
				Nodes []ProjectMilestone `json:"nodes"`
			} `json:"projectMilestones"`
		} `json:"project"`
	}

	vars := map[string]any{
		"projectId": projectID,
	}

	err := c.query(ctx, queryProjectMilestones, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Project.ProjectMilestones.Nodes, nil
}

// GetProjectUpdates fetches status updates for a project
func (c *Client) GetProjectUpdates(ctx context.Context, projectID string) ([]ProjectUpdate, error) {
	var result struct {
		Project struct {
			ProjectUpdates struct {
				Nodes []ProjectUpdate `json:"nodes"`
			} `json:"projectUpdates"`
		} `json:"project"`
	}

	vars := map[string]any{
		"projectId": projectID,
	}

	err := c.query(ctx, queryProjectUpdates, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Project.ProjectUpdates.Nodes, nil
}

// CreateProjectUpdate creates a new status update on a project
func (c *Client) CreateProjectUpdate(ctx context.Context, projectID, body, health string) (*ProjectUpdate, error) {
	var result struct {
		ProjectUpdateCreate struct {
			Success       bool          `json:"success"`
			ProjectUpdate ProjectUpdate `json:"projectUpdate"`
		} `json:"projectUpdateCreate"`
	}

	vars := map[string]any{
		"projectId": projectID,
		"body":      body,
	}
	if health != "" {
		vars["health"] = health
	}

	err := c.query(ctx, mutationCreateProjectUpdate, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.ProjectUpdateCreate.Success {
		return nil, fmt.Errorf("failed to create project update")
	}

	return &result.ProjectUpdateCreate.ProjectUpdate, nil
}

// GetInitiativeUpdates fetches status updates for an initiative
func (c *Client) GetInitiativeUpdates(ctx context.Context, initiativeID string) ([]InitiativeUpdate, error) {
	var result struct {
		Initiative struct {
			InitiativeUpdates struct {
				Nodes []InitiativeUpdate `json:"nodes"`
			} `json:"initiativeUpdates"`
		} `json:"initiative"`
	}

	vars := map[string]any{
		"initiativeId": initiativeID,
	}

	err := c.query(ctx, queryInitiativeUpdates, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Initiative.InitiativeUpdates.Nodes, nil
}

// CreateInitiativeUpdate creates a new status update on an initiative
func (c *Client) CreateInitiativeUpdate(ctx context.Context, initiativeID, body, health string) (*InitiativeUpdate, error) {
	var result struct {
		InitiativeUpdateCreate struct {
			Success          bool             `json:"success"`
			InitiativeUpdate InitiativeUpdate `json:"initiativeUpdate"`
		} `json:"initiativeUpdateCreate"`
	}

	vars := map[string]any{
		"initiativeId": initiativeID,
		"body":         body,
	}
	if health != "" {
		vars["health"] = health
	}

	err := c.query(ctx, mutationCreateInitiativeUpdate, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.InitiativeUpdateCreate.Success {
		return nil, fmt.Errorf("failed to create initiative update")
	}

	return &result.InitiativeUpdateCreate.InitiativeUpdate, nil
}

// CreateProject creates a new project
func (c *Client) CreateProject(ctx context.Context, input map[string]any) (*Project, error) {
	var result struct {
		ProjectCreate struct {
			Success bool    `json:"success"`
			Project Project `json:"project"`
		} `json:"projectCreate"`
	}

	vars := map[string]any{
		"input": input,
	}

	err := c.query(ctx, mutationCreateProject, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.ProjectCreate.Success {
		return nil, fmt.Errorf("project creation failed")
	}

	return &result.ProjectCreate.Project, nil
}

// ArchiveProject archives a project (soft delete)
func (c *Client) ArchiveProject(ctx context.Context, projectID string) error {
	var result struct {
		ProjectArchive struct {
			Success bool `json:"success"`
		} `json:"projectArchive"`
	}

	vars := map[string]any{
		"id": projectID,
	}

	err := c.query(ctx, mutationArchiveProject, vars, &result)
	if err != nil {
		return err
	}

	if !result.ProjectArchive.Success {
		return fmt.Errorf("project archive failed")
	}

	return nil
}

// AddProjectToInitiative links a project to an initiative
func (c *Client) AddProjectToInitiative(ctx context.Context, projectID, initiativeID string) error {
	var result struct {
		InitiativeToProjectCreate struct {
			Success bool `json:"success"`
		} `json:"initiativeToProjectCreate"`
	}

	vars := map[string]any{
		"projectId":    projectID,
		"initiativeId": initiativeID,
	}

	err := c.query(ctx, mutationInitiativeToProjectCreate, vars, &result)
	if err != nil {
		return err
	}

	if !result.InitiativeToProjectCreate.Success {
		return fmt.Errorf("failed to add project to initiative")
	}

	return nil
}

// RemoveProjectFromInitiative unlinks a project from an initiative
func (c *Client) RemoveProjectFromInitiative(ctx context.Context, projectID, initiativeID string) error {
	var result struct {
		InitiativeToProjectDelete struct {
			Success bool `json:"success"`
		} `json:"initiativeToProjectDelete"`
	}

	vars := map[string]any{
		"projectId":    projectID,
		"initiativeId": initiativeID,
	}

	err := c.query(ctx, mutationInitiativeToProjectDelete, vars, &result)
	if err != nil {
		return err
	}

	if !result.InitiativeToProjectDelete.Success {
		return fmt.Errorf("failed to remove project from initiative")
	}

	return nil
}

// GetTeamCycles fetches cycles for a team
func (c *Client) GetTeamCycles(ctx context.Context, teamID string) ([]Cycle, error) {
	var result struct {
		Team struct {
			Cycles struct {
				Nodes []Cycle `json:"nodes"`
			} `json:"cycles"`
		} `json:"team"`
	}

	vars := map[string]any{
		"teamId": teamID,
	}

	err := c.query(ctx, queryTeamCycles, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Team.Cycles.Nodes, nil
}

// GetCycleIssues fetches issues belonging to a cycle with pagination
func (c *Client) GetCycleIssues(ctx context.Context, cycleID string) ([]CycleIssue, error) {
	var allIssues []CycleIssue
	var cursor *string

	for {
		var result struct {
			Cycle struct {
				Issues struct {
					PageInfo PageInfo     `json:"pageInfo"`
					Nodes    []CycleIssue `json:"nodes"`
				} `json:"issues"`
			} `json:"cycle"`
		}

		vars := map[string]any{
			"cycleId": cycleID,
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryCycleIssues, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.Cycle.Issues.Nodes...)

		if !result.Cycle.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.Cycle.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetTeamLabels fetches labels for a team (team + workspace labels)
func (c *Client) GetTeamLabels(ctx context.Context, teamID string) ([]Label, error) {
	var result struct {
		Team struct {
			Labels struct {
				Nodes []Label `json:"nodes"`
			} `json:"labels"`
		} `json:"team"`
		IssueLabels struct {
			Nodes []Label `json:"nodes"`
		} `json:"issueLabels"`
	}

	vars := map[string]any{
		"teamId": teamID,
	}

	err := c.query(ctx, queryTeamLabels, vars, &result)
	if err != nil {
		return nil, err
	}

	// Combine team labels and workspace labels, deduplicating by ID
	seen := make(map[string]bool)
	var labels []Label
	for _, l := range result.Team.Labels.Nodes {
		if !seen[l.ID] {
			seen[l.ID] = true
			labels = append(labels, l)
		}
	}
	for _, l := range result.IssueLabels.Nodes {
		if !seen[l.ID] {
			seen[l.ID] = true
			labels = append(labels, l)
		}
	}

	return labels, nil
}

// CreateLabel creates a new label
func (c *Client) CreateLabel(ctx context.Context, input map[string]any) (*Label, error) {
	var result struct {
		IssueLabelCreate struct {
			Success    bool  `json:"success"`
			IssueLabel Label `json:"issueLabel"`
		} `json:"issueLabelCreate"`
	}

	vars := map[string]any{
		"input": input,
	}

	err := c.query(ctx, mutationCreateLabel, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.IssueLabelCreate.Success {
		return nil, fmt.Errorf("label creation failed")
	}

	return &result.IssueLabelCreate.IssueLabel, nil
}

// UpdateLabel updates an existing label
func (c *Client) UpdateLabel(ctx context.Context, id string, input map[string]any) (*Label, error) {
	var result struct {
		IssueLabelUpdate struct {
			Success    bool  `json:"success"`
			IssueLabel Label `json:"issueLabel"`
		} `json:"issueLabelUpdate"`
	}

	vars := map[string]any{
		"id":    id,
		"input": input,
	}

	err := c.query(ctx, mutationUpdateLabel, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.IssueLabelUpdate.Success {
		return nil, fmt.Errorf("label update failed")
	}

	return &result.IssueLabelUpdate.IssueLabel, nil
}

// DeleteLabel deletes a label
func (c *Client) DeleteLabel(ctx context.Context, id string) error {
	var result struct {
		IssueLabelDelete struct {
			Success bool `json:"success"`
		} `json:"issueLabelDelete"`
	}

	vars := map[string]any{
		"id": id,
	}

	err := c.query(ctx, mutationDeleteLabel, vars, &result)
	if err != nil {
		return err
	}

	if !result.IssueLabelDelete.Success {
		return fmt.Errorf("label deletion failed")
	}

	return nil
}

// GetUsers fetches all users in the workspace
func (c *Client) GetUsers(ctx context.Context) ([]User, error) {
	var result struct {
		Users struct {
			Nodes []User `json:"nodes"`
		} `json:"users"`
	}

	err := c.query(ctx, queryUsers, nil, &result)
	if err != nil {
		return nil, err
	}

	return result.Users.Nodes, nil
}

// GetTeamMembers fetches members of a specific team
func (c *Client) GetTeamMembers(ctx context.Context, teamID string) ([]User, error) {
	var result struct {
		Team struct {
			Members struct {
				Nodes []User `json:"nodes"`
			} `json:"members"`
		} `json:"team"`
	}

	vars := map[string]any{
		"teamId": teamID,
	}

	err := c.query(ctx, queryTeamMembers, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Team.Members.Nodes, nil
}

// GetUserIssues fetches issues assigned to a specific user
func (c *Client) GetUserIssues(ctx context.Context, userID string) ([]Issue, error) {
	var allIssues []Issue
	var cursor *string

	for {
		var result struct {
			User struct {
				AssignedIssues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []Issue  `json:"nodes"`
				} `json:"assignedIssues"`
			} `json:"user"`
		}

		vars := map[string]any{
			"userId": userID,
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		err := c.query(ctx, queryUserIssues, vars, &result)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, result.User.AssignedIssues.Nodes...)

		if !result.User.AssignedIssues.PageInfo.HasNextPage {
			break
		}
		cursor = &result.User.AssignedIssues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// CreateIssue creates a new issue
func (c *Client) CreateIssue(ctx context.Context, input map[string]any) (*Issue, error) {
	var result struct {
		IssueCreate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueCreate"`
	}

	vars := map[string]any{
		"input": input,
	}

	err := c.query(ctx, mutationCreateIssue, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.IssueCreate.Success {
		return nil, fmt.Errorf("issue creation failed")
	}

	return &result.IssueCreate.Issue, nil
}

// GetIssueComments fetches comments for an issue
func (c *Client) GetIssueComments(ctx context.Context, issueID string) ([]Comment, error) {
	var result struct {
		Issue struct {
			Comments struct {
				Nodes []Comment `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}

	vars := map[string]any{
		"issueId": issueID,
	}

	err := c.query(ctx, queryIssueComments, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Issue.Comments.Nodes, nil
}

// CreateComment creates a new comment on an issue
func (c *Client) CreateComment(ctx context.Context, issueID string, body string) (*Comment, error) {
	var result struct {
		CommentCreate struct {
			Success bool    `json:"success"`
			Comment Comment `json:"comment"`
		} `json:"commentCreate"`
	}

	vars := map[string]any{
		"issueId": issueID,
		"body":    body,
	}

	err := c.query(ctx, mutationCreateComment, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.CommentCreate.Success {
		return nil, fmt.Errorf("comment creation failed")
	}

	return &result.CommentCreate.Comment, nil
}

// UpdateComment updates an existing comment
func (c *Client) UpdateComment(ctx context.Context, commentID string, body string) (*Comment, error) {
	var result struct {
		CommentUpdate struct {
			Success bool    `json:"success"`
			Comment Comment `json:"comment"`
		} `json:"commentUpdate"`
	}

	vars := map[string]any{
		"id":   commentID,
		"body": body,
	}

	err := c.query(ctx, mutationUpdateComment, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.CommentUpdate.Success {
		return nil, fmt.Errorf("comment update failed")
	}

	return &result.CommentUpdate.Comment, nil
}

// DeleteComment deletes a comment
func (c *Client) DeleteComment(ctx context.Context, commentID string) error {
	var result struct {
		CommentDelete struct {
			Success bool `json:"success"`
		} `json:"commentDelete"`
	}

	vars := map[string]any{
		"id": commentID,
	}

	err := c.query(ctx, mutationDeleteComment, vars, &result)
	if err != nil {
		return err
	}

	if !result.CommentDelete.Success {
		return fmt.Errorf("comment deletion failed")
	}

	return nil
}

// GetIssueDocuments fetches documents attached to an issue
func (c *Client) GetIssueDocuments(ctx context.Context, issueID string) ([]Document, error) {
	var result struct {
		Issue struct {
			Documents struct {
				Nodes []Document `json:"nodes"`
			} `json:"documents"`
		} `json:"issue"`
	}

	vars := map[string]any{
		"issueId": issueID,
	}

	err := c.query(ctx, queryIssueDocuments, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Issue.Documents.Nodes, nil
}

// GetTeamDocuments returns an empty list since Linear API doesn't support team-level documents
// Documents can be attached to issues or projects, but not directly to teams
func (c *Client) GetTeamDocuments(ctx context.Context, teamID string) ([]Document, error) {
	return []Document{}, nil
}

// GetProjectDocuments fetches documents for a project
func (c *Client) GetProjectDocuments(ctx context.Context, projectID string) ([]Document, error) {
	var result struct {
		Documents struct {
			Nodes []Document `json:"nodes"`
		} `json:"documents"`
	}

	vars := map[string]any{
		"projectId": projectID,
	}

	err := c.query(ctx, queryProjectDocuments, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Documents.Nodes, nil
}

// CreateDocument creates a new document
func (c *Client) CreateDocument(ctx context.Context, input map[string]any) (*Document, error) {
	var result struct {
		DocumentCreate struct {
			Success  bool     `json:"success"`
			Document Document `json:"document"`
		} `json:"documentCreate"`
	}

	vars := map[string]any{
		"input": input,
	}

	err := c.query(ctx, mutationCreateDocument, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.DocumentCreate.Success {
		return nil, fmt.Errorf("document creation failed")
	}

	return &result.DocumentCreate.Document, nil
}

// UpdateDocument updates an existing document
func (c *Client) UpdateDocument(ctx context.Context, documentID string, input map[string]any) error {
	var result struct {
		DocumentUpdate struct {
			Success bool `json:"success"`
		} `json:"documentUpdate"`
	}

	vars := map[string]any{
		"id":    documentID,
		"input": input,
	}

	err := c.query(ctx, mutationUpdateDocument, vars, &result)
	if err != nil {
		return err
	}

	if !result.DocumentUpdate.Success {
		return fmt.Errorf("document update failed")
	}

	return nil
}

// DeleteDocument deletes a document
func (c *Client) DeleteDocument(ctx context.Context, documentID string) error {
	var result struct {
		DocumentDelete struct {
			Success bool `json:"success"`
		} `json:"documentDelete"`
	}

	vars := map[string]any{
		"id": documentID,
	}

	err := c.query(ctx, mutationDeleteDocument, vars, &result)
	if err != nil {
		return err
	}

	if !result.DocumentDelete.Success {
		return fmt.Errorf("document deletion failed")
	}

	return nil
}

// GetInitiatives fetches all initiatives
func (c *Client) GetInitiatives(ctx context.Context) ([]Initiative, error) {
	var result struct {
		Initiatives struct {
			Nodes []Initiative `json:"nodes"`
		} `json:"initiatives"`
	}

	err := c.query(ctx, queryInitiatives, nil, &result)
	if err != nil {
		return nil, err
	}

	return result.Initiatives.Nodes, nil
}
