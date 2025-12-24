package fs

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
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

// LinearFS implements a FUSE filesystem backed by Linear.
// Read operations go through the Repository (SQLite + on-demand API fetch).
// Write operations (mutations) use the API Client directly.
type LinearFS struct {
	client     *api.Client      // For mutations only
	repo       *repo.SQLiteRepository // For all read operations
	server     *fuse.Server     // FUSE server for kernel cache invalidation
	store      *db.Store        // SQLite store (owned by repo, kept for sync worker)
	syncWorker *sync.Worker     // Background sync worker
	debug      bool
	uid        uint32 // Owner UID for files/dirs
	gid        uint32 // Owner GID for files/dirs

	// File cache for embedded files
	fileCacheDir  string
	fileCacheMu   gosync.RWMutex
	fileCache     map[string][]byte // in-memory cache (file ID -> content)
	fileCacheInit gosync.Once
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
	if b.lfs != nil {
		out.Uid = b.lfs.uid
		out.Gid = b.lfs.gid
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

	client := api.NewClientWithOptions(cfg.APIKey, api.ClientOptions{
		APIStatsEnabled: cfg.Log.APIStats,
	})

	// Initialize file cache directory
	cacheDir := filepath.Join(os.Getenv("HOME"), "Library", "Caches", "linearfs", "files")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[linearfs] Warning: failed to create cache dir: %v", err)
	}

	return &LinearFS{
		uid:          uid,
		gid:          gid,
		client:       client,
		debug:        debug,
		fileCacheDir: cacheDir,
		fileCache:    make(map[string][]byte),
	}, nil
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
	// Close API client (stops stats logger)
	if lfs.client != nil {
		lfs.client.Close()
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

	// Fetch and set the current user (for /my views)
	viewer, err := lfs.client.GetViewer(ctx)
	if err != nil {
		log.Printf("[sqlite] Warning: failed to get viewer: %v", err)
	} else if viewer != nil {
		lfs.repo.SetCurrentUser(viewer)
		log.Printf("[sqlite] Current user: %s (%s)", viewer.Email, viewer.ID)
	}

	// Create and start sync worker
	lfs.syncWorker = sync.NewWorker(lfs.client, store, sync.DefaultConfig())
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

func (lfs *LinearFS) InvalidateTeamIssues(teamID string) {
	// No-op: SQLite is source of truth, sync worker will refresh
}

// SetServer sets the FUSE server reference for kernel cache invalidation
func (lfs *LinearFS) SetServer(server *fuse.Server) {
	lfs.server = server
}

// InvalidateKernelInode tells the kernel to drop cached data for an inode
func (lfs *LinearFS) InvalidateKernelInode(ino uint64) {
	if lfs.server != nil {
		lfs.server.InodeNotify(ino, 0, -1) // -1 = entire file
	}
}

// InvalidateKernelEntry tells the kernel to drop a cached directory entry
func (lfs *LinearFS) InvalidateKernelEntry(parent uint64, name string) {
	if lfs.server != nil {
		lfs.server.EntryNotify(parent, name)
	}
}

// InvalidateFilteredIssues clears all filtered issue cache entries for a team
// No-op: SQLite is source of truth
func (lfs *LinearFS) InvalidateFilteredIssues(teamID string) {
	// No-op: SQLite is source of truth, sync worker will refresh
}

// InvalidateIssueById clears a specific issue from the identifier cache
// No-op: SQLite is source of truth
func (lfs *LinearFS) InvalidateIssueById(identifier string) {
	// No-op: SQLite is source of truth
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
	params, err := db.APILabelToDBLabel(label, teamID)
	if err != nil {
		return err
	}
	return lfs.store.Queries().UpsertLabel(ctx, params)
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

// SearchTeamIssues performs full-text search on issues within a team
func (lfs *LinearFS) SearchTeamIssues(ctx context.Context, teamID, query string) ([]api.Issue, error) {
	return lfs.repo.SearchTeamIssues(ctx, teamID, query)
}

// SearchAllIssues performs full-text search across all issues
func (lfs *LinearFS) SearchAllIssues(ctx context.Context, query string) ([]api.Issue, error) {
	return lfs.repo.SearchIssues(ctx, query)
}

// InvalidateMyIssues is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateMyIssues() {
	// No-op: SQLite is source of truth
}

// ArchiveIssue archives an issue
func (lfs *LinearFS) ArchiveIssue(ctx context.Context, issueID string, teamID string, assigneeID string) error {
	return lfs.client.ArchiveIssue(ctx, issueID)
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
		}
	}
	return result, nil
}

