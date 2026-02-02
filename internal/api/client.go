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
	"strconv"
	"strings"
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
	stats      *APIStats
}

// ClientOptions configures the API client.
type ClientOptions struct {
	APIStatsEnabled bool // Enable periodic stats logging
}

func NewClient(apiKey string) *Client {
	return NewClientWithOptions(apiKey, ClientOptions{})
}

func NewClientWithOptions(apiKey string, opts ClientOptions) *Client {
	// Linear allows 1,500 requests/hour (25/min).
	// Burst of 50 handles cold cache scenarios; rate of 2/sec for sustained use.
	limiter := rate.NewLimiter(rate.Limit(2), 50)

	return &Client{
		apiKey:     apiKey,
		apiURL:     defaultAPIURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		limiter:    limiter,
		stats:      NewAPIStats(opts.APIStatsEnabled),
	}
}

// Close shuts down the client and stops any background goroutines.
func (c *Client) Close() {
	if c.stats != nil {
		c.stats.Close()
	}
}

// AuthHeader returns the Authorization header value for API requests.
func (c *Client) AuthHeader() string {
	return c.apiKey
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
	// Extract operation name for stats and logging
	opName := extractOpName(query)
	if debugAPI {
		log.Printf("[API] Calling %s vars=%v", opName, variables)
	}

	// Log token bucket exhaustion before blocking
	if tokens := c.limiter.Tokens(); tokens <= 0 {
		log.Printf("[ratelimit] token bucket empty, %s will block until tokens replenish", opName)
	}

	// Verbose debug: log every wait >1ms
	if debugRateLimit {
		reservation := c.limiter.Reserve()
		delay := reservation.Delay()
		if delay > time.Millisecond {
			log.Printf("[ratelimit] debug: %s reservation delay %v", opName, delay)
		}
		reservation.Cancel()
	}

	rateLimitStart := time.Now()
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait cancelled: %w", err)
	}
	rateLimitWait := time.Since(rateLimitStart)
	if rateLimitWait > time.Millisecond {
		c.stats.RecordRateLimitWait(rateLimitWait)
	}
	// Always log noisy rate limit waits (no env var required)
	if rateLimitWait > 100*time.Millisecond {
		hourly := c.stats.HourlyCount()
		pct := float64(hourly) / float64(linearHourlyLimit) * 100
		log.Printf("[ratelimit] %s waited %s (budget: %d/%d this hour, %.0f%% used)",
			opName, rateLimitWait.Round(time.Millisecond), hourly, linearHourlyLimit, pct)
	}

	// Track request duration for stats
	reqStart := time.Now()
	var queryErr error
	defer func() {
		c.stats.Record(opName, time.Since(reqStart), queryErr)
	}()

	reqBody := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		queryErr = fmt.Errorf("failed to marshal request: %w", err)
		return queryErr
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(body))
	if err != nil {
		queryErr = fmt.Errorf("failed to create request: %w", err)
		return queryErr
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		queryErr = fmt.Errorf("failed to execute request: %w", err)
		return queryErr
	}
	defer resp.Body.Close()

	// Check server-side rate limit headers
	c.checkRateLimitHeaders(resp, opName)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		queryErr = fmt.Errorf("failed to read response: %w", err)
		return queryErr
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		queryErr = fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
		log.Printf("[ratelimit] ERROR: %s rate limited by Linear API (HTTP 429): %s", opName, string(respBody))
		return queryErr
	}

	if resp.StatusCode != http.StatusOK {
		queryErr = fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
		return queryErr
	}

	var gqlResp graphQLResponse
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		queryErr = fmt.Errorf("failed to parse response: %w", err)
		return queryErr
	}

	if len(gqlResp.Errors) > 0 {
		errMsg := gqlResp.Errors[0].Message
		queryErr = fmt.Errorf("GraphQL error: %s", errMsg)
		if strings.Contains(errMsg, "RATELIMITED") || strings.Contains(strings.ToLower(errMsg), "rate limit") {
			log.Printf("[ratelimit] ERROR: %s rate limited by Linear API: %s", opName, errMsg)
		}
		return queryErr
	}

	if err := json.Unmarshal(gqlResp.Data, result); err != nil {
		queryErr = fmt.Errorf("failed to parse data: %w", err)
		return queryErr
	}

	return nil
}

// checkRateLimitHeaders logs warnings when Linear's rate limit headers indicate low remaining budget.
func (c *Client) checkRateLimitHeaders(resp *http.Response, opName string) {
	remaining := resp.Header.Get("X-RateLimit-Requests-Remaining")
	limit := resp.Header.Get("X-RateLimit-Requests-Limit")

	if remaining == "" {
		return
	}

	rem, err := strconv.Atoi(remaining)
	if err != nil {
		return
	}

	lim := linearHourlyLimit
	if limit != "" {
		if parsed, err := strconv.Atoi(limit); err == nil {
			lim = parsed
		}
	}

	// Warn when below 20% of limit
	if lim > 0 && float64(rem)/float64(lim) < 0.20 {
		log.Printf("[ratelimit] Linear API: %d/%d requests remaining this hour (after %s)", rem, lim, opName)
	}
}

