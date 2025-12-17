package fs

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/cache"
	"github.com/jra3/linear-fuse/internal/config"
	"golang.org/x/sync/singleflight"
)

type LinearFS struct {
	client              *api.Client
	teamCache           *cache.Cache[[]api.Team]
	issueCache          *cache.Cache[[]api.Issue]
	issueByIdCache      *cache.Cache[api.Issue] // Individual issues by identifier (e.g., "ENG-123")
	filteredIssueCache  *cache.Cache[[]api.Issue] // Server-side filtered issues (keys: "status:teamID:value", etc.)
	stateCache          *cache.Cache[[]api.State]
	labelCache          *cache.Cache[[]api.Label]
	cycleCache          *cache.Cache[[]api.Cycle]
	cycleIssueCache     *cache.Cache[[]api.CycleIssue]
	projectCache        *cache.Cache[[]api.Project]
	projectIssueCache   *cache.Cache[[]api.ProjectIssue]
	myIssueCache        *cache.Cache[[]api.Issue]
	myCreatedCache      *cache.Cache[[]api.Issue]
	myActiveCache       *cache.Cache[[]api.Issue]
	userCache           *cache.Cache[[]api.User]
	userIssueCache      *cache.Cache[[]api.Issue]
	commentCache        *cache.Cache[[]api.Comment]
	documentCache       *cache.Cache[[]api.Document]
	debug               bool
	sfGroup             singleflight.Group // Deduplicates concurrent requests for same data
}

func NewLinearFS(cfg *config.Config, debug bool) (*LinearFS, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("LINEAR_API_KEY not set - set env var or add api_key to config file")
	}

	return &LinearFS{
		client:              api.NewClient(cfg.APIKey),
		teamCache:           cache.New[[]api.Team](cfg.Cache.TTL),
		issueCache:          cache.New[[]api.Issue](cfg.Cache.TTL),
		issueByIdCache:      cache.New[api.Issue](cfg.Cache.TTL), // Individual issues for fast lookup
		filteredIssueCache:  cache.New[[]api.Issue](cfg.Cache.TTL), // Server-side filtered issues
		stateCache:          cache.New[[]api.State](cfg.Cache.TTL * 10), // States change rarely
		labelCache:          cache.New[[]api.Label](cfg.Cache.TTL * 10), // Labels change rarely
		cycleCache:          cache.New[[]api.Cycle](cfg.Cache.TTL),      // Cycles change with issues
		cycleIssueCache:     cache.New[[]api.CycleIssue](cfg.Cache.TTL),
		projectCache:        cache.New[[]api.Project](cfg.Cache.TTL),
		projectIssueCache:   cache.New[[]api.ProjectIssue](cfg.Cache.TTL),
		myIssueCache:        cache.New[[]api.Issue](cfg.Cache.TTL),
		myCreatedCache:      cache.New[[]api.Issue](cfg.Cache.TTL),
		myActiveCache:       cache.New[[]api.Issue](cfg.Cache.TTL),
		userCache:           cache.New[[]api.User](cfg.Cache.TTL * 10), // Users change rarely
		userIssueCache:      cache.New[[]api.Issue](cfg.Cache.TTL),
		commentCache:        cache.New[[]api.Comment](cfg.Cache.TTL),
		documentCache:       cache.New[[]api.Document](cfg.Cache.TTL),
		debug:               debug,
	}, nil
}

func (lfs *LinearFS) GetTeams(ctx context.Context) ([]api.Team, error) {
	if teams, ok := lfs.teamCache.Get("teams"); ok {
		return teams, nil
	}

	teams, err := lfs.client.GetTeams(ctx)
	if err != nil {
		return nil, err
	}

	lfs.teamCache.Set("teams", teams)
	return teams, nil
}

