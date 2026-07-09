package fixtures

import (
	"fmt"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// API type fixtures return fully populated api.* types.
// Use these for testing Repository and LinearFS methods.

var (
	// Base timestamp for fixtures
	fixtureTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
)

// FixtureAPITeam returns a test team.
func FixtureAPITeam() api.Team {
	return api.Team{
		ID:        "team-1",
		Key:       "TST",
		Name:      "Test Team",
		Icon:      "team",
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureTime,
	}
}

// FixtureAPITeams returns multiple test teams.
func FixtureAPITeams() []api.Team {
	return []api.Team{
		{ID: "team-1", Key: "TST", Name: "Test Team", Icon: "team", CreatedAt: fixtureTime, UpdatedAt: fixtureTime},
		{ID: "team-2", Key: "ENG", Name: "Engineering", Icon: "code", CreatedAt: fixtureTime, UpdatedAt: fixtureTime},
	}
}

// FixtureAPIState returns a test workflow state.
func FixtureAPIState(stateType string) api.State {
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
	return api.State{
		ID:   "state-" + stateType,
		Name: name,
		Type: stateType,
	}
}

// FixtureAPIStates returns all standard workflow states.
func FixtureAPIStates() []api.State {
	return []api.State{
		{ID: "state-backlog", Name: "Backlog", Type: "backlog"},
		{ID: "state-unstarted", Name: "Todo", Type: "unstarted"},
		{ID: "state-started", Name: "In Progress", Type: "started"},
		{ID: "state-completed", Name: "Done", Type: "completed"},
		{ID: "state-canceled", Name: "Canceled", Type: "canceled"},
	}
}

// FixtureAPILabel returns a test label with the given name.
func FixtureAPILabel(name string) api.Label {
	return api.Label{
		ID:          "label-" + name,
		Name:        name,
		Color:       "#ff0000",
		Description: "Test label " + name,
	}
}

// FixtureAPILabels returns a set of test labels.
func FixtureAPILabels() []api.Label {
	return []api.Label{
		{ID: "label-bug", Name: "Bug", Color: "#ff0000", Description: "Bug label"},
		{ID: "label-feature", Name: "Feature", Color: "#00ff00", Description: "Feature label"},
		{ID: "label-docs", Name: "Documentation", Color: "#0000ff", Description: "Documentation label"},
	}
}

// FixtureAPIProjectLabels returns a mini workspace project-label catalog
// exercising the group/retired lifecycle: group "Area" with children
// "Backend"/"Frontend", standalone "Ops", and retired "Legacy". The live
// workspace has no groups or retired labels, so tests must synthesize them.
func FixtureAPIProjectLabels() []api.ProjectLabel {
	retired := fixtureTime
	area := &api.ProjectLabel{ID: "plabel-area", Name: "Area"}
	return []api.ProjectLabel{
		{ID: "plabel-area", Name: "Area", IsGroup: true, CreatedAt: fixtureTime, UpdatedAt: fixtureTime},
		{ID: "plabel-backend", Name: "Backend", Color: "#5e6ad2", Parent: area, CreatedAt: fixtureTime, UpdatedAt: fixtureTime},
		{ID: "plabel-frontend", Name: "Frontend", Color: "#f2994a", Parent: area, CreatedAt: fixtureTime, UpdatedAt: fixtureTime},
		{ID: "plabel-ops", Name: "Ops", Color: "#4cb782", CreatedAt: fixtureTime, UpdatedAt: fixtureTime},
		{ID: "plabel-legacy", Name: "Legacy", Color: "#95a2b3", RetiredAt: &retired, CreatedAt: fixtureTime, UpdatedAt: fixtureTime},
	}
}

// FixtureAPIUser returns a test user.
func FixtureAPIUser() api.User {
	return api.User{
		ID:          "user-1",
		Name:        "Test User",
		Email:       "test@example.com",
		DisplayName: "Test User",
		Active:      true,
	}
}

// FixtureAPIUsers returns multiple test users.
func FixtureAPIUsers() []api.User {
	return []api.User{
		{ID: "user-1", Name: "Test User", Email: "test@example.com", DisplayName: "Test User", Active: true},
		{ID: "user-2", Name: "Jane Dev", Email: "jane@example.com", DisplayName: "Jane", Active: true},
		{ID: "user-3", Name: "Bob PM", Email: "bob@example.com", DisplayName: "Bob", Active: true},
	}
}

// IssueOption is a functional option for customizing FixtureAPIIssue.
type IssueOption func(*api.Issue)

// WithIssueID sets the issue ID and identifier.
func WithIssueID(id string, identifier string) IssueOption {
	return func(i *api.Issue) {
		i.ID = id
		i.Identifier = identifier
	}
}

// WithTitle sets the issue title.
func WithTitle(title string) IssueOption {
	return func(i *api.Issue) {
		i.Title = title
	}
}

// WithDescription sets the issue description.
func WithDescription(desc string) IssueOption {
	return func(i *api.Issue) {
		i.Description = desc
	}
}

// WithState sets the issue state.
func WithState(state api.State) IssueOption {
	return func(i *api.Issue) {
		i.State = state
	}
}

// WithUpdatedAt sets the issue's updatedAt (drives recent/ ordering and mtime).
func WithUpdatedAt(t time.Time) IssueOption {
	return func(i *api.Issue) {
		i.UpdatedAt = t
	}
}

// WithCreatedAt sets the issue's createdAt (ctime).
func WithCreatedAt(t time.Time) IssueOption {
	return func(i *api.Issue) {
		i.CreatedAt = t
	}
}

// WithAssignee sets the issue assignee.
func WithAssignee(user *api.User) IssueOption {
	return func(i *api.Issue) {
		i.Assignee = user
	}
}

// WithPriority sets the issue priority.
func WithPriority(p int) IssueOption {
	return func(i *api.Issue) {
		i.Priority = p
	}
}

// WithLabels sets the issue labels.
func WithLabels(labels ...api.Label) IssueOption {
	return func(i *api.Issue) {
		i.Labels = api.Labels{Nodes: labels}
	}
}

// WithTeam sets the issue team.
func WithTeam(team *api.Team) IssueOption {
	return func(i *api.Issue) {
		i.Team = team
	}
}

// WithProject sets the issue project.
func WithProject(project *api.Project) IssueOption {
	return func(i *api.Issue) {
		i.Project = project
	}
}

// WithCycle sets the issue cycle.
func WithCycle(cycle *api.IssueCycle) IssueOption {
	return func(i *api.Issue) {
		i.Cycle = cycle
	}
}

// WithParent sets the issue parent.
func WithParent(parent *api.ParentIssue) IssueOption {
	return func(i *api.Issue) {
		i.Parent = parent
	}
}

// FixtureAPIIssue returns a test issue with optional customization.
func FixtureAPIIssue(opts ...IssueOption) api.Issue {
	team := FixtureAPITeam()
	user := FixtureAPIUser()
	issue := api.Issue{
		ID:          "issue-1",
		Identifier:  "TST-1",
		Title:       "Test Issue",
		Description: "This is a test issue description",
		State:       FixtureAPIState("started"),
		Assignee:    &user,
		Priority:    2,
		Labels:      api.Labels{Nodes: []api.Label{FixtureAPILabel("Bug")}},
		CreatedAt:   fixtureTime,
		UpdatedAt:   fixtureTime.Add(24 * time.Hour),
		URL:         "https://linear.app/test/issue/TST-1",
		Team:        &team,
		Children:    api.ChildIssues{Nodes: []api.ChildIssue{}},
	}

	for _, opt := range opts {
		opt(&issue)
	}

	return issue
}

// FixtureAPIIssues returns n test issues with sequential IDs.
func FixtureAPIIssues(n int) []api.Issue {
	issues := make([]api.Issue, n)
	team := FixtureAPITeam()
	for i := 0; i < n; i++ {
		issues[i] = FixtureAPIIssue(
			WithIssueID(fmt.Sprintf("issue-%d", i+1), fmt.Sprintf("TST-%d", i+1)),
			WithTitle(fmt.Sprintf("Test Issue %d", i+1)),
			WithTeam(&team),
		)
	}
	return issues
}

// FixtureAPIComment returns a test comment.
func FixtureAPIComment() api.Comment {
	user := FixtureAPIUser()
	return api.Comment{
		ID:        "comment-1",
		Body:      "This is a test comment",
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureTime,
		User:      &user,
	}
}

// FixtureAPIComments returns n test comments with sequential IDs.
func FixtureAPIComments(n int) []api.Comment {
	comments := make([]api.Comment, n)
	user := FixtureAPIUser()
	for i := 0; i < n; i++ {
		comments[i] = api.Comment{
			ID:        fmt.Sprintf("comment-%d", i+1),
			Body:      fmt.Sprintf("Test comment %d", i+1),
			CreatedAt: fixtureTime.Add(time.Duration(i) * time.Hour),
			UpdatedAt: fixtureTime.Add(time.Duration(i) * time.Hour),
			User:      &user,
		}
	}
	return comments
}

// FixtureAPIDocument returns a test document.
func FixtureAPIDocument() api.Document {
	user := FixtureAPIUser()
	return api.Document{
		ID:        "doc-1",
		Title:     "Test Document",
		Content:   "# Test Document\n\nThis is test content.",
		SlugID:    "test-document",
		URL:       "https://linear.app/test/document/test-document",
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureTime,
		Creator:   &user,
	}
}

// FixtureAPIDocuments returns n test documents with sequential IDs.
func FixtureAPIDocuments(n int) []api.Document {
	docs := make([]api.Document, n)
	user := FixtureAPIUser()
	for i := 0; i < n; i++ {
		docs[i] = api.Document{
			ID:        fmt.Sprintf("doc-%d", i+1),
			Title:     fmt.Sprintf("Test Document %d", i+1),
			Content:   fmt.Sprintf("# Test Document %d\n\nThis is test content for document %d.", i+1, i+1),
			SlugID:    fmt.Sprintf("test-document-%d", i+1),
			URL:       fmt.Sprintf("https://linear.app/test/document/test-document-%d", i+1),
			CreatedAt: fixtureTime.Add(time.Duration(i) * time.Hour),
			UpdatedAt: fixtureTime.Add(time.Duration(i) * time.Hour),
			Creator:   &user,
		}
	}
	return docs
}

// FixtureAPIIssueDocument returns a document attached to an issue.
func FixtureAPIIssueDocument(issueID string, n int) api.Document {
	user := FixtureAPIUser()
	return api.Document{
		ID:        fmt.Sprintf("doc-issue-%s-%d", issueID, n),
		Title:     fmt.Sprintf("Issue Document %d", n),
		Content:   fmt.Sprintf("# Issue Document %d\n\nDocument attached to issue %s.", n, issueID),
		SlugID:    fmt.Sprintf("issue-doc-%s-%d", issueID, n),
		URL:       fmt.Sprintf("https://linear.app/test/document/issue-doc-%s-%d", issueID, n),
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureTime,
		Creator:   &user,
		Issue:     &api.Issue{ID: issueID},
	}
}

// FixtureAPIProjectDocument returns a document attached to a project.
func FixtureAPIProjectDocument(projectID string, n int) api.Document {
	user := FixtureAPIUser()
	return api.Document{
		ID:        fmt.Sprintf("doc-project-%s-%d", projectID, n),
		Title:     fmt.Sprintf("Project Document %d", n),
		Content:   fmt.Sprintf("# Project Document %d\n\nDocument attached to project %s.", n, projectID),
		SlugID:    fmt.Sprintf("project-doc-%s-%d", projectID, n),
		URL:       fmt.Sprintf("https://linear.app/test/document/project-doc-%s-%d", projectID, n),
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureTime,
		Creator:   &user,
		Project:   &api.Project{ID: projectID},
	}
}

// FixtureAPIProject returns a test project.
func FixtureAPIProject() api.Project {
	user := FixtureAPIUser()
	startDate := "2024-01-01"
	targetDate := "2024-06-30"
	return api.Project{
		ID:          "project-1",
		Name:        "Test Project",
		Slug:        "test-project",
		Description: "A test project",
		URL:         "https://linear.app/test/project/test-project",
		State:       "started",
		StartDate:   &startDate,
		TargetDate:  &targetDate,
		CreatedAt:   fixtureTime,
		UpdatedAt:   fixtureTime,
		Lead:        &user,
	}
}

// FixtureAPICycle returns a test cycle.
func FixtureAPICycle() api.Cycle {
	return api.Cycle{
		ID:       "cycle-1",
		Number:   42,
		Name:     "Sprint 42",
		StartsAt: fixtureTime,
		EndsAt:   fixtureTime.Add(14 * 24 * time.Hour),
	}
}

// FixtureAPIInitiative returns a test initiative.
func FixtureAPIInitiative() api.Initiative {
	user := FixtureAPIUser()
	targetDate := "2024-12-31"
	return api.Initiative{
		ID:          "initiative-1",
		Name:        "Test Initiative",
		Slug:        "test-initiative",
		Description: "A test initiative",
		Status:      "active",
		Color:       "#0000ff",
		TargetDate:  &targetDate,
		URL:         "https://linear.app/test/initiative/test-initiative",
		CreatedAt:   fixtureTime,
		UpdatedAt:   fixtureTime,
		Owner:       &user,
		Projects: api.InitiativeProjects{
			Nodes: []api.InitiativeProject{
				{ID: "project-1", Name: "Test Project", Slug: "test-project"},
			},
		},
	}
}

// FixtureAPIProjectUpdate returns a test project status update.
func FixtureAPIProjectUpdate() api.ProjectUpdate {
	user := FixtureAPIUser()
	return api.ProjectUpdate{
		ID:        "update-1",
		Body:      "Sprint completed successfully",
		Health:    "onTrack",
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureTime,
		User:      &user,
	}
}

// FixtureAPIInitiativeUpdate returns a test initiative status update.
func FixtureAPIInitiativeUpdate() api.InitiativeUpdate {
	user := FixtureAPIUser()
	return api.InitiativeUpdate{
		ID:        "update-1",
		Body:      "Initiative on track",
		Health:    "onTrack",
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureTime,
		User:      &user,
	}
}

// FixtureAPIProjectMilestone returns a test project milestone.
func FixtureAPIProjectMilestone() api.ProjectMilestone {
	targetDate := "2024-03-31"
	return api.ProjectMilestone{
		ID:          "milestone-1",
		Name:        "Alpha Release",
		Description: "First alpha release",
		TargetDate:  &targetDate,
		SortOrder:   1.0,
	}
}

// WithRelations sets the issue's outgoing relations. They ride the issue's
// data blob, so issue.meta renders them; the relations/ directory reads the
// issue_relations table instead (see PopulateIssueRelations).
func WithRelations(rels ...api.IssueRelation) IssueOption {
	return func(i *api.Issue) {
		i.Relations = api.IssueRelations{Nodes: rels}
	}
}

// WithInverseRelations sets the issue's incoming relations (see WithRelations).
func WithInverseRelations(rels ...api.IssueRelation) IssueOption {
	return func(i *api.Issue) {
		i.InverseRelations = api.IssueRelations{Nodes: rels}
	}
}

// FixtureAPIIssueRelation returns an outgoing relation: TST-1 blocks TST-3.
// Both endpoints exist in the standard fixture issue set, so the enrichment
// path (relationView resolving the other end's identifier/title) is exercised.
func FixtureAPIIssueRelation() api.IssueRelation {
	return api.IssueRelation{
		ID:   "rel-1",
		Type: "blocks",
		RelatedIssue: &api.ParentIssue{
			ID:         "issue-3",
			Identifier: "TST-3",
			Title:      "Test Issue 3 - High Priority",
		},
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureTime,
	}
}

// FixtureAPIAttachment returns an external URL attachment (rendered as a
// *.link file in the attachments/ directory).
func FixtureAPIAttachment() api.Attachment {
	user := FixtureAPIUser()
	return api.Attachment{
		ID:         "attachment-1",
		Title:      "Design Spec",
		Subtitle:   "example.com",
		URL:        "https://example.com/design-spec",
		SourceType: "url",
		Creator:    &user,
		CreatedAt:  fixtureTime,
		UpdatedAt:  fixtureTime,
	}
}

// FixtureAPIHistoryEntries returns issue history entries with a describable
// change (state transition), so HistoryToMarkdown renders a non-empty body.
func FixtureAPIHistoryEntries() []api.IssueHistoryEntry {
	actor := FixtureAPIUser()
	from := FixtureAPIState("unstarted")
	to := FixtureAPIState("started")
	return []api.IssueHistoryEntry{
		{
			ID:        "history-1",
			CreatedAt: fixtureTime.Add(time.Hour),
			Actor:     &actor,
			FromState: &from,
			ToState:   &to,
		},
	}
}
