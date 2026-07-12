package fs

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	gosync "sync"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/repo"
	"github.com/jra3/linear-fuse/internal/sync"
	"github.com/jra3/linear-fuse/internal/telemetry"
)

// IssueError represents a validation error from a failed write operation

// LinearFS implements a FUSE filesystem backed by Linear.
// Read operations go through the Repository (SQLite + on-demand API fetch).
// Write operations (mutations) use the API Client directly.
type LinearFS struct {
	client       *api.Client    // Reads (on-demand fetch) + infrastructure (stats, viewer, close)
	mutatorImpl  MutationClient // Mutations only; defaults to client, swappable for tests
	verifierImpl verifyReader   // Read-your-writes re-fetch; defaults to client, swappable for tests
	mutatorMu    gosync.RWMutex // guards mutatorImpl + verifierImpl + catalogRefreshImpl (handlers read while tests swap)

	// catalogRefreshImpl is the validation-failure catalog-refresh seam (#246):
	// how a name→ID resolution miss refreshes its catalog before the one retry
	// (see catalogrefresh.go). nil selects the default, sync-worker-backed
	// refreshCatalogViaSync; tests swap in a stub via InjectTestCatalogRefresher
	// so offline suites stay network-free.
	catalogRefreshImpl func(ctx context.Context, kind CatalogKind, scopeID string) error

	repo       *repo.SQLiteRepository // For all read operations
	store      *db.Store              // SQLite store (owned by repo, kept for sync worker)
	syncWorker *sync.Worker           // Background sync worker
	requestLog io.Closer              // per-request debug log writer (nil when disabled); closed in Close
	debug      bool
	uid        uint32 // Owner UID for files/dirs
	gid        uint32 // Owner GID for files/dirs
	mountPoint string // Filesystem mount path (for README generation)

	// Mount lifetime: every background goroutine LinearFS launches derives its
	// ctx from lifeCtx via spawn, so Close can cancel + wait before tearing
	// down the store the goroutines read (see spawn / Close).
	lifeCtx    context.Context
	lifeCancel context.CancelFunc
	lifeWG     gosync.WaitGroup
	lifeMu     gosync.Mutex // guards lifeClosed vs. spawn's wg.Add (Add-vs-Wait race)
	lifeClosed bool         // set at the start of Close; spawn declines after this

	// The one coupling to the FUSE server: kernel-cache invalidation (see
	// invalidate.go). Embedded, so lfs.SetServer / lfs.InvalidateCreated / … promote.
	kernelNotify

	// Embedded-file bytes (memory→disk→CDN); see embeddedfilecache.go.
	// Embedded, so lfs.FetchEmbeddedFile promotes — consistent with the two
	// sibling sub-modules above.
	*embeddedFileCache

	// .error / .last state for every writable surface (see writefeedback.go).
	// Embedded, so lfs.SetWriteError / lfs.AppendWriteSuccess / … promote.
	writeFeedback
}

// BaseNode provides common functionality for all LinearFS nodes.
// All node types should embed this instead of fs.Inode directly.
// This ensures consistent UID/GID ownership across the filesystem.
type BaseNode struct {
	fs.Inode
	lfs *LinearFS
}

// SetOwner sets the UID and GID on the given AttrOut.
// Call this in every Getattr implementation.
func (b *BaseNode) SetOwner(out *fuse.AttrOut) {
	b.setOwnerAttr(&out.Attr)
}

// setOwnerAttr is SetOwner for a bare fuse.Attr (Lookup EntryOut fills).
func (b *BaseNode) setOwnerAttr(attr *fuse.Attr) {
	if b.lfs != nil {
		attr.Uid = b.lfs.uid
		attr.Gid = b.lfs.gid
	}
}

// LFS returns the LinearFS instance.
func (b *BaseNode) LFS() *LinearFS {
	return b.lfs
}

