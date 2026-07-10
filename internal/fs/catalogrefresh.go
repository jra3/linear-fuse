package fs

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// Validation-failure refresh-and-retry (#246).
//
// The name→ID resolvers (linearfs.go's Resolve* methods) resolve against the
// locally-cached catalog in SQLite, which rides a minutes-long sync cadence —
// so a name a teammate created moments ago is a *local* miss, not a bad name.
// Every resolver therefore routes its lookup through resolveWithRefresh: on a
// miss it triggers exactly ONE targeted refresh of that catalog and retries
// the resolution ONCE, and only then surfaces the original validation error
// (same message, same .error / EINVAL contract as before). API-side rejections
// (classifyMutationErr territory) never reach this path — the refresh fires
// only on the typed local-miss error below, before any mutation is attempted.

// CatalogKind names one locally-cached name→ID catalog a resolver can miss
// against. It scopes the targeted refresh: team-scoped kinds refresh via the
// team-metadata drain, workspace-scoped kinds via the workspace drain.
type CatalogKind string

const (
	CatalogStates      CatalogKind = "states"      // scopeID = team ID
	CatalogLabels      CatalogKind = "labels"      // scopeID = team ID
	CatalogProjects    CatalogKind = "projects"    // scopeID = team ID
	CatalogMilestones  CatalogKind = "milestones"  // scopeID = project ID
	CatalogCycles      CatalogKind = "cycles"      // scopeID = team ID
	CatalogUsers       CatalogKind = "users"       // scopeID unused (workspace)
	CatalogInitiatives CatalogKind = "initiatives" // scopeID unused (workspace)
)

// unknownNameError marks a LOCAL name→ID resolution miss: the name is absent
// from the cached catalog. It is the one trigger for the refresh-and-retry —
// repo/infrastructure failures pass through untouched. Error() must stay
// byte-identical to the historical "unknown <label>: <name>" message; the
// .error content is part of the write contract.
type unknownNameError struct{ label, name string }

func (e *unknownNameError) Error() string {
	return fmt.Sprintf("unknown %s: %s", e.label, e.name)
}

// resolveWithRefresh runs resolve against the local catalog; on an
// unknown-name miss it triggers exactly one targeted refresh of the named
// catalog and retries the resolution once. No loops: a second miss surfaces
// the same error the first produced. A refresh failure (budget refusal,
// network, no sync worker) is logged and swallowed — the caller's error is
// the original miss, exactly as if the refresh never existed.
func (lfs *LinearFS) resolveWithRefresh(ctx context.Context, kind CatalogKind, scopeID string, resolve func() (string, error)) (string, error) {
	id, err := resolve()
	var miss *unknownNameError
	if err == nil || !errors.As(err, &miss) {
		return id, err
	}
	if refreshErr := lfs.refreshCatalog(ctx, kind, scopeID); refreshErr != nil {
		log.Printf("[fs] %s catalog refresh after resolution miss (%v) failed: %v", kind, err, refreshErr)
		return "", err
	}
	return resolve()
}

// refreshCatalog dispatches one targeted catalog refresh through the seam:
// the injected test stub when present, otherwise the sync-worker-backed
// default. Reads the field under the same lock InjectTestCatalogRefresher
// writes it (mutatorMu, shared with the sibling mutator/verifier seams).
func (lfs *LinearFS) refreshCatalog(ctx context.Context, kind CatalogKind, scopeID string) error {
	lfs.mutatorMu.RLock()
	impl := lfs.catalogRefreshImpl
	lfs.mutatorMu.RUnlock()
	if impl == nil {
		impl = lfs.refreshCatalogViaSync
	}
	return impl(ctx, kind, scopeID)
}

// refreshCatalogViaSync is the production refresh: it reuses the sync
// worker's existing complete-drain machinery (fetch from the API, upsert to
// SQLite, prune under the same licenses as a background cycle), so budget
// gates — GetTeamMetadata's and GetWorkspace's LowBudget preflights — apply
// automatically. Without a worker (fixture mode, unit tests) it declines,
// which keeps offline suites network-free by construction: the resolver then
// surfaces the original miss unchanged.
func (lfs *LinearFS) refreshCatalogViaSync(ctx context.Context, kind CatalogKind, scopeID string) error {
	w := lfs.syncWorker
	if w == nil {
		return fmt.Errorf("catalog refresh unavailable: no sync worker")
	}
	switch kind {
	case CatalogUsers, CatalogInitiatives:
		return w.RefreshWorkspaceCatalogs(ctx)
	case CatalogMilestones:
		// Milestones ride the team-metadata drain (nested under projects);
		// map the owning project to its canonical team first — locally, no
		// API. A project not yet in the store cannot be scoped, so the
		// refresh declines and the miss surfaces.
		teamID, err := lfs.projectPrimaryTeamID(ctx, scopeID)
		if err != nil {
			return fmt.Errorf("resolve team for project %s: %w", scopeID, err)
		}
		return w.RefreshTeamCatalogs(ctx, teamID)
	default: // states, labels, cycles, projects — team-scoped, one combined drain
		return w.RefreshTeamCatalogs(ctx, scopeID)
	}
}

// projectPrimaryTeamID maps a project ID to its canonical team's ID via the
// local store (GetProjectPrimaryTeamKey owns the "which team hosts this
// project" rule; it returns the key, so match it against the teams table).
func (lfs *LinearFS) projectPrimaryTeamID(ctx context.Context, projectID string) (string, error) {
	if lfs.store == nil || lfs.repo == nil {
		return "", fmt.Errorf("no local store")
	}
	key, err := lfs.store.Queries().GetProjectPrimaryTeamKey(ctx, projectID)
	if err != nil {
		return "", err
	}
	teams, err := lfs.repo.GetTeams(ctx)
	if err != nil {
		return "", err
	}
	for _, team := range teams {
		if team.Key == key {
			return team.ID, nil
		}
	}
	return "", fmt.Errorf("no team with key %s", key)
}

// InjectTestCatalogRefresher swaps the validation-failure catalog-refresh
// seam for a test stub, so offline suites can exercise the refresh-and-retry
// contract (stale catalog → refresh supplies the name → write succeeds)
// without the network. Pass nil to restore the default sync-worker-backed
// behavior.
func (lfs *LinearFS) InjectTestCatalogRefresher(fn func(ctx context.Context, kind CatalogKind, scopeID string) error) {
	lfs.mutatorMu.Lock()
	defer lfs.mutatorMu.Unlock()
	lfs.catalogRefreshImpl = fn
}
