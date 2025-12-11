package linear

import (
	"fmt"
)

// ListIssues retrieves all issues from Linear
func (c *Client) ListIssues() ([]Issue, error) {
	query := `
		query {
			issues {
				nodes {
					id
					identifier
					title
					description
					priority
					createdAt
					updatedAt
					state {
						id
						name
						type
					}
					assignee {
						id
						name
						email
					}
					creator {
						id
						name
						email
					}
					team {
						id
						key
						name
					}
					labels {
						nodes {
							id
							name
						}
					}
				}
				pageInfo {
					hasNextPage
					endCursor
				}
			}
		}
	`

	var resp IssuesResponse
	if err := c.Query(query, nil, &resp); err != nil {
		return nil, fmt.Errorf("failed to list issues: %w", err)
	}

	return resp.Issues.Nodes, nil
}

// GetIssue retrieves a single issue by ID
func (c *Client) GetIssue(id string) (*Issue, error) {
	query := `
		query($id: String!) {
			issue(id: $id) {
				id
				identifier
				title
				description
				priority
				createdAt
				updatedAt
				state {
					id
					name
					type
				}
				assignee {
					id
					name
					email
				}
				creator {
					id
					name
					email
				}
				team {
					id
					key
					name
				}
				labels {
					nodes {
						id
						name
					}
				}
			}
		}
	`

	variables := map[string]interface{}{
		"id": id,
	}

	var resp IssueResponse
	if err := c.Query(query, variables, &resp); err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}

	return &resp.Issue, nil
}

// UpdateIssue updates an issue in Linear
func (c *Client) UpdateIssue(id string, input map[string]interface{}) (*Issue, error) {
	query := `
		mutation($id: String!, $input: IssueUpdateInput!) {
			issueUpdate(id: $id, input: $input) {
				success
				issue {
					id
					identifier
					title
					description
					priority
					createdAt
					updatedAt
					state {
						id
						name
						type
					}
					assignee {
						id
						name
						email
					}
					creator {
						id
						name
						email
					}
					team {
						id
						key
						name
					}
					labels {
						nodes {
							id
							name
						}
					}
				}
			}
		}
	`

	variables := map[string]interface{}{
		"id":    id,
		"input": input,
	}

	var resp UpdateIssueResponse
	if err := c.Query(query, variables, &resp); err != nil {
		return nil, fmt.Errorf("failed to update issue: %w", err)
	}

	if !resp.IssueUpdate.Success {
		return nil, fmt.Errorf("issue update was not successful")
	}

	return &resp.IssueUpdate.Issue, nil
}

// CreateIssue creates a new issue in Linear
func (c *Client) CreateIssue(input map[string]interface{}) (*Issue, error) {
	// If no team is specified, get the first team from the user's teams
	if _, hasTeam := input["teamId"]; !hasTeam {
		teams, err := c.GetTeams()
		if err != nil {
			return nil, fmt.Errorf("failed to get teams: %w", err)
		}
		if len(teams) == 0 {
			return nil, fmt.Errorf("no teams available")
		}
		input["teamId"] = teams[0].ID
	}

	query := `
		mutation($input: IssueCreateInput!) {
			issueCreate(input: $input) {
				success
				issue {
					id
					identifier
					title
					description
					priority
					createdAt
					updatedAt
					state {
						id
						name
						type
					}
					assignee {
						id
						name
						email
					}
					creator {
						id
						name
						email
					}
					team {
						id
						key
						name
					}
					labels {
						nodes {
							id
							name
						}
					}
				}
			}
		}
	`

	variables := map[string]interface{}{
		"input": input,
	}

	var resp CreateIssueResponse
	if err := c.Query(query, variables, &resp); err != nil {
		return nil, fmt.Errorf("failed to create issue: %w", err)
	}

	if !resp.IssueCreate.Success {
		return nil, fmt.Errorf("issue creation was not successful")
	}

	return &resp.IssueCreate.Issue, nil
}

// GetTeams retrieves all teams from Linear
func (c *Client) GetTeams() ([]Team, error) {
	query := `
		query {
			teams {
				nodes {
					id
					key
					name
				}
			}
		}
	`

	var resp struct {
		Teams struct {
			Nodes []Team `json:"nodes"`
		} `json:"teams"`
	}

	if err := c.Query(query, nil, &resp); err != nil {
		return nil, fmt.Errorf("failed to get teams: %w", err)
	}

	return resp.Teams.Nodes, nil
}
