package fs

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
)

// IssueError represents a validation error from a failed write operation

// LinearFS implements a FUSE filesystem backed by Linear.
// Read operations go through the Repository (SQLite + on-demand API fetch).
// Write operations (mutations) use the API Client directly.
type LinearFS struct {
	client       *api.Client    // Reads (on-demand fetch) + infrastructure (stats, viewer, close)
	mutatorImpl  MutationClient // Mutations only; defaults to client, swappable for tests
	verifierImpl verifyReader   // Read-your-writes re-fetch; defaults to client, swappable for tests
	mutatorMu    gosync.RWMutex // guards mutatorImpl + verifierImpl (handlers read while tests swap)

	repo       *repo.SQLiteRepository // For all read operations
	store      *db.Store              // SQLite store (owned by repo, kept for sync worker)
	syncWorker *sync.Worker           // Background sync worker
	debug      bool
	uid        uint32 // Owner UID for files/dirs
	gid        uint32 // Owner GID for files/dirs
	mountPoint string // Filesystem mount path (for README generation)

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

	// Initialize file cache directory
	cacheDir := filepath.Join(os.Getenv("HOME"), "Library", "Caches", "linearfs", "files")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[linearfs] Warning: failed to create cache dir: %v", err)
	}

	lfs := &LinearFS{
		uid:          uid,
		gid:          gid,
		client:       client,
		mutatorImpl:  client,
		verifierImpl: client,
		debug:        debug,
	}
	// Wire the feedback store's kernel-cache seam to this instance. The method
	// value binds the pointer, so it is safe to set after lfs exists.
	lfs.writeFeedback = newWriteFeedback(lfs.InvalidateUpdated)
	// The embedded-file cache's seams are late-bound: repo is wired later (in
	// EnableSQLiteCache), so persist reads lfs.repo at call time — and no-ops
	// while it is still nil (a fetch before the cache is enabled).
	lfs.embeddedFileCache = newEmbeddedFileCache(cacheDir,
		func() string { return lfs.client.AuthHeader() },
		func(ctx context.Context, fileID, path string, size int64) error {
			if lfs.repo == nil {
				return nil
			}
			return lfs.repo.UpdateEmbeddedFileCache(ctx, fileID, path, size)
		},
	)
	return lfs, nil
}

// Close stops all background operations and releases resources
func (lfs *LinearFS) Close() {
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
}

// EnableSQLiteCache initializes the SQLite backend and starts background sync.
// This MUST be called after creating LinearFS but before mounting.
// If dbPath is empty, uses the default path (~/.config/linearfs/cache.db).
func (lfs *LinearFS) EnableSQLiteCache(ctx context.Context, dbPath string) error {
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
	if cachedViewerID, err := store.Queries().GetViewerUserID(ctx); err == nil {
		if dbUser, err := store.Queries().GetUser(ctx, cachedViewerID); err == nil {
			apiUser := db.DBUserToAPIUser(dbUser)
			lfs.repo.SetCurrentUser(&apiUser)
			log.Printf("[sqlite] Loaded cached viewer: %s (%s)", apiUser.Email, apiUser.ID)
		}
	}

	// Refresh viewer from API in background to keep cache fresh
	go func() {
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
	}()

	// Create and start sync worker
	lfs.syncWorker = sync.NewWorker(lfs.client, store, sync.DefaultConfig())
	lfs.syncWorker.SetBudgetReporter(lfs.client)
	lfs.syncWorker.SetCatchUpModeToggler(lfs.repo)
	lfs.syncWorker.Start(ctx)

	log.Printf("[sqlite] Enabled persistent cache at %s", dbPath)
	return nil
}

// HasSQLiteCache returns true if SQLite backend is enabled
func (lfs *LinearFS) HasSQLiteCache() bool {
	return lfs.repo != nil
}

func (lfs *LinearFS) GetTeams(ctx context.Context) ([]api.Team, error) {
	return lfs.repo.GetTeams(ctx)
}

func (lfs *LinearFS) GetTeamIssues(ctx context.Context, teamID string) ([]api.Issue, error) {
	return lfs.repo.GetTeamIssues(ctx, teamID)
}

func (lfs *LinearFS) GetIssueAttachments(ctx context.Context, issueID string) ([]api.Attachment, error) {
	return lfs.repo.GetIssueAttachments(ctx, issueID)
}

func (lfs *LinearFS) GetIssueEmbeddedFiles(ctx context.Context, issueID string) ([]api.EmbeddedFile, error) {
	return lfs.repo.GetIssueEmbeddedFiles(ctx, issueID)
}

