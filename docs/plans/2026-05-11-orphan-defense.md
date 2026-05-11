# Orphan-Refresh-Loop Defense Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generalize the issue-orphan fix from `f2417ba` to projects and initiatives, then add an adaptive reconciliation pass so orphans get discovered and cleaned automatically (with bounded API cost) instead of looping forever on FUSE traversals.

**Architecture:** Three layers in `internal/repo/sqlite.go`. Layer 1 adds `deleteOrphanProject` / `deleteOrphanInitiative` helpers and wires them into the four sibling refresh closures (mirror of the existing `deleteOrphanIssue` pattern). Layer 2 adds a `maybeScheduleReconcile()` trigger that fires from every `deleteOrphan*` helper, gated by a 6-hour cooldown and an in-flight flag. Layer 3 implements `runReconcile`, which uses three new ID-only Linear API queries to enumerate the authoritative ID set per entity type, diff against SQLite, and call the orphan helpers for missing rows.

**Tech Stack:** Go, SQLite via sqlc, Linear GraphQL API, existing `golang.org/x/time/rate` limiter.

**Spec:** `docs/plans/2026-05-11-orphan-defense-design.md`

---

## File Structure

**Created:**
- None (all changes go in existing files)

**Modified:**
- `internal/api/queries.go` — three new ID-only GraphQL query strings (`queryTeamIssueIDs`, `queryWorkspaceProjectIDs`, `queryWorkspaceInitiativeIDs`).
- `internal/api/client.go` — three new `Get*IDs` methods + a `LowBudget()` helper.
- `internal/api/client_test.go` — tests for `LowBudget` and the new query methods (using existing test patterns with mock HTTP server).
- `internal/repo/sqlite.go` — two new `deleteOrphan*` helpers, wiring into four refresh closures, the `maybeScheduleReconcile` trigger and its state, and the `runReconcile` orchestration + per-entity reconcile functions.
- `internal/repo/sqlite_test.go` — unit tests for new helpers, the trigger gate, and the per-entity reconcile diffs.

**Boundary check:** all three layers fit naturally in `sqlite.go` because they own SQLite state and use the existing `*api.Client`. The file is already large (~1200 lines) but the additions are tightly scoped to the orphan-handling area; splitting it isn't justified by this change.

---

## Task 1: Add `deleteOrphanProject` helper

**Why first:** Smallest unit of Layer 1. Mirrors the existing `deleteOrphanIssue` pattern. Independently testable and committable.

**Files:**
- Modify: `internal/repo/sqlite.go` (add helper near existing `deleteOrphanIssue`, around line 760)
- Modify: `internal/repo/sqlite_test.go` (add test near existing `TestDeleteOrphanIssue`, end of file)

- [ ] **Step 1: Write the failing test**

Append to `internal/repo/sqlite_test.go`:

```go
func TestDeleteOrphanProject(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()
	now := time.Now()
	const projectID = "proj-orphan"
	const otherID = "proj-keep"

	team := api.Team{ID: "team-1", Key: "TST", Name: "Test", CreatedAt: now, UpdatedAt: now}
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	q := store.Queries()
	mustExec := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	// Seed both projects.
	for _, id := range []string{projectID, otherID} {
		mustExec("project", q.UpsertProject(ctx, db.UpsertProjectParams{
			ID: id, SlugID: id, Name: id, SyncedAt: now, Data: []byte("{}"),
		}))
	}
	// Sub-resources on the orphan.
	mustExec("project-team", q.UpsertProjectTeam(ctx, db.UpsertProjectTeamParams{
		ProjectID: projectID, TeamID: "team-1", SyncedAt: now,
	}))
	mustExec("project-doc", q.UpsertDocument(ctx, db.UpsertDocumentParams{
		ID: "pd1", SlugID: "pd1", Title: "Doc",
		ProjectID: sql.NullString{String: projectID, Valid: true},
		SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("project-update", q.UpsertProjectUpdate(ctx, db.UpsertProjectUpdateParams{
		ID: "pu1", ProjectID: projectID, Body: "ok", CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("project-milestone", q.UpsertProjectMilestone(ctx, db.UpsertProjectMilestoneParams{
		ID: "pm1", ProjectID: projectID, Name: "MS", SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("initiative-project link", q.UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
		InitiativeID: "init-1", ProjectID: projectID, SyncedAt: now,
	}))
	// Sub-resources on the keeper.
	mustExec("keeper doc", q.UpsertDocument(ctx, db.UpsertDocumentParams{
		ID: "pd-keep", SlugID: "pd-keep", Title: "Keep",
		ProjectID: sql.NullString{String: otherID, Valid: true},
		SyncedAt: now, Data: []byte("{}"),
	}))

	repo.deleteOrphanProject(ctx, projectID)

	// Orphan gone.
	if _, err := q.GetProject(ctx, projectID); err != sql.ErrNoRows {
		t.Errorf("orphan project not deleted: err=%v", err)
	}
	if got, _ := q.ListProjectTeamIDs(ctx, projectID); len(got) != 0 {
		t.Errorf("orphan project-team links not deleted: %d remain", len(got))
	}
	if got, _ := q.ListProjectDocuments(ctx, sql.NullString{String: projectID, Valid: true}); len(got) != 0 {
		t.Errorf("orphan project docs not deleted: %d remain", len(got))
	}
	if got, _ := q.ListProjectUpdates(ctx, projectID); len(got) != 0 {
		t.Errorf("orphan project updates not deleted: %d remain", len(got))
	}
	if got, _ := q.ListProjectMilestones(ctx, projectID); len(got) != 0 {
		t.Errorf("orphan milestones not deleted: %d remain", len(got))
	}
	// Keeper survives.
	if _, err := q.GetProject(ctx, otherID); err != nil {
		t.Errorf("keeper project was deleted: %v", err)
	}
	if got, _ := q.ListProjectDocuments(ctx, sql.NullString{String: otherID, Valid: true}); len(got) != 1 {
		t.Errorf("keeper doc clobbered: %d remain", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repo/ -run TestDeleteOrphanProject -count=1 -v`