func (lfs *LinearFS) GetTeamProjects(ctx context.Context, teamID string) ([]api.Project, error) {
	return lfs.repo.GetTeamProjects(ctx, teamID)
}

// InvalidateTeamProjects is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateTeamProjects(teamID string) {
	// No-op: SQLite is source of truth
}

// InvalidateProjectIssues is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateProjectIssues(projectID string) {
	// No-op: SQLite is source of truth
}

// CreateProject creates a new project
func (lfs *LinearFS) CreateProject(ctx context.Context, input map[string]any) (*api.Project, error) {
	return lfs.client.CreateProject(ctx, input)
}

// ArchiveProject archives a project
func (lfs *LinearFS) ArchiveProject(ctx context.Context, projectID string, teamID string) error {
	return lfs.client.ArchiveProject(ctx, projectID)
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

// InvalidateUserIssues is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateUserIssues(userID string) {
	// No-op: SQLite is source of truth
}

func (lfs *LinearFS) GetIssueComments(ctx context.Context, issueID string) ([]api.Comment, error) {
	return lfs.repo.GetIssueComments(ctx, issueID)
}

// InvalidateIssueComments is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateIssueComments(issueID string) {
	// No-op: SQLite is source of truth
}

// TryGetCachedComments returns comments from SQLite
func (lfs *LinearFS) TryGetCachedComments(issueID string) ([]api.Comment, bool) {
	comments, err := lfs.repo.GetIssueComments(context.Background(), issueID)
	if err != nil {
		return nil, false
	}
	return comments, len(comments) > 0
}

func (lfs *LinearFS) CreateComment(ctx context.Context, issueID string, body string) (*api.Comment, error) {
	return lfs.client.CreateComment(ctx, issueID, body)
}

func (lfs *LinearFS) UpdateComment(ctx context.Context, issueID string, commentID string, body string) (*api.Comment, error) {
	return lfs.client.UpdateComment(ctx, commentID, body)
}

func (lfs *LinearFS) DeleteComment(ctx context.Context, issueID string, commentID string) error {
	// Delete from API
	if err := lfs.client.DeleteComment(ctx, commentID); err != nil {
		return err
	}
	// Delete from SQLite so it's immediately removed from listings
	return lfs.store.Queries().DeleteComment(ctx, commentID)
}

// Document methods

func (lfs *LinearFS) GetIssueDocuments(ctx context.Context, issueID string) ([]api.Document, error) {
	return lfs.repo.GetIssueDocuments(ctx, issueID)
}

// GetTeamDocuments returns documents for a team (currently via API as not synced)
func (lfs *LinearFS) GetTeamDocuments(ctx context.Context, teamID string) ([]api.Document, error) {
	return lfs.client.GetTeamDocuments(ctx, teamID)
}

func (lfs *LinearFS) GetProjectDocuments(ctx context.Context, projectID string) ([]api.Document, error) {
	return lfs.repo.GetProjectDocuments(ctx, projectID)
}

// InvalidateIssueDocuments is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateIssueDocuments(issueID string) {
	// No-op: SQLite is source of truth
}

// InvalidateTeamDocuments is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateTeamDocuments(teamID string) {
	// No-op: SQLite is source of truth
}

// InvalidateProjectDocuments is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateProjectDocuments(projectID string) {
	// No-op: SQLite is source of truth
}

func (lfs *LinearFS) CreateDocument(ctx context.Context, input map[string]any) (*api.Document, error) {
	return lfs.client.CreateDocument(ctx, input)
}

func (lfs *LinearFS) UpdateDocument(ctx context.Context, documentID string, input map[string]any, issueID, teamID, projectID string) (*api.Document, error) {
	return lfs.client.UpdateDocument(ctx, documentID, input)
}

