package repo

import (
	"context"
	"strings"

	"github.com/jra3/linear-fuse/internal/api"
)

// MockRepository implements Repository with in-memory data for testing.
// All data is stored in maps and can be set directly for test setup.
type MockRepository struct {
	// Entity storage
	Teams       []api.Team
	Issues      map[string][]api.Issue // keyed by teamID
	States      map[string][]api.State // keyed by teamID
	Labels      map[string][]api.Label // keyed by teamID
	Users       []api.User
	TeamMembers map[string][]api.User // keyed by teamID
	Cycles      map[string][]api.Cycle // keyed by teamID
	Projects    map[string][]api.Project // keyed by teamID
	Milestones  map[string][]api.ProjectMilestone // keyed by projectID
	Comments    map[string][]api.Comment // keyed by issueID
	Documents   map[string][]api.Document // keyed by issueID or projectID
	Initiatives []api.Initiative
	InitiativeProjects map[string][]api.Project // keyed by initiativeID
	ProjectUpdates map[string][]api.ProjectUpdate // keyed by projectID
	InitiativeUpdates map[string][]api.InitiativeUpdate // keyed by initiativeID
	Attachments    map[string][]api.Attachment   // keyed by issueID
	EmbeddedFiles  map[string][]api.EmbeddedFile // keyed by issueID

	// Current user
	CurrentUser *api.User

	// Issue indexes for lookups
	issuesByID         map[string]*api.Issue
	issuesByIdentifier map[string]*api.Issue
}

// NewMockRepository creates a new mock repository with empty data stores
func NewMockRepository() *MockRepository {
	return &MockRepository{
		Issues:             make(map[string][]api.Issue),
		States:             make(map[string][]api.State),
		Labels:             make(map[string][]api.Label),
		TeamMembers:        make(map[string][]api.User),
		Cycles:             make(map[string][]api.Cycle),
		Projects:           make(map[string][]api.Project),
		Milestones:         make(map[string][]api.ProjectMilestone),
		Comments:           make(map[string][]api.Comment),
		Documents:          make(map[string][]api.Document),
		InitiativeProjects: make(map[string][]api.Project),
		ProjectUpdates:     make(map[string][]api.ProjectUpdate),
		InitiativeUpdates:  make(map[string][]api.InitiativeUpdate),
		Attachments:        make(map[string][]api.Attachment),
		EmbeddedFiles:      make(map[string][]api.EmbeddedFile),
		issuesByID:         make(map[string]*api.Issue),
		issuesByIdentifier: make(map[string]*api.Issue),
	}
}

// AddIssue adds an issue to the mock data and indexes it
func (m *MockRepository) AddIssue(issue api.Issue) {
	teamID := ""
	if issue.Team != nil {
		teamID = issue.Team.ID
	}
	m.Issues[teamID] = append(m.Issues[teamID], issue)
	m.issuesByID[issue.ID] = &issue
	m.issuesByIdentifier[issue.Identifier] = &issue
}

// =============================================================================
// Teams
// =============================================================================

func (m *MockRepository) GetTeams(ctx context.Context) ([]api.Team, error) {
	return m.Teams, nil
}

func (m *MockRepository) GetTeamByKey(ctx context.Context, key string) (*api.Team, error) {
	for i := range m.Teams {
		if m.Teams[i].Key == key {
			return &m.Teams[i], nil
		}
	}
	return nil, nil
}

// =============================================================================
// Issues
// =============================================================================

func (m *MockRepository) GetTeamIssues(ctx context.Context, teamID string) ([]api.Issue, error) {
	return m.Issues[teamID], nil
}

func (m *MockRepository) GetIssueByIdentifier(ctx context.Context, identifier string) (*api.Issue, error) {
	return m.issuesByIdentifier[identifier], nil
}

func (m *MockRepository) GetIssueByID(ctx context.Context, id string) (*api.Issue, error) {
	return m.issuesByID[id], nil
}

