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

// TeamsResponse returns a response structure for GetTeams. The pageInfo is
// required: GetTeams drains the connection, and fetchAll errors on a
// response missing pageInfo.
func TeamsResponse(teams ...map[string]any) map[string]any {
	if len(teams) == 0 {
		teams = []map[string]any{FixtureTeam()}
	}
	return map[string]any{
		"teams": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
			"nodes":    teams,
		},
	}
}

// IssueResponse returns a response structure for GetIssue.
func IssueResponse(issue map[string]any) map[string]any {
	return map[string]any{
		"issue": issue,
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

// FilteredIssuesResponse returns a response for filtered issue queries (status, label, assignee, unassigned).
func FilteredIssuesResponse(issues ...map[string]any) map[string]any {
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

// ArchiveIssueResponse returns a response for ArchiveIssue mutation.
func ArchiveIssueResponse(success bool) map[string]any {
	return map[string]any{
		"issueArchive": map[string]any{
			"success": success,
		},
	}
}

// CreateIssueResponse returns a response for CreateIssue mutation.
func CreateIssueResponse(issue map[string]any) map[string]any {
	return map[string]any{
		"issueCreate": map[string]any{
			"success": true,
			"issue":   issue,
		},
	}
}

// TeamProjectsResponse returns a response for GetTeamProjects. The
// pageInfo is required: connections without it fail the paginate module's
// silent-truncation guard.
func TeamProjectsResponse(projects ...map[string]any) map[string]any {
	return map[string]any{
		"team": map[string]any{
			"projects": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes":    projects,
			},
		},
	}
}

// ProjectUpdatesResponse returns a response for GetProjectUpdates. The
// pageInfo is required: the updates connection is drained (fetchAll).
func ProjectUpdatesResponse(updates ...map[string]any) map[string]any {
	return map[string]any{
		"project": map[string]any{
			"projectUpdates": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes":    updates,
			},
		},
	}
}

// CreateProjectUpdateResponse returns a response for CreateProjectUpdate mutation.
func CreateProjectUpdateResponse(update map[string]any) map[string]any {
	return map[string]any{
		"projectUpdateCreate": map[string]any{
			"success":       true,
			"projectUpdate": update,
		},
	}
}

// FixtureCycleIssue returns a minimal cycle issue as a map.
func FixtureCycleIssue() map[string]any {
	return map[string]any{
		"id":         "issue-cycle-123",
		"identifier": "TST-789",
		"title":      "Cycle Issue",
		"state":      FixtureState("started"),
	}
}

// CreateLabelResponse returns a response for CreateLabel mutation.
func CreateLabelResponse(label map[string]any) map[string]any {
	return map[string]any{
		"issueLabelCreate": map[string]any{
			"success":    true,
			"issueLabel": label,
		},
	}
}

// UpdateLabelResponse returns a response for UpdateLabel mutation.
func UpdateLabelResponse(label map[string]any) map[string]any {
	return map[string]any{
		"issueLabelUpdate": map[string]any{
			"success":    true,
			"issueLabel": label,
		},
	}
}

// DeleteLabelResponse returns a response for DeleteLabel mutation.
func DeleteLabelResponse(success bool) map[string]any {
	return map[string]any{
		"issueLabelDelete": map[string]any{
			"success": success,
		},
	}
}

// UpdateCommentResponse returns a response for UpdateComment mutation.
func UpdateCommentResponse(comment map[string]any) map[string]any {
	return map[string]any{
		"commentUpdate": map[string]any{
			"success": true,
			"comment": comment,
		},
	}
}

// DeleteCommentResponse returns a response for DeleteComment mutation.
func DeleteCommentResponse(success bool) map[string]any {
	return map[string]any{
		"commentDelete": map[string]any{
			"success": success,
		},
	}
}

// CreateDocumentResponse returns a response for CreateDocument mutation.
func CreateDocumentResponse(doc map[string]any) map[string]any {
	return map[string]any{
		"documentCreate": map[string]any{
			"success":  true,
			"document": doc,
		},
	}
}

// UpdateDocumentResponse returns a response for UpdateDocument mutation.
func UpdateDocumentResponse(doc map[string]any) map[string]any {
	return map[string]any{
		"documentUpdate": map[string]any{
			"success":  true,
			"document": doc,
		},
	}
}

// DeleteDocumentResponse returns a response for DeleteDocument mutation.
func DeleteDocumentResponse(success bool) map[string]any {
	return map[string]any{
		"documentDelete": map[string]any{
			"success": success,
		},
	}
}

// FixtureInitiativeUpdate returns a test initiative update as a map.
func FixtureInitiativeUpdate() map[string]any {
	return map[string]any{
		"id":        "init-update-123",
		"body":      "Initiative on track",
		"health":    "onTrack",
		"createdAt": time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt": time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"user":      FixtureUser(),
	}
}

// InitiativeUpdatesResponse returns a response for GetInitiativeUpdates. The
// pageInfo is required: the updates connection is drained (fetchAll).
func InitiativeUpdatesResponse(updates ...map[string]any) map[string]any {
	return map[string]any{
		"initiative": map[string]any{
			"initiativeUpdates": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes":    updates,
			},
		},
	}
}

// CreateInitiativeUpdateResponse returns a response for CreateInitiativeUpdate mutation.
func CreateInitiativeUpdateResponse(update map[string]any) map[string]any {
	return map[string]any{
		"initiativeUpdateCreate": map[string]any{
			"success":          true,
			"initiativeUpdate": update,
		},
	}
}

// ProjectDocumentsResponse returns a response for GetProjectDocuments. The
// pageInfo is required: the documents connection is drained (fetchAll).
func ProjectDocumentsResponse(docs ...map[string]any) map[string]any {
	return map[string]any{
		"documents": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
			"nodes":    docs,
		},
	}
}