func (lfs *LinearFS) DeleteDocument(ctx context.Context, documentID string, issueID, teamID, projectID string) error {
	// Delete from API
	if err := lfs.client.DeleteDocument(ctx, documentID); err != nil {
		return err
	}
	// Delete from SQLite so it's immediately removed from listings
	return lfs.store.Queries().DeleteDocument(ctx, documentID)
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

// ResolveProjectID converts a project name to its ID for a given team
func (lfs *LinearFS) ResolveProjectID(ctx context.Context, teamID string, projectName string) (string, error) {
	projects, err := lfs.GetTeamProjects(ctx, teamID)
	if err != nil {
		return "", err
	}

	// Try exact match first
	for _, project := range projects {
		if project.Name == projectName {
			return project.ID, nil
		}
	}

	// Try case-insensitive match
	lowerName := strings.ToLower(projectName)
	for _, project := range projects {
		if strings.ToLower(project.Name) == lowerName {
			return project.ID, nil
		}
	}

	return "", fmt.Errorf("unknown project: %s", projectName)
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

	// Try exact match first
	for _, milestone := range milestones {
		if milestone.Name == milestoneName {
			return milestone.ID, nil
		}
	}

	// Try case-insensitive match
	lowerName := strings.ToLower(milestoneName)
	for _, milestone := range milestones {
		if strings.ToLower(milestone.Name) == lowerName {
			return milestone.ID, nil
		}
	}

	return "", fmt.Errorf("unknown milestone: %s", milestoneName)
}

// ResolveCycleID resolves a cycle name to its ID
func (lfs *LinearFS) ResolveCycleID(ctx context.Context, teamID string, cycleName string) (string, error) {
	cycles, err := lfs.GetTeamCycles(ctx, teamID)
	if err != nil {
		return "", err
	}

	// Try exact match first
	for _, cycle := range cycles {
		if cycle.Name == cycleName {
			return cycle.ID, nil
		}
	}

	// Try case-insensitive match
	lowerName := strings.ToLower(cycleName)
	for _, cycle := range cycles {
		if strings.ToLower(cycle.Name) == lowerName {
			return cycle.ID, nil
		}
	}

	return "", fmt.Errorf("unknown cycle: %s", cycleName)
}

// GetProjectUpdates fetches status updates for a project
func (lfs *LinearFS) GetProjectUpdates(ctx context.Context, projectID string) ([]api.ProjectUpdate, error) {
	return lfs.repo.GetProjectUpdates(ctx, projectID)
}

// InvalidateProjectUpdates is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateProjectUpdates(projectID string) {
	// No-op: SQLite is source of truth
}

// CreateProjectUpdate creates a new status update on a project
func (lfs *LinearFS) CreateProjectUpdate(ctx context.Context, projectID, body, health string) (*api.ProjectUpdate, error) {
	return lfs.client.CreateProjectUpdate(ctx, projectID, body, health)
}

// GetInitiativeUpdates fetches status updates for an initiative
func (lfs *LinearFS) GetInitiativeUpdates(ctx context.Context, initiativeID string) ([]api.InitiativeUpdate, error) {
	return lfs.repo.GetInitiativeUpdates(ctx, initiativeID)
}

// InvalidateInitiativeUpdates is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateInitiativeUpdates(initiativeID string) {
	// No-op: SQLite is source of truth
}

// CreateInitiativeUpdate creates a new status update on an initiative
func (lfs *LinearFS) CreateInitiativeUpdate(ctx context.Context, initiativeID, body, health string) (*api.InitiativeUpdate, error) {
	return lfs.client.CreateInitiativeUpdate(ctx, initiativeID, body, health)
}

// ResolveInitiativeID converts an initiative name to its ID
func (lfs *LinearFS) ResolveInitiativeID(ctx context.Context, initiativeName string) (string, error) {
	initiatives, err := lfs.GetInitiatives(ctx)
	if err != nil {
		return "", err
	}

	// Try exact match first
	for _, initiative := range initiatives {
		if initiative.Name == initiativeName {
			return initiative.ID, nil
		}
	}

	// Try case-insensitive match
	lowerName := strings.ToLower(initiativeName)
	for _, initiative := range initiatives {
		if strings.ToLower(initiative.Name) == lowerName {
			return initiative.ID, nil
		}
	}

	return "", fmt.Errorf("unknown initiative: %s", initiativeName)
}