func (lfs *LinearFS) GetIssueHistory(ctx context.Context, issueID string) ([]api.IssueHistoryEntry, error) {
	return lfs.repo.GetIssueHistory(ctx, issueID)
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

// GetFilteredIssuesByPriority fetches issues filtered by priority
func (lfs *LinearFS) GetFilteredIssuesByPriority(ctx context.Context, teamID string, priority int) ([]api.Issue, error) {
	return lfs.repo.GetIssuesByPriority(ctx, teamID, priority)
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

// GetFilteredIssuesByAssignee fetches issues filtered by assignee
func (lfs *LinearFS) GetFilteredIssuesByAssignee(ctx context.Context, teamID, assigneeID string) ([]api.Issue, error) {
	return lfs.repo.GetIssuesByAssignee(ctx, teamID, assigneeID)
}

// GetFilteredIssuesUnassigned fetches issues with no assignee
func (lfs *LinearFS) GetFilteredIssuesUnassigned(ctx context.Context, teamID string) ([]api.Issue, error) {
	return lfs.repo.GetUnassignedIssues(ctx, teamID)
}

func (lfs *LinearFS) GetMyIssues(ctx context.Context) ([]api.Issue, error) {
	return lfs.repo.GetMyIssues(ctx)
}

func (lfs *LinearFS) GetMyCreatedIssues(ctx context.Context) ([]api.Issue, error) {
	return lfs.repo.GetMyCreatedIssues(ctx)
}

func (lfs *LinearFS) GetMyActiveIssues(ctx context.Context) ([]api.Issue, error) {
	return lfs.repo.GetMyActiveIssues(ctx)
}

func (lfs *LinearFS) GetTeamStates(ctx context.Context, teamID string) ([]api.State, error) {
	return lfs.repo.GetTeamStates(ctx, teamID)
}

func (lfs *LinearFS) GetTeamLabels(ctx context.Context, teamID string) ([]api.Label, error) {
	return lfs.repo.GetTeamLabels(ctx, teamID)
}

func (lfs *LinearFS) GetTeamCycles(ctx context.Context, teamID string) ([]api.Cycle, error) {
	return lfs.repo.GetTeamCycles(ctx, teamID)
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

func (lfs *LinearFS) GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error) {
	return lfs.repo.GetTeamProjects(ctx, teamID)
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

func (lfs *LinearFS) GetUsers(ctx context.Context) ([]api.User, error) {
	return lfs.repo.GetUsers(ctx)
}

func (lfs *LinearFS) GetTeamMembers(ctx context.Context, teamID string) ([]api.User, error) {
	return lfs.repo.GetTeamMembers(ctx, teamID)
}

// GetUserIssues returns issues assigned to a user across all teams
func (lfs *LinearFS) GetUserIssues(ctx context.Context, userID string) ([]api.Issue, error) {
	return lfs.repo.GetUserIssues(ctx, userID)
}

// GetIssueChildren returns child issues of a parent issue
func (lfs *LinearFS) GetIssueChildren(ctx context.Context, parentID string) ([]api.Issue, error) {
	return lfs.repo.GetIssueChildren(ctx, parentID)
}

func (lfs *LinearFS) MaybeRefreshIssueDetails(issueID string) {
	lfs.repo.MaybeRefreshIssueDetails(issueID)
}

func (lfs *LinearFS) GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error) {
	return lfs.repo.GetIssueComments(ctx, issueID)
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

// Document methods

func (lfs *LinearFS) GetIssueDocuments(ctx context.Context, issueID string) ([]api.Document, error) {
	return lfs.repo.GetIssueDocuments(ctx, issueID)
}

// GetTeamDocuments returns documents for a team (currently via API as not
// synced). This is a synchronous API call on a live FUSE path — the caller's
// user is blocked on it — so it promotes to the interactive budget tier.
func (lfs *LinearFS) GetTeamDocuments(ctx context.Context, teamID string) ([]api.Document, error) {
	return lfs.client.GetTeamDocuments(api.WithInteractive(ctx), teamID)
}

func (lfs *LinearFS) GetProjectDocuments(ctx context.Context, projectID string) ([]api.Document, error) {
	return lfs.repo.GetProjectDocuments(ctx, projectID)
}

func (lfs *LinearFS) GetInitiativeDocuments(ctx context.Context, initiativeID string) ([]api.Document, error) {
	return lfs.repo.GetInitiativeDocuments(ctx, initiativeID)
}

func (lfs *LinearFS) UpdateDocument(ctx context.Context, documentID string, input map[string]any, issueID, teamID, projectID string) (*api.Document, error) {
	return lfs.mutator().UpdateDocument(ctx, documentID, input)
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

// ResolveStateID converts a state name to its ID for a given team
func (lfs *LinearFS) ResolveStateID(ctx context.Context, teamID string, stateName string) (string, error) {
	states, err := lfs.GetTeamStates(ctx, teamID)
	if err != nil {
		return "", err
	}
	return resolveByName(states, stateName, "state",
		func(s api.State) string { return s.Name }, func(s api.State) string { return s.ID })
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

// ResolveProjectID converts a project name to its ID for a given team
func (lfs *LinearFS) ResolveProjectID(ctx context.Context, teamID string, projectName string) (string, error) {
	projects, err := lfs.GetTeamProjects(ctx, teamID)
	if err != nil {
		return "", err
	}
	return resolveByName(projects, projectName, "project",
		func(p api.Project) string { return p.Name }, func(p api.Project) string { return p.ID })
}

// ResolveProjectSlugToID converts a project slug to its ID by searching all teams.
// Used by initiatives which are workspace-level and can link to projects from any team.
func (lfs *LinearFS) ResolveProjectSlugToID(ctx context.Context, projectSlug string) (string, error) {
	teams, err := lfs.GetTeams(ctx)
	if err != nil {
		return "", err
	}

	// Search all teams for a project with this slug
	for _, team := range teams {
		projects, err := lfs.GetTeamProjects(ctx, team.ID)
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

// GetProjectMilestones fetches milestones for a project
func (lfs *LinearFS) GetProjectMilestones(ctx context.Context, projectID string) ([]api.ProjectMilestone, error) {
	return lfs.repo.GetProjectMilestones(ctx, projectID)
}

// ResolveMilestoneID converts a milestone name to its ID for a given project
func (lfs *LinearFS) ResolveMilestoneID(ctx context.Context, projectID string, milestoneName string) (string, error) {
	milestones, err := lfs.GetProjectMilestones(ctx, projectID)
	if err != nil {
		return "", err
	}
	return resolveByName(milestones, milestoneName, "milestone",
		func(m api.ProjectMilestone) string { return m.Name }, func(m api.ProjectMilestone) string { return m.ID })
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

// ResolveCycleID resolves a cycle name to its ID
func (lfs *LinearFS) ResolveCycleID(ctx context.Context, teamID string, cycleName string) (string, error) {
	cycles, err := lfs.GetTeamCycles(ctx, teamID)
	if err != nil {
		return "", err
	}
	return resolveByName(cycles, cycleName, "cycle",
		func(c api.Cycle) string { return c.Name }, func(c api.Cycle) string { return c.ID })
}

// GetProjectUpdates fetches status updates for a project
func (lfs *LinearFS) GetProjectUpdates(ctx context.Context, projectID string) ([]api.ProjectUpdate, error) {
	return lfs.repo.GetProjectUpdates(ctx, projectID)
}

// GetInitiativeUpdates fetches status updates for an initiative
func (lfs *LinearFS) GetInitiativeUpdates(ctx context.Context, initiativeID string) ([]api.InitiativeUpdate, error) {
	return lfs.repo.GetInitiativeUpdates(ctx, initiativeID)
}

// ResolveInitiativeID converts an initiative name to its ID
func (lfs *LinearFS) ResolveInitiativeID(ctx context.Context, initiativeName string) (string, error) {
	initiatives, err := lfs.GetInitiatives(ctx)
	if err != nil {
		return "", err
	}
	return resolveByName(initiatives, initiativeName, "initiative",
		func(i api.Initiative) string { return i.Name }, func(i api.Initiative) string { return i.ID })
}

// UpdateLabel updates a label
func (lfs *LinearFS) UpdateLabel(ctx context.Context, labelID string, input map[string]any, teamID string) (*api.Label, error) {
	return lfs.mutator().UpdateLabel(ctx, labelID, input)
}

// GetInitiatives fetches all initiatives
func (lfs *LinearFS) GetInitiatives(ctx context.Context) ([]api.Initiative, error) {
	return lfs.repo.GetInitiatives(ctx)
}

// GetProjectLabels returns the workspace project-label catalog (Parent names
// stitched by the repo read).
func (lfs *LinearFS) GetProjectLabels(ctx context.Context) ([]api.ProjectLabel, error) {
	return lfs.repo.GetProjectLabels(ctx)
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
	if catalog, err := lfs.GetProjectLabels(ctx); err == nil {
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