func (m *MockRepository) GetIssueChildren(ctx context.Context, parentID string) ([]api.Issue, error) {
	var children []api.Issue
	for _, issues := range m.Issues {
		for _, issue := range issues {
			if issue.Parent != nil && issue.Parent.ID == parentID {
				children = append(children, issue)
			}
		}
	}
	return children, nil
}

// =============================================================================
// Filtered Issue Queries
// =============================================================================

func (m *MockRepository) GetIssuesByState(ctx context.Context, teamID, stateID string) ([]api.Issue, error) {
	var result []api.Issue
	for _, issue := range m.Issues[teamID] {
		if issue.State.ID == stateID {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (m *MockRepository) GetIssuesByAssignee(ctx context.Context, teamID, assigneeID string) ([]api.Issue, error) {
	var result []api.Issue
	for _, issue := range m.Issues[teamID] {
		if issue.Assignee != nil && issue.Assignee.ID == assigneeID {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (m *MockRepository) GetIssuesByLabel(ctx context.Context, teamID, labelID string) ([]api.Issue, error) {
	var result []api.Issue
	for _, issue := range m.Issues[teamID] {
		for _, label := range issue.Labels.Nodes {
			if label.ID == labelID {
				result = append(result, issue)
				break
			}
		}
	}
	return result, nil
}

func (m *MockRepository) GetIssuesByPriority(ctx context.Context, teamID string, priority int) ([]api.Issue, error) {
	var result []api.Issue
	for _, issue := range m.Issues[teamID] {
		if issue.Priority == priority {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (m *MockRepository) GetUnassignedIssues(ctx context.Context, teamID string) ([]api.Issue, error) {
	var result []api.Issue
	for _, issue := range m.Issues[teamID] {
		if issue.Assignee == nil {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (m *MockRepository) GetIssuesByProject(ctx context.Context, projectID string) ([]api.Issue, error) {
	var result []api.Issue
	for _, issues := range m.Issues {
		for _, issue := range issues {
			if issue.Project != nil && issue.Project.ID == projectID {
				result = append(result, issue)
			}
		}
	}
	return result, nil
}

func (m *MockRepository) GetIssuesByCycle(ctx context.Context, cycleID string) ([]api.Issue, error) {
	var result []api.Issue
	for _, issues := range m.Issues {
		for _, issue := range issues {
			if issue.Cycle != nil && issue.Cycle.ID == cycleID {
				result = append(result, issue)
			}
		}
	}
	return result, nil
}

// =============================================================================
// My Issues
// =============================================================================

func (m *MockRepository) GetMyIssues(ctx context.Context) ([]api.Issue, error) {
	if m.CurrentUser == nil {
		return []api.Issue{}, nil
	}
	var result []api.Issue
	for _, issues := range m.Issues {
		for _, issue := range issues {
			if issue.Assignee != nil && issue.Assignee.ID == m.CurrentUser.ID {
				result = append(result, issue)
			}
		}
	}
	return result, nil
}

func (m *MockRepository) GetMyCreatedIssues(ctx context.Context) ([]api.Issue, error) {
	// Mock doesn't track creator, return empty
	return []api.Issue{}, nil
}

func (m *MockRepository) GetMyActiveIssues(ctx context.Context) ([]api.Issue, error) {
	if m.CurrentUser == nil {
		return []api.Issue{}, nil
	}
	var result []api.Issue
	for _, issues := range m.Issues {
		for _, issue := range issues {
			if issue.Assignee != nil && issue.Assignee.ID == m.CurrentUser.ID {
				stateType := issue.State.Type
				if stateType != "completed" && stateType != "canceled" {
					result = append(result, issue)
				}
			}
		}
	}
	return result, nil
}

func (m *MockRepository) GetUserIssues(ctx context.Context, userID string) ([]api.Issue, error) {
	var result []api.Issue
	for _, issues := range m.Issues {
		for _, issue := range issues {
			if issue.Assignee != nil && issue.Assignee.ID == userID {
				result = append(result, issue)
			}
		}
	}
	return result, nil
}

// =============================================================================
// Search
// =============================================================================

func (m *MockRepository) SearchIssues(ctx context.Context, query string) ([]api.Issue, error) {
	query = strings.ToLower(query)
	var result []api.Issue
	for _, issues := range m.Issues {
		for _, issue := range issues {
			if strings.Contains(strings.ToLower(issue.Title), query) ||
				strings.Contains(strings.ToLower(issue.Description), query) ||
				strings.Contains(strings.ToLower(issue.Identifier), query) {
				result = append(result, issue)
			}
		}
	}
	return result, nil
}

func (m *MockRepository) SearchTeamIssues(ctx context.Context, teamID, query string) ([]api.Issue, error) {
	query = strings.ToLower(query)
	var result []api.Issue
	for _, issue := range m.Issues[teamID] {
		if strings.Contains(strings.ToLower(issue.Title), query) ||
			strings.Contains(strings.ToLower(issue.Description), query) ||
			strings.Contains(strings.ToLower(issue.Identifier), query) {
			result = append(result, issue)
		}
	}
	return result, nil
}

// =============================================================================
// States
// =============================================================================

func (m *MockRepository) GetTeamStates(ctx context.Context, teamID string) ([]api.State, error) {
	return m.States[teamID], nil
}

func (m *MockRepository) GetStateByName(ctx context.Context, teamID, name string) (*api.State, error) {
	for i := range m.States[teamID] {
		if m.States[teamID][i].Name == name {
			return &m.States[teamID][i], nil
		}
	}
	return nil, nil
}

// =============================================================================
// Labels
// =============================================================================

func (m *MockRepository) GetTeamLabels(ctx context.Context, teamID string) ([]api.Label, error) {
	return m.Labels[teamID], nil
}

func (m *MockRepository) GetLabelByName(ctx context.Context, teamID, name string) (*api.Label, error) {
	for i := range m.Labels[teamID] {
		if m.Labels[teamID][i].Name == name {
			return &m.Labels[teamID][i], nil
		}
	}
	return nil, nil
}

// =============================================================================
// Users
// =============================================================================

func (m *MockRepository) GetUsers(ctx context.Context) ([]api.User, error) {
	return m.Users, nil
}

func (m *MockRepository) GetUserByID(ctx context.Context, id string) (*api.User, error) {
	for i := range m.Users {
		if m.Users[i].ID == id {
			return &m.Users[i], nil
		}
	}
	return nil, nil
}

func (m *MockRepository) GetUserByEmail(ctx context.Context, email string) (*api.User, error) {
	for i := range m.Users {
		if m.Users[i].Email == email {
			return &m.Users[i], nil
		}
	}
	return nil, nil
}

func (m *MockRepository) GetCurrentUser(ctx context.Context) (*api.User, error) {
	return m.CurrentUser, nil
}

func (m *MockRepository) SetCurrentUser(user *api.User) {
	m.CurrentUser = user
}

func (m *MockRepository) GetTeamMembers(ctx context.Context, teamID string) ([]api.User, error) {
	return m.TeamMembers[teamID], nil
}

// =============================================================================
// Cycles
// =============================================================================

func (m *MockRepository) GetTeamCycles(ctx context.Context, teamID string) ([]api.Cycle, error) {
	return m.Cycles[teamID], nil
}

func (m *MockRepository) GetCycleByName(ctx context.Context, teamID, name string) (*api.Cycle, error) {
	for i := range m.Cycles[teamID] {
		if m.Cycles[teamID][i].Name == name {
			return &m.Cycles[teamID][i], nil
		}
	}
	return nil, nil
}

// =============================================================================
// Projects
// =============================================================================

func (m *MockRepository) GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error) {
	return m.Projects[teamID], nil
}

func (m *MockRepository) GetProjectBySlug(ctx context.Context, slug string) (*api.Project, error) {
	for _, projects := range m.Projects {
		for i := range projects {
			if projects[i].Slug == slug {
				return &projects[i], nil
			}
		}
	}
	return nil, nil
}

func (m *MockRepository) GetProjectByID(ctx context.Context, id string) (*api.Project, error) {
	for _, projects := range m.Projects {
		for i := range projects {
			if projects[i].ID == id {
				return &projects[i], nil
			}
		}
	}
	return nil, nil
}

// =============================================================================
// Project Milestones
// =============================================================================

func (m *MockRepository) GetProjectMilestones(ctx context.Context, projectID string) ([]api.ProjectMilestone, error) {
	return m.Milestones[projectID], nil
}

func (m *MockRepository) GetMilestoneByName(ctx context.Context, projectID, name string) (*api.ProjectMilestone, error) {
	for i := range m.Milestones[projectID] {
		if m.Milestones[projectID][i].Name == name {
			return &m.Milestones[projectID][i], nil
		}
	}
	return nil, nil
}

// =============================================================================
// Comments
// =============================================================================

func (m *MockRepository) GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error) {
	return m.Comments[issueID], nil
}

func (m *MockRepository) GetCommentByID(ctx context.Context, id string) (*api.Comment, error) {
	for _, comments := range m.Comments {
		for i := range comments {
			if comments[i].ID == id {
				return &comments[i], nil
			}
		}
	}
	return nil, nil
}

// =============================================================================
// Documents
// =============================================================================

func (m *MockRepository) GetIssueDocuments(ctx context.Context, issueID string) ([]api.Document, error) {
	return m.Documents[issueID], nil
}

func (m *MockRepository) GetProjectDocuments(ctx context.Context, projectID string) ([]api.Document, error) {
	return m.Documents[projectID], nil
}

func (m *MockRepository) GetDocumentBySlug(ctx context.Context, slug string) (*api.Document, error) {
	for _, docs := range m.Documents {
		for i := range docs {
			if docs[i].SlugID == slug {
				return &docs[i], nil
			}
		}
	}
	return nil, nil
}

// =============================================================================
// Initiatives
// =============================================================================

func (m *MockRepository) GetInitiatives(ctx context.Context) ([]api.Initiative, error) {
	return m.Initiatives, nil
}

func (m *MockRepository) GetInitiativeBySlug(ctx context.Context, slug string) (*api.Initiative, error) {
	for i := range m.Initiatives {
		if m.Initiatives[i].Slug == slug {
			return &m.Initiatives[i], nil
		}
	}
	return nil, nil
}

func (m *MockRepository) GetInitiativeProjects(ctx context.Context, initiativeID string) ([]api.Project, error) {
	return m.InitiativeProjects[initiativeID], nil
}

// =============================================================================
// Status Updates
// =============================================================================

func (m *MockRepository) GetProjectUpdates(ctx context.Context, projectID string) ([]api.ProjectUpdate, error) {
	return m.ProjectUpdates[projectID], nil
}

func (m *MockRepository) GetInitiativeUpdates(ctx context.Context, initiativeID string) ([]api.InitiativeUpdate, error) {
	return m.InitiativeUpdates[initiativeID], nil
}

// =============================================================================
// Attachments
// =============================================================================

func (m *MockRepository) GetIssueAttachments(ctx context.Context, issueID string) ([]api.Attachment, error) {
	return m.Attachments[issueID], nil
}

func (m *MockRepository) GetIssueEmbeddedFiles(ctx context.Context, issueID string) ([]api.EmbeddedFile, error) {
	return m.EmbeddedFiles[issueID], nil
}

func (m *MockRepository) UpdateEmbeddedFileCache(ctx context.Context, id, cachePath string, size int64) error {
	// In mock, find and update the file
	for issueID, files := range m.EmbeddedFiles {
		for i, f := range files {
			if f.ID == id {
				m.EmbeddedFiles[issueID][i].CachePath = cachePath
				m.EmbeddedFiles[issueID][i].FileSize = size
				return nil
			}
		}
	}
	return nil
}

// Ensure MockRepository implements Repository
var _ Repository = (*MockRepository)(nil)