func NewLinearFS(cfg *config.Config, debug bool) (*LinearFS, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("LINEAR_API_KEY not set - set env var or add api_key to config file")
	}

	// Get current user's UID/GID for file ownership
	uid := uint32(os.Getuid())
	gid := uint32(os.Getgid())

	client := api.NewClient(cfg.APIKey)

	// Optional per-request JSONL debug log (telemetry.requests.*, default
	// off). Wired at client construction — the config lives under telemetry
	// but the client is born here, not in cmd. Failure to open it must never
	// block mounting: log and continue without it.
	var requestLog io.Closer
	if w, err := telemetry.NewRequestLog(cfg.Telemetry.Requests); err != nil {
		log.Printf("[linearfs] request log disabled: %v", err)
	} else if w != nil {
		client.SetRequestLog(w)
		requestLog = w
	}

	// Initialize file cache directory
	cacheDir := embeddedFileCacheDir()
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[linearfs] Warning: failed to create cache dir: %v", err)
	}

	lfs := &LinearFS{
		uid:          uid,
		gid:          gid,
		client:       client,
		mutatorImpl:  client,
		verifierImpl: client,
		requestLog:   requestLog,
		debug:        debug,
	}
	// Mint the mount-lifetime context. Background is correct here: the mount's
	// lifetime is bounded by Close, not by any caller's request ctx.
	lfs.lifeCtx, lfs.lifeCancel = context.WithCancel(context.Background())
	// Wire the feedback store's kernel-cache seam to this instance. The method
	// value binds the pointer, so it is safe to set after lfs exists.
	lfs.writeFeedback = newWriteFeedback(lfs.InvalidateUpdated)
	// The embedded-file cache's seams are late-bound: repo is wired later (in
	// EnableSQLiteCache), so persist reads lfs.repo at call time — and no-ops
	// while it is still nil (a fetch before the cache is enabled).
	lfs.embeddedFileCache = newEmbeddedFileCache(cacheDir,
		api.NewCDNClient(func() string { return lfs.client.AuthHeader() }),
		func(ctx context.Context, fileID, path string, size int64) error {
			if lfs.repo == nil {
				return nil
			}
			return lfs.repo.UpdateEmbeddedFileCache(ctx, fileID, path, size)
		},
	)
	return lfs, nil
}

// spawn launches fn as a background goroutine bound to the mount lifetime:
// fn receives lifeCtx (cancelled at the start of Close) and Close waits for it
// to return before closing the store. Once Close has begun, spawn declines to
// start fn at all — checking the closed flag and calling wg.Add under the same
// mutex is what makes Add ordered before Close's Wait (the classic WaitGroup
// Add-vs-Wait race). This is the ONLY way LinearFS may start a goroutine that
// outlives its caller.
func (lfs *LinearFS) spawn(fn func(ctx context.Context)) {
	lfs.lifeMu.Lock()
	defer lfs.lifeMu.Unlock()
	if lfs.lifeClosed || lfs.lifeCtx == nil || lfs.lifeCtx.Err() != nil {
		return // shutdown has begun; the work would only race the store teardown
	}
	lfs.lifeWG.Add(1)
	go func() {
		defer lfs.lifeWG.Done()
		fn(lfs.lifeCtx)
	}()
}

// Close stops all background operations and releases resources
func (lfs *LinearFS) Close() {
	// Cancel the mount-lifetime ctx and wait for every spawned goroutine.
	// Cancelling BEFORE syncWorker.Stop is deliberate: the worker's ctx
	// derives from lifeCtx, so a mid-flight sync cycle aborts promptly
	// instead of Stop waiting it out.
	lfs.lifeMu.Lock()
	lfs.lifeClosed = true
	if lfs.lifeCancel != nil {
		lfs.lifeCancel()
	}
	lfs.lifeMu.Unlock()
	lfs.lifeWG.Wait()
	// Stop sync worker first
	if lfs.syncWorker != nil {
		lfs.syncWorker.Stop()
	}
	// Close repository (stops background refresh goroutines)
	if lfs.repo != nil {
		lfs.repo.Close()
	}
	// Close SQLite store
	if lfs.store != nil {
		lfs.store.Close()
	}
	// Release the request debug log writer (no more queries after this)
	if lfs.requestLog != nil {
		_ = lfs.requestLog.Close()
	}
}

