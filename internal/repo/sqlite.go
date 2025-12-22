package repo

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// Default staleness threshold for on-demand data (comments, documents, updates)
const defaultStalenessThreshold = 5 * time.Minute

// SQLiteRepository implements Repository using SQLite as the data store.
// It reads from SQLite and optionally falls back to the API client
// for data that hasn't been synced yet.
//
// For on-demand data (comments, documents, updates), it implements
// stale-while-revalidate: returns cached data immediately and triggers
// a background refresh if the data is stale.
type SQLiteRepository struct {
	store              *db.Store
	client             *api.Client   // Optional: for fallback/on-demand fetch
	currentUser        *api.User     // Cached current user
	stalenessThreshold time.Duration // How long before data is considered stale

	// Track in-flight refreshes to avoid duplicate API calls
	refreshMu      sync.Mutex
	refreshing     map[string]bool
	refreshContext context.Context
	refreshCancel  context.CancelFunc
}

// NewSQLiteRepository creates a new SQLite-backed repository.
// If client is nil, the repository will only serve data from SQLite.
func NewSQLiteRepository(store *db.Store, client *api.Client) *SQLiteRepository {
	ctx, cancel := context.WithCancel(context.Background())
	return &SQLiteRepository{
		store:              store,
		client:             client,
		stalenessThreshold: defaultStalenessThreshold,
		refreshing:         make(map[string]bool),
		refreshContext:     ctx,
		refreshCancel:      cancel,
	}
}

// SetStalenessThreshold sets how long before on-demand data is considered stale
func (r *SQLiteRepository) SetStalenessThreshold(d time.Duration) {
	r.stalenessThreshold = d
}

// Close stops any background refresh operations
func (r *SQLiteRepository) Close() {
	r.refreshCancel()
}

// triggerBackgroundRefresh starts a background refresh if not already in progress
func (r *SQLiteRepository) triggerBackgroundRefresh(key string, refreshFn func(context.Context) error) {
	if r.client == nil {
		return
	}

	r.refreshMu.Lock()
	if r.refreshing[key] {
		r.refreshMu.Unlock()
		return
	}
	r.refreshing[key] = true
	r.refreshMu.Unlock()

	go func() {
		defer func() {
			r.refreshMu.Lock()
			delete(r.refreshing, key)
			r.refreshMu.Unlock()
		}()

		if err := refreshFn(r.refreshContext); err != nil {
			if r.refreshContext.Err() == nil {
				log.Printf("[repo] background refresh %s failed: %v", key, err)
			}
		}
	}()
}

// =============================================================================
// Teams
// =============================================================================

func (r *SQLiteRepository) GetTeams(ctx context.Context) ([]api.Team, error) {
	teams, err := r.store.Queries().ListTeams(ctx)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	return db.DBTeamsToAPITeams(teams), nil
}

func (r *SQLiteRepository) GetTeamByKey(ctx context.Context, key string) (*api.Team, error) {
	team, err := r.store.Queries().GetTeamByKey(ctx, key)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get team by key: %w", err)
	}
	result := db.DBTeamToAPITeam(team)
	return &result, nil
}

// =============================================================================
// Issues
// =============================================================================

