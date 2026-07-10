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
	if issue.BranchName != "" {
		d.BranchName = &issue.BranchName
	}

	// Workflow timestamps
	d.StartedAt = issue.StartedAt
	d.CompletedAt = issue.CompletedAt
	d.CanceledAt = issue.CanceledAt
	d.ArchivedAt = issue.ArchivedAt

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

// DBStateToAPIState converts a db.State to api.State.
// Hydrate-then-overlay: see the reverse-conversion contract at
// DBMilestoneToAPIProjectMilestone.
func DBStateToAPIState(state State) api.State {
	var s api.State
	if len(state.Data) > 0 {
		// Best-effort: on a bad blob keep the zero struct and rely on the columns.
		_ = json.Unmarshal(state.Data, &s)
	}
	s.ID = state.ID
	s.Name = state.Name
	s.Type = state.Type
	return s
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

// APILabelToDBLabel converts an api.Label to UpsertLabelParams. The row's
// team_id comes from the label's own team — Linear's authoritative source —
// not the team whose sync pass is running: a nil team is a workspace-level
// label and stays team_id=NULL, so it no longer churns to whichever team
// synced it last (team.labels returns workspace labels mixed in, so the old
// caller-supplied teamID mis-stamped them).
func APILabelToDBLabel(label api.Label) (UpsertLabelParams, error) {
	data, err := json.Marshal(label)
	if err != nil {
		return UpsertLabelParams{}, err
	}
	teamID := ""
	if label.Team != nil {
		teamID = label.Team.ID
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

// DBLabelToAPILabel converts a db.Label to api.Label.
// Hydrate-then-overlay: see the reverse-conversion contract at
// DBMilestoneToAPIProjectMilestone. Team comes from the team_id column — the
// authoritative source (see APILabelToDBLabel) — never from the blob's copy,
// so a NULL column reads as a workspace label even if the blob disagrees.
func DBLabelToAPILabel(label Label) api.Label {
	var l api.Label
	if len(label.Data) > 0 {
		// Best-effort: on a bad blob keep the zero struct and rely on the columns.
		_ = json.Unmarshal(label.Data, &l)
	}
	l.ID = label.ID
	l.Name = label.Name
	l.Color = NullStringValue(label.Color)
	l.Description = NullStringValue(label.Description)
	if label.TeamID.Valid {
		l.Team = &api.Team{ID: label.TeamID.String}
	} else {
		l.Team = nil
	}
	return l
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
// ProjectLabel Conversion (workspace-scoped catalog; see CONTEXT.md
// "Project-label selection")
// =============================================================================

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// APIProjectLabelToDBProjectLabel converts an api.ProjectLabel to
// UpsertProjectLabelParams. parent_id comes from the label's own Parent edge
// (only the ID is fetched; names stitch locally at the repo read).
func APIProjectLabelToDBProjectLabel(label api.ProjectLabel) (UpsertProjectLabelParams, error) {
	data, err := json.Marshal(label)
	if err != nil {
		return UpsertProjectLabelParams{}, err
	}
	params := UpsertProjectLabelParams{
		ID:          label.ID,
		Name:        label.Name,
		Color:       sql.NullString{String: label.Color, Valid: label.Color != ""},
		Description: sql.NullString{String: label.Description, Valid: label.Description != ""},
		IsGroup:     boolToInt64(label.IsGroup),
		CreatedAt:   sql.NullTime{Time: label.CreatedAt, Valid: !label.CreatedAt.IsZero()},
		UpdatedAt:   sql.NullTime{Time: label.UpdatedAt, Valid: !label.UpdatedAt.IsZero()},
		SyncedAt:    Now(),
		Data:        data,
	}
	if label.Parent != nil {
		params.ParentID = sql.NullString{String: label.Parent.ID, Valid: label.Parent.ID != ""}
	}
	if label.RetiredAt != nil {
		params.RetiredAt = sql.NullTime{Time: *label.RetiredAt, Valid: true}
	}
	return params, nil
}

// DBProjectLabelToAPIProjectLabel converts a db.ProjectLabel to
// api.ProjectLabel. Hydrate-then-overlay: see the reverse-conversion contract
// at DBMilestoneToAPIProjectMilestone. Parent comes strictly from the
// parent_id column — the authoritative source — never from the blob's copy;
// only the ID is populated here (the repo read stitches Parent.Name over the
// catalog's id→row map).
func DBProjectLabelToAPIProjectLabel(label ProjectLabel) api.ProjectLabel {
	var l api.ProjectLabel
	if len(label.Data) > 0 {
		// Best-effort: on a bad blob keep the zero struct and rely on the columns.
		_ = json.Unmarshal(label.Data, &l)
	}
	l.ID = label.ID
	l.Name = label.Name
	l.Color = NullStringValue(label.Color)
	l.Description = NullStringValue(label.Description)
	l.IsGroup = label.IsGroup != 0
	if label.ParentID.Valid {
		l.Parent = &api.ProjectLabel{ID: label.ParentID.String}
	} else {
		l.Parent = nil
	}
	if label.RetiredAt.Valid {
		t := label.RetiredAt.Time
		l.RetiredAt = &t
	} else {
		l.RetiredAt = nil
	}
	if label.CreatedAt.Valid {
		l.CreatedAt = label.CreatedAt.Time
	}
	if label.UpdatedAt.Valid {
		l.UpdatedAt = label.UpdatedAt.Time
	}
	return l
}

// DBProjectLabelsToAPIProjectLabels converts a slice of db.ProjectLabel to
// api.ProjectLabel
func DBProjectLabelsToAPIProjectLabels(labels []ProjectLabel) []api.ProjectLabel {
	result := make([]api.ProjectLabel, len(labels))
	for i, label := range labels {
		result[i] = DBProjectLabelToAPIProjectLabel(label)
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

// DBUserToAPIUser converts a db.User to api.User.
// Hydrate-then-overlay: see the reverse-conversion contract at
// DBMilestoneToAPIProjectMilestone.
func DBUserToAPIUser(user User) api.User {
	var u api.User
	if len(user.Data) > 0 {
		// Best-effort: on a bad blob keep the zero struct and rely on the columns.
		_ = json.Unmarshal(user.Data, &u)
	}
	u.ID = user.ID
	u.Name = user.Name
	u.Email = user.Email
	u.DisplayName = NullStringValue(user.DisplayName)
	u.Active = user.Active == 1
	return u
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

// DBCycleToAPICycle converts a db.Cycle to api.Cycle.
// Hydrate-then-overlay: see the reverse-conversion contract at
// DBMilestoneToAPIProjectMilestone. Reading columns only — as this did before —
// dropped the JSON-only history arrays, so cycle.md rendered its progress
// figures (the last elements of those arrays) as a permanent 0/0.
func DBCycleToAPICycle(cycle Cycle) api.Cycle {
	var c api.Cycle
	if len(cycle.Data) > 0 {
		// Best-effort: on a bad blob keep the zero struct and rely on the columns.
		_ = json.Unmarshal(cycle.Data, &c)
	}
	c.ID = cycle.ID
	c.Number = int(cycle.Number)
	c.Name = NullStringValue(cycle.Name)
	c.StartsAt = cycle.StartsAt.Time
	c.EndsAt = cycle.EndsAt.Time
	return c
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
	if document.Initiative != nil {
		params.InitiativeID = sql.NullString{String: document.Initiative.ID, Valid: true}
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

// DBMilestoneToAPIProjectMilestone converts a db.ProjectMilestone to
// api.ProjectMilestone.
//
// THE REVERSE-CONVERSION CONTRACT (this comment is the canonical statement;
// State/Label/User/Cycle reference it): a reverse conversion starts from the
// `data` blob and overlays its queryable columns. The columns are the
// authoritative source; the blob is a best-effort enrichment that carries any
// api field without a column. Reading columns *only* silently drops such a
// JSON-only field, so a field added to the api struct would persist yet vanish
// on read (for Cycle this was a live bug — see DBCycleToAPICycle). Hydrating
// from `data` first, then overlaying the columns, closes that loss: a new api
// field flows through with zero converter edits. The entities whose blob IS
// the whole row (Issue, Project, Comment, …) pure-unmarshal and trivially
// satisfy the contract; EmbeddedFile is the excluded case (its table has no
// blob). Each converter is pinned by a Test*RoundTrip in convert_test.go.
//
// The overlay converters are deliberately more defensive than the
// pure-unmarshal ones (DBIssueToAPIIssue et al.), which propagate a parse
// error: a corrupt or empty `data` blob here falls back to the columns rather
// than failing, so one bad row cannot poison a whole listing (and a legacy
// "{}" row still reads correctly from its columns).
func DBMilestoneToAPIProjectMilestone(milestone ProjectMilestone) api.ProjectMilestone {
	var m api.ProjectMilestone
	if len(milestone.Data) > 0 {
		// Best-effort: on a bad blob keep the zero struct and rely on the columns.
		_ = json.Unmarshal(milestone.Data, &m)
	}
	m.ID = milestone.ID
	m.Name = milestone.Name
	m.Description = NullStringValue(milestone.Description)
	m.TargetDate = NullStringPtr(milestone.TargetDate)
	m.SortOrder = milestone.SortOrder.Float64
	return m
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
// IssueRelation Conversion
// =============================================================================

// IssueRelationUpsertParams builds the issue_relations row for a fetched
// relation. issueID is the owning side (the row's issue_id) and
// relatedIssueID the target: an outgoing fetch passes (thisIssue,
// rel.RelatedIssue.ID), an inverse fetch passes them the other way around
// (rel.Issue.ID, thisIssue) — the row is always stored from its owner's
// perspective, whichever end fetched it.
func IssueRelationUpsertParams(rel api.IssueRelation, issueID, relatedIssueID string) UpsertIssueRelationParams {
	return UpsertIssueRelationParams{
		ID:             rel.ID,
		IssueID:        issueID,
		RelatedIssueID: relatedIssueID,
		Type:           rel.Type,
		CreatedAt:      sql.NullTime{Time: rel.CreatedAt, Valid: !rel.CreatedAt.IsZero()},
		UpdatedAt:      sql.NullTime{Time: rel.UpdatedAt, Valid: !rel.UpdatedAt.IsZero()},
		SyncedAt:       Now(),
	}
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

// =============================================================================
// Attachment Conversion (external links: GitHub PRs, Slack, etc.)
// =============================================================================

// APIAttachmentToDBAttachment converts an api.Attachment to UpsertAttachmentParams
func APIAttachmentToDBAttachment(attachment api.Attachment, issueID string) (UpsertAttachmentParams, error) {
	data, err := json.Marshal(attachment)
	if err != nil {
		return UpsertAttachmentParams{}, err
	}
	metadata, _ := json.Marshal(attachment.Metadata)

	params := UpsertAttachmentParams{
		ID:         attachment.ID,
		IssueID:    issueID,
		Title:      attachment.Title,
		Subtitle:   sql.NullString{String: attachment.Subtitle, Valid: attachment.Subtitle != ""},
		Url:        attachment.URL,
		SourceType: sql.NullString{String: attachment.SourceType, Valid: attachment.SourceType != ""},
		Metadata:   metadata,
		CreatedAt:  sql.NullTime{Time: attachment.CreatedAt, Valid: !attachment.CreatedAt.IsZero()},
		UpdatedAt:  sql.NullTime{Time: attachment.UpdatedAt, Valid: !attachment.UpdatedAt.IsZero()},
		SyncedAt:   Now(),
		Data:       data,
	}
	if attachment.Creator != nil {
		params.CreatorID = sql.NullString{String: attachment.Creator.ID, Valid: true}
		params.CreatorName = sql.NullString{String: attachment.Creator.Name, Valid: true}
		params.CreatorEmail = sql.NullString{String: attachment.Creator.Email, Valid: true}
	}
	return params, nil
}

// DBAttachmentToAPIAttachment converts a db.Attachment to api.Attachment
func DBAttachmentToAPIAttachment(attachment Attachment) (api.Attachment, error) {
	var apiAttachment api.Attachment
	if err := json.Unmarshal(attachment.Data, &apiAttachment); err != nil {
		return api.Attachment{}, err
	}
	return apiAttachment, nil
}

// DBAttachmentsToAPIAttachments converts a slice of db.Attachment to api.Attachment
func DBAttachmentsToAPIAttachments(attachments []Attachment) ([]api.Attachment, error) {
	result := make([]api.Attachment, len(attachments))
	for i, attachment := range attachments {
		apiAttachment, err := DBAttachmentToAPIAttachment(attachment)
		if err != nil {
			return nil, err
		}
		result[i] = apiAttachment
	}
	return result, nil
}

// =============================================================================
// Entity External Link Conversion (project/initiative "Links / Resources")
// =============================================================================

// APIEntityExternalLinkToDB converts an api.EntityExternalLink to
// UpsertEntityExternalLinkParams. Exactly one of projectID/initiativeID is set
// (the other empty), matching the entity's polymorphic parent.
func APIEntityExternalLinkToDB(link api.EntityExternalLink, projectID, initiativeID string) (UpsertEntityExternalLinkParams, error) {
	data, err := json.Marshal(link)
	if err != nil {
		return UpsertEntityExternalLinkParams{}, err
	}

	params := UpsertEntityExternalLinkParams{
		ID:           link.ID,
		ProjectID:    sql.NullString{String: projectID, Valid: projectID != ""},
		InitiativeID: sql.NullString{String: initiativeID, Valid: initiativeID != ""},
		Label:        link.Label,
		Url:          link.URL,
		SortOrder:    sql.NullFloat64{Float64: link.SortOrder, Valid: true},
		CreatedAt:    sql.NullTime{Time: link.CreatedAt, Valid: !link.CreatedAt.IsZero()},
		UpdatedAt:    sql.NullTime{Time: link.UpdatedAt, Valid: !link.UpdatedAt.IsZero()},
		SyncedAt:     Now(),
		Data:         data,
	}
	if link.Creator != nil {
		params.CreatorID = sql.NullString{String: link.Creator.ID, Valid: true}
		params.CreatorName = sql.NullString{String: link.Creator.Name, Valid: true}
		params.CreatorEmail = sql.NullString{String: link.Creator.Email, Valid: true}
	}
	return params, nil
}

// DBEntityExternalLinkToAPI converts a db.EntityExternalLink to api.EntityExternalLink
func DBEntityExternalLinkToAPI(link EntityExternalLink) (api.EntityExternalLink, error) {
	var apiLink api.EntityExternalLink
	if err := json.Unmarshal(link.Data, &apiLink); err != nil {
		return api.EntityExternalLink{}, err
	}
	return apiLink, nil
}

// DBEntityExternalLinksToAPI converts a slice of db.EntityExternalLink to api.EntityExternalLink
func DBEntityExternalLinksToAPI(links []EntityExternalLink) ([]api.EntityExternalLink, error) {
	result := make([]api.EntityExternalLink, len(links))
	for i, link := range links {
		apiLink, err := DBEntityExternalLinkToAPI(link)
		if err != nil {
			return nil, err
		}
		result[i] = apiLink
	}
	return result, nil
}

// =============================================================================
// EmbeddedFile Conversion (images, PDFs from Linear CDN)
// =============================================================================

// DBEmbeddedFileToAPIFile converts a db.EmbeddedFile to api.EmbeddedFile
func DBEmbeddedFileToAPIFile(file EmbeddedFile) api.EmbeddedFile {
	return api.EmbeddedFile{
		ID:        file.ID,
		IssueID:   file.IssueID,
		URL:       file.Url,
		Filename:  file.Filename,
		MimeType:  NullStringValue(file.MimeType),
		FileSize:  file.FileSize.Int64,
		CachePath: NullStringValue(file.CachePath),
		Source:    file.Source,
		CreatedAt: file.CreatedAt,
		SyncedAt:  file.SyncedAt,
	}
}

// DBEmbeddedFilesToAPIFiles converts a slice of db.EmbeddedFile to api.EmbeddedFile
func DBEmbeddedFilesToAPIFiles(files []EmbeddedFile) []api.EmbeddedFile {
	result := make([]api.EmbeddedFile, len(files))
	for i, file := range files {
		result[i] = DBEmbeddedFileToAPIFile(file)
	}
	return result
}