// EnableSQLiteCache initializes the SQLite backend and starts background sync.
// This MUST be called after creating LinearFS but before mounting.
// If dbPath is empty, uses the default path (~/.config/linearfs/cache.db).
// Everything here runs under lifeCtx (the mount lifetime) — a caller ctx would
// be wrong, since the background work it starts must outlive the caller and
// die with Close instead.
func (lfs *LinearFS) EnableSQLiteCache(dbPath string) error {
	if dbPath == "" {
		dbPath = db.DefaultDBPath()
	}

	store, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}

	lfs.store = store

	// Create repository with API client for on-demand fetching
	lfs.repo = repo.NewSQLiteRepository(store, lfs.client)

	// H-1: Load viewer from SQLite cache immediately for /my views (no API wait)
	if cachedViewerID, err := store.Queries().GetViewerUserID(lfs.lifeCtx); err == nil {
		if dbUser, err := store.Queries().GetUser(lfs.lifeCtx, cachedViewerID); err == nil {
			apiUser := db.DBUserToAPIUser(dbUser)
			lfs.repo.SetCurrentUser(&apiUser)
			log.Printf("[sqlite] Loaded cached viewer: %s (%s)", apiUser.Email, apiUser.ID)
		}
	}

	// Refresh viewer from API in background to keep cache fresh. Spawned under
	// the mount lifetime so Close cancels + waits for it — with a bare
	// Background ctx this loop's 60s backoff could outlive store.Close and
	// retry against a closed store.
	lfs.spawn(func(ctx context.Context) {
		delays := []time.Duration{0, 5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}
		for i := 0; ; i++ {
			delay := delays[min(i, len(delays)-1)]
			if delay > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
			}
			v, err := lfs.client.GetViewer(ctx)
			if err != nil {
				if i == 0 {
					log.Printf("[sqlite] Warning: failed to get viewer: %v", err)
				} else {
					log.Printf("[sqlite] Warning: failed to get viewer (retry %d): %v", i, err)
				}
				if i == 0 {
					continue // retry immediately after first failure
				}
				continue
			}
			if v != nil {
				lfs.repo.SetCurrentUser(v)
				// Persist viewer ID so next startup is instant
				if err := store.Queries().SetViewerUserID(ctx, db.SetViewerUserIDParams{
					UserID:   v.ID,
					SyncedAt: db.Now(),
				}); err != nil {
					log.Printf("[sqlite] Warning: failed to persist viewer: %v", err)
				}
				log.Printf("[sqlite] Current user: %s (%s)", v.Email, v.ID)
			}
			return
		}
	})

	// Create and start sync worker. The worker keeps its own stop mechanism;
	// it merely derives its ctx from the mount lifetime now, so Close's
	// cancel aborts a mid-flight sync cycle before Stop is even called.
	lfs.syncWorker = sync.NewWorker(lfs.client, store, sync.DefaultConfig())
	lfs.syncWorker.SetBudgetReporter(lfs.client)
	lfs.syncWorker.SetCatchUpModeToggler(lfs.repo)
	lfs.syncWorker.SetIssueIDReconciler(lfs.repo)
	lfs.syncWorker.Start(lfs.lifeCtx)

	log.Printf("[sqlite] Enabled persistent cache at %s", dbPath)
	return nil
}

// HasSQLiteCache returns true if SQLite backend is enabled
func (lfs *LinearFS) HasSQLiteCache() bool {
	return lfs.repo != nil
}

// MountPoint returns the filesystem mount path
func (lfs *LinearFS) MountPoint() string {
	if lfs.mountPoint == "" {
		return os.Getenv("HOME") + "/linear" // fallback for tests
	}
	return lfs.mountPoint
}

// UpsertIssue inserts or updates an issue in SQLite.
// This is primarily for testing - allows tests to make API-created issues
// immediately visible in the filesystem without waiting for sync.
func (lfs *LinearFS) UpsertIssue(ctx context.Context, issue api.Issue) error {
	if lfs.store == nil {
		return fmt.Errorf("SQLite not enabled")
	}
	issueData, err := db.APIIssueToDBIssue(issue)
	if err != nil {
		return err
	}
	return lfs.store.Queries().UpsertIssue(ctx, issueData.ToUpsertParams())
}