func (r *SQLiteRepository) GetTeamIssues(ctx context.Context, teamID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssues(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssueByIdentifier(ctx context.Context, identifier string) (*api.Issue, error) {
	issue, err := r.store.Queries().GetIssueByIdentifier(ctx, identifier)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get issue by identifier: %w", err)
	}
	result, err := db.DBIssueToAPIIssue(issue)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *SQLiteRepository) GetIssueByID(ctx context.Context, id string) (*api.Issue, error) {
	issue, err := r.store.Queries().GetIssueByID(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get issue by id: %w", err)
	}
	result, err := db.DBIssueToAPIIssue(issue)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *SQLiteRepository) GetIssueChildren(ctx context.Context, parentID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssuesByParent(ctx, sql.NullString{String: parentID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list issue children: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

// =============================================================================
// Filtered Issue Queries
// =============================================================================

func (r *SQLiteRepository) GetIssuesByState(ctx context.Context, teamID, stateID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssuesByState(ctx, db.ListTeamIssuesByStateParams{
		TeamID:  teamID,
		StateID: sql.NullString{String: stateID, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list issues by state: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssuesByAssignee(ctx context.Context, teamID, assigneeID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssuesByAssignee(ctx, db.ListTeamIssuesByAssigneeParams{
		TeamID:     teamID,
		AssigneeID: sql.NullString{String: assigneeID, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list issues by assignee: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssuesByLabel(ctx context.Context, teamID, labelID string) ([]api.Issue, error) {
	// Get label name first
	label, err := r.store.Queries().GetLabel(ctx, labelID)
	if err != nil {
		if err == sql.ErrNoRows {
			return []api.Issue{}, nil
		}
		return nil, fmt.Errorf("get label: %w", err)
	}

	// Use the store's JSON-based label query
	issues, err := r.store.ListIssuesByLabel(ctx, teamID, label.Name)
	if err != nil {
		return nil, fmt.Errorf("list issues by label: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssuesByPriority(ctx context.Context, teamID string, priority int) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamIssuesByPriority(ctx, db.ListTeamIssuesByPriorityParams{
		TeamID:   teamID,
		Priority: sql.NullInt64{Int64: int64(priority), Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list issues by priority: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetUnassignedIssues(ctx context.Context, teamID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListTeamUnassignedIssues(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list unassigned issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssuesByProject(ctx context.Context, projectID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListProjectIssues(ctx, sql.NullString{String: projectID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list issues by project: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetIssuesByCycle(ctx context.Context, cycleID string) ([]api.Issue, error) {
	issues, err := r.store.Queries().ListCycleIssues(ctx, sql.NullString{String: cycleID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list issues by cycle: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

// =============================================================================
// My Issues
// =============================================================================

func (r *SQLiteRepository) GetMyIssues(ctx context.Context) ([]api.Issue, error) {
	user, err := r.GetCurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return []api.Issue{}, nil
	}

	issues, err := r.store.Queries().ListUserAssignedIssues(ctx, sql.NullString{String: user.ID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list my issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) GetMyCreatedIssues(ctx context.Context) ([]api.Issue, error) {
	// Note: creator_id isn't in our schema, so we need to query from JSON
	// For now, fallback to API if available
	if r.client != nil {
		return r.client.GetMyCreatedIssues(ctx)
	}
	return []api.Issue{}, nil
}

func (r *SQLiteRepository) GetMyActiveIssues(ctx context.Context) ([]api.Issue, error) {
	user, err := r.GetCurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return []api.Issue{}, nil
	}

	issues, err := r.store.Queries().ListUserActiveIssues(ctx, sql.NullString{String: user.ID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list my active issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

// =============================================================================
// Search
// =============================================================================

func (r *SQLiteRepository) SearchIssues(ctx context.Context, query string) ([]api.Issue, error) {
	issues, err := r.store.SearchIssues(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

func (r *SQLiteRepository) SearchTeamIssues(ctx context.Context, teamID, query string) ([]api.Issue, error) {
	issues, err := r.store.SearchTeamIssues(ctx, query, teamID)
	if err != nil {
		return nil, fmt.Errorf("search team issues: %w", err)
	}
	return db.DBIssuesToAPIIssues(issues)
}

// =============================================================================
// States
// =============================================================================

func (r *SQLiteRepository) GetTeamStates(ctx context.Context, teamID string) ([]api.State, error) {
	states, err := r.store.Queries().ListTeamStates(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team states: %w", err)
	}
	return db.DBStatesToAPIStates(states), nil
}

func (r *SQLiteRepository) GetStateByName(ctx context.Context, teamID, name string) (*api.State, error) {
	state, err := r.store.Queries().GetStateByName(ctx, db.GetStateByNameParams{
		TeamID: teamID,
		Name:   name,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get state by name: %w", err)
	}
	result := db.DBStateToAPIState(state)
	return &result, nil
}

// =============================================================================
// Labels
// =============================================================================

func (r *SQLiteRepository) GetTeamLabels(ctx context.Context, teamID string) ([]api.Label, error) {
	labels, err := r.store.Queries().ListTeamLabels(ctx, sql.NullString{String: teamID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list team labels: %w", err)
	}
	return db.DBLabelsToAPILabels(labels), nil
}

func (r *SQLiteRepository) GetLabelByName(ctx context.Context, teamID, name string) (*api.Label, error) {
	label, err := r.store.Queries().GetLabelByName(ctx, db.GetLabelByNameParams{
		TeamID: sql.NullString{String: teamID, Valid: true},
		Name:   name,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get label by name: %w", err)
	}
	result := db.DBLabelToAPILabel(label)
	return &result, nil
}

// =============================================================================
// Users
// =============================================================================

func (r *SQLiteRepository) GetUsers(ctx context.Context) ([]api.User, error) {
	users, err := r.store.Queries().ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return db.DBUsersToAPIUsers(users), nil
}

func (r *SQLiteRepository) GetUserByID(ctx context.Context, id string) (*api.User, error) {
	user, err := r.store.Queries().GetUser(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	result := db.DBUserToAPIUser(user)
	return &result, nil
}

func (r *SQLiteRepository) GetUserByEmail(ctx context.Context, email string) (*api.User, error) {
	user, err := r.store.Queries().GetUserByEmail(ctx, email)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	result := db.DBUserToAPIUser(user)
	return &result, nil
}

func (r *SQLiteRepository) GetCurrentUser(ctx context.Context) (*api.User, error) {
	// Return cached user if set (via SetCurrentUser)
	if r.currentUser != nil {
		return r.currentUser, nil
	}

	// Current user must be set externally via SetCurrentUser
	// This is typically done during LinearFS initialization
	return nil, nil
}

func (r *SQLiteRepository) GetTeamMembers(ctx context.Context, teamID string) ([]api.User, error) {
	users, err := r.store.Queries().ListTeamMembers(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}
	return db.DBUsersToAPIUsers(users), nil
}

// =============================================================================
// Cycles
// =============================================================================

func (r *SQLiteRepository) GetTeamCycles(ctx context.Context, teamID string) ([]api.Cycle, error) {
	cycles, err := r.store.Queries().ListTeamCycles(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team cycles: %w", err)
	}
	return db.DBCyclesToAPICycles(cycles), nil
}

func (r *SQLiteRepository) GetCycleByName(ctx context.Context, teamID, name string) (*api.Cycle, error) {
	cycle, err := r.store.Queries().GetCycleByName(ctx, db.GetCycleByNameParams{
		TeamID: teamID,
		Name:   sql.NullString{String: name, Valid: true},
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get cycle by name: %w", err)
	}
	result := db.DBCycleToAPICycle(cycle)
	return &result, nil
}

// =============================================================================
// Projects
// =============================================================================

func (r *SQLiteRepository) GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error) {
	projects, err := r.store.Queries().ListTeamProjects(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team projects: %w", err)
	}
	return db.DBProjectsToAPIProjects(projects)
}

func (r *SQLiteRepository) GetProjectBySlug(ctx context.Context, slug string) (*api.Project, error) {
	project, err := r.store.Queries().GetProjectBySlug(ctx, slug)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get project by slug: %w", err)
	}
	result, err := db.DBProjectToAPIProject(project)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *SQLiteRepository) GetProjectByID(ctx context.Context, id string) (*api.Project, error) {
	project, err := r.store.Queries().GetProject(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get project by id: %w", err)
	}
	result, err := db.DBProjectToAPIProject(project)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// =============================================================================
// Project Milestones
// =============================================================================

func (r *SQLiteRepository) GetProjectMilestones(ctx context.Context, projectID string) ([]api.ProjectMilestone, error) {
	milestones, err := r.store.Queries().ListProjectMilestones(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project milestones: %w", err)
	}
	return db.DBMilestonesToAPIProjectMilestones(milestones), nil
}

func (r *SQLiteRepository) GetMilestoneByName(ctx context.Context, projectID, name string) (*api.ProjectMilestone, error) {
	milestone, err := r.store.Queries().GetMilestoneByName(ctx, db.GetMilestoneByNameParams{
		ProjectID: projectID,
		Name:      name,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get milestone by name: %w", err)
	}
	result := db.DBMilestoneToAPIProjectMilestone(milestone)
	return &result, nil
}

// =============================================================================
// Comments
// =============================================================================

func (r *SQLiteRepository) GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error) {
	comments, err := r.store.Queries().ListIssueComments(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("list issue comments: %w", err)
	}

	// Check staleness and trigger background refresh if needed
	r.maybeRefreshComments(issueID, len(comments) == 0)

	return db.DBCommentsToAPIComments(comments)
}

// maybeRefreshComments checks if comments need refreshing and triggers background fetch
func (r *SQLiteRepository) maybeRefreshComments(issueID string, isEmpty bool) {
	if r.client == nil {
		return
	}

	// Check staleness
	syncedAt, err := r.store.Queries().GetIssueCommentsSyncedAt(context.Background(), issueID)
	isStale := err != nil || syncedAt == nil || time.Since(parseTime(syncedAt)) > r.stalenessThreshold

	// If empty, fetch synchronously on first access; otherwise refresh in background
	if isEmpty && isStale {
		// Synchronous fetch for empty data
		r.refreshComments(context.Background(), issueID)
	} else if isStale {
		// Background refresh for stale data
		r.triggerBackgroundRefresh("comments:"+issueID, func(ctx context.Context) error {
			return r.refreshComments(ctx, issueID)
		})
	}
}

// refreshComments fetches comments from API and stores in SQLite
func (r *SQLiteRepository) refreshComments(ctx context.Context, issueID string) error {
	comments, err := r.client.GetIssueComments(ctx, issueID)
	if err != nil {
		return err
	}

	for _, comment := range comments {
		params, err := db.APICommentToDBComment(comment, issueID)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertComment(ctx, params); err != nil {
			log.Printf("[repo] upsert comment %s failed: %v", comment.ID, err)
		}
	}
	return nil
}

// parseTime converts interface{} from SQLite to time.Time
func parseTime(v interface{}) time.Time {
	if v == nil {
		return time.Time{}
	}
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		parsed, _ := time.Parse(time.RFC3339, t)
		return parsed
	default:
		return time.Time{}
	}
}

func (r *SQLiteRepository) GetCommentByID(ctx context.Context, id string) (*api.Comment, error) {
	comment, err := r.store.Queries().GetComment(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get comment by id: %w", err)
	}
	result, err := db.DBCommentToAPIComment(comment)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// =============================================================================
// Documents
// =============================================================================

func (r *SQLiteRepository) GetIssueDocuments(ctx context.Context, issueID string) ([]api.Document, error) {
	docs, err := r.store.Queries().ListIssueDocuments(ctx, sql.NullString{String: issueID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list issue documents: %w", err)
	}

	// Check staleness and trigger background refresh if needed
	r.maybeRefreshIssueDocuments(issueID, len(docs) == 0)

	return db.DBDocumentsToAPIDocuments(docs)
}

// maybeRefreshIssueDocuments checks if issue documents need refreshing
func (r *SQLiteRepository) maybeRefreshIssueDocuments(issueID string, isEmpty bool) {
	if r.client == nil {
		return
	}

	syncedAt, err := r.store.Queries().GetIssueDocumentsSyncedAt(context.Background(), sql.NullString{String: issueID, Valid: true})
	isStale := err != nil || syncedAt == nil || time.Since(parseTime(syncedAt)) > r.stalenessThreshold

	if isEmpty && isStale {
		r.refreshIssueDocuments(context.Background(), issueID)
	} else if isStale {
		r.triggerBackgroundRefresh("issue-docs:"+issueID, func(ctx context.Context) error {
			return r.refreshIssueDocuments(ctx, issueID)
		})
	}
}

// refreshIssueDocuments fetches documents from API and stores in SQLite
func (r *SQLiteRepository) refreshIssueDocuments(ctx context.Context, issueID string) error {
	docs, err := r.client.GetIssueDocuments(ctx, issueID)
	if err != nil {
		return err
	}

	for _, doc := range docs {
		params, err := db.APIDocumentToDBDocument(doc)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertDocument(ctx, params); err != nil {
			log.Printf("[repo] upsert document %s failed: %v", doc.ID, err)
		}
	}
	return nil
}

func (r *SQLiteRepository) GetProjectDocuments(ctx context.Context, projectID string) ([]api.Document, error) {
	docs, err := r.store.Queries().ListProjectDocuments(ctx, sql.NullString{String: projectID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list project documents: %w", err)
	}

	// Check staleness and trigger background refresh if needed
	r.maybeRefreshProjectDocuments(projectID, len(docs) == 0)

	return db.DBDocumentsToAPIDocuments(docs)
}

// maybeRefreshProjectDocuments checks if project documents need refreshing
func (r *SQLiteRepository) maybeRefreshProjectDocuments(projectID string, isEmpty bool) {
	if r.client == nil {
		return
	}

	syncedAt, err := r.store.Queries().GetProjectDocumentsSyncedAt(context.Background(), sql.NullString{String: projectID, Valid: true})
	isStale := err != nil || syncedAt == nil || time.Since(parseTime(syncedAt)) > r.stalenessThreshold

	if isEmpty && isStale {
		r.refreshProjectDocuments(context.Background(), projectID)
	} else if isStale {
		r.triggerBackgroundRefresh("project-docs:"+projectID, func(ctx context.Context) error {
			return r.refreshProjectDocuments(ctx, projectID)
		})
	}
}

// refreshProjectDocuments fetches documents from API and stores in SQLite
func (r *SQLiteRepository) refreshProjectDocuments(ctx context.Context, projectID string) error {
	docs, err := r.client.GetProjectDocuments(ctx, projectID)
	if err != nil {
		return err
	}

	for _, doc := range docs {
		params, err := db.APIDocumentToDBDocument(doc)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertDocument(ctx, params); err != nil {
			log.Printf("[repo] upsert document %s failed: %v", doc.ID, err)
		}
	}
	return nil
}

func (r *SQLiteRepository) GetDocumentBySlug(ctx context.Context, slug string) (*api.Document, error) {
	doc, err := r.store.Queries().GetDocumentBySlug(ctx, slug)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get document by slug: %w", err)
	}
	result, err := db.DBDocumentToAPIDocument(doc)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// =============================================================================
// Initiatives
// =============================================================================

func (r *SQLiteRepository) GetInitiatives(ctx context.Context) ([]api.Initiative, error) {
	initiatives, err := r.store.Queries().ListInitiatives(ctx)
	if err != nil {
		return nil, fmt.Errorf("list initiatives: %w", err)
	}
	return db.DBInitiativesToAPIInitiatives(initiatives)
}

func (r *SQLiteRepository) GetInitiativeBySlug(ctx context.Context, slug string) (*api.Initiative, error) {
	initiative, err := r.store.Queries().GetInitiativeBySlug(ctx, slug)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get initiative by slug: %w", err)
	}
	result, err := db.DBInitiativeToAPIInitiative(initiative)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *SQLiteRepository) GetInitiativeProjects(ctx context.Context, initiativeID string) ([]api.Project, error) {
	projects, err := r.store.Queries().ListInitiativeProjects(ctx, initiativeID)
	if err != nil {
		return nil, fmt.Errorf("list initiative projects: %w", err)
	}
	return db.DBProjectsToAPIProjects(projects)
}

// =============================================================================
// Status Updates
// =============================================================================

func (r *SQLiteRepository) GetProjectUpdates(ctx context.Context, projectID string) ([]api.ProjectUpdate, error) {
	updates, err := r.store.Queries().ListProjectUpdates(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project updates: %w", err)
	}

	// Check staleness and trigger background refresh if needed
	r.maybeRefreshProjectUpdates(projectID, len(updates) == 0)

	return db.DBProjectUpdatesToAPIUpdates(updates)
}

// maybeRefreshProjectUpdates checks if project updates need refreshing
func (r *SQLiteRepository) maybeRefreshProjectUpdates(projectID string, isEmpty bool) {
	if r.client == nil {
		return
	}

	syncedAt, err := r.store.Queries().GetProjectUpdatesSyncedAt(context.Background(), projectID)
	isStale := err != nil || syncedAt == nil || time.Since(parseTime(syncedAt)) > r.stalenessThreshold

	if isEmpty && isStale {
		r.refreshProjectUpdates(context.Background(), projectID)
	} else if isStale {
		r.triggerBackgroundRefresh("project-updates:"+projectID, func(ctx context.Context) error {
			return r.refreshProjectUpdates(ctx, projectID)
		})
	}
}

// refreshProjectUpdates fetches updates from API and stores in SQLite
func (r *SQLiteRepository) refreshProjectUpdates(ctx context.Context, projectID string) error {
	updates, err := r.client.GetProjectUpdates(ctx, projectID)
	if err != nil {
		return err
	}

	for _, update := range updates {
		params, err := db.APIProjectUpdateToDBUpdate(update, projectID)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertProjectUpdate(ctx, params); err != nil {
			log.Printf("[repo] upsert project update %s failed: %v", update.ID, err)
		}
	}
	return nil
}

func (r *SQLiteRepository) GetInitiativeUpdates(ctx context.Context, initiativeID string) ([]api.InitiativeUpdate, error) {
	updates, err := r.store.Queries().ListInitiativeUpdates(ctx, initiativeID)
	if err != nil {
		return nil, fmt.Errorf("list initiative updates: %w", err)
	}

	// Check staleness and trigger background refresh if needed
	r.maybeRefreshInitiativeUpdates(initiativeID, len(updates) == 0)

	return db.DBInitiativeUpdatesToAPIUpdates(updates)
}

// maybeRefreshInitiativeUpdates checks if initiative updates need refreshing
func (r *SQLiteRepository) maybeRefreshInitiativeUpdates(initiativeID string, isEmpty bool) {
	if r.client == nil {
		return
	}

	syncedAt, err := r.store.Queries().GetInitiativeUpdatesSyncedAt(context.Background(), initiativeID)
	isStale := err != nil || syncedAt == nil || time.Since(parseTime(syncedAt)) > r.stalenessThreshold

	if isEmpty && isStale {
		r.refreshInitiativeUpdates(context.Background(), initiativeID)
	} else if isStale {
		r.triggerBackgroundRefresh("initiative-updates:"+initiativeID, func(ctx context.Context) error {
			return r.refreshInitiativeUpdates(ctx, initiativeID)
		})
	}
}

// refreshInitiativeUpdates fetches updates from API and stores in SQLite
func (r *SQLiteRepository) refreshInitiativeUpdates(ctx context.Context, initiativeID string) error {
	updates, err := r.client.GetInitiativeUpdates(ctx, initiativeID)
	if err != nil {
		return err
	}

	for _, update := range updates {
		params, err := db.APIInitiativeUpdateToDBUpdate(update, initiativeID)
		if err != nil {
			continue
		}
		if err := r.store.Queries().UpsertInitiativeUpdate(ctx, params); err != nil {
			log.Printf("[repo] upsert initiative update %s failed: %v", update.ID, err)
		}
	}
	return nil
}

// =============================================================================
// Store Access (for sync worker)
// =============================================================================

// Store returns the underlying database store for direct access (e.g., sync worker)
func (r *SQLiteRepository) Store() *db.Store {
	return r.store
}

// SetCurrentUser sets the cached current user (useful for testing)
func (r *SQLiteRepository) SetCurrentUser(user *api.User) {
	r.currentUser = user
}
