package testutil

import "time"

// Fixture functions return map[string]any for JSON encoding.
// This avoids import cycles with the api package.

// FixtureTeam returns a test team as a map.
func FixtureTeam() map[string]any {
	return map[string]any{
		"id":        "team-123",
		"key":       "TST",
		"name":      "Test Team",
		"icon":      "team",
		"createdAt": time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt": time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
}

// FixtureUser returns a test user as a map.
func FixtureUser() map[string]any {
	return map[string]any{
		"id":          "user-123",
		"name":        "Test User",
		"email":       "test@example.com",
		"displayName": "Test User",
		"active":      true,
	}
}

// FixtureState returns a test workflow state as a map.
func FixtureState(stateType string) map[string]any {
	names := map[string]string{
		"backlog":   "Backlog",
		"unstarted": "Todo",
		"started":   "In Progress",
		"completed": "Done",
		"canceled":  "Canceled",
	}
	name := names[stateType]
	if name == "" {
		name = "Todo"
		stateType = "unstarted"
	}
	return map[string]any{
		"id":   "state-" + stateType,
		"name": name,
		"type": stateType,
	}
}

// FixtureLabel returns a test label as a map.
func FixtureLabel(name string) map[string]any {
	return map[string]any{
		"id":          "label-" + name,
		"name":        name,
		"color":       "#ff0000",
		"description": "Test label " + name,
	}
}

// FixtureIssue returns a fully populated test issue as a map.
func FixtureIssue() map[string]any {
	return map[string]any{
		"id":          "issue-123",
		"identifier":  "TST-123",
		"title":       "Test Issue",
		"description": "This is a test issue description",
		"state":       FixtureState("started"),
		"assignee":    FixtureUser(),
		"priority":    2,
		"labels": map[string]any{
			"nodes": []map[string]any{
				FixtureLabel("Bug"),
				FixtureLabel("Backend"),
			},
		},
		"dueDate":   "2024-12-31",
		"estimate":  3.0,
		"createdAt": time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt": time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"url":       "https://linear.app/test/issue/TST-123",
		"team":      FixtureTeam(),
		"project":   nil,
		"parent":    nil,
		"children":  map[string]any{"nodes": []any{}},
		"cycle":     nil,
	}
}

// FixtureIssueMinimal returns an issue with only required fields.
func FixtureIssueMinimal() map[string]any {
	return map[string]any{
		"id":          "issue-456",
		"identifier":  "TST-456",
		"title":       "Minimal Issue",
		"description": "",
		"state":       FixtureState("unstarted"),
		"assignee":    nil,
		"priority":    0,
		"labels":      map[string]any{"nodes": []any{}},
		"dueDate":     nil,
		"estimate":    nil,
		"createdAt":   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt":   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"url":         "https://linear.app/test/issue/TST-456",
		"team":        nil,
		"project":     nil,
		"parent":      nil,
		"children":    map[string]any{"nodes": []any{}},
		"cycle":       nil,
	}
}

// FixtureComment returns a test comment as a map.
func FixtureComment() map[string]any {
	return map[string]any{
		"id":        "comment-123",
		"body":      "This is a test comment",
		"createdAt": time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt": time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"editedAt":  nil,
		"user":      FixtureUser(),
	}
}

// FixtureDocument returns a test document as a map.
func FixtureDocument() map[string]any {
	return map[string]any{
		"id":        "doc-123",
		"title":     "Test Document",
		"content":   "# Test Document\n\nThis is test content.",
		"slugId":    "test-document",
		"url":       "https://linear.app/test/document/test-document",
		"icon":      "",
		"color":     "",
		"createdAt": time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt": time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"creator":   FixtureUser(),
	}
}

// FixtureProject returns a test project as a map.
func FixtureProject() map[string]any {
	return map[string]any{
		"id":          "project-123",
		"name":        "Test Project",
		"slugId":      "test-project",
		"description": "A test project",
		"url":         "https://linear.app/test/project/test-project",
		"state":       "started",
		"startDate":   "2024-01-01",
		"targetDate":  "2024-06-30",
		"createdAt":   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt":   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"lead":        FixtureUser(),
	}
}

// FixtureInitiative returns a test initiative as a map.
func FixtureInitiative() map[string]any {
	return map[string]any{
		"id":          "initiative-123",
		"name":        "Test Initiative",
		"slugId":      "test-initiative",
		"description": "A test initiative",
		"status":      "active",
		"color":       "#0000ff",
		"targetDate":  "2024-12-31",
		"url":         "https://linear.app/test/initiative/test-initiative",
		"createdAt":   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt":   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"owner":       FixtureUser(),
		"projects": map[string]any{
			"nodes": []map[string]any{
				{"id": "project-123", "name": "Test Project", "slugId": "test-project"},
			},
		},
	}
}

// FixtureCycle returns a test cycle as a map.
func FixtureCycle() map[string]any {
	return map[string]any{
		"id":       "cycle-123",
		"number":   42,
		"name":     "Sprint 42",
		"startsAt": time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"endsAt":   time.Date(2024, 1, 14, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
}

// FixtureProjectUpdate returns a test project update as a map.
func FixtureProjectUpdate() map[string]any {
	return map[string]any{
		"id":        "update-123",
		"body":      "Sprint completed successfully",
		"health":    "onTrack",
		"createdAt": time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt": time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"user":      FixtureUser(),
	}
}

// Response builders for mock server

// TeamsResponse returns a response structure for GetTeams.
func TeamsResponse(teams ...map[string]any) map[string]any {
	if len(teams) == 0 {
		teams = []map[string]any{FixtureTeam()}
	}
	return map[string]any{
		"teams": map[string]any{
			"nodes": teams,
		},
	}
}

// IssueResponse returns a response structure for GetIssue.
func IssueResponse(issue map[string]any) map[string]any {
	return map[string]any{
		"issue": issue,
	}
}

// TeamIssuesResponse returns a response structure for GetTeamIssues.
func TeamIssuesResponse(issues ...map[string]any) map[string]any {
	return map[string]any{
		"team": map[string]any{
			"issues": map[string]any{
				"pageInfo": map[string]any{
					"hasNextPage": false,
					"endCursor":   "",
				},
				"nodes": issues,
			},
		},
	}
}

// UpdateIssueResponse returns a response for UpdateIssue mutation.
func UpdateIssueResponse(success bool) map[string]any {
	return map[string]any{
		"issueUpdate": map[string]any{
			"success": success,
		},
	}
}

// CreateCommentResponse returns a response for CreateComment mutation.
func CreateCommentResponse(comment map[string]any) map[string]any {
	return map[string]any{
		"commentCreate": map[string]any{
			"success": true,
			"comment": comment,
		},
	}
}

// TeamStatesResponse returns a response for GetTeamStates.
func TeamStatesResponse(states ...map[string]any) map[string]any {
	if len(states) == 0 {
		states = []map[string]any{
			FixtureState("backlog"),
			FixtureState("unstarted"),
			FixtureState("started"),
			FixtureState("completed"),
			FixtureState("canceled"),
		}
	}
	return map[string]any{
		"team": map[string]any{
			"states": map[string]any{
				"nodes": states,
			},
		},
	}
}

// TeamLabelsResponse returns a response for GetTeamLabels.
func TeamLabelsResponse(labels ...map[string]any) map[string]any {
	return map[string]any{
		"team": map[string]any{
			"labels": map[string]any{
				"nodes": labels,
			},
		},
		"issueLabels": map[string]any{
			"nodes": []map[string]any{},
		},
	}
}

// UsersResponse returns a response for GetUsers.
func UsersResponse(users ...map[string]any) map[string]any {
	if len(users) == 0 {
		users = []map[string]any{FixtureUser()}
	}
	return map[string]any{
		"users": map[string]any{
			"nodes": users,
		},
	}
}

// IssueCommentsResponse returns a response for GetIssueComments.
func IssueCommentsResponse(comments ...map[string]any) map[string]any {
	return map[string]any{
		"issue": map[string]any{
			"comments": map[string]any{
				"nodes": comments,
			},
		},
	}
}

// IssueDocumentsResponse returns a response for GetIssueDocuments.
func IssueDocumentsResponse(docs ...map[string]any) map[string]any {
	return map[string]any{
		"issue": map[string]any{
			"documents": map[string]any{
				"nodes": docs,
			},
		},
	}
}