// UpsertComment inserts or updates a comment in SQLite.
func (lfs *LinearFS) UpsertComment(ctx context.Context, issueID string, comment api.Comment) error {
	if lfs.store == nil {
		return nil // SQLite not enabled, skip silently
	}
	params, err := db.APICommentToDBComment(comment, issueID)
	if err != nil {
		return err
	}
	return lfs.store.Queries().UpsertComment(ctx, params)
}

// UpsertDocument inserts or updates a document in SQLite.
func (lfs *LinearFS) UpsertDocument(ctx context.Context, document api.Document) error {
	if lfs.store == nil {
		return nil // SQLite not enabled, skip silently
	}
	params, err := db.APIDocumentToDBDocument(document)
	if err != nil {
		return err
	}
	return lfs.store.Queries().UpsertDocument(ctx, params)
}

// UpsertLabel inserts or updates a label in SQLite.
func (lfs *LinearFS) UpsertLabel(ctx context.Context, teamID string, label api.Label) error {
	if lfs.store == nil {
		return nil // SQLite not enabled, skip silently
	}
	// A label created/edited under teams/{KEY}/labels is team-scoped; if the
	// mutation response didn't carry team, stamp the known context so it isn't
	// mistaken for a workspace label. A workspace label keeps team.
	if label.Team == nil && teamID != "" {
		label.Team = &api.Team{ID: teamID}
	}
	params, err := db.APILabelToDBLabel(label)
	if err != nil {
		return err
	}
	return lfs.store.Queries().UpsertLabel(ctx, params)
}

// UpsertProject inserts or updates a project in SQLite.
func (lfs *LinearFS) UpsertProject(ctx context.Context, teamID string, project api.Project) error {
	if lfs.store == nil {
		return nil // SQLite not enabled, skip silently
	}
	params, err := db.APIProjectToDBProject(project)
	if err != nil {
		return err
	}
	if err := lfs.store.Queries().UpsertProject(ctx, params); err != nil {
		return err
	}
	// Also create project-team association
	return lfs.store.Queries().UpsertProjectTeam(ctx, db.UpsertProjectTeamParams{
		ProjectID: project.ID,
		TeamID:    teamID,
		SyncedAt:  db.Now(),
	})
}

// UpsertProjectUpdate inserts or updates a project update in SQLite.
func (lfs *LinearFS) UpsertProjectUpdate(ctx context.Context, projectID string, update api.ProjectUpdate) error {
	if lfs.store == nil {
		return nil // SQLite not enabled, skip silently
	}
	params, err := db.APIProjectUpdateToDBUpdate(update, projectID)
	if err != nil {
		return err
	}
	return lfs.store.Queries().UpsertProjectUpdate(ctx, params)
}

// UpsertInitiativeUpdate inserts or updates an initiative update in SQLite.
func (lfs *LinearFS) UpsertInitiativeUpdate(ctx context.Context, initiativeID string, update api.InitiativeUpdate) error {
	if lfs.store == nil {
		return nil // SQLite not enabled, skip silently
	}
	params, err := db.APIInitiativeUpdateToDBUpdate(update, initiativeID)
	if err != nil {
		return err
	}
	return lfs.store.Queries().UpsertInitiativeUpdate(ctx, params)
}

// UpsertInitiative inserts or updates an initiative in SQLite.
func (lfs *LinearFS) UpsertInitiative(ctx context.Context, initiative api.Initiative) error {
	if lfs.store == nil {
		return nil // SQLite not enabled, skip silently
	}
	params, err := db.APIInitiativeToDBInitiative(initiative)
	if err != nil {
		return err
	}
	return lfs.store.Queries().UpsertInitiative(ctx, params)
}

// UpsertProjectMilestone inserts or updates a milestone in SQLite for immediate
// visibility after a mutation (the write handler owns the upsert, per the
// API/DB decoupling principle).
func (lfs *LinearFS) UpsertProjectMilestone(ctx context.Context, projectID string, milestone api.ProjectMilestone) error {
	if lfs.store == nil {
		return nil // SQLite not enabled, skip silently
	}
	params, err := db.APIProjectMilestoneToDBMilestone(milestone, projectID)
	if err != nil {
		return err
	}
	return lfs.store.Queries().UpsertProjectMilestone(ctx, params)
}

