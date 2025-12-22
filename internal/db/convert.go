package db

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// APIIssueToDBIssue converts an api.Issue to db.IssueData for upserting
func APIIssueToDBIssue(issue api.Issue) (*IssueData, error) {
	// Serialize the full issue to JSON for the data column
	data, err := json.Marshal(issue)
	if err != nil {
		return nil, err
	}

	d := &IssueData{
		ID:         issue.ID,
		Identifier: issue.Identifier,
		Title:      issue.Title,
		Priority:   issue.Priority,
		CreatedAt:  issue.CreatedAt,
		UpdatedAt:  issue.UpdatedAt,
		Data:       data,
	}

	// Team
	if issue.Team != nil {
		d.TeamID = issue.Team.ID
	}

	// Description (empty string -> nil)
	if issue.Description != "" {
		d.Description = &issue.Description
	}

	// State
	if issue.State.ID != "" {
		d.StateID = &issue.State.ID
		d.StateName = &issue.State.Name
		d.StateType = &issue.State.Type
	}

	// Assignee
	if issue.Assignee != nil {
		d.AssigneeID = &issue.Assignee.ID
		d.AssigneeEmail = &issue.Assignee.Email
	}

	// Project
	if issue.Project != nil {
		d.ProjectID = &issue.Project.ID
		d.ProjectName = &issue.Project.Name
	}

	// Cycle
	if issue.Cycle != nil {
		d.CycleID = &issue.Cycle.ID
		d.CycleName = &issue.Cycle.Name
	}

	// Parent
	if issue.Parent != nil {
		d.ParentID = &issue.Parent.ID
	}

	// Optional fields
	d.DueDate = issue.DueDate
	d.Estimate = issue.Estimate
	if issue.URL != "" {
		d.URL = &issue.URL
	}

	return d, nil
}

// DBIssueToAPIIssue converts a db.Issue back to api.Issue
func DBIssueToAPIIssue(issue Issue) (api.Issue, error) {
	var apiIssue api.Issue
	if err := json.Unmarshal(issue.Data, &apiIssue); err != nil {
		return api.Issue{}, err
	}
	return apiIssue, nil
}

// DBIssuesToAPIIssues converts a slice of db.Issue to api.Issue
func DBIssuesToAPIIssues(issues []Issue) ([]api.Issue, error) {
	result := make([]api.Issue, len(issues))
	for i, issue := range issues {
		apiIssue, err := DBIssueToAPIIssue(issue)
		if err != nil {
			return nil, err
		}
		result[i] = apiIssue
	}
	return result, nil
}

// APITeamToDBTeam converts an api.Team to db.UpsertTeamParams
func APITeamToDBTeam(team api.Team) UpsertTeamParams {
	return UpsertTeamParams{
		ID:   team.ID,
		Key:  team.Key,
		Name: team.Name,
		Icon: sql.NullString{String: team.Icon, Valid: team.Icon != ""},
		CreatedAt: sql.NullTime{
			Time:  team.CreatedAt,
			Valid: !team.CreatedAt.IsZero(),
		},
		UpdatedAt: sql.NullTime{
			Time:  team.UpdatedAt,
			Valid: !team.UpdatedAt.IsZero(),
		},
		SyncedAt: time.Now(),
	}
}

// DBTeamToAPITeam converts a db.Team to api.Team
func DBTeamToAPITeam(team Team) api.Team {
	return api.Team{
		ID:        team.ID,
		Key:       team.Key,
		Name:      team.Name,
		Icon:      team.Icon.String,
		CreatedAt: team.CreatedAt.Time,
		UpdatedAt: team.UpdatedAt.Time,
	}
}

// DBTeamsToAPITeams converts a slice of db.Team to api.Team
func DBTeamsToAPITeams(teams []Team) []api.Team {
	result := make([]api.Team, len(teams))
	for i, team := range teams {
		result[i] = DBTeamToAPITeam(team)
	}
	return result
}

// NullStringValue returns the string value or empty string if null
func NullStringValue(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// NullStringPtr returns a pointer to the string or nil if null
func NullStringPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}