Expected: FAIL with `repo.deleteOrphanProject undefined` (compile error).

- [ ] **Step 3: Add the helper**

In `internal/repo/sqlite.go`, immediately after the existing `deleteOrphanIssue` function (which ends with `log.Printf("[repo] deleted orphan issue %s ..."`), add:

```go
// deleteOrphanProject removes a project and all its sub-resources from SQLite.
// Mirrors deleteOrphanIssue. Does not modify the issues.project_id column on
// issues that referenced this project — those stay until the issue is next
// synced.
func (r *SQLiteRepository) deleteOrphanProject(ctx context.Context, projectID string) {
	q := r.store.Queries()
	if err := q.DeleteProjectTeams(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: project-teams for %s: %v", projectID, err)
	}
	if err := q.DeleteProjectDocuments(ctx, sql.NullString{String: projectID, Valid: true}); err != nil {
		log.Printf("[repo] orphan cleanup: project documents for %s: %v", projectID, err)
	}
	if err := q.DeleteProjectUpdates(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: project updates for %s: %v", projectID, err)
	}
	if err := q.DeleteProjectMilestones(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: project milestones for %s: %v", projectID, err)
	}
	if err := q.DeleteInitiativeProjectsByProject(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: initiative-project links for %s: %v", projectID, err)
	}
	if err := q.DeleteProject(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: project %s: %v", projectID, err)
		return
	}
	log.Printf("[repo] deleted orphan project %s (no longer exists in Linear)", projectID)
}
```

Note: `DeleteInitiativeProjectsByProject` doesn't exist yet — existing query `DeleteInitiativeProject` deletes by `(initiative_id, project_id)` pair, not by `project_id` alone. Add it in the next step.

- [ ] **Step 4: Add missing SQL query for initiative-project link deletion by project**

In `internal/db/queries.sql`, find the existing `DeleteInitiativeProjects` query (around line 617) and add a new one right after it:

```sql
-- name: DeleteInitiativeProjectsByProject :exec
DELETE FROM initiative_projects WHERE project_id = ?;
```

Run: `sqlc generate`
Expected: no output, `internal/db/queries.sql.go` updated with the new function.

Verify: `grep -n "DeleteInitiativeProjectsByProject" internal/db/queries.sql.go`
Expected: two matches (const + func).

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/repo/ -run TestDeleteOrphanProject -count=1 -v`
Expected: PASS, with log line `[repo] deleted orphan project proj-orphan (no longer exists in Linear)`.

- [ ] **Step 6: Commit**

```bash
git add internal/db/queries.sql internal/db/queries.sql.go internal/repo/sqlite.go internal/repo/sqlite_test.go
git commit -m "$(cat <<'EOF'
feat: add deleteOrphanProject helper for reactive cleanup

Mirrors deleteOrphanIssue. Removes the project plus its sub-resources
(project_teams, documents, project_updates, milestones, initiative-project
links). Adds a new DeleteInitiativeProjectsByProject query since the
existing DeleteInitiativeProject takes the (initiative_id, project_id)
pair.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `deleteOrphanInitiative` helper

**Why second:** Mirror of Task 1 for initiatives. Same shape, smaller set of sub-resources.

**Files:**
- Modify: `internal/repo/sqlite.go` (add helper after `deleteOrphanProject`)
- Modify: `internal/repo/sqlite_test.go` (add test after `TestDeleteOrphanProject`)

- [ ] **Step 1: Write the failing test**

Append to `internal/repo/sqlite_test.go`:

```go
func TestDeleteOrphanInitiative(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()
	now := time.Now()
	const initID = "init-orphan"
	const otherID = "init-keep"

	q := store.Queries()
	mustExec := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	for _, id := range []string{initID, otherID} {
		mustExec("initiative", q.UpsertInitiative(ctx, db.UpsertInitiativeParams{
			ID: id, SlugID: id, Name: id, SyncedAt: now, Data: []byte("{}"),
		}))
	}
	mustExec("init-doc", q.UpsertDocument(ctx, db.UpsertDocumentParams{
		ID: "id1", SlugID: "id1", Title: "Doc",
		InitiativeID: sql.NullString{String: initID, Valid: true},
		SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("init-update", q.UpsertInitiativeUpdate(ctx, db.UpsertInitiativeUpdateParams{
		ID: "iu1", InitiativeID: initID, Body: "ok", CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}))
	mustExec("init-project link", q.UpsertInitiativeProject(ctx, db.UpsertInitiativeProjectParams{
		InitiativeID: initID, ProjectID: "some-proj", SyncedAt: now,
	}))
	// Keeper sub-resource.
	mustExec("keeper update", q.UpsertInitiativeUpdate(ctx, db.UpsertInitiativeUpdateParams{
		ID: "iu-keep", InitiativeID: otherID, Body: "keep", CreatedAt: now, UpdatedAt: now, SyncedAt: now, Data: []byte("{}"),
	}))

	repo.deleteOrphanInitiative(ctx, initID)

	if _, err := q.GetInitiative(ctx, initID); err != sql.ErrNoRows {
		t.Errorf("orphan initiative not deleted: err=%v", err)
	}
	if got, _ := q.ListInitiativeDocuments(ctx, sql.NullString{String: initID, Valid: true}); len(got) != 0 {
		t.Errorf("orphan init docs not deleted: %d remain", len(got))
	}
	if got, _ := q.ListInitiativeUpdates(ctx, initID); len(got) != 0 {
		t.Errorf("orphan init updates not deleted: %d remain", len(got))
	}
	if got, _ := q.ListInitiativeProjectIDs(ctx, initID); len(got) != 0 {
		t.Errorf("orphan init-project links not deleted: %d remain", len(got))
	}
	if _, err := q.GetInitiative(ctx, otherID); err != nil {
		t.Errorf("keeper initiative was deleted: %v", err)
	}
	if got, _ := q.ListInitiativeUpdates(ctx, otherID); len(got) != 1 {
		t.Errorf("keeper init update clobbered: %d remain", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repo/ -run TestDeleteOrphanInitiative -count=1 -v`