// GetIssueByIdentifier returns an issue by identifier (e.g., "ENG-123")
func (lfs *LinearFS) GetIssueByIdentifier(identifier string) *api.Issue {
	issue, err := lfs.repo.GetIssueByIdentifier(context.Background(), identifier)
	if err != nil {
		return nil
	}
	return issue
}

// FetchIssueByIdentifier retrieves an issue by identifier with on-demand fetching.
func (lfs *LinearFS) FetchIssueByIdentifier(ctx context.Context, identifier string) (*api.Issue, error) {
	issue, err := lfs.repo.GetIssueByIdentifier(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, fmt.Errorf("issue not found: %s", identifier)
	}
	return issue, nil
}

// GetFilteredIssuesByStatus fetches issues filtered by status name
func (lfs *LinearFS) GetFilteredIssuesByStatus(ctx context.Context, teamID, statusName string) ([]api.Issue, error) {
	state, err := lfs.repo.GetStateByName(ctx, teamID, statusName)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return []api.Issue{}, nil
	}
	return lfs.repo.GetIssuesByState(ctx, teamID, state.ID)
}

// GetFilteredIssuesByLabel fetches issues filtered by label name
func (lfs *LinearFS) GetFilteredIssuesByLabel(ctx context.Context, teamID, labelName string) ([]api.Issue, error) {
	label, err := lfs.repo.GetLabelByName(ctx, teamID, labelName)
	if err != nil {
		return nil, err
	}
	if label == nil {
		return []api.Issue{}, nil
	}
	return lfs.repo.GetIssuesByLabel(ctx, teamID, label.ID)
}

// GetCycleIssues returns issues in a cycle as CycleIssue
// Uses repository and converts to CycleIssue for symlink display
func (lfs *LinearFS) GetCycleIssues(ctx context.Context, cycleID string) ([]api.CycleIssue, error) {
	issues, err := lfs.repo.GetIssuesByCycle(ctx, cycleID)
	if err != nil {
		return nil, err
	}
	// Convert to CycleIssue (minimal type for directory listing)
	result := make([]api.CycleIssue, len(issues))
	for i, issue := range issues {
		result[i] = api.CycleIssue{
			ID:         issue.ID,
			Identifier: issue.Identifier,
			Title:      issue.Title,
			CreatedAt:  issue.CreatedAt,
			UpdatedAt:  issue.UpdatedAt,
		}
	}
	return result, nil
}

// GetProjectIssues returns issues in a project as ProjectIssue
// Uses repository and converts to ProjectIssue for symlink display
func (lfs *LinearFS) GetProjectIssues(ctx context.Context, projectID string) ([]api.ProjectIssue, error) {
	issues, err := lfs.repo.GetIssuesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	// Convert to ProjectIssue (minimal type for directory listing)
	result := make([]api.ProjectIssue, len(issues))
	for i, issue := range issues {
		result[i] = api.ProjectIssue{
			ID:         issue.ID,
			Identifier: issue.Identifier,
			Title:      issue.Title,
			CreatedAt:  issue.CreatedAt,
			UpdatedAt:  issue.UpdatedAt,
			Team:       issue.Team,
		}
	}
	return result, nil
}

// TryGetCachedComments returns comments from SQLite
func (lfs *LinearFS) TryGetCachedComments(issueID string) ([]api.Comment, bool) {
	comments, err := lfs.repo.GetIssueComments(context.Background(), issueID)
	if err != nil {
		return nil, false
	}
	return comments, len(comments) > 0
}

func (lfs *LinearFS) UpdateComment(ctx context.Context, issueID string, commentID string, body string) (*api.Comment, error) {
	return lfs.mutator().UpdateComment(ctx, commentID, body)
}

// GetTeamDocuments returns documents for a team (currently via API as not
// synced). This is a synchronous API call on a live FUSE path — the caller's
// user is blocked on it — so it promotes to the interactive budget tier.
func (lfs *LinearFS) GetTeamDocuments(ctx context.Context, teamID string) ([]api.Document, error) {
	return lfs.client.GetTeamDocuments(api.WithInteractive(ctx), teamID)
}

func (lfs *LinearFS) UpdateDocument(ctx context.Context, documentID string, input map[string]any, issueID, teamID, projectID string) (*api.Document, error) {
	return lfs.mutator().UpdateDocument(ctx, documentID, input)
}

