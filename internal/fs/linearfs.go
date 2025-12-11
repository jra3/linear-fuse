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
)

type LinearFS struct {
	client            *api.Client
	teamCache         *cache.Cache[[]api.Team]
	issueCache        *cache.Cache[[]api.Issue]
	stateCache        *cache.Cache[[]api.State]
	labelCache        *cache.Cache[[]api.Label]
	cycleCache        *cache.Cache[[]api.Cycle]
	projectCache      *cache.Cache[[]api.Project]
	projectIssueCache *cache.Cache[[]api.ProjectIssue]
	myIssueCache      *cache.Cache[[]api.Issue]
	userCache         *cache.Cache[[]api.User]
	userIssueCache    *cache.Cache[[]api.Issue]
	debug             bool
}

func NewLinearFS(cfg *config.Config, debug bool) (*LinearFS, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("LINEAR_API_KEY not set - set env var or add api_key to config file")
	}

	return &LinearFS{
		client:            api.NewClient(cfg.APIKey),
		teamCache:         cache.New[[]api.Team](cfg.Cache.TTL),
		issueCache:        cache.New[[]api.Issue](cfg.Cache.TTL),
		stateCache:        cache.New[[]api.State](cfg.Cache.TTL * 10), // States change rarely
		labelCache:        cache.New[[]api.Label](cfg.Cache.TTL * 10), // Labels change rarely
		cycleCache:        cache.New[[]api.Cycle](cfg.Cache.TTL),      // Cycles change with issues
		projectCache:      cache.New[[]api.Project](cfg.Cache.TTL),
		projectIssueCache: cache.New[[]api.ProjectIssue](cfg.Cache.TTL),
		myIssueCache:      cache.New[[]api.Issue](cfg.Cache.TTL),
		userCache:         cache.New[[]api.User](cfg.Cache.TTL * 10), // Users change rarely
		userIssueCache:    cache.New[[]api.Issue](cfg.Cache.TTL),
		debug:             debug,
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
		return issues, nil
	}

	issues, err := lfs.client.GetTeamIssues(ctx, teamID)
	if err != nil {
		return nil, err
	}

	lfs.issueCache.Set(cacheKey, issues)
	return issues, nil
}

func (lfs *LinearFS) InvalidateTeamIssues(teamID string) {
	lfs.issueCache.Delete("issues:" + teamID)
}

func (lfs *LinearFS) InvalidateMyIssues() {
	lfs.myIssueCache.Delete("my")
}

func (lfs *LinearFS) GetMyIssues(ctx context.Context) ([]api.Issue, error) {
	if issues, ok := lfs.myIssueCache.Get("my"); ok {
		return issues, nil
	}

	issues, err := lfs.client.GetMyIssues(ctx)
	if err != nil {
		return nil, err
	}

	lfs.myIssueCache.Set("my", issues)
	return issues, nil
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
		return issues, nil
	}

	issues, err := lfs.client.GetUserIssues(ctx, userID)
	if err != nil {
		return nil, err
	}

	lfs.userIssueCache.Set(cacheKey, issues)
	return issues, nil
}

func (lfs *LinearFS) InvalidateUserIssues(userID string) {
	lfs.userIssueCache.Delete("user-issues:" + userID)
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

func Mount(mountpoint string, cfg *config.Config, debug bool) (*fuse.Server, error) {
	lfs, err := NewLinearFS(cfg, debug)
	if err != nil {
		return nil, err
	}

	root := &RootNode{lfs: lfs}

	// Use longer timeouts to reduce kernel→userspace calls
	timeout := 30 * time.Second

	opts := &fs.Options{
		AttrTimeout:  &timeout,
		EntryTimeout: &timeout,
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
