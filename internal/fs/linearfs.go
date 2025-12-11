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
	client       *api.Client
	teamCache    *cache.Cache[[]api.Team]
	issueCache   *cache.Cache[[]api.Issue]
	stateCache   *cache.Cache[[]api.State]
	myIssueCache *cache.Cache[[]api.Issue]
	debug        bool
}

func NewLinearFS(cfg *config.Config, debug bool) (*LinearFS, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("LINEAR_API_KEY not set - set env var or add api_key to config file")
	}

	return &LinearFS{
		client:       api.NewClient(cfg.APIKey),
		teamCache:    cache.New[[]api.Team](cfg.Cache.TTL),
		issueCache:   cache.New[[]api.Issue](cfg.Cache.TTL),
		stateCache:   cache.New[[]api.State](cfg.Cache.TTL * 10), // States change rarely
		myIssueCache: cache.New[[]api.Issue](cfg.Cache.TTL),
		debug:        debug,
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