// Stats returns the client's API stats tracker for external inspection.
func (c *Client) Stats() *APIStats {
	return c.stats
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

// GetTeamMetadata fetches all metadata for a team in a single query:
// states, labels (team + workspace, deduplicated), cycles, projects (with milestones), and members.
func (c *Client) GetTeamMetadata(ctx context.Context, teamID string) (*TeamMetadata, error) {
	var result struct {
		Team struct {
			States struct {
				Nodes []State `json:"nodes"`
			} `json:"states"`
			Labels struct {
				Nodes []Label `json:"nodes"`
			} `json:"labels"`
			Cycles struct {
				Nodes []Cycle `json:"nodes"`
			} `json:"cycles"`
			Projects struct {
				Nodes []Project `json:"nodes"`
			} `json:"projects"`
			Members struct {
				Nodes []User `json:"nodes"`
			} `json:"members"`
		} `json:"team"`
		IssueLabels struct {
			Nodes []Label `json:"nodes"`
		} `json:"issueLabels"`
	}

	vars := map[string]any{
		"teamId": teamID,
	}

	err := c.query(ctx, queryTeamMetadata, vars, &result)
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

	return &TeamMetadata{
		States:   result.Team.States.Nodes,
		Labels:   labels,
		Cycles:   result.Team.Cycles.Nodes,
		Projects: result.Team.Projects.Nodes,
		Members:  result.Team.Members.Nodes,
	}, nil
}

// GetWorkspace fetches workspace-level entities (users and initiatives) in a single query.
func (c *Client) GetWorkspace(ctx context.Context) (*WorkspaceData, error) {
	var result struct {
		Users struct {
			Nodes []User `json:"nodes"`
		} `json:"users"`
		Initiatives struct {
			Nodes []Initiative `json:"nodes"`
		} `json:"initiatives"`
	}

	err := c.query(ctx, queryWorkspace, nil, &result)
	if err != nil {
		return nil, err
	}

	return &WorkspaceData{
		Users:       result.Users.Nodes,
		Initiatives: result.Initiatives.Nodes,
	}, nil
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

// CreateProjectMilestone creates a new milestone for a project
func (c *Client) CreateProjectMilestone(ctx context.Context, projectID, name, description string) (*ProjectMilestone, error) {
	var result struct {
		ProjectMilestoneCreate struct {
			Success          bool             `json:"success"`
			ProjectMilestone ProjectMilestone `json:"projectMilestone"`
		} `json:"projectMilestoneCreate"`
	}

	vars := map[string]any{
		"projectId": projectID,
		"name":      name,
	}
	if description != "" {
		vars["description"] = description
	}

	err := c.query(ctx, mutationCreateProjectMilestone, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.ProjectMilestoneCreate.Success {
		return nil, fmt.Errorf("milestone creation failed")
	}

	return &result.ProjectMilestoneCreate.ProjectMilestone, nil
}

// UpdateProjectMilestone updates an existing milestone
func (c *Client) UpdateProjectMilestone(ctx context.Context, milestoneID string, input ProjectMilestoneUpdateInput) (*ProjectMilestone, error) {
	var result struct {
		ProjectMilestoneUpdate struct {
			Success          bool             `json:"success"`
			ProjectMilestone ProjectMilestone `json:"projectMilestone"`
		} `json:"projectMilestoneUpdate"`
	}

	vars := map[string]any{
		"id":    milestoneID,
		"input": input,
	}

	err := c.query(ctx, mutationUpdateProjectMilestone, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.ProjectMilestoneUpdate.Success {
		return nil, fmt.Errorf("milestone update failed")
	}

	return &result.ProjectMilestoneUpdate.ProjectMilestone, nil
}

// DeleteProjectMilestone deletes a milestone
func (c *Client) DeleteProjectMilestone(ctx context.Context, milestoneID string) error {
	var result struct {
		ProjectMilestoneDelete struct {
			Success bool `json:"success"`
		} `json:"projectMilestoneDelete"`
	}

	vars := map[string]any{
		"id": milestoneID,
	}

	err := c.query(ctx, mutationDeleteProjectMilestone, vars, &result)
	if err != nil {
		return err
	}

	if !result.ProjectMilestoneDelete.Success {
		return fmt.Errorf("milestone deletion failed")
	}

	return nil
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

// GetViewer fetches the currently authenticated user
func (c *Client) GetViewer(ctx context.Context) (*User, error) {
	var result struct {
		Viewer User `json:"viewer"`
	}

	err := c.query(ctx, queryViewer, nil, &result)
	if err != nil {
		return nil, err
	}

	return &result.Viewer, nil
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

// IssueDetails contains comments, documents, and attachments for an issue
type IssueDetails struct {
	Comments    []Comment
	Documents   []Document
	Attachments []Attachment
}

// GetIssueDetails fetches comments, documents, and attachments for an issue in a single query
func (c *Client) GetIssueDetails(ctx context.Context, issueID string) (*IssueDetails, error) {
	var result struct {
		Issue struct {
			Comments struct {
				Nodes []Comment `json:"nodes"`
			} `json:"comments"`
			Documents struct {
				Nodes []Document `json:"nodes"`
			} `json:"documents"`
			Attachments struct {
				Nodes []Attachment `json:"nodes"`
			} `json:"attachments"`
		} `json:"issue"`
	}

	vars := map[string]any{
		"issueId": issueID,
	}

	err := c.query(ctx, queryIssueDetails, vars, &result)
	if err != nil {
		return nil, err
	}

	return &IssueDetails{
		Comments:    result.Issue.Comments.Nodes,
		Documents:   result.Issue.Documents.Nodes,
		Attachments: result.Issue.Attachments.Nodes,
	}, nil
}

// GetIssueDetailsBatch fetches comments, documents, and attachments for multiple issues in a single query.
// Returns a map of issueID -> IssueDetails. Uses GraphQL aliases to batch requests.
func (c *Client) GetIssueDetailsBatch(ctx context.Context, issueIDs []string) (map[string]*IssueDetails, error) {
	if len(issueIDs) == 0 {
		return make(map[string]*IssueDetails), nil
	}

	// Build a batched query using aliases
	// Example: query { i0: issue(id: "id1") { ... } i1: issue(id: "id2") { ... } }
	var queryParts []string
	vars := make(map[string]any)

	for i, id := range issueIDs {
		alias := fmt.Sprintf("i%d", i)
		varName := fmt.Sprintf("id%d", i)
		queryParts = append(queryParts, fmt.Sprintf(`%s: issue(id: $%s) {
			comments(first: 100) { nodes { ...CommentFields } }
			documents(first: 100) { nodes { ...DocumentFields } }
			attachments(first: 100) { nodes { ...AttachmentFields } }
		}`, alias, varName))
		vars[varName] = id
	}

	// Build variable declarations
	var varDecls []string
	for i := range issueIDs {
		varDecls = append(varDecls, fmt.Sprintf("$id%d: String!", i))
	}

	query := fmt.Sprintf(`query IssueDetailsBatch(%s) { %s } %s %s %s`,
		strings.Join(varDecls, ", "),
		strings.Join(queryParts, " "),
		CommentFieldsFragment,
		DocumentFieldsFragment,
		AttachmentFieldsFragment,
	)

	// Result will be a map of alias -> issue data
	var rawResult map[string]json.RawMessage
	err := c.query(ctx, query, vars, &rawResult)
	if err != nil {
		return nil, err
	}

	// Parse each aliased result
	result := make(map[string]*IssueDetails, len(issueIDs))
	for i, id := range issueIDs {
		alias := fmt.Sprintf("i%d", i)
		raw, ok := rawResult[alias]
		if !ok {
			continue
		}

		var issueData struct {
			Comments struct {
				Nodes []Comment `json:"nodes"`
			} `json:"comments"`
			Documents struct {
				Nodes []Document `json:"nodes"`
			} `json:"documents"`
			Attachments struct {
				Nodes []Attachment `json:"nodes"`
			} `json:"attachments"`
		}
		if err := json.Unmarshal(raw, &issueData); err != nil {
			continue
		}

		result[id] = &IssueDetails{
			Comments:    issueData.Comments.Nodes,
			Documents:   issueData.Documents.Nodes,
			Attachments: issueData.Attachments.Nodes,
		}
	}

	return result, nil
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

// GetIssueAttachments fetches attachments (external links) for an issue
func (c *Client) GetIssueAttachments(ctx context.Context, issueID string) ([]Attachment, error) {
	var result struct {
		Issue struct {
			Attachments struct {
				Nodes []Attachment `json:"nodes"`
			} `json:"attachments"`
		} `json:"issue"`
	}

	vars := map[string]any{
		"issueId": issueID,
	}

	err := c.query(ctx, queryIssueAttachments, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Issue.Attachments.Nodes, nil
}

// GetIssueHistory fetches the history/audit trail for an issue
func (c *Client) GetIssueHistory(ctx context.Context, issueID string) ([]IssueHistoryEntry, error) {
	var result struct {
		Issue struct {
			History struct {
				Nodes []IssueHistoryEntry `json:"nodes"`
			} `json:"history"`
		} `json:"issue"`
	}

	vars := map[string]any{
		"issueId": issueID,
	}

	err := c.query(ctx, queryIssueHistory, vars, &result)
	if err != nil {
		return nil, err
	}

	return result.Issue.History.Nodes, nil
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

// GetInitiativeDocuments fetches documents for an initiative
func (c *Client) GetInitiativeDocuments(ctx context.Context, initiativeID string) ([]Document, error) {
	var result struct {
		Documents struct {
			Nodes []Document `json:"nodes"`
		} `json:"documents"`
	}

	vars := map[string]any{
		"initiativeId": initiativeID,
	}

	err := c.query(ctx, queryInitiativeDocuments, vars, &result)
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
func (c *Client) UpdateDocument(ctx context.Context, documentID string, input map[string]any) (*Document, error) {
	var result struct {
		DocumentUpdate struct {
			Success  bool     `json:"success"`
			Document Document `json:"document"`
		} `json:"documentUpdate"`
	}

	vars := map[string]any{
		"id":    documentID,
		"input": input,
	}

	err := c.query(ctx, mutationUpdateDocument, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.DocumentUpdate.Success {
		return nil, fmt.Errorf("document update failed")
	}

	return &result.DocumentUpdate.Document, nil
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

// =============================================================================
// Issue Relations
// =============================================================================

// CreateIssueRelation creates a relation between two issues
// relationType must be one of: blocks, duplicate, related, similar
func (c *Client) CreateIssueRelation(ctx context.Context, issueID, relatedIssueID, relationType string) (*IssueRelation, error) {
	var result struct {
		IssueRelationCreate struct {
			Success       bool          `json:"success"`
			IssueRelation IssueRelation `json:"issueRelation"`
		} `json:"issueRelationCreate"`
	}

	vars := map[string]any{
		"issueId":        issueID,
		"relatedIssueId": relatedIssueID,
		"type":           relationType,
	}

	err := c.query(ctx, mutationCreateIssueRelation, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.IssueRelationCreate.Success {
		return nil, fmt.Errorf("issue relation creation failed")
	}

	return &result.IssueRelationCreate.IssueRelation, nil
}

// DeleteIssueRelation deletes an issue relation
func (c *Client) DeleteIssueRelation(ctx context.Context, relationID string) error {
	var result struct {
		IssueRelationDelete struct {
			Success bool `json:"success"`
		} `json:"issueRelationDelete"`
	}

	vars := map[string]any{
		"id": relationID,
	}

	err := c.query(ctx, mutationDeleteIssueRelation, vars, &result)
	if err != nil {
		return err
	}

	if !result.IssueRelationDelete.Success {
		return fmt.Errorf("issue relation deletion failed")
	}

	return nil
}

// =============================================================================
// Attachments Create/Link
// =============================================================================

// CreateAttachment creates a generic attachment (external link) on an issue
func (c *Client) CreateAttachment(ctx context.Context, issueID, title, url, subtitle string) (*Attachment, error) {
	var result struct {
		AttachmentCreate struct {
			Success    bool       `json:"success"`
			Attachment Attachment `json:"attachment"`
		} `json:"attachmentCreate"`
	}

	vars := map[string]any{
		"issueId": issueID,
		"title":   title,
		"url":     url,
	}
	if subtitle != "" {
		vars["subtitle"] = subtitle
	}

	err := c.query(ctx, mutationCreateAttachment, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.AttachmentCreate.Success {
		return nil, fmt.Errorf("attachment creation failed")
	}

	return &result.AttachmentCreate.Attachment, nil
}

// LinkURL creates an attachment by linking a URL (Linear auto-detects type)
func (c *Client) LinkURL(ctx context.Context, issueID, url, title string) (*Attachment, error) {
	var result struct {
		AttachmentLinkURL struct {
			Success    bool       `json:"success"`
			Attachment Attachment `json:"attachment"`
		} `json:"attachmentLinkURL"`
	}

	vars := map[string]any{
		"issueId": issueID,
		"url":     url,
	}
	if title != "" {
		vars["title"] = title
	}

	err := c.query(ctx, mutationLinkURL, vars, &result)
	if err != nil {
		return nil, err
	}

	if !result.AttachmentLinkURL.Success {
		return nil, fmt.Errorf("URL linking failed")
	}

	return &result.AttachmentLinkURL.Attachment, nil
}

// DeleteAttachment deletes an attachment
func (c *Client) DeleteAttachment(ctx context.Context, attachmentID string) error {
	var result struct {
		AttachmentDelete struct {
			Success bool `json:"success"`
		} `json:"attachmentDelete"`
	}

	vars := map[string]any{
		"id": attachmentID,
	}

	err := c.query(ctx, mutationDeleteAttachment, vars, &result)
	if err != nil {
		return err
	}

	if !result.AttachmentDelete.Success {
		return fmt.Errorf("attachment deletion failed")
	}

	return nil
}