Expected: FAIL with `repo.deleteOrphanInitiative undefined` (compile error).

- [ ] **Step 3: Add the helper**

In `internal/repo/sqlite.go`, immediately after the new `deleteOrphanProject` function from Task 1, add:

```go
// deleteOrphanInitiative removes an initiative and all its sub-resources from SQLite.
// Mirrors deleteOrphanProject.
func (r *SQLiteRepository) deleteOrphanInitiative(ctx context.Context, initiativeID string) {
	q := r.store.Queries()
	if err := q.DeleteInitiativeDocuments(ctx, sql.NullString{String: initiativeID, Valid: true}); err != nil {
		log.Printf("[repo] orphan cleanup: initiative documents for %s: %v", initiativeID, err)
	}
	if err := q.DeleteInitiativeUpdates(ctx, initiativeID); err != nil {
		log.Printf("[repo] orphan cleanup: initiative updates for %s: %v", initiativeID, err)
	}
	if err := q.DeleteInitiativeProjects(ctx, initiativeID); err != nil {
		log.Printf("[repo] orphan cleanup: initiative-project links for %s: %v", initiativeID, err)
	}
	if err := q.DeleteInitiative(ctx, initiativeID); err != nil {
		log.Printf("[repo] orphan cleanup: initiative %s: %v", initiativeID, err)
		return
	}
	log.Printf("[repo] deleted orphan initiative %s (no longer exists in Linear)", initiativeID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/repo/ -run TestDeleteOrphanInitiative -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/sqlite.go internal/repo/sqlite_test.go
git commit -m "$(cat <<'EOF'
feat: add deleteOrphanInitiative helper for reactive cleanup

Mirrors deleteOrphanProject. Removes the initiative plus its documents,
updates, and initiative-project links.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Wire reactive cleanup into the four sibling refresh closures

**Why third:** Layer 1 is incomplete until the new helpers are actually invoked when Linear returns "Entity not found". Four mechanical edits.

**Files:**
- Modify: `internal/repo/sqlite.go` — four `refresh*` functions (`refreshProjectDocuments`, `refreshInitiativeDocuments`, `refreshProjectUpdates`, `refreshInitiativeUpdates`).

These functions live in the project/initiative sections of `sqlite.go`. Each currently has the shape `if err != nil { return err }` after the API call.

- [ ] **Step 1: Update `refreshProjectDocuments`**

Find this block (around line 894):

```go
func (r *SQLiteRepository) refreshProjectDocuments(ctx context.Context, projectID string) error {
	docs, err := r.client.GetProjectDocuments(ctx, projectID)
	if err != nil {
		return err
	}
```

Change the `if err != nil` block to:

```go
func (r *SQLiteRepository) refreshProjectDocuments(ctx context.Context, projectID string) error {
	docs, err := r.client.GetProjectDocuments(ctx, projectID)
	if err != nil {
		if isEntityNotFound(err) {
			r.deleteOrphanProject(ctx, projectID)
		}
		return err
	}
```

- [ ] **Step 2: Update `refreshInitiativeDocuments`**

Find (around line 942):

```go
func (r *SQLiteRepository) refreshInitiativeDocuments(ctx context.Context, initiativeID string) error {
	docs, err := r.client.GetInitiativeDocuments(ctx, initiativeID)
	if err != nil {
		return err
	}
```

Change to:

```go
func (r *SQLiteRepository) refreshInitiativeDocuments(ctx context.Context, initiativeID string) error {
	docs, err := r.client.GetInitiativeDocuments(ctx, initiativeID)
	if err != nil {
		if isEntityNotFound(err) {
			r.deleteOrphanInitiative(ctx, initiativeID)
		}
		return err
	}
```

- [ ] **Step 3: Update `refreshProjectUpdates`**

Find (around line 1044):

```go
func (r *SQLiteRepository) refreshProjectUpdates(ctx context.Context, projectID string) error {
	updates, err := r.client.GetProjectUpdates(ctx, projectID)
	if err != nil {
		return err
```

Change to:

```go
func (r *SQLiteRepository) refreshProjectUpdates(ctx context.Context, projectID string) error {
	updates, err := r.client.GetProjectUpdates(ctx, projectID)
	if err != nil {
		if isEntityNotFound(err) {
			r.deleteOrphanProject(ctx, projectID)
		}
		return err
```

- [ ] **Step 4: Update `refreshInitiativeUpdates`**

Find `refreshInitiativeUpdates` (around line 1092). Apply the same change:

```go
func (r *SQLiteRepository) refreshInitiativeUpdates(ctx context.Context, initiativeID string) error {
	updates, err := r.client.GetInitiativeUpdates(ctx, initiativeID)
	if err != nil {
		if isEntityNotFound(err) {
			r.deleteOrphanInitiative(ctx, initiativeID)
		}
		return err
```

- [ ] **Step 5: Verify compile + existing tests still pass**

Run: `go build ./... && go test ./internal/repo/ -count=1`
Expected: build succeeds, `ok github.com/jra3/linear-fuse/internal/repo ...`.

The wiring is intentionally not unit-tested directly — it would require mocking the API client at the repo layer, which the codebase doesn't do today (the existing `f2417ba` issue wiring isn't unit-tested either). The helpers themselves are covered by Tasks 1 and 2; the wiring is verifiable by inspection.

- [ ] **Step 6: Commit**

```bash
git add internal/repo/sqlite.go
git commit -m "$(cat <<'EOF'
feat: wire reactive orphan cleanup into project/initiative refresh paths

Mirrors the issue-refresh handling from f2417ba. When Linear returns
"Entity not found" from a project or initiative sub-resource refresh,
the corresponding deleteOrphan* helper removes the local row and all
its sub-resources.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add adaptive reconcile trigger

**Why fourth:** Layer 2 wires the proactive layer to actually fire. Independent of Layer 3 (reconcile can be wired up before its target body exists, as long as the target compiles — we'll add a no-op stub here and replace it in Task 6).

**Files:**
- Modify: `internal/repo/sqlite.go` — add state to `SQLiteRepository`, add `maybeScheduleReconcile`, add no-op `runReconcile` stub, call the trigger from all three `deleteOrphan*` helpers.
- Modify: `internal/repo/sqlite_test.go` — tests for the trigger gate.

- [ ] **Step 1: Write the failing tests**

Append to `internal/repo/sqlite_test.go`:

```go
func TestMaybeScheduleReconcile_ColdStart(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	client := &api.Client{} // non-nil so the trigger isn't skipped
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	// Cold start: lastReconcileAt is zero, so the first call should schedule.
	repo.maybeScheduleReconcile()

	// reconcilePending should be set immediately (sync part of the trigger).
	// The goroutine clears it; we only check that the gate engaged.
	if !repo.reconcilePending.Load() && repo.lastReconcileAt.IsZero() {
		// Race: goroutine may have run and finished by now. Either pending
		// was momentarily set, or lastReconcileAt is now non-zero. Wait briefly
		// for the latter.
		for i := 0; i < 50; i++ {
			if !repo.lastReconcileAt.IsZero() {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if repo.lastReconcileAt.IsZero() {
			t.Fatal("trigger did not fire on cold start (lastReconcileAt still zero)")
		}
	}
}

func TestMaybeScheduleReconcile_CooldownGate(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	client := &api.Client{}
	repo := NewSQLiteRepository(store, client)
	defer repo.Close()

	// Simulate a recent reconcile.
	repo.reconcileMu.Lock()
	repo.lastReconcileAt = time.Now()
	repo.reconcileMu.Unlock()

	// Should not fire while within cooldown.
	repo.maybeScheduleReconcile()
	if repo.reconcilePending.Load() {
		t.Error("trigger fired despite cooldown")
	}
}

func TestMaybeScheduleReconcile_NilClient(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	repo.maybeScheduleReconcile() // must not panic
	if repo.reconcilePending.Load() {
		t.Error("trigger fired with nil client")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/repo/ -run TestMaybeScheduleReconcile -count=1 -v`
Expected: FAIL with `repo.reconcilePending undefined` and `repo.maybeScheduleReconcile undefined`.

- [ ] **Step 3: Add the trigger state and `sync/atomic` import**

In `internal/repo/sqlite.go`, update the import block from:

```go
import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)
```

to:

```go
import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)
```

Add a constant near `defaultStalenessThreshold` at the top of the file (around line 19):

```go
// reconcileCooldown is the minimum gap between proactive reconciliation
// passes. The pass is triggered by reactive orphan deletions, then
// suppressed for this window to bound API cost.
const reconcileCooldown = 6 * time.Hour
```

Add fields to the `SQLiteRepository` struct (around line 37–51). After the existing `refreshSem chan struct{}` line, add:

```go
	// Adaptive reconciliation: triggered by reactive orphan deletions,
	// rate-limited by reconcileCooldown.
	reconcileMu      sync.Mutex
	lastReconcileAt  time.Time
	reconcilePending atomic.Bool
```

- [ ] **Step 4: Add the `maybeScheduleReconcile` method and stub `runReconcile`**

In `internal/repo/sqlite.go`, add a new section after `triggerBackgroundRefresh` (around line 137):

```go
// maybeScheduleReconcile fires a proactive reconciliation pass if no pass
// has run within reconcileCooldown. Called from every deleteOrphan* helper
// after a successful orphan deletion — the deletion itself is evidence of
// drift between SQLite and Linear, justifying a full sweep to find siblings.
func (r *SQLiteRepository) maybeScheduleReconcile() {
	if r.client == nil {
		return
	}
	if r.reconcilePending.Load() {
		return
	}

	r.reconcileMu.Lock()
	elapsed := time.Since(r.lastReconcileAt)
	r.reconcileMu.Unlock()

	if elapsed < reconcileCooldown {
		return
	}
	if !r.reconcilePending.CompareAndSwap(false, true) {
		return
	}

	go r.runReconcile()
}

// runReconcile performs a full sweep across issues, projects, and
// initiatives, deleting any local row whose ID is absent from Linear's
// authoritative response. Stubbed here; the body is implemented in the
// reconciliation task.
func (r *SQLiteRepository) runReconcile() {
	defer r.reconcilePending.Store(false)

	// Task 6 will replace this stub with the real per-entity reconcile calls.
	r.reconcileMu.Lock()
	r.lastReconcileAt = time.Now()
	r.reconcileMu.Unlock()
	log.Printf("[reconcile] pass complete: stub (no work yet)")
}
```

- [ ] **Step 5: Wire the trigger into all three `deleteOrphan*` helpers**

In `deleteOrphanIssue`, find the final `log.Printf("[repo] deleted orphan issue %s ..."` line and add a call to `maybeScheduleReconcile` after it:

```go
	if err := q.DeleteIssue(ctx, issueID); err != nil {
		log.Printf("[repo] orphan cleanup: issue %s: %v", issueID, err)
		return
	}
	log.Printf("[repo] deleted orphan issue %s (no longer exists in Linear)", issueID)
	r.maybeScheduleReconcile()
}
```

Apply the same change at the end of `deleteOrphanProject`:

```go
	if err := q.DeleteProject(ctx, projectID); err != nil {
		log.Printf("[repo] orphan cleanup: project %s: %v", projectID, err)
		return
	}
	log.Printf("[repo] deleted orphan project %s (no longer exists in Linear)", projectID)
	r.maybeScheduleReconcile()
}
```

And at the end of `deleteOrphanInitiative`:

```go
	if err := q.DeleteInitiative(ctx, initiativeID); err != nil {
		log.Printf("[repo] orphan cleanup: initiative %s: %v", initiativeID, err)
		return
	}
	log.Printf("[repo] deleted orphan initiative %s (no longer exists in Linear)", initiativeID)
	r.maybeScheduleReconcile()
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/repo/ -run "TestMaybeScheduleReconcile|TestDeleteOrphan" -count=1 -v`
Expected: PASS for all four (the three new trigger tests plus the existing `TestDeleteOrphan*` from Tasks 1 and 2 which now incidentally exercise the trigger).

The trigger goroutine in `TestDeleteOrphan*` tests will fire the stub `runReconcile`, log the stub completion line, and clear `reconcilePending`. That's harmless.

- [ ] **Step 7: Commit**

```bash
git add internal/repo/sqlite.go internal/repo/sqlite_test.go
git commit -m "$(cat <<'EOF'
feat: add adaptive reconcile trigger fired by orphan deletions

Every successful deleteOrphan{Issue,Project,Initiative} now calls
maybeScheduleReconcile, which kicks off a full reconciliation pass
unless one has run within the 6h cooldown. runReconcile is stubbed in
this commit; the real per-entity body lands in the next change.

Cold-start orphans heal automatically: lastReconcileAt starts zero, so
the first reactive cleanup after a restart triggers a sweep.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Add `LowBudget` helper and three ID-only API queries

**Why fifth:** Layer 3's prerequisites — the API client needs methods that return ID-only lists, and a way to check the rate-limit budget before launching a per-team paginated sweep.

**Files:**
- Modify: `internal/api/queries.go` — three new query strings.
- Modify: `internal/api/client.go` — `LowBudget` helper + three new methods.
- Modify: `internal/api/client_test.go` — tests for `LowBudget` and the three methods.

- [ ] **Step 1: Write the failing tests**

Look at existing tests in `internal/api/client_test.go` to find the mock-HTTP-server pattern. The repo uses `httptest.NewServer` extensively; mimic an existing query test.

Append to `internal/api/client_test.go`:

```go
func TestClient_LowBudget(t *testing.T) {
	c := NewClient("test-key")
	// Fresh limiter has full burst (10 tokens). Should not be low.
	if c.LowBudget() {
		t.Error("LowBudget true with full burst")
	}
	// Drain burst.
	for i := 0; i < 9; i++ {
		c.limiter.Reserve()
	}
	if !c.LowBudget() {
		t.Error("LowBudget false with <5 tokens remaining")
	}
}

func TestClient_GetTeamIssueIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"i1"},{"id":"i2"},{"id":"i3"}]}}}}`))
	}))
	defer server.Close()

	c := NewClient("test-key")
	c.SetAPIURL(server.URL)

	ids, err := c.GetTeamIssueIDs(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Join(ids, ","); got != "i1,i2,i3" {
		t.Errorf("got %q, want i1,i2,i3", got)
	}
}

func TestClient_GetWorkspaceProjectIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"projects":{"nodes":[{"id":"p1"},{"id":"p2"}]}}}`))
	}))
	defer server.Close()

	c := NewClient("test-key")
	c.SetAPIURL(server.URL)

	ids, err := c.GetWorkspaceProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Join(ids, ","); got != "p1,p2" {
		t.Errorf("got %q, want p1,p2", got)
	}
}

func TestClient_GetWorkspaceInitiativeIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"initiatives":{"nodes":[{"id":"i1"}]}}}`))
	}))
	defer server.Close()

	c := NewClient("test-key")
	c.SetAPIURL(server.URL)

	ids, err := c.GetWorkspaceInitiativeIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Join(ids, ","); got != "i1" {
		t.Errorf("got %q, want i1", got)
	}
}
```

If `strings`, `httptest`, or `net/http` aren't already imported in this test file, the compile error in Step 2 will surface them — add them then.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/ -run "TestClient_LowBudget|TestClient_GetTeamIssueIDs|TestClient_GetWorkspaceProjectIDs|TestClient_GetWorkspaceInitiativeIDs" -count=1 -v`
Expected: FAIL with undefined method errors.

- [ ] **Step 3: Add the three GraphQL query strings**

Append to `internal/api/queries.go`:

```go
// queryTeamIssueIDs paginates issue IDs for a team. Used by the
// reconciliation pass to enumerate the authoritative set of issue IDs
// without paying the cost of full IssueFields.
const queryTeamIssueIDs = `
query TeamIssueIDs($teamId: String!, $first: Int!, $after: String) {
  team(id: $teamId) {
    issues(first: $first, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { id }
    }
  }
}
`

// queryWorkspaceProjectIDs returns IDs of all projects in the workspace.
// Linear's project list is small (~10s–100s); pagination not required.
const queryWorkspaceProjectIDs = `
query WorkspaceProjectIDs {
  projects {
    nodes { id }
  }
}
`

// queryWorkspaceInitiativeIDs returns IDs of all initiatives in the workspace.
const queryWorkspaceInitiativeIDs = `
query WorkspaceInitiativeIDs {
  initiatives {
    nodes { id }
  }
}
`
```