// ResolveUserID converts an email or name to a user ID. A local catalog miss
// triggers one targeted refresh + retry (see catalogrefresh.go).
func (lfs *LinearFS) ResolveUserID(ctx context.Context, identifier string) (string, error) {
	return lfs.resolveWithRefresh(ctx, CatalogUsers, "", func() (string, error) {
		return lfs.lookupUserID(ctx, identifier)
	})
}

// lookupUserID is ResolveUserID's local half: one pass over the cached users,
// exact email → case-insensitive email → name → case-insensitive name.
func (lfs *LinearFS) lookupUserID(ctx context.Context, identifier string) (string, error) {
	users, err := lfs.repo.GetUsers(ctx)
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

	return "", &unknownNameError{label: "user", name: identifier}
}

// ResolveIssueID converts an issue identifier (e.g., "ENG-123") to its UUID
func (lfs *LinearFS) ResolveIssueID(ctx context.Context, identifier string) (string, error) {
	issue, err := lfs.repo.GetIssueByIdentifier(ctx, identifier)
	if err != nil {
		return "", err
	}
	if issue == nil {
		return "", fmt.Errorf("unknown issue: %s", identifier)
	}
	return issue.ID, nil
}

// ResolveStateID converts a state name to its ID for a given team. A local
// catalog miss triggers one targeted refresh + retry (see catalogrefresh.go).
func (lfs *LinearFS) ResolveStateID(ctx context.Context, teamID string, stateName string) (string, error) {
	return lfs.resolveWithRefresh(ctx, CatalogStates, teamID, func() (string, error) {
		states, err := lfs.repo.GetTeamStates(ctx, teamID)
		if err != nil {
			return "", err
		}
		return resolveByName(states, stateName, "state",
			func(s api.State) string { return s.Name }, func(s api.State) string { return s.ID })
	})
}

// ResolveLabelIDs converts label names to their IDs for a given team.
// Returns the list of label IDs and any labels that couldn't be resolved.
// Local misses may just be a stale catalog, so one targeted refresh + one
// retry runs before the misses are reported (see catalogrefresh.go).
func (lfs *LinearFS) ResolveLabelIDs(ctx context.Context, teamID string, labelNames []string) ([]string, []string, error) {
	ids, notFound, err := lfs.lookupLabelIDs(ctx, teamID, labelNames)
	if err != nil || len(notFound) == 0 {
		return ids, notFound, err
	}
	if refreshErr := lfs.refreshCatalog(ctx, CatalogLabels, teamID); refreshErr != nil {
		log.Printf("[fs] labels catalog refresh after resolution miss (%v) failed: %v", notFound, refreshErr)
		return ids, notFound, nil
	}
	return lfs.lookupLabelIDs(ctx, teamID, labelNames)
}

