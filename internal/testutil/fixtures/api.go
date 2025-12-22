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