- [ ] **Step 4: Add `LowBudget` and the three methods to `*Client`**

Append to `internal/api/client.go` (anywhere reasonable; near the existing rate-limit helpers around line 280 is a good spot):

```go
// LowBudget reports whether the rate limiter has fewer than 5 tokens left.
// The reconciliation pass uses this to defer the next per-team page when
// budget is tight, leaving headroom for user-facing writes and ongoing sync.
func (c *Client) LowBudget() bool {
	return c.limiter.Tokens() < 5
}

// GetTeamIssueIDs paginates issue IDs for the given team. Used by the
// reconciliation pass — much cheaper than fetching full IssueFields.
func (c *Client) GetTeamIssueIDs(ctx context.Context, teamID string) ([]string, error) {
	var ids []string
	var cursor string
	const pageSize = 100

	for {
		var result struct {
			Team struct {
				Issues struct {
					PageInfo PageInfo `json:"pageInfo"`
					Nodes    []struct {
						ID string `json:"id"`
					} `json:"nodes"`
				} `json:"issues"`
			} `json:"team"`
		}

		vars := map[string]any{
			"teamId": teamID,
			"first":  pageSize,
		}
		if cursor != "" {
			vars["after"] = cursor
		}

		if err := c.query(ctx, queryTeamIssueIDs, vars, &result); err != nil {
			return nil, err
		}

		for _, node := range result.Team.Issues.Nodes {
			ids = append(ids, node.ID)
		}

		if !result.Team.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = result.Team.Issues.PageInfo.EndCursor
	}

	return ids, nil
}

// GetWorkspaceProjectIDs returns IDs of every project in the workspace.
func (c *Client) GetWorkspaceProjectIDs(ctx context.Context) ([]string, error) {
	var result struct {
		Projects struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"projects"`
	}
	if err := c.query(ctx, queryWorkspaceProjectIDs, nil, &result); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(result.Projects.Nodes))
	for _, n := range result.Projects.Nodes {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

// GetWorkspaceInitiativeIDs returns IDs of every initiative in the workspace.
func (c *Client) GetWorkspaceInitiativeIDs(ctx context.Context) ([]string, error) {
	var result struct {
		Initiatives struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"initiatives"`
	}
	if err := c.query(ctx, queryWorkspaceInitiativeIDs, nil, &result); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(result.Initiatives.Nodes))
	for _, n := range result.Initiatives.Nodes {
		ids = append(ids, n.ID)
	}
	return ids, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/api/ -run "TestClient_LowBudget|TestClient_GetTeamIssueIDs|TestClient_GetWorkspaceProjectIDs|TestClient_GetWorkspaceInitiativeIDs" -count=1 -v`
Expected: PASS for all four.

Then full API package test: `go test ./internal/api/ -count=1`
Expected: `ok github.com/jra3/linear-fuse/internal/api ...`.

- [ ] **Step 6: Commit**

```bash
git add internal/api/queries.go internal/api/client.go internal/api/client_test.go
git commit -m "$(cat <<'EOF'
feat: add ID-only Linear queries and LowBudget helper for reconcile

Adds GetTeamIssueIDs (paginated), GetWorkspaceProjectIDs, and
GetWorkspaceInitiativeIDs — used by the reconciliation pass to fetch
authoritative ID sets cheaply (no full IssueFields/ProjectFields).

LowBudget reports whether the rate limiter has fewer than 5 tokens
remaining, so reconcile can defer to leave headroom for user writes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Implement the reconciliation pass

**Why last:** Replaces the `runReconcile` stub from Task 4 with the real per-entity body, using the API methods from Task 5 and the orphan helpers from Tasks 1–2.

**Files:**
- Modify: `internal/repo/sqlite.go` — replace stub `runReconcile`; add `reconcileIssues`, `reconcileProjects`, `reconcileInitiatives`, and a `setDiff` helper.
- Modify: `internal/repo/sqlite_test.go` — add tests using a tiny client interface or by exercising the diff helper directly.

The existing codebase does not have an injectable API client for the repo (the repo holds `*api.Client` directly). To test reconcile behavior without rewiring, we test the `setDiff` helper directly and test the `reconcileIssues` flow via a small refactor: extract a function that takes pre-fetched ID lists and performs the diff + delete. The integration with the real client is then mechanical.

- [ ] **Step 1: Write failing tests for the diff helper and the deletion driver**

Append to `internal/repo/sqlite_test.go`:

```go
func TestSetDiff(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		local, api     []string
		wantOrphanIDs  []string
	}{
		{"all present", []string{"a", "b"}, []string{"a", "b", "c"}, nil},
		{"one missing", []string{"a", "b", "c"}, []string{"a", "c"}, []string{"b"}},
		{"all missing", []string{"a", "b"}, []string{}, []string{"a", "b"}},
		{"empty local", []string{}, []string{"a"}, nil},
	}
	for _, c := range cases {
		got := setDiff(c.local, c.api)
		// Order-independent compare.
		gotSet := make(map[string]bool, len(got))
		for _, id := range got {
			gotSet[id] = true
		}
		if len(gotSet) != len(c.wantOrphanIDs) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.wantOrphanIDs)
			continue
		}
		for _, want := range c.wantOrphanIDs {
			if !gotSet[want] {
				t.Errorf("%s: missing %q in %v", c.name, want, got)
			}
		}
	}
}