// lookupLabelIDs is ResolveLabelIDs' local half: one case-insensitive pass
// over the cached team labels.
func (lfs *LinearFS) lookupLabelIDs(ctx context.Context, teamID string, labelNames []string) ([]string, []string, error) {
	labels, err := lfs.repo.GetTeamLabels(ctx, teamID)
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

// ResolveProjectID converts a project name to its ID for a given team. A local
// catalog miss triggers one targeted refresh + retry (see catalogrefresh.go).
func (lfs *LinearFS) ResolveProjectID(ctx context.Context, teamID string, projectName string) (string, error) {
	return lfs.resolveWithRefresh(ctx, CatalogProjects, teamID, func() (string, error) {
		projects, err := lfs.repo.GetTeamProjects(ctx, teamID)
		if err != nil {
			return "", err
		}
		return resolveByName(projects, projectName, "project",
			func(p api.Project) string { return p.Name }, func(p api.Project) string { return p.ID })
	})
}

// ResolveProjectSlugToID converts a project slug to its ID by searching all teams.
// Used by initiatives which are workspace-level and can link to projects from any team.
func (lfs *LinearFS) ResolveProjectSlugToID(ctx context.Context, projectSlug string) (string, error) {
	teams, err := lfs.repo.GetTeams(ctx)
	if err != nil {
		return "", err
	}

	// Search all teams for a project with this slug
	for _, team := range teams {
		projects, err := lfs.repo.GetTeamProjects(ctx, team.ID)
		if err != nil {
			continue // Skip teams with errors
		}
		for _, project := range projects {
			if project.Slug == projectSlug {
				return project.ID, nil
			}
		}
	}

	return "", fmt.Errorf("unknown project slug: %s", projectSlug)
}

// ResolveMilestoneID converts a milestone name to its ID for a given project.
// A local catalog miss triggers one targeted refresh + retry (see catalogrefresh.go).
func (lfs *LinearFS) ResolveMilestoneID(ctx context.Context, projectID string, milestoneName string) (string, error) {
	return lfs.resolveWithRefresh(ctx, CatalogMilestones, projectID, func() (string, error) {
		milestones, err := lfs.repo.GetProjectMilestones(ctx, projectID)
		if err != nil {
			return "", err
		}
		return resolveByName(milestones, milestoneName, "milestone",
			func(m api.ProjectMilestone) string { return m.Name }, func(m api.ProjectMilestone) string { return m.ID })
	})
}

// UpdateProjectMilestone updates an existing milestone via the mutation seam,
// then upserts to SQLite. The owning project ID is recovered from the cache so
// the upsert keeps the association.
func (lfs *LinearFS) UpdateProjectMilestone(ctx context.Context, milestoneID string, input api.ProjectMilestoneUpdateInput) (*api.ProjectMilestone, error) {
	milestone, err := lfs.mutator().UpdateProjectMilestone(ctx, milestoneID, input)
	if err != nil {
		return nil, err
	}
	var projectID string
	if lfs.store != nil {
		if existing, err := lfs.store.Queries().GetProjectMilestone(ctx, milestoneID); err == nil {
			projectID = existing.ProjectID
		}
	}
	if err := lfs.UpsertProjectMilestone(ctx, projectID, *milestone); err != nil {
		log.Printf("[fs] upsert milestone %s failed: %v", milestone.ID, err)
	}
	return milestone, nil
}

// ResolveCycleID resolves a cycle name to its ID. A local catalog miss
// triggers one targeted refresh + retry (see catalogrefresh.go).
func (lfs *LinearFS) ResolveCycleID(ctx context.Context, teamID string, cycleName string) (string, error) {
	return lfs.resolveWithRefresh(ctx, CatalogCycles, teamID, func() (string, error) {
		cycles, err := lfs.repo.GetTeamCycles(ctx, teamID)
		if err != nil {
			return "", err
		}
		return resolveByName(cycles, cycleName, "cycle",
			func(c api.Cycle) string { return c.Name }, func(c api.Cycle) string { return c.ID })
	})
}

// ResolveInitiativeID converts an initiative name to its ID. A local catalog
// miss triggers one targeted refresh + retry (see catalogrefresh.go).
func (lfs *LinearFS) ResolveInitiativeID(ctx context.Context, initiativeName string) (string, error) {
	return lfs.resolveWithRefresh(ctx, CatalogInitiatives, "", func() (string, error) {
		initiatives, err := lfs.repo.GetInitiatives(ctx)
		if err != nil {
			return "", err
		}
		return resolveByName(initiatives, initiativeName, "initiative",
			func(i api.Initiative) string { return i.Name }, func(i api.Initiative) string { return i.ID })
	})
}

// UpdateLabel updates a label
func (lfs *LinearFS) UpdateLabel(ctx context.Context, labelID string, input map[string]any, teamID string) (*api.Label, error) {
	return lfs.mutator().UpdateLabel(ctx, labelID, input)
}

// projectLabelNames maps a project's labelIds to catalog names for rendering.
// An ID missing from the catalog renders VERBATIM, never dropped — the
// round-trip invariant: a cold or stale catalog must not cause an untouched
// save to strip a label (the resolver accepts exact-ID passthrough for the
// same reason; see resolveProjectLabels).
func (lfs *LinearFS) projectLabelNames(ctx context.Context, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]string)
	if catalog, err := lfs.repo.GetProjectLabels(ctx); err == nil {
		for _, l := range catalog {
			byID[l.ID] = l.Name
		}
	}
	names := make([]string, len(ids))
	for i, id := range ids {
		if name, ok := byID[id]; ok && name != "" {
			names[i] = name
		} else {
			names[i] = id
		}
	}
	return names
}

