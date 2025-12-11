package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const linearAPIURL = "https://api.linear.app/graphql"

type Client struct {
	apiKey     string
	httpClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
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
	reqBody := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", linearAPIURL, bytes.NewReader(body))
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
	var result struct {
		Viewer struct {
			AssignedIssues struct {
				Nodes []Issue `json:"nodes"`
			} `json:"assignedIssues"`
		} `json:"viewer"`
	}

	err := c.query(ctx, queryMyIssues, nil, &result)
	if err != nil {
		return nil, err
	}

	return result.Viewer.AssignedIssues.Nodes, nil
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
