package db

import (
	"database/sql"
	"encoding/json"

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

	// Creator
	if issue.Creator != nil {
		d.CreatorID = &issue.Creator.ID
		d.CreatorEmail = &issue.Creator.Email
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
		SyncedAt: Now(),
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

// =============================================================================
// State Conversion
// =============================================================================

// APIStateToDBState converts an api.State to UpsertStateParams
func APIStateToDBState(state api.State, teamID string) (UpsertStateParams, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return UpsertStateParams{}, err
	}
	return UpsertStateParams{
		ID:       state.ID,
		TeamID:   teamID,
		Name:     state.Name,
		Type:     state.Type,
		SyncedAt: Now(),
		Data:     data,
	}, nil
}

// DBStateToAPIState converts a db.State to api.State
func DBStateToAPIState(state State) api.State {
	return api.State{
		ID:   state.ID,
		Name: state.Name,
		Type: state.Type,
	}
}

// DBStatesToAPIStates converts a slice of db.State to api.State
func DBStatesToAPIStates(states []State) []api.State {
	result := make([]api.State, len(states))
	for i, state := range states {
		result[i] = DBStateToAPIState(state)
	}
	return result
}

// =============================================================================
// Label Conversion
// =============================================================================

// APILabelToDBLabel converts an api.Label to UpsertLabelParams
func APILabelToDBLabel(label api.Label, teamID string) (UpsertLabelParams, error) {
	data, err := json.Marshal(label)
	if err != nil {
		return UpsertLabelParams{}, err
	}
	return UpsertLabelParams{
		ID:          label.ID,
		TeamID:      sql.NullString{String: teamID, Valid: teamID != ""},
		Name:        label.Name,
		Color:       sql.NullString{String: label.Color, Valid: label.Color != ""},
		Description: sql.NullString{String: label.Description, Valid: label.Description != ""},
		SyncedAt:    Now(),
		Data:        data,
	}, nil
}

// DBLabelToAPILabel converts a db.Label to api.Label
func DBLabelToAPILabel(label Label) api.Label {
	return api.Label{
		ID:          label.ID,
		Name:        label.Name,
		Color:       NullStringValue(label.Color),
		Description: NullStringValue(label.Description),
	}
}

// DBLabelsToAPILabels converts a slice of db.Label to api.Label
func DBLabelsToAPILabels(labels []Label) []api.Label {
	result := make([]api.Label, len(labels))
	for i, label := range labels {
		result[i] = DBLabelToAPILabel(label)
	}
	return result
}

// =============================================================================
// User Conversion
// =============================================================================

// APIUserToDBUser converts an api.User to UpsertUserParams
func APIUserToDBUser(user api.User) (UpsertUserParams, error) {
	data, err := json.Marshal(user)
	if err != nil {
		return UpsertUserParams{}, err
	}
	active := int64(0)
	if user.Active {
		active = 1
	}
	return UpsertUserParams{
		ID:          user.ID,
		Email:       user.Email,
		Name:        user.Name,
		DisplayName: sql.NullString{String: user.DisplayName, Valid: user.DisplayName != ""},
		Active:      active,
		SyncedAt:    Now(),
		Data:        data,
	}, nil
}

// DBUserToAPIUser converts a db.User to api.User
func DBUserToAPIUser(user User) api.User {
	return api.User{
		ID:          user.ID,
		Name:        user.Name,
		Email:       user.Email,
		DisplayName: NullStringValue(user.DisplayName),
		Active:      user.Active == 1,
	}
}

// DBUsersToAPIUsers converts a slice of db.User to api.User
func DBUsersToAPIUsers(users []User) []api.User {
	result := make([]api.User, len(users))
	for i, user := range users {
		result[i] = DBUserToAPIUser(user)
	}
	return result
}

// =============================================================================
// Cycle Conversion
// =============================================================================

// APICycleToDBCycle converts an api.Cycle to UpsertCycleParams
func APICycleToDBCycle(cycle api.Cycle, teamID string) (UpsertCycleParams, error) {
	data, err := json.Marshal(cycle)
	if err != nil {
		return UpsertCycleParams{}, err
	}
	return UpsertCycleParams{
		ID:       cycle.ID,
		TeamID:   teamID,
		Number:   int64(cycle.Number),
		Name:     sql.NullString{String: cycle.Name, Valid: cycle.Name != ""},
		StartsAt: sql.NullTime{Time: cycle.StartsAt, Valid: !cycle.StartsAt.IsZero()},
		EndsAt:   sql.NullTime{Time: cycle.EndsAt, Valid: !cycle.EndsAt.IsZero()},
		SyncedAt: Now(),
		Data:     data,
	}, nil
}

// DBCycleToAPICycle converts a db.Cycle to api.Cycle
func DBCycleToAPICycle(cycle Cycle) api.Cycle {
	return api.Cycle{
		ID:       cycle.ID,
		Number:   int(cycle.Number),
		Name:     NullStringValue(cycle.Name),
		StartsAt: cycle.StartsAt.Time,
		EndsAt:   cycle.EndsAt.Time,
	}
}

// DBCyclesToAPICycles converts a slice of db.Cycle to api.Cycle
func DBCyclesToAPICycles(cycles []Cycle) []api.Cycle {
	result := make([]api.Cycle, len(cycles))
	for i, cycle := range cycles {
		result[i] = DBCycleToAPICycle(cycle)
	}
	return result
}

// =============================================================================
// Project Conversion
// =============================================================================

// APIProjectToDBProject converts an api.Project to UpsertProjectParams
func APIProjectToDBProject(project api.Project) (UpsertProjectParams, error) {
	data, err := json.Marshal(project)
	if err != nil {
		return UpsertProjectParams{}, err
	}
	params := UpsertProjectParams{
		ID:          project.ID,
		SlugID:      project.Slug,
		Name:        project.Name,
		Description: sql.NullString{String: project.Description, Valid: project.Description != ""},
		State:       sql.NullString{String: project.State, Valid: project.State != ""},
		Url:         sql.NullString{String: project.URL, Valid: project.URL != ""},
		CreatedAt:   sql.NullTime{Time: project.CreatedAt, Valid: !project.CreatedAt.IsZero()},
		UpdatedAt:   sql.NullTime{Time: project.UpdatedAt, Valid: !project.UpdatedAt.IsZero()},
		SyncedAt:    Now(),
		Data:        data,
	}
	if project.StartDate != nil {
		params.StartDate = sql.NullString{String: *project.StartDate, Valid: true}
	}
	if project.TargetDate != nil {
		params.TargetDate = sql.NullString{String: *project.TargetDate, Valid: true}
	}
	if project.Lead != nil {
		params.LeadID = sql.NullString{String: project.Lead.ID, Valid: true}
	}
	return params, nil
}

// DBProjectToAPIProject converts a db.Project to api.Project
func DBProjectToAPIProject(project Project) (api.Project, error) {
	var apiProject api.Project
	if err := json.Unmarshal(project.Data, &apiProject); err != nil {
		return api.Project{}, err
	}
	return apiProject, nil
}

// DBProjectsToAPIProjects converts a slice of db.Project to api.Project
func DBProjectsToAPIProjects(projects []Project) ([]api.Project, error) {
	result := make([]api.Project, len(projects))
	for i, project := range projects {
		apiProject, err := DBProjectToAPIProject(project)
		if err != nil {
			return nil, err
		}
		result[i] = apiProject
	}
	return result, nil
}

// =============================================================================
// Comment Conversion
// =============================================================================

// APICommentToDBComment converts an api.Comment to UpsertCommentParams
func APICommentToDBComment(comment api.Comment, issueID string) (UpsertCommentParams, error) {
	data, err := json.Marshal(comment)
	if err != nil {
		return UpsertCommentParams{}, err
	}
	params := UpsertCommentParams{
		ID:        comment.ID,
		IssueID:   issueID,
		Body:      comment.Body,
		CreatedAt: comment.CreatedAt,
		UpdatedAt: comment.UpdatedAt,
		SyncedAt:  Now(),
		Data:      data,
	}
	if comment.User != nil {
		params.UserID = sql.NullString{String: comment.User.ID, Valid: true}
		params.UserName = sql.NullString{String: comment.User.Name, Valid: true}
		params.UserEmail = sql.NullString{String: comment.User.Email, Valid: true}
	}
	if comment.EditedAt != nil {
		params.EditedAt = sql.NullTime{Time: *comment.EditedAt, Valid: true}
	}
	return params, nil
}

// DBCommentToAPIComment converts a db.Comment to api.Comment
func DBCommentToAPIComment(comment Comment) (api.Comment, error) {
	var apiComment api.Comment
	if err := json.Unmarshal(comment.Data, &apiComment); err != nil {
		return api.Comment{}, err
	}
	return apiComment, nil
}

// DBCommentsToAPIComments converts a slice of db.Comment to api.Comment
func DBCommentsToAPIComments(comments []Comment) ([]api.Comment, error) {
	result := make([]api.Comment, len(comments))
	for i, comment := range comments {
		apiComment, err := DBCommentToAPIComment(comment)
		if err != nil {
			return nil, err
		}
		result[i] = apiComment
	}
	return result, nil
}

// =============================================================================
// Document Conversion
// =============================================================================

// APIDocumentToDBDocument converts an api.Document to UpsertDocumentParams
func APIDocumentToDBDocument(document api.Document) (UpsertDocumentParams, error) {
	data, err := json.Marshal(document)
	if err != nil {
		return UpsertDocumentParams{}, err
	}
	params := UpsertDocumentParams{
		ID:        document.ID,
		SlugID:    document.SlugID,
		Title:     document.Title,
		Icon:      sql.NullString{String: document.Icon, Valid: document.Icon != ""},
		Color:     sql.NullString{String: document.Color, Valid: document.Color != ""},
		Content:   sql.NullString{String: document.Content, Valid: document.Content != ""},
		Url:       sql.NullString{String: document.URL, Valid: document.URL != ""},
		CreatedAt: sql.NullTime{Time: document.CreatedAt, Valid: !document.CreatedAt.IsZero()},
		UpdatedAt: sql.NullTime{Time: document.UpdatedAt, Valid: !document.UpdatedAt.IsZero()},
		SyncedAt:  Now(),
		Data:      data,
	}
	if document.Issue != nil {
		params.IssueID = sql.NullString{String: document.Issue.ID, Valid: true}
	}
	if document.Project != nil {
		params.ProjectID = sql.NullString{String: document.Project.ID, Valid: true}
	}
	if document.Creator != nil {
		params.CreatorID = sql.NullString{String: document.Creator.ID, Valid: true}
	}
	return params, nil
}

// DBDocumentToAPIDocument converts a db.Document to api.Document
func DBDocumentToAPIDocument(doc Document) (api.Document, error) {
	var apiDoc api.Document
	if err := json.Unmarshal(doc.Data, &apiDoc); err != nil {
		return api.Document{}, err
	}
	return apiDoc, nil
}

// DBDocumentsToAPIDocuments converts a slice of db.Document to api.Document
func DBDocumentsToAPIDocuments(docs []Document) ([]api.Document, error) {
	result := make([]api.Document, len(docs))
	for i, doc := range docs {
		apiDoc, err := DBDocumentToAPIDocument(doc)
		if err != nil {
			return nil, err
		}
		result[i] = apiDoc
	}
	return result, nil
}

// =============================================================================
// Initiative Conversion
// =============================================================================

// APIInitiativeToDBInitiative converts an api.Initiative to UpsertInitiativeParams
func APIInitiativeToDBInitiative(initiative api.Initiative) (UpsertInitiativeParams, error) {
	data, err := json.Marshal(initiative)
	if err != nil {
		return UpsertInitiativeParams{}, err
	}
	params := UpsertInitiativeParams{
		ID:          initiative.ID,
		SlugID:      initiative.Slug,
		Name:        initiative.Name,
		Description: sql.NullString{String: initiative.Description, Valid: initiative.Description != ""},
		Icon:        sql.NullString{String: initiative.Icon, Valid: initiative.Icon != ""},
		Color:       sql.NullString{String: initiative.Color, Valid: initiative.Color != ""},
		Status:      sql.NullString{String: initiative.Status, Valid: initiative.Status != ""},
		Url:         sql.NullString{String: initiative.URL, Valid: initiative.URL != ""},
		CreatedAt:   sql.NullTime{Time: initiative.CreatedAt, Valid: !initiative.CreatedAt.IsZero()},
		UpdatedAt:   sql.NullTime{Time: initiative.UpdatedAt, Valid: !initiative.UpdatedAt.IsZero()},
		SyncedAt:    Now(),
		Data:        data,
	}
	if initiative.TargetDate != nil {
		params.TargetDate = sql.NullString{String: *initiative.TargetDate, Valid: true}
	}
	if initiative.Owner != nil {
		params.OwnerID = sql.NullString{String: initiative.Owner.ID, Valid: true}
	}
	return params, nil
}

// DBInitiativeToAPIInitiative converts a db.Initiative to api.Initiative
func DBInitiativeToAPIInitiative(initiative Initiative) (api.Initiative, error) {
	var apiInitiative api.Initiative
	if err := json.Unmarshal(initiative.Data, &apiInitiative); err != nil {
		return api.Initiative{}, err
	}
	return apiInitiative, nil
}

// DBInitiativesToAPIInitiatives converts a slice of db.Initiative to api.Initiative
func DBInitiativesToAPIInitiatives(initiatives []Initiative) ([]api.Initiative, error) {
	result := make([]api.Initiative, len(initiatives))
	for i, initiative := range initiatives {
		apiInitiative, err := DBInitiativeToAPIInitiative(initiative)
		if err != nil {
			return nil, err
		}
		result[i] = apiInitiative
	}
	return result, nil
}

// =============================================================================
// ProjectMilestone Conversion
// =============================================================================

// APIProjectMilestoneToDBMilestone converts an api.ProjectMilestone to UpsertProjectMilestoneParams
func APIProjectMilestoneToDBMilestone(milestone api.ProjectMilestone, projectID string) (UpsertProjectMilestoneParams, error) {
	data, err := json.Marshal(milestone)
	if err != nil {
		return UpsertProjectMilestoneParams{}, err
	}
	params := UpsertProjectMilestoneParams{
		ID:          milestone.ID,
		ProjectID:   projectID,
		Name:        milestone.Name,
		Description: sql.NullString{String: milestone.Description, Valid: milestone.Description != ""},
		SortOrder:   sql.NullFloat64{Float64: milestone.SortOrder, Valid: true},
		SyncedAt:    Now(),
		Data:        data,
	}
	if milestone.TargetDate != nil {
		params.TargetDate = sql.NullString{String: *milestone.TargetDate, Valid: true}
	}
	return params, nil
}

// DBMilestoneToAPIProjectMilestone converts a db.ProjectMilestone to api.ProjectMilestone
func DBMilestoneToAPIProjectMilestone(milestone ProjectMilestone) api.ProjectMilestone {
	return api.ProjectMilestone{
		ID:          milestone.ID,
		Name:        milestone.Name,
		Description: NullStringValue(milestone.Description),
		TargetDate:  NullStringPtr(milestone.TargetDate),
		SortOrder:   milestone.SortOrder.Float64,
	}
}

// DBMilestonesToAPIProjectMilestones converts a slice of db.ProjectMilestone to api.ProjectMilestone
func DBMilestonesToAPIProjectMilestones(milestones []ProjectMilestone) []api.ProjectMilestone {
	result := make([]api.ProjectMilestone, len(milestones))
	for i, milestone := range milestones {
		result[i] = DBMilestoneToAPIProjectMilestone(milestone)
	}
	return result
}

// =============================================================================
// ProjectUpdate Conversion
// =============================================================================

// APIProjectUpdateToDBUpdate converts an api.ProjectUpdate to UpsertProjectUpdateParams
func APIProjectUpdateToDBUpdate(update api.ProjectUpdate, projectID string) (UpsertProjectUpdateParams, error) {
	data, err := json.Marshal(update)
	if err != nil {
		return UpsertProjectUpdateParams{}, err
	}
	params := UpsertProjectUpdateParams{
		ID:        update.ID,
		ProjectID: projectID,
		Body:      update.Body,
		Health:    sql.NullString{String: update.Health, Valid: update.Health != ""},
		CreatedAt: update.CreatedAt,
		UpdatedAt: update.UpdatedAt,
		SyncedAt:  Now(),
		Data:      data,
	}
	if update.User != nil {
		params.UserID = sql.NullString{String: update.User.ID, Valid: true}
		params.UserName = sql.NullString{String: update.User.Name, Valid: true}
	}
	return params, nil
}

// DBProjectUpdateToAPIUpdate converts a db.ProjectUpdate to api.ProjectUpdate
func DBProjectUpdateToAPIUpdate(update ProjectUpdate) (api.ProjectUpdate, error) {
	var apiUpdate api.ProjectUpdate
	if err := json.Unmarshal(update.Data, &apiUpdate); err != nil {
		return api.ProjectUpdate{}, err
	}
	return apiUpdate, nil
}

// DBProjectUpdatesToAPIUpdates converts a slice of db.ProjectUpdate to api.ProjectUpdate
func DBProjectUpdatesToAPIUpdates(updates []ProjectUpdate) ([]api.ProjectUpdate, error) {
	result := make([]api.ProjectUpdate, len(updates))
	for i, update := range updates {
		apiUpdate, err := DBProjectUpdateToAPIUpdate(update)
		if err != nil {
			return nil, err
		}
		result[i] = apiUpdate
	}
	return result, nil
}

// =============================================================================
// InitiativeUpdate Conversion
// =============================================================================

// APIInitiativeUpdateToDBUpdate converts an api.InitiativeUpdate to UpsertInitiativeUpdateParams
func APIInitiativeUpdateToDBUpdate(update api.InitiativeUpdate, initiativeID string) (UpsertInitiativeUpdateParams, error) {
	data, err := json.Marshal(update)
	if err != nil {
		return UpsertInitiativeUpdateParams{}, err
	}
	params := UpsertInitiativeUpdateParams{
		ID:           update.ID,
		InitiativeID: initiativeID,
		Body:         update.Body,
		Health:       sql.NullString{String: update.Health, Valid: update.Health != ""},
		CreatedAt:    update.CreatedAt,
		UpdatedAt:    update.UpdatedAt,
		SyncedAt:     Now(),
		Data:         data,
	}
	if update.User != nil {
		params.UserID = sql.NullString{String: update.User.ID, Valid: true}
		params.UserName = sql.NullString{String: update.User.Name, Valid: true}
	}
	return params, nil
}

// DBInitiativeUpdateToAPIUpdate converts a db.InitiativeUpdate to api.InitiativeUpdate
func DBInitiativeUpdateToAPIUpdate(update InitiativeUpdate) (api.InitiativeUpdate, error) {
	var apiUpdate api.InitiativeUpdate
	if err := json.Unmarshal(update.Data, &apiUpdate); err != nil {
		return api.InitiativeUpdate{}, err
	}
	return apiUpdate, nil
}

// DBInitiativeUpdatesToAPIUpdates converts a slice of db.InitiativeUpdate to api.InitiativeUpdate
func DBInitiativeUpdatesToAPIUpdates(updates []InitiativeUpdate) ([]api.InitiativeUpdate, error) {
	result := make([]api.InitiativeUpdate, len(updates))
	for i, update := range updates {
		apiUpdate, err := DBInitiativeUpdateToAPIUpdate(update)
		if err != nil {
			return nil, err
		}
		result[i] = apiUpdate
	}
	return result, nil
}