func TestReconcileIssuesForTeam_DeletesOrphans(t *testing.T) {
	t.Parallel()
	store, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewSQLiteRepository(store, nil)
	ctx := context.Background()
	now := time.Now()

	team := api.Team{ID: "team-1", Key: "TST", Name: "T", CreatedAt: now, UpdatedAt: now}
	if err := store.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	// Seed three local issues; "alive" stays on API, "gone" and "alsogone" do not.
	for _, id := range []string{"alive", "gone", "alsogone"} {
		issue := api.Issue{
			ID: id, Identifier: id, Title: id, Team: &team,
			State: api.State{ID: "s1", Name: "Todo", Type: "unstarted"},
			CreatedAt: now, UpdatedAt: now,
		}
		data, _ := db.APIIssueToDBIssue(issue)
		if err := store.Queries().UpsertIssue(ctx, data.ToUpsertParams()); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	// Authoritative list from "Linear": only "alive" exists.
	deleted := repo.reconcileIssuesForTeam(ctx, "team-1", []string{"alive"})
	if deleted != 2 {
		t.Errorf("got deleted=%d, want 2", deleted)
	}

	if _, err := store.Queries().GetIssueByID(ctx, "alive"); err != nil {
		t.Errorf("alive issue was deleted: %v", err)
	}
	if _, err := store.Queries().GetIssueByID(ctx, "gone"); err != sql.ErrNoRows {
		t.Errorf("gone issue still present: err=%v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/repo/ -run "TestSetDiff|TestReconcileIssuesForTeam" -count=1 -v`
Expected: FAIL with undefined `setDiff` and `reconcileIssuesForTeam`.

- [ ] **Step 3: Implement `setDiff` and the per-team reconcile helper**

In `internal/repo/sqlite.go`, replace the stub `runReconcile` body (added in Task 4) with the real implementation, and add the helpers below it:

```go
// runReconcile performs a full sweep across issues, projects, and
// initiatives, deleting any local row whose ID is absent from Linear's
// authoritative response. Triggered by maybeScheduleReconcile.
func (r *SQLiteRepository) runReconcile() {
	defer r.reconcilePending.Store(false)
	ctx, cancel := context.WithTimeout(r.refreshContext, 10*time.Minute)
	defer cancel()

	log.Printf("[reconcile] adaptive trigger after orphan delete; pass starting")
	start := time.Now()

	issues := r.reconcileIssues(ctx)
	projects := r.reconcileProjects(ctx)
	initiatives := r.reconcileInitiatives(ctx)

	r.reconcileMu.Lock()
	r.lastReconcileAt = time.Now()
	r.reconcileMu.Unlock()

	log.Printf("[reconcile] pass complete: issues=%d projects=%d initiatives=%d duration=%s",
		issues, projects, initiatives, time.Since(start).Round(time.Millisecond))
}

// reconcileIssues walks every team in SQLite and, for each, fetches the
// authoritative issue ID set from Linear, diffs against the local set,
// and deletes the orphans. Returns the total number of orphans removed.
func (r *SQLiteRepository) reconcileIssues(ctx context.Context) int {
	teams, err := r.store.Queries().ListTeams(ctx)
	if err != nil {
		log.Printf("[reconcile] list teams: %v", err)
		return 0
	}
	deleted := 0
	for _, team := range teams {
		if r.client.LowBudget() {
			log.Printf("[reconcile] budget low; deferring remaining teams")
			return deleted
		}
		apiIDs, err := r.client.GetTeamIssueIDs(ctx, team.ID)
		if err != nil {
			log.Printf("[reconcile] issues team %s: %v (skipping)", team.Key, err)
			continue
		}
		deleted += r.reconcileIssuesForTeam(ctx, team.ID, apiIDs)
	}
	return deleted
}

// reconcileIssuesForTeam diffs apiIDs against SQLite's issue IDs for the
// given team and deletes any locals missing from the API set. Split out
// so tests can drive the diff/delete logic without needing a live client.
func (r *SQLiteRepository) reconcileIssuesForTeam(ctx context.Context, teamID string, apiIDs []string) int {
	rows, err := r.store.Queries().ListTeamIssueIDs(ctx, teamID)
	if err != nil {
		log.Printf("[reconcile] list local issues for team %s: %v", teamID, err)
		return 0
	}
	localIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		localIDs = append(localIDs, row.ID)
	}
	deleted := 0
	for _, id := range setDiff(localIDs, apiIDs) {
		r.deleteOrphanIssue(ctx, id)
		deleted++
	}
	return deleted
}

// reconcileProjects fetches the authoritative project ID set from Linear,
// diffs against SQLite, and deletes the orphans.
func (r *SQLiteRepository) reconcileProjects(ctx context.Context) int {
	if r.client.LowBudget() {
		log.Printf("[reconcile] budget low; skipping projects")
		return 0
	}
	apiIDs, err := r.client.GetWorkspaceProjectIDs(ctx)
	if err != nil {
		log.Printf("[reconcile] projects fetch: %v (skipping)", err)
		return 0
	}
	rows, err := r.store.Queries().ListProjects(ctx)
	if err != nil {
		log.Printf("[reconcile] list local projects: %v", err)
		return 0
	}
	localIDs := make([]string, 0, len(rows))
	for _, p := range rows {
		localIDs = append(localIDs, p.ID)
	}
	deleted := 0
	for _, id := range setDiff(localIDs, apiIDs) {
		r.deleteOrphanProject(ctx, id)
		deleted++
	}
	return deleted
}

// reconcileInitiatives fetches the authoritative initiative ID set,
// diffs against SQLite, and deletes the orphans.
func (r *SQLiteRepository) reconcileInitiatives(ctx context.Context) int {
	if r.client.LowBudget() {
		log.Printf("[reconcile] budget low; skipping initiatives")
		return 0
	}
	apiIDs, err := r.client.GetWorkspaceInitiativeIDs(ctx)
	if err != nil {
		log.Printf("[reconcile] initiatives fetch: %v (skipping)", err)
		return 0
	}
	rows, err := r.store.Queries().ListInitiatives(ctx)
	if err != nil {
		log.Printf("[reconcile] list local initiatives: %v", err)
		return 0
	}
	localIDs := make([]string, 0, len(rows))
	for _, i := range rows {
		localIDs = append(localIDs, i.ID)
	}
	deleted := 0
	for _, id := range setDiff(localIDs, apiIDs) {
		r.deleteOrphanInitiative(ctx, id)
		deleted++
	}
	return deleted
}

// setDiff returns elements in `local` that are not in `api`. Used by the
// reconciliation pass to identify orphan rows.
func setDiff(local, api []string) []string {
	if len(local) == 0 {
		return nil
	}
	apiSet := make(map[string]struct{}, len(api))
	for _, id := range api {
		apiSet[id] = struct{}{}
	}
	var orphans []string
	for _, id := range local {
		if _, ok := apiSet[id]; !ok {
			orphans = append(orphans, id)
		}
	}
	return orphans
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/repo/ -run "TestSetDiff|TestReconcileIssuesForTeam" -count=1 -v`
Expected: PASS.

Then full repo + api + integration tests: `go test ./... -count=1`
Expected: all `ok`.

- [ ] **Step 5: Manual smoke verification**

Build and install: `make install && cp ~/.local/bin/linearfs ~/bin/linearfs` (path drift; see CLAUDE.md and previous fix).

```bash
make stop && make start
sleep 5
journalctl --user -u linearfs --since "30 sec ago" --no-pager | head -40
```

Expected: normal sync output (`[sync] team ...`), no `[ratelimit] ... waited` storms, no `Entity not found` flood.

To exercise reconcile end-to-end (optional, requires a manufactured orphan):

```bash
# Insert a fake orphan issue (no real Linear ID will match this UUID).
sqlite3 ~/.config/linearfs/cache.db "INSERT INTO issues (id, identifier, team_id, title, created_at, updated_at, synced_at, data) VALUES ('fake-orphan-id', 'TST-9999', (SELECT id FROM teams LIMIT 1), 'fake', datetime('now'), datetime('now'), datetime('now'), '{}');"
# Touch the mount to trigger a refresh that will fail with Entity not found.
ls ~/am/linear/teams/TST/issues/TST-9999/ 2>&1 || true
sleep 2
# Logs should show: orphan cleanup, then "[reconcile] adaptive trigger after orphan delete; pass starting"
journalctl --user -u linearfs --since "10 sec ago" --no-pager | grep -E "orphan|reconcile"
```

- [ ] **Step 6: Commit**

```bash
git add internal/repo/sqlite.go internal/repo/sqlite_test.go
git commit -m "$(cat <<'EOF'
feat: implement reconciliation pass for issues, projects, initiatives

Replaces the runReconcile stub with the real per-entity sweep. For each
of issues (per team, paginated), projects, and initiatives, fetch the
authoritative ID set from Linear via the ID-only queries, diff against
SQLite, and delete any local row whose ID is absent.

Respects LowBudget() to defer when rate limit is tight, leaving headroom
for user writes. Per-team failures (network/auth) skip that team only,
never delete-on-partial-fetch.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

Spec coverage:

| Spec section | Covered by |
|---|---|
| Layer 1: `deleteOrphanProject` | Task 1 |
| Layer 1: `deleteOrphanInitiative` | Task 2 |
| Layer 1: refresh-closure wiring (4 sites) | Task 3 |
| Layer 2: `maybeScheduleReconcile` + state + 6h cooldown | Task 4 |
| Layer 2: cold-start handling | Task 4 (covered by zero-`lastReconcileAt` test) |
| Layer 2: bursts collapsed via `reconcilePending` | Task 4 (`CompareAndSwap` guard, test covers gate) |
| Layer 3: 3 new ID-only API queries | Task 5 |
| Layer 3: `LowBudget` helper | Task 5 |
| Layer 3: `runReconcile` + per-entity sweeps | Task 6 |
| Layer 3: whole-scope-or-nothing diff | Task 6 (`continue` on per-team error) |
| Observability: `[reconcile] adaptive trigger` + `pass complete` logs | Task 6 |

Type/method consistency:
- `deleteOrphanProject`, `deleteOrphanInitiative` — same signature shape as existing `deleteOrphanIssue`. ✓
- `maybeScheduleReconcile`, `runReconcile` — no-arg methods on `*SQLiteRepository`. Consistent across Tasks 4 and 6. ✓
- `LowBudget()` — defined Task 5, called from `reconcileIssues`/`reconcileProjects`/`reconcileInitiatives` in Task 6. ✓
- `setDiff(local, api []string) []string` — defined Task 6, called same task. ✓

Known follow-ups (not in scope):
- Manual cleanup of the `~/bin/linearfs` vs `~/.local/bin/linearfs` path drift in the installed systemd unit.
- The `viewer_cache` schema warning visible in startup logs (`table viewer_cache has no column named singleton`). Unrelated to this work; pre-existing migration issue.