// MountFS mounts an existing LinearFS instance at the given path.
// This is useful for testing when you need to configure LinearFS before mounting.
func MountFS(mountpoint string, lfs *LinearFS, debug bool) (*fuse.Server, error) {
	root := &RootNode{BaseNode: BaseNode{lfs: lfs}}

	// Use longer timeouts to reduce kernel→userspace calls
	attrTimeout := 60 * time.Second
	entryTimeout := 30 * time.Second

	opts := &fs.Options{
		AttrTimeout:  &attrTimeout,
		EntryTimeout: &entryTimeout,
		MountOptions: fuse.MountOptions{
			Name:   "linearfs",
			FsName: "linear",
			Debug:  debug,
		},
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		return nil, err
	}

	// Block until the kernel has completed the mount handshake. fs.Mount
	// returns as soon as the serve loop starts in the background, so without
	// this the first operation against the mount can race the handshake and
	// get EIO — observed as a flaky "readdirent teams: input/output error"
	// in CI's integration suite.
	if err := server.WaitMount(); err != nil {
		_ = server.Unmount()
		return nil, fmt.Errorf("wait for mount: %w", err)
	}

	lfs.SetServer(server)
	lfs.mountPoint = mountpoint
	return server, nil
}

// InjectTestStore sets up the SQLite store and repository for testing.
// This is used by integration tests to pre-populate the database with fixtures.
func (lfs *LinearFS) InjectTestStore(store *db.Store) error {
	lfs.store = store
	lfs.repo = repo.NewSQLiteRepository(store, nil)
	// Mirror EnableSQLiteCache's viewer load (but never the API refresh): a
	// fixture-populated viewer_cache row resolves the current user, so the
	// my/ views are exercisable offline.
	ctx := context.Background()
	if cachedViewerID, err := store.Queries().GetViewerUserID(ctx); err == nil {
		if dbUser, err := store.Queries().GetUser(ctx, cachedViewerID); err == nil {
			apiUser := db.DBUserToAPIUser(dbUser)
			lfs.repo.SetCurrentUser(&apiUser)
		}
	}
	return nil
}

// GetStore returns the SQLite store for direct database access in tests.
func (lfs *LinearFS) GetStore() *db.Store {
	return lfs.store
}

// InjectTestMutationClient swaps the mutation surface for a test fake, so
// fixture-mode tests can exercise the success half of the write contract offline
// (create/edit reach ClearWriteError/AppendWriteSuccess instead of failing at the
// network). Reads/infrastructure continue to use the real client. Pass nil to
// restore the default (mutations hit the real client and fail without an API key,
// which the loud-failure tests rely on).
// If mc also implements verifyReader (as the mockmutation fake does), the
// read-your-writes verify fetch is routed to it too, so the edit-commit tail
// runs against fake state offline instead of taking the "unverified" branch.
func (lfs *LinearFS) InjectTestMutationClient(mc MutationClient) {
	lfs.mutatorMu.Lock()
	defer lfs.mutatorMu.Unlock()
	if mc == nil {
		lfs.mutatorImpl = lfs.client
		lfs.verifierImpl = lfs.client
		return
	}
	lfs.mutatorImpl = mc
	if vr, ok := mc.(verifyReader); ok {
		lfs.verifierImpl = vr
	} else {
		lfs.verifierImpl = lfs.client
	}
}

// mutator returns the current mutation client under a read lock, so a FUSE
// handler goroutine never races a test swapping the client via
// InjectTestMutationClient.
func (lfs *LinearFS) mutator() MutationClient {
	lfs.mutatorMu.RLock()
	defer lfs.mutatorMu.RUnlock()
	return lfs.mutatorImpl
}

// verify returns the current read-your-writes reader under a read lock (same
// guard as mutator). Production uses the real client; tests may swap in a
// store-backed fake via InjectTestMutationClient.
func (lfs *LinearFS) verify() verifyReader {
	lfs.mutatorMu.RLock()
	defer lfs.mutatorMu.RUnlock()
	return lfs.verifierImpl
}