// InvalidateTeamLabels is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateTeamLabels(teamID string) {
	// No-op: SQLite is source of truth
}

// CreateLabel creates a new label
func (lfs *LinearFS) CreateLabel(ctx context.Context, input map[string]any) (*api.Label, error) {
	return lfs.client.CreateLabel(ctx, input)
}

// UpdateLabel updates a label
func (lfs *LinearFS) UpdateLabel(ctx context.Context, labelID string, input map[string]any, teamID string) (*api.Label, error) {
	return lfs.client.UpdateLabel(ctx, labelID, input)
}

// DeleteLabel deletes a label
func (lfs *LinearFS) DeleteLabel(ctx context.Context, labelID string, teamID string) error {
	return lfs.client.DeleteLabel(ctx, labelID)
}

// GetInitiatives fetches all initiatives
func (lfs *LinearFS) GetInitiatives(ctx context.Context) ([]api.Initiative, error) {
	return lfs.repo.GetInitiatives(ctx)
}

// InvalidateInitiatives is a no-op; SQLite is the source of truth
func (lfs *LinearFS) InvalidateInitiatives() {
	// No-op: SQLite is source of truth
}

func Mount(mountpoint string, cfg *config.Config, debug bool) (*fuse.Server, *LinearFS, error) {
	lfs, err := NewLinearFS(cfg, debug)
	if err != nil {
		return nil, nil, err
	}

	root := &RootNode{BaseNode: BaseNode{lfs: lfs}}

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
		return nil, nil, err
	}

	// Store server reference for kernel cache invalidation
	lfs.SetServer(server)

	return server, lfs, nil
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

	lfs.SetServer(server)
	return server, nil
}

// InjectTestStore sets up the SQLite store and repository for testing.
// This is used by integration tests to pre-populate the database with fixtures.
func (lfs *LinearFS) InjectTestStore(store *db.Store) error {
	lfs.store = store
	lfs.repo = repo.NewSQLiteRepository(store, nil)
	return nil
}

// GetStore returns the SQLite store for direct database access in tests.
func (lfs *LinearFS) GetStore() *db.Store {
	return lfs.store
}

// =============================================================================
// File Cache for Embedded Files
// =============================================================================

// FetchEmbeddedFile downloads an embedded file from Linear's CDN, caching it locally.
// Returns the file content. Files are cached both in memory and on disk.
func (lfs *LinearFS) FetchEmbeddedFile(ctx context.Context, file api.EmbeddedFile) ([]byte, error) {
	// Check in-memory cache first
	lfs.fileCacheMu.RLock()
	if content, ok := lfs.fileCache[file.ID]; ok {
		lfs.fileCacheMu.RUnlock()
		return content, nil
	}
	lfs.fileCacheMu.RUnlock()

	// Check disk cache
	diskPath := filepath.Join(lfs.fileCacheDir, file.ID)
	if file.CachePath != "" {
		diskPath = file.CachePath
	}

	if content, err := os.ReadFile(diskPath); err == nil {
		// Found on disk, add to in-memory cache
		lfs.fileCacheMu.Lock()
		lfs.fileCache[file.ID] = content
		lfs.fileCacheMu.Unlock()
		return content, nil
	}

	// Download from Linear CDN
	content, err := lfs.downloadFile(ctx, file.URL)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}

	// Cache to disk
	if err := os.WriteFile(diskPath, content, 0644); err != nil {
		log.Printf("[cache] Warning: failed to cache file %s: %v", file.Filename, err)
	} else {
		// Update database with cache path
		if err := lfs.repo.UpdateEmbeddedFileCache(ctx, file.ID, diskPath, int64(len(content))); err != nil {
			log.Printf("[cache] Warning: failed to update cache path: %v", err)
		}
	}

	// Cache in memory
	lfs.fileCacheMu.Lock()
	lfs.fileCache[file.ID] = content
	lfs.fileCacheMu.Unlock()

	return content, nil
}

// downloadFile fetches a file from Linear's CDN
func (lfs *LinearFS) downloadFile(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Linear CDN requires authentication for private files
	req.Header.Set("Authorization", lfs.client.AuthHeader())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	return io.ReadAll(resp.Body)
}
