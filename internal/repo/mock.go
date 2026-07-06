package repo

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// MockRepository implements Repository with in-memory data for testing.
// All data is stored in maps and can be set directly for test setup.
type MockRepository struct {
	// Entity storage
	Teams              []api.Team
	Issues             map[string][]api.Issue // keyed by teamID
	States             map[string][]api.State // keyed by teamID
	Labels             map[string][]api.Label // keyed by teamID
	Users              []api.User
	TeamMembers        map[string][]api.User             // keyed by teamID
	Cycles             map[string][]api.Cycle            // keyed by teamID
	Projects           map[string][]api.Project          // keyed by teamID
	Milestones         map[string][]api.ProjectMilestone // keyed by projectID
	Comments           map[string][]api.Comment          // keyed by issueID
	Documents          map[string][]api.Document         // keyed by issueID or projectID
	Initiatives        []api.Initiative
	InitiativeProjects map[string][]api.Project           // keyed by initiativeID
	ProjectUpdates     map[string][]api.ProjectUpdate     // keyed by projectID
	InitiativeUpdates  map[string][]api.InitiativeUpdate  // keyed by initiativeID
	Attachments        map[string][]api.Attachment        // keyed by issueID
	EmbeddedFiles      map[string][]api.EmbeddedFile      // keyed by issueID
	IssueHistory       map[string][]api.IssueHistoryEntry // keyed by issueID

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
// Query helpers — every filter/finder in the mock collapses to these four,
// so only the predicate (the real per-query variance) is written by hand.
// =============================================================================

// filter returns the items matching pred, preserving order.
func filter[T any](items []T, pred func(T) bool) []T {
	var out []T
	for _, it := range items {
		if pred(it) {
			out = append(out, it)
		}
	}
	return out
}

// first returns a pointer to the first item matching pred, or nil. The pointer
// aliases the slice element, matching the hand-written finders it replaced.
func first[T any](items []T, pred func(T) bool) *T {
	for i := range items {
		if pred(items[i]) {
			return &items[i]
		}
	}
	return nil
}

// filterInMap returns every inner-slice element matching pred across all map
// values, in unspecified (map-iteration) order.
func filterInMap[K comparable, V any](m map[K][]V, pred func(V) bool) []V {
	var out []V
	for _, vs := range m {
		for _, v := range vs {
			if pred(v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// firstInMap returns a pointer to the first inner-slice element matching pred
// across all map values, or nil. Map order is unspecified, so callers use it
// for lookups by a unique key.
func firstInMap[K comparable, V any](m map[K][]V, pred func(V) bool) *V {
	for _, vs := range m {
		for i := range vs {
			if pred(vs[i]) {
				return &vs[i]
			}
		}
	}
	return nil
}

// =============================================================================
// Teams
// =============================================================================

func (m *MockRepository) GetTeams(ctx context.Context) ([]api.Team, error) {
	return m.Teams, nil
}

func (m *MockRepository) GetTeamByKey(ctx context.Context, key string) (*api.Team, error) {
	return first(m.Teams, func(t api.Team) bool { return t.Key == key }), nil
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
	return filterInMap(m.Issues, func(i api.Issue) bool {
		return i.Parent != nil && i.Parent.ID == parentID
	}), nil
}

// =============================================================================
// Filtered Issue Queries
// =============================================================================

func (m *MockRepository) GetIssuesByState(ctx context.Context, teamID, stateID string) ([]api.Issue, error) {
	return filter(m.Issues[teamID], func(i api.Issue) bool { return i.State.ID == stateID }), nil
}

func (m *MockRepository) GetIssuesByAssignee(ctx context.Context, teamID, assigneeID string) ([]api.Issue, error) {
	return filter(m.Issues[teamID], func(i api.Issue) bool {
		return i.Assignee != nil && i.Assignee.ID == assigneeID
	}), nil
}

func (m *MockRepository) GetIssuesByLabel(ctx context.Context, teamID, labelID string) ([]api.Issue, error) {
	return filter(m.Issues[teamID], func(i api.Issue) bool {
		return slices.ContainsFunc(i.Labels.Nodes, func(l api.Label) bool { return l.ID == labelID })
	}), nil
}

func (m *MockRepository) GetIssuesByPriority(ctx context.Context, teamID string, priority int) ([]api.Issue, error) {
	return filter(m.Issues[teamID], func(i api.Issue) bool { return i.Priority == priority }), nil
}

func (m *MockRepository) GetUnassignedIssues(ctx context.Context, teamID string) ([]api.Issue, error) {
	return filter(m.Issues[teamID], func(i api.Issue) bool { return i.Assignee == nil }), nil
}

func (m *MockRepository) GetIssuesByProject(ctx context.Context, projectID string) ([]api.Issue, error) {
	return filterInMap(m.Issues, func(i api.Issue) bool {
		return i.Project != nil && i.Project.ID == projectID
	}), nil
}

func (m *MockRepository) GetIssuesByCycle(ctx context.Context, cycleID string) ([]api.Issue, error) {
	return filterInMap(m.Issues, func(i api.Issue) bool {
		return i.Cycle != nil && i.Cycle.ID == cycleID
	}), nil
}

// =============================================================================
// My Issues
// =============================================================================

func (m *MockRepository) GetMyIssues(ctx context.Context) ([]api.Issue, error) {
	if m.CurrentUser == nil {
		return []api.Issue{}, nil
	}
	return filterInMap(m.Issues, func(i api.Issue) bool {
		return i.Assignee != nil && i.Assignee.ID == m.CurrentUser.ID
	}), nil
}

func (m *MockRepository) GetMyCreatedIssues(ctx context.Context) ([]api.Issue, error) {
	// Mock doesn't track creator, return empty
	return []api.Issue{}, nil
}

func (m *MockRepository) GetMyActiveIssues(ctx context.Context) ([]api.Issue, error) {
	if m.CurrentUser == nil {
		return []api.Issue{}, nil
	}
	return filterInMap(m.Issues, func(i api.Issue) bool {
		if i.Assignee == nil || i.Assignee.ID != m.CurrentUser.ID {
			return false
		}
		return i.State.Type != "completed" && i.State.Type != "canceled"
	}), nil
}

func (m *MockRepository) GetUserIssues(ctx context.Context, userID string) ([]api.Issue, error) {
	return filterInMap(m.Issues, func(i api.Issue) bool {
		return i.Assignee != nil && i.Assignee.ID == userID
	}), nil
}

// =============================================================================
// States
// =============================================================================

func (m *MockRepository) GetTeamStates(ctx context.Context, teamID string) ([]api.State, error) {
	return m.States[teamID], nil
}

func (m *MockRepository) GetStateByName(ctx context.Context, teamID, name string) (*api.State, error) {
	return first(m.States[teamID], func(s api.State) bool { return s.Name == name }), nil
}

// =============================================================================
// Labels
// =============================================================================

func (m *MockRepository) GetTeamLabels(ctx context.Context, teamID string) ([]api.Label, error) {
	return m.Labels[teamID], nil
}

func (m *MockRepository) GetLabelByName(ctx context.Context, teamID, name string) (*api.Label, error) {
	return first(m.Labels[teamID], func(l api.Label) bool { return l.Name == name }), nil
}

// =============================================================================
// Users
// =============================================================================

func (m *MockRepository) GetUsers(ctx context.Context) ([]api.User, error) {
	return m.Users, nil
}

func (m *MockRepository) GetUserByID(ctx context.Context, id string) (*api.User, error) {
	return first(m.Users, func(u api.User) bool { return u.ID == id }), nil
}

func (m *MockRepository) GetUserByEmail(ctx context.Context, email string) (*api.User, error) {
	return first(m.Users, func(u api.User) bool { return u.Email == email }), nil
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
	return first(m.Cycles[teamID], func(c api.Cycle) bool { return c.Name == name }), nil
}

// =============================================================================
// Projects
// =============================================================================

func (m *MockRepository) GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error) {
	return m.Projects[teamID], nil
}

func (m *MockRepository) GetProjectPrimaryTeamKey(ctx context.Context, projectID string) (string, error) {
	primary := ""
	for teamID, projects := range m.Projects {
		if !slices.ContainsFunc(projects, func(p api.Project) bool { return p.ID == projectID }) {
			continue
		}
		for _, team := range m.Teams {
			if team.ID == teamID && (primary == "" || team.Key < primary) {
				primary = team.Key
			}
		}
	}
	return primary, nil
}

func (m *MockRepository) GetProjectBySlug(ctx context.Context, slug string) (*api.Project, error) {
	return firstInMap(m.Projects, func(p api.Project) bool { return p.Slug == slug }), nil
}

func (m *MockRepository) GetProjectByID(ctx context.Context, id string) (*api.Project, error) {
	return firstInMap(m.Projects, func(p api.Project) bool { return p.ID == id }), nil
}

// =============================================================================
// Project Milestones
// =============================================================================

func (m *MockRepository) GetProjectMilestones(ctx context.Context, projectID string) ([]api.ProjectMilestone, error) {
	return m.Milestones[projectID], nil
}

func (m *MockRepository) GetMilestoneByName(ctx context.Context, projectID, name string) (*api.ProjectMilestone, error) {
	return first(m.Milestones[projectID], func(ms api.ProjectMilestone) bool { return ms.Name == name }), nil
}

func (m *MockRepository) GetMilestoneByID(ctx context.Context, id string) (*api.ProjectMilestone, error) {
	return firstInMap(m.Milestones, func(ms api.ProjectMilestone) bool { return ms.ID == id }), nil
}

func (m *MockRepository) CreateProjectMilestone(ctx context.Context, projectID, name, description string) (*api.ProjectMilestone, error) {
	milestone := api.ProjectMilestone{
		ID:          "milestone-" + name,
		Name:        name,
		Description: description,
		SortOrder:   float64(len(m.Milestones[projectID])),
	}
	m.Milestones[projectID] = append(m.Milestones[projectID], milestone)
	return &milestone, nil
}

func (m *MockRepository) UpdateProjectMilestone(ctx context.Context, milestoneID string, input api.ProjectMilestoneUpdateInput) (*api.ProjectMilestone, error) {
	for projectID, milestones := range m.Milestones {
		for i := range milestones {
			if milestones[i].ID == milestoneID {
				if input.Name != nil {
					m.Milestones[projectID][i].Name = *input.Name
				}
				if input.Description != nil {
					m.Milestones[projectID][i].Description = *input.Description
				}
				if input.TargetDate != nil {
					m.Milestones[projectID][i].TargetDate = input.TargetDate
				}
				if input.SortOrder != nil {
					m.Milestones[projectID][i].SortOrder = *input.SortOrder
				}
				return &m.Milestones[projectID][i], nil
			}
		}
	}
	return nil, fmt.Errorf("milestone not found")
}

func (m *MockRepository) DeleteProjectMilestone(ctx context.Context, milestoneID string) error {
	for projectID, milestones := range m.Milestones {
		for i := range milestones {
			if milestones[i].ID == milestoneID {
				m.Milestones[projectID] = append(m.Milestones[projectID][:i], m.Milestones[projectID][i+1:]...)
				return nil
			}
		}
	}
	return fmt.Errorf("milestone not found")
}

// =============================================================================
// Sub-resource refresh
// =============================================================================

func (m *MockRepository) MaybeRefreshIssueDetails(issueID string) {}

// =============================================================================
// Comments
// =============================================================================

func (m *MockRepository) GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error) {
	return m.Comments[issueID], nil
}

func (m *MockRepository) GetCommentByID(ctx context.Context, id string) (*api.Comment, error) {
	return firstInMap(m.Comments, func(c api.Comment) bool { return c.ID == id }), nil
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

func (m *MockRepository) GetInitiativeDocuments(ctx context.Context, initiativeID string) ([]api.Document, error) {
	return m.Documents[initiativeID], nil
}

func (m *MockRepository) GetDocumentBySlug(ctx context.Context, slug string) (*api.Document, error) {
	return firstInMap(m.Documents, func(d api.Document) bool { return d.SlugID == slug }), nil
}

// =============================================================================
// Initiatives
// =============================================================================

func (m *MockRepository) GetInitiatives(ctx context.Context) ([]api.Initiative, error) {
	return m.Initiatives, nil
}

func (m *MockRepository) GetInitiativeBySlug(ctx context.Context, slug string) (*api.Initiative, error) {
	return first(m.Initiatives, func(i api.Initiative) bool { return i.Slug == slug }), nil
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

func (m *MockRepository) GetAttachmentByID(ctx context.Context, id string) (*api.Attachment, error) {
	return firstInMap(m.Attachments, func(a api.Attachment) bool { return a.ID == id }), nil
}

func (m *MockRepository) GetIssueHistory(ctx context.Context, issueID string) ([]api.IssueHistoryEntry, error) {
	if m.IssueHistory != nil {
		return m.IssueHistory[issueID], nil
	}
	return nil, nil
}

func (m *MockRepository) GetIssueRelations(ctx context.Context, issueID string) ([]api.IssueRelation, error) {
	return nil, nil // Mock returns empty
}

func (m *MockRepository) GetIssueInverseRelations(ctx context.Context, issueID string) ([]api.IssueRelation, error) {
	return nil, nil // Mock returns empty
}

func (m *MockRepository) GetIssueRelationByID(ctx context.Context, id string) (*api.IssueRelation, error) {
	return nil, nil // Mock returns nil
}

func (m *MockRepository) TouchIssueSubResources(ctx context.Context, issueID string, syncedAt time.Time) {
	// No-op for mock
}

// Ensure MockRepository implements Repository
var _ Repository = (*MockRepository)(nil)
