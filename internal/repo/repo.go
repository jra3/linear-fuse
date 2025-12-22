// Package repo provides the data access layer for LinearFS.
// It abstracts away the underlying storage (SQLite) and provides
// a clean interface for FUSE nodes to query data.
package repo

import (
	"context"

	"github.com/jra3/linear-fuse/internal/api"
)

// Repository defines the data access interface for LinearFS.
// All read operations return api.* types directly.
// Implementations may cache data in SQLite with background refresh.
type Repository interface {
	// ==========================================================================
	// Teams
	// ==========================================================================

	// GetTeams returns all teams the user has access to
	GetTeams(ctx context.Context) ([]api.Team, error)

	// GetTeamByKey returns a team by its key (e.g., "ENG")
	GetTeamByKey(ctx context.Context, key string) (*api.Team, error)

	// ==========================================================================
	// Issues
	// ==========================================================================

	// GetTeamIssues returns all issues for a team
	GetTeamIssues(ctx context.Context, teamID string) ([]api.Issue, error)

	// GetIssueByIdentifier returns an issue by its identifier (e.g., "ENG-123")
	GetIssueByIdentifier(ctx context.Context, identifier string) (*api.Issue, error)

	// GetIssueByID returns an issue by its internal ID
	GetIssueByID(ctx context.Context, id string) (*api.Issue, error)

	// GetIssueChildren returns child issues of a parent issue
	GetIssueChildren(ctx context.Context, parentID string) ([]api.Issue, error)

	// ==========================================================================
	// Filtered Issue Queries
	// ==========================================================================

	// GetIssuesByState returns issues in a specific state
	GetIssuesByState(ctx context.Context, teamID, stateID string) ([]api.Issue, error)

	// GetIssuesByAssignee returns issues assigned to a user
	GetIssuesByAssignee(ctx context.Context, teamID, assigneeID string) ([]api.Issue, error)

	// GetIssuesByLabel returns issues with a specific label
	GetIssuesByLabel(ctx context.Context, teamID, labelID string) ([]api.Issue, error)

	// GetIssuesByPriority returns issues with a specific priority
	GetIssuesByPriority(ctx context.Context, teamID string, priority int) ([]api.Issue, error)

	// GetUnassignedIssues returns issues without an assignee
	GetUnassignedIssues(ctx context.Context, teamID string) ([]api.Issue, error)

	// GetIssuesByProject returns issues in a specific project
	GetIssuesByProject(ctx context.Context, projectID string) ([]api.Issue, error)

	// GetIssuesByCycle returns issues in a specific cycle
	GetIssuesByCycle(ctx context.Context, cycleID string) ([]api.Issue, error)

	// ==========================================================================
	// My Issues (current user)
	// ==========================================================================

	// GetMyIssues returns issues assigned to the current user
	GetMyIssues(ctx context.Context) ([]api.Issue, error)

	// GetMyCreatedIssues returns issues created by the current user
	GetMyCreatedIssues(ctx context.Context) ([]api.Issue, error)

	// GetMyActiveIssues returns non-completed issues assigned to the current user
	GetMyActiveIssues(ctx context.Context) ([]api.Issue, error)

	// GetUserIssues returns all issues assigned to a specific user (across all teams)
	GetUserIssues(ctx context.Context, userID string) ([]api.Issue, error)

	// ==========================================================================
	// Search
	// ==========================================================================

	// SearchIssues performs full-text search across issues
	SearchIssues(ctx context.Context, query string) ([]api.Issue, error)

	// SearchTeamIssues performs full-text search within a team
	SearchTeamIssues(ctx context.Context, teamID, query string) ([]api.Issue, error)

	// ==========================================================================
	// States (workflow states)
	// ==========================================================================

	// GetTeamStates returns workflow states for a team
	GetTeamStates(ctx context.Context, teamID string) ([]api.State, error)

	// GetStateByName returns a state by name within a team
	GetStateByName(ctx context.Context, teamID, name string) (*api.State, error)

	// ==========================================================================
	// Labels
	// ==========================================================================

	// GetTeamLabels returns labels for a team
	GetTeamLabels(ctx context.Context, teamID string) ([]api.Label, error)

	// GetLabelByName returns a label by name within a team
	GetLabelByName(ctx context.Context, teamID, name string) (*api.Label, error)

	// ==========================================================================
	// Users
	// ==========================================================================

	// GetUsers returns all users in the workspace
	GetUsers(ctx context.Context) ([]api.User, error)

	// GetUserByID returns a user by ID
	GetUserByID(ctx context.Context, id string) (*api.User, error)

	// GetUserByEmail returns a user by email
	GetUserByEmail(ctx context.Context, email string) (*api.User, error)

	// GetCurrentUser returns the authenticated user
	GetCurrentUser(ctx context.Context) (*api.User, error)

	// GetTeamMembers returns members of a team
	GetTeamMembers(ctx context.Context, teamID string) ([]api.User, error)

	// ==========================================================================
	// Cycles
	// ==========================================================================

	// GetTeamCycles returns cycles for a team
	GetTeamCycles(ctx context.Context, teamID string) ([]api.Cycle, error)

	// GetCycleByName returns a cycle by name within a team
	GetCycleByName(ctx context.Context, teamID, name string) (*api.Cycle, error)

	// ==========================================================================
	// Projects
	// ==========================================================================

	// GetTeamProjects returns projects associated with a team
	GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error)

	// GetProjectBySlug returns a project by its slug
	GetProjectBySlug(ctx context.Context, slug string) (*api.Project, error)

	// GetProjectByID returns a project by its ID
	GetProjectByID(ctx context.Context, id string) (*api.Project, error)

	// ==========================================================================
	// Project Milestones
	// ==========================================================================

	// GetProjectMilestones returns milestones for a project
	GetProjectMilestones(ctx context.Context, projectID string) ([]api.ProjectMilestone, error)

	// GetMilestoneByName returns a milestone by name within a project
	GetMilestoneByName(ctx context.Context, projectID, name string) (*api.ProjectMilestone, error)

	// ==========================================================================
	// Comments (on-demand fetch)
	// ==========================================================================

	// GetIssueComments returns comments for an issue
	// May trigger background refresh if data is stale
	GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error)

	// GetCommentByID returns a comment by ID
	GetCommentByID(ctx context.Context, id string) (*api.Comment, error)

	// ==========================================================================
	// Documents (on-demand fetch)
	// ==========================================================================

	// GetIssueDocuments returns documents attached to an issue
	GetIssueDocuments(ctx context.Context, issueID string) ([]api.Document, error)

	// GetProjectDocuments returns documents attached to a project
	GetProjectDocuments(ctx context.Context, projectID string) ([]api.Document, error)

	// GetDocumentBySlug returns a document by its slug
	GetDocumentBySlug(ctx context.Context, slug string) (*api.Document, error)

	// ==========================================================================
	// Initiatives
	// ==========================================================================

	// GetInitiatives returns all initiatives
	GetInitiatives(ctx context.Context) ([]api.Initiative, error)

	// GetInitiativeBySlug returns an initiative by its slug
	GetInitiativeBySlug(ctx context.Context, slug string) (*api.Initiative, error)

	// GetInitiativeProjects returns projects linked to an initiative
	GetInitiativeProjects(ctx context.Context, initiativeID string) ([]api.Project, error)

	// ==========================================================================
	// Status Updates
	// ==========================================================================

	// GetProjectUpdates returns status updates for a project
	GetProjectUpdates(ctx context.Context, projectID string) ([]api.ProjectUpdate, error)

	// GetInitiativeUpdates returns status updates for an initiative
	GetInitiativeUpdates(ctx context.Context, initiativeID string) ([]api.InitiativeUpdate, error)
}