func (lfs *LinearFS) GetTeamIssues(ctx context.Context, teamID string) ([]api.Issue, error) {
	cacheKey := "issues:" + teamID
	if issues, ok := lfs.issueCache.Get(cacheKey); ok {
		if lfs.debug {
			log.Printf("[CACHE HIT] GetTeamIssues %s (%d issues)", teamID, len(issues))
		}
		return issues, nil
	}

	if lfs.debug {
		log.Printf("[CACHE MISS] GetTeamIssues %s", teamID)
	}

	// Deduplicate concurrent requests for the same team's issues
	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		// Double-check cache in case another request just populated it
		if issues, ok := lfs.issueCache.Get(cacheKey); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetTeamIssues(ctx, teamID)
		if err != nil {
			return nil, err
		}
		lfs.issueCache.Set(cacheKey, issues)
		// Cache individual issues for fast lookup in IssuesNode.Lookup
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

func (lfs *LinearFS) InvalidateTeamIssues(teamID string) {
	lfs.issueCache.Delete("issues:" + teamID)
}

// cacheIssuesByIdentifier caches individual issues by their identifier for fast lookup
func (lfs *LinearFS) cacheIssuesByIdentifier(issues []api.Issue) {
	for _, issue := range issues {
		lfs.issueByIdCache.Set(issue.Identifier, issue)
	}
}

// GetIssueByIdentifier returns a cached issue by identifier (e.g., "ENG-123")
// Returns nil if not cached - does NOT make API calls
func (lfs *LinearFS) GetIssueByIdentifier(identifier string) *api.Issue {
	if issue, ok := lfs.issueByIdCache.Get(identifier); ok {
		return &issue
	}
	return nil
}

// GetFilteredIssuesByStatus fetches issues filtered by status name using server-side filtering
func (lfs *LinearFS) GetFilteredIssuesByStatus(ctx context.Context, teamID, statusName string) ([]api.Issue, error) {
	cacheKey := "status:" + teamID + ":" + statusName
	if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
		return issues, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetTeamIssuesByStatus(ctx, teamID, statusName)
		if err != nil {
			return nil, err
		}
		lfs.filteredIssueCache.Set(cacheKey, issues)
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

// GetFilteredIssuesByPriority fetches issues filtered by priority using server-side filtering
func (lfs *LinearFS) GetFilteredIssuesByPriority(ctx context.Context, teamID string, priority int) ([]api.Issue, error) {
	cacheKey := fmt.Sprintf("priority:%s:%d", teamID, priority)
	if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
		return issues, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetTeamIssuesByPriority(ctx, teamID, priority)
		if err != nil {
			return nil, err
		}
		lfs.filteredIssueCache.Set(cacheKey, issues)
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

// GetFilteredIssuesByLabel fetches issues filtered by label name using server-side filtering
func (lfs *LinearFS) GetFilteredIssuesByLabel(ctx context.Context, teamID, labelName string) ([]api.Issue, error) {
	cacheKey := "label:" + teamID + ":" + labelName
	if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
		return issues, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetTeamIssuesByLabel(ctx, teamID, labelName)
		if err != nil {
			return nil, err
		}
		lfs.filteredIssueCache.Set(cacheKey, issues)
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

// GetFilteredIssuesByAssignee fetches issues filtered by assignee using server-side filtering
func (lfs *LinearFS) GetFilteredIssuesByAssignee(ctx context.Context, teamID, assigneeID string) ([]api.Issue, error) {
	cacheKey := "assignee:" + teamID + ":" + assigneeID
	if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
		return issues, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetTeamIssuesByAssignee(ctx, teamID, assigneeID)
		if err != nil {
			return nil, err
		}
		lfs.filteredIssueCache.Set(cacheKey, issues)
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

// GetFilteredIssuesUnassigned fetches issues with no assignee using server-side filtering
func (lfs *LinearFS) GetFilteredIssuesUnassigned(ctx context.Context, teamID string) ([]api.Issue, error) {
	cacheKey := "unassigned:" + teamID
	if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
		return issues, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.filteredIssueCache.Get(cacheKey); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetTeamIssuesUnassigned(ctx, teamID)
		if err != nil {
			return nil, err
		}
		lfs.filteredIssueCache.Set(cacheKey, issues)
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

func (lfs *LinearFS) InvalidateMyIssues() {
	lfs.myIssueCache.Delete("my")
	lfs.myCreatedCache.Delete("created")
	lfs.myActiveCache.Delete("active")
}

// ArchiveIssue archives an issue and invalidates all relevant caches
func (lfs *LinearFS) ArchiveIssue(ctx context.Context, issueID string, teamID string, assigneeID string) error {
	err := lfs.client.ArchiveIssue(ctx, issueID)
	if err != nil {
		return err
	}

	// Invalidate all caches that might contain this issue
	lfs.InvalidateTeamIssues(teamID)
	lfs.InvalidateMyIssues()
	if assigneeID != "" {
		lfs.InvalidateUserIssues(assigneeID)
	}

	return nil
}

func (lfs *LinearFS) GetMyIssues(ctx context.Context) ([]api.Issue, error) {
	cacheKey := "my:assigned"
	if issues, ok := lfs.myIssueCache.Get("my"); ok {
		return issues, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.myIssueCache.Get("my"); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetMyIssues(ctx)
		if err != nil {
			return nil, err
		}
		lfs.myIssueCache.Set("my", issues)
		// Cache individual issues for fast symlink resolution
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

func (lfs *LinearFS) GetMyCreatedIssues(ctx context.Context) ([]api.Issue, error) {
	cacheKey := "my:created"
	if issues, ok := lfs.myCreatedCache.Get("created"); ok {
		return issues, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.myCreatedCache.Get("created"); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetMyCreatedIssues(ctx)
		if err != nil {
			return nil, err
		}
		lfs.myCreatedCache.Set("created", issues)
		// Cache individual issues for fast symlink resolution
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

func (lfs *LinearFS) GetMyActiveIssues(ctx context.Context) ([]api.Issue, error) {
	cacheKey := "my:active"
	if issues, ok := lfs.myActiveCache.Get("active"); ok {
		return issues, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.myActiveCache.Get("active"); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetMyActiveIssues(ctx)
		if err != nil {
			return nil, err
		}
		lfs.myActiveCache.Set("active", issues)
		// Cache individual issues for fast symlink resolution
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

func (lfs *LinearFS) GetTeamStates(ctx context.Context, teamID string) ([]api.State, error) {
	cacheKey := "states:" + teamID
	if states, ok := lfs.stateCache.Get(cacheKey); ok {
		return states, nil
	}

	states, err := lfs.client.GetTeamStates(ctx, teamID)
	if err != nil {
		return nil, err
	}

	lfs.stateCache.Set(cacheKey, states)
	return states, nil
}

func (lfs *LinearFS) GetTeamLabels(ctx context.Context, teamID string) ([]api.Label, error) {
	cacheKey := "labels:" + teamID
	if labels, ok := lfs.labelCache.Get(cacheKey); ok {
		return labels, nil
	}

	labels, err := lfs.client.GetTeamLabels(ctx, teamID)
	if err != nil {
		return nil, err
	}

	lfs.labelCache.Set(cacheKey, labels)
	return labels, nil
}

func (lfs *LinearFS) GetTeamCycles(ctx context.Context, teamID string) ([]api.Cycle, error) {
	cacheKey := "cycles:" + teamID
	if cycles, ok := lfs.cycleCache.Get(cacheKey); ok {
		return cycles, nil
	}

	cycles, err := lfs.client.GetTeamCycles(ctx, teamID)
	if err != nil {
		return nil, err
	}

	lfs.cycleCache.Set(cacheKey, cycles)
	return cycles, nil
}

func (lfs *LinearFS) GetCycleIssues(ctx context.Context, cycleID string) ([]api.CycleIssue, error) {
	cacheKey := "cycle-issues:" + cycleID
	if issues, ok := lfs.cycleIssueCache.Get(cacheKey); ok {
		return issues, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.cycleIssueCache.Get(cacheKey); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetCycleIssues(ctx, cycleID)
		if err != nil {
			return nil, err
		}
		lfs.cycleIssueCache.Set(cacheKey, issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.CycleIssue), nil
}

func (lfs *LinearFS) GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error) {
	cacheKey := "projects:" + teamID
	if projects, ok := lfs.projectCache.Get(cacheKey); ok {
		return projects, nil
	}

	projects, err := lfs.client.GetTeamProjects(ctx, teamID)
	if err != nil {
		return nil, err
	}

	lfs.projectCache.Set(cacheKey, projects)
	return projects, nil
}

func (lfs *LinearFS) InvalidateTeamProjects(teamID string) {
	lfs.projectCache.Delete("projects:" + teamID)
}

// CreateProject creates a new project and invalidates the cache
func (lfs *LinearFS) CreateProject(ctx context.Context, input map[string]any) (*api.Project, error) {
	project, err := lfs.client.CreateProject(ctx, input)
	if err != nil {
		return nil, err
	}

	// Invalidate cache for all teams (project can be associated with multiple teams)
	// For simplicity, invalidate all project caches
	if teamIDs, ok := input["teamIds"].([]string); ok {
		for _, teamID := range teamIDs {
			lfs.InvalidateTeamProjects(teamID)
		}
	}

	return project, nil
}

// ArchiveProject archives a project and invalidates the cache
func (lfs *LinearFS) ArchiveProject(ctx context.Context, projectID string, teamID string) error {
	err := lfs.client.ArchiveProject(ctx, projectID)
	if err != nil {
		return err
	}

	lfs.InvalidateTeamProjects(teamID)
	return nil
}

func (lfs *LinearFS) GetProjectIssues(ctx context.Context, projectID string) ([]api.ProjectIssue, error) {
	cacheKey := "project-issues:" + projectID
	if issues, ok := lfs.projectIssueCache.Get(cacheKey); ok {
		return issues, nil
	}

	issues, err := lfs.client.GetProjectIssues(ctx, projectID)
	if err != nil {
		return nil, err
	}

	lfs.projectIssueCache.Set(cacheKey, issues)
	return issues, nil
}

func (lfs *LinearFS) GetUsers(ctx context.Context) ([]api.User, error) {
	if users, ok := lfs.userCache.Get("users"); ok {
		return users, nil
	}

	users, err := lfs.client.GetUsers(ctx)
	if err != nil {
		return nil, err
	}

	// Filter to active users only
	active := make([]api.User, 0, len(users))
	for _, u := range users {
		if u.Active {
			active = append(active, u)
		}
	}

	lfs.userCache.Set("users", active)
	return active, nil
}

func (lfs *LinearFS) GetUserIssues(ctx context.Context, userID string) ([]api.Issue, error) {
	cacheKey := "user-issues:" + userID
	if issues, ok := lfs.userIssueCache.Get(cacheKey); ok {
		if lfs.debug {
			log.Printf("[CACHE HIT] GetUserIssues %s (%d issues)", userID, len(issues))
		}
		return issues, nil
	}

	if lfs.debug {
		log.Printf("[CACHE MISS] GetUserIssues %s", userID)
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if issues, ok := lfs.userIssueCache.Get(cacheKey); ok {
			return issues, nil
		}
		issues, err := lfs.client.GetUserIssues(ctx, userID)
		if err != nil {
			return nil, err
		}
		lfs.userIssueCache.Set(cacheKey, issues)
		// Cache individual issues for fast symlink resolution
		lfs.cacheIssuesByIdentifier(issues)
		return issues, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Issue), nil
}

func (lfs *LinearFS) InvalidateUserIssues(userID string) {
	lfs.userIssueCache.Delete("user-issues:" + userID)
}

func (lfs *LinearFS) GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error) {
	cacheKey := "comments:" + issueID
	if comments, ok := lfs.commentCache.Get(cacheKey); ok {
		return comments, nil
	}

	result, err, _ := lfs.sfGroup.Do(cacheKey, func() (interface{}, error) {
		if comments, ok := lfs.commentCache.Get(cacheKey); ok {
			return comments, nil
		}
		comments, err := lfs.client.GetIssueComments(ctx, issueID)
		if err != nil {
			return nil, err
		}
		lfs.commentCache.Set(cacheKey, comments)
		return comments, nil
	})

	if err != nil {
		return nil, err
	}
	return result.([]api.Comment), nil
}

func (lfs *LinearFS) InvalidateIssueComments(issueID string) {
	lfs.commentCache.Delete("comments:" + issueID)
}

// TryGetCachedComments returns cached comments if available, without making API calls
func (lfs *LinearFS) TryGetCachedComments(issueID string) ([]api.Comment, bool) {
	return lfs.commentCache.Get("comments:" + issueID)
}

func (lfs *LinearFS) CreateComment(ctx context.Context, issueID string, body string) (*api.Comment, error) {
	comment, err := lfs.client.CreateComment(ctx, issueID, body)
	if err != nil {
		return nil, err
	}

	// Invalidate cache so next read shows the new comment
	lfs.InvalidateIssueComments(issueID)
	return comment, nil
}

func (lfs *LinearFS) UpdateComment(ctx context.Context, issueID string, commentID string, body string) (*api.Comment, error) {
	comment, err := lfs.client.UpdateComment(ctx, commentID, body)
	if err != nil {
		return nil, err
	}

	lfs.InvalidateIssueComments(issueID)
	return comment, nil
}

func (lfs *LinearFS) DeleteComment(ctx context.Context, issueID string, commentID string) error {
	err := lfs.client.DeleteComment(ctx, commentID)
	if err != nil {
		return err
	}

	lfs.InvalidateIssueComments(issueID)
	return nil
}

// Document methods

func (lfs *LinearFS) GetIssueDocuments(ctx context.Context, issueID string) ([]api.Document, error) {
	cacheKey := "docs:issue:" + issueID
	if docs, ok := lfs.documentCache.Get(cacheKey); ok {
		return docs, nil
	}

	docs, err := lfs.client.GetIssueDocuments(ctx, issueID)
	if err != nil {
		return nil, err
	}

	lfs.documentCache.Set(cacheKey, docs)
	return docs, nil
}

func (lfs *LinearFS) GetTeamDocuments(ctx context.Context, teamID string) ([]api.Document, error) {
	cacheKey := "docs:team:" + teamID
	if docs, ok := lfs.documentCache.Get(cacheKey); ok {
		return docs, nil
	}

	docs, err := lfs.client.GetTeamDocuments(ctx, teamID)
	if err != nil {
		return nil, err
	}

	lfs.documentCache.Set(cacheKey, docs)
	return docs, nil
}

func (lfs *LinearFS) GetProjectDocuments(ctx context.Context, projectID string) ([]api.Document, error) {
	cacheKey := "docs:project:" + projectID
	if docs, ok := lfs.documentCache.Get(cacheKey); ok {
		return docs, nil
	}

	docs, err := lfs.client.GetProjectDocuments(ctx, projectID)
	if err != nil {
		return nil, err
	}

	lfs.documentCache.Set(cacheKey, docs)
	return docs, nil
}

func (lfs *LinearFS) InvalidateIssueDocuments(issueID string) {
	lfs.documentCache.Delete("docs:issue:" + issueID)
}

// TryGetCachedIssueDocuments returns cached documents if available, without making API calls
func (lfs *LinearFS) TryGetCachedIssueDocuments(issueID string) ([]api.Document, bool) {
	return lfs.documentCache.Get("docs:issue:" + issueID)
}

// TryGetCachedProjectDocuments returns cached documents if available, without making API calls
func (lfs *LinearFS) TryGetCachedProjectDocuments(projectID string) ([]api.Document, bool) {
	return lfs.documentCache.Get("docs:project:" + projectID)
}

func (lfs *LinearFS) InvalidateTeamDocuments(teamID string) {
	lfs.documentCache.Delete("docs:team:" + teamID)
}

func (lfs *LinearFS) InvalidateProjectDocuments(projectID string) {
	lfs.documentCache.Delete("docs:project:" + projectID)
}

func (lfs *LinearFS) CreateDocument(ctx context.Context, input map[string]any) (*api.Document, error) {
	doc, err := lfs.client.CreateDocument(ctx, input)
	if err != nil {
		return nil, err
	}

	// Invalidate relevant caches based on what parent the document belongs to
	if issueID, ok := input["issueId"].(string); ok {
		lfs.InvalidateIssueDocuments(issueID)
	}
	if teamID, ok := input["teamId"].(string); ok {
		lfs.InvalidateTeamDocuments(teamID)
	}
	if projectID, ok := input["projectId"].(string); ok {
		lfs.InvalidateProjectDocuments(projectID)
	}

	return doc, nil
}

func (lfs *LinearFS) UpdateDocument(ctx context.Context, documentID string, input map[string]any, issueID, teamID, projectID string) error {
	err := lfs.client.UpdateDocument(ctx, documentID, input)
	if err != nil {
		return err
	}

	// Invalidate relevant caches
	if issueID != "" {
		lfs.InvalidateIssueDocuments(issueID)
	}
	if teamID != "" {
		lfs.InvalidateTeamDocuments(teamID)
	}
	if projectID != "" {
		lfs.InvalidateProjectDocuments(projectID)
	}

	return nil
}

func (lfs *LinearFS) DeleteDocument(ctx context.Context, documentID string, issueID, teamID, projectID string) error {
	err := lfs.client.DeleteDocument(ctx, documentID)
	if err != nil {
		return err
	}

	// Invalidate relevant caches
	if issueID != "" {
		lfs.InvalidateIssueDocuments(issueID)
	}
	if teamID != "" {
		lfs.InvalidateTeamDocuments(teamID)
	}
	if projectID != "" {
		lfs.InvalidateProjectDocuments(projectID)
	}

	return nil
}

// ResolveUserID converts an email or name to a user ID
func (lfs *LinearFS) ResolveUserID(ctx context.Context, identifier string) (string, error) {
	users, err := lfs.GetUsers(ctx)
	if err != nil {
		return "", err
	}

	// Try exact email match first
	for _, user := range users {
		if user.Email == identifier {
			return user.ID, nil
		}
	}

	// Try case-insensitive email match
	lowerID := strings.ToLower(identifier)
	for _, user := range users {
		if strings.ToLower(user.Email) == lowerID {
			return user.ID, nil
		}
	}

	// Try name match
	for _, user := range users {
		if user.Name == identifier || user.DisplayName == identifier {
			return user.ID, nil
		}
	}

	// Try case-insensitive name match
	for _, user := range users {
		if strings.ToLower(user.Name) == lowerID || strings.ToLower(user.DisplayName) == lowerID {
			return user.ID, nil
		}
	}

	return "", fmt.Errorf("unknown user: %s", identifier)
}

// ResolveStateID converts a state name to its ID for a given team
func (lfs *LinearFS) ResolveStateID(ctx context.Context, teamID string, stateName string) (string, error) {
	states, err := lfs.GetTeamStates(ctx, teamID)
	if err != nil {
		return "", err
	}

	// Try exact match first
	for _, state := range states {
		if state.Name == stateName {
			return state.ID, nil
		}
	}

	// Try case-insensitive match
	lowerName := strings.ToLower(stateName)
	for _, state := range states {
		if strings.ToLower(state.Name) == lowerName {
			return state.ID, nil
		}
	}

	return "", fmt.Errorf("unknown state: %s", stateName)
}

// ResolveLabelIDs converts label names to their IDs for a given team
// Returns the list of label IDs and any labels that couldn't be resolved
func (lfs *LinearFS) ResolveLabelIDs(ctx context.Context, teamID string, labelNames []string) ([]string, []string, error) {
	labels, err := lfs.GetTeamLabels(ctx, teamID)
	if err != nil {
		return nil, nil, err
	}

	// Build lookup map (case-insensitive)
	labelMap := make(map[string]string) // lowercase name -> ID
	for _, label := range labels {
		labelMap[strings.ToLower(label.Name)] = label.ID
	}

	var ids []string
	var notFound []string

	for _, name := range labelNames {
		if id, ok := labelMap[strings.ToLower(name)]; ok {
			ids = append(ids, id)
		} else {
			notFound = append(notFound, name)
		}
	}

	return ids, notFound, nil
}

// InvalidateTeamLabels clears the label cache for a team
func (lfs *LinearFS) InvalidateTeamLabels(teamID string) {
	lfs.labelCache.Delete("labels:" + teamID)
}

// CreateLabel creates a new label and invalidates the cache
func (lfs *LinearFS) CreateLabel(ctx context.Context, input map[string]any) (*api.Label, error) {
	label, err := lfs.client.CreateLabel(ctx, input)
	if err != nil {
		return nil, err
	}

	// Invalidate cache for the team
	if teamID, ok := input["teamId"].(string); ok {
		lfs.InvalidateTeamLabels(teamID)
	}

	return label, nil
}

// UpdateLabel updates a label and invalidates the cache
func (lfs *LinearFS) UpdateLabel(ctx context.Context, labelID string, input map[string]any, teamID string) error {
	_, err := lfs.client.UpdateLabel(ctx, labelID, input)
	if err != nil {
		return err
	}

	lfs.InvalidateTeamLabels(teamID)
	return nil
}

// DeleteLabel deletes a label and invalidates the cache
func (lfs *LinearFS) DeleteLabel(ctx context.Context, labelID string, teamID string) error {
	err := lfs.client.DeleteLabel(ctx, labelID)
	if err != nil {
		return err
	}

	lfs.InvalidateTeamLabels(teamID)
	return nil
}

func Mount(mountpoint string, cfg *config.Config, debug bool) (*fuse.Server, error) {
	lfs, err := NewLinearFS(cfg, debug)
	if err != nil {
		return nil, err
	}

	root := &RootNode{lfs: lfs}

	// Use longer timeouts to reduce kernel→userspace calls
	// AttrTimeout: how long kernel caches file attributes (size, mtime, etc.)
	// EntryTimeout: how long kernel caches directory entry lookups
	attrTimeout := 60 * time.Second  // Attributes change less often
	entryTimeout := 30 * time.Second // Directory contents change more often

	opts := &fs.Options{
		AttrTimeout:  &attrTimeout,
		EntryTimeout: &entryTimeout,
		MountOptions: fuse.MountOptions{
			Name:   "linearfs",
			FsName: "linear",
			Debug:  debug,
		},
	}

	if debug {
		log.Println("Mounting with debug enabled")
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		return nil, err
	}

	return server, nil
}
