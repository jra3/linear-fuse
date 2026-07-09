# Project labels (issue #130) — design

**Status: ACCEPTED — ready to build.** Grilled 2026-07-08: all four live spikes run
(results in "Grilling outcomes" at the end of this doc) and all six contested
decisions resolved. Headline deltas from the proposal below: the per-team
**symlink is now IN scope** (rides `symlinkNode`, zero times — the renderFile
target reports real times through the link); **`ProjectFields` fragment
consolidation is IN scope as commit #2** of the same PR; the `retiredBy` fallback
(L0) and `prune: nil` fallback (L1) are both **dead** — spikes passed; the
retired-label check is **kept as docs-faithful policy** even though the server
turned out to accept retired assignment (L3b surprise); delete-the-line-clears,
targeted clobber guard, and duplicate-name ambiguity error all confirmed as
proposed.
**Scope (locked by user): level 2 — READ (catalog file + labels on project.md) and ASSIGN (`labels:` frontmatter → `ProjectUpdateInput.labelIds`). No catalog CRUD; must not foreclose it.**
**Base architecture: DESIGN UNIFY (panel winner 9 / 8 / 7.5 across the three lenses; the agent-UX lens preferred AGENT-UX 8.5 but its wins are content-level grafts, all absorbed below).**
**Repo facts re-verified 2026-07-07 against the working tree:** `syncCollection(ctx, spec)` with `upsert func(context.Context, T) error` / `prune func(context.Context) error` (synccollection.go:24-41); `pruneCutoff := db.Now()` already taken at the top of `syncWorkspace` before any fetch (worker.go:473); `TestInodeNamespaceDistinct` is a hand-enumerated map a new kind must join manually (ino_test.go:28-69); converters stamp `SyncedAt: Now()` internally; `DBProjectToAPIProject` is a pure blob unmarshal (convert.go:413); `commitWriteBack`/`writeBackSpec` compare-closure shape (editcommit.go:36-90); `ResolveLabelIDs` is a lowercase name→ID map with **no** ID passthrough (linearfs.go:640-664); team `labels.md` uses a top-level `labels:` frontmatter key (teams.go:257-287); `retiredAt` on `ProjectLabel` is doc-tagged `[Internal]` in the schema (schema line ~32624) while `retiredBy` is public; `opBaseTier` defaults unlisted ops to `pList` (ratebudget.go:101-126); the paginate `drain`/`fetchAll` helpers exist (paginate.go:78,142); `MutationClient.UpdateProject` already exists (mutationclient.go:42).

## Summary

`ProjectLabel` is a **workspace-scoped** entity (organization edge only — no team edge anywhere in the schema, and `type Team` has no `projectLabels` field), with a lifecycle issue labels don't have: **groups** (`isGroup` labels are containers; only one child per group may be applied) and **retirement** (`retiredAt` labels stay on existing projects but can't be newly applied). `Project.labelIds: [String!]!` is a cheap scalar array and `ProjectUpdateInput.labelIds` is an atomic full-set write with no `removedLabelIds` analog.

The slice: a new `project_labels` SQLite table (twin of `labels`, **not** a kind-column extension — see decision record), synced once per cycle in `syncWorkspace` via a `syncCollectionSpec`, rendered as a root-level read-only `project-labels.md` renderFile, and a `labels:` names list in project.md frontmatter that resolves locally against the catalog and rides the **existing single `UpdateProject` call** (scalar-edit territory, explicitly not reconcileLinks). One new pure file, `internal/fs/projectlabels.go`, holds the catalog renderer, the resolver, and the selection-policy validation. No new interfaces, no new node types, no migration machinery (additive `CREATE IF NOT EXISTS`; `labelIds` rides the project `data` blob).

Load-bearing invariant (the panel's consensus depth win): **render unknown label IDs verbatim, and the resolver accepts exact-ID passthrough** — so a cold or stale catalog can never cause an untouched save to strip a label, and agents may assign by ID.

## Decision record

| # | Decision | Choice | Why |
|---|---|---|---|
| 1 | **Unify with issue labels?** | **Rejected at table/type/render layers; shared only through the already-generic seams** (renderFile, syncCollection, hydrate-then-overlay, FieldError/writeFeedback, `StringSliceFromYAML`, paginate, ino namespace). | A `kind` column on `labels` breaks on repo-verified facts: `GetLabelByName`'s `(team_id = ? OR team_id IS NULL)` union (queries.sql:250-251) would resolve project-label names as issue labels and hand ProjectLabel IDs to `issueUpdate.labelIds`; `ListTeamLabels` would leak project labels into every team's `labels.md` and `labels/` CRUD dir where `rm` fires `issueLabelDelete` at a ProjectLabel ID. Lifecycles differ (retirement must be *retained* by sync), prune regimes differ (team-scoped vs workspace-wide), and GraphQL fragments cannot span `IssueLabel`/`ProjectLabel` so the only expensive share is forbidden by the wire anyway. Recorded in CONTEXT.md so no future round re-derives the merge. |
| 2 | **Catalog placement** | **Mount-root `project-labels.md`, single read-only file. No per-team file, no per-team symlink (revisit trigger recorded).** | Schema decides it: no team edge → workspace surface, following the `initiatives/`-at-root precedent (issue `labels.md` is per-team *because* `IssueLabel` has a team edge). A per-team copy misrepresents scope; a symlink is surface-without-information. Discovery = README reference-files line + `<project_frontmatter>` annotation + every `.error` message pointing at the file. If live agent transcripts show root-file misses, a `teams/{KEY}/project-labels.md → ../../project-labels.md` symlink is a ~10-line additive follow-up. |
| 3 | **File vs directory** | File. | One `Read` gets the whole catalog (the `states.md`/`labels.md` idiom agents already know). The team-level `labels.md`-beside-`labels/` coexistence is the exact precedent for level 3 adding a root `project-labels/` CRUD dir *beside* the file — nothing to undo. |
| 4 | **Table shape** | New `project_labels` table; columns `is_group`, `parent_id`, `retired_at` earn their keep (read by the validation policy and renderer from day one). **No** `GetProjectLabel :one`, **no** `DeleteProjectLabel`, **no** parent index in this slice (deletion test — nothing reads a single row or filters by parent; validation/render load the whole catalog). | Grafted from MINIMALIST per judge 1; UNIFY's original DDL shipped speculative provisioning it lectured others about. Level 3 adds those queries when a reader exists. |
| 5 | **Fetch shape** | `Project.labelIds` scalar (one token added to the three project queries), **not** the `labels {nodes{...}}` connection. Names resolve locally against the catalog. Catalog fetched via its own paginated workspace drain, `parent { id }` **only** — no parent name on the wire. | The full catalog is always in hand (SQLite), so parent/group names stitch locally; keeps the complexity-capped 50-page project query flat. `Parent.Name` is hydrated in **one in-memory pass at the repo read** (AGENT-UX graft): the converter keeps `Parent` strictly as `&api.ProjectLabel{ID: parent_id}` from the column, the repo stitches names over the id→row map. |
| 6 | **Groups/retired rendering** | Retired and group labels are **listed** in the catalog (retirement is lifecycle, not deletion — names on existing projects must keep resolving), flagged with omit-when-false keys in frontmatter and a spelled-out Flags column in the table. The three assignment rules live as prose **in the catalog file itself** (it's the file an agent reads after an `.error`). Empty catalog renders header + rules + "No project labels defined." — **never ENOENT** (stable surface the README can promise; AGENT-UX graft). Catalog frontmatter top key is **`labels:`**, matching the existing `labels.md` idiom (judge-3 fix of UNIFY's off-idiom `projectLabels:`). |
| 7 | **Groups/retired validation** | Client-side pre-validation for `.error` quality, **before any mutation**; Linear stays authoritative (server errors still land via `classifyMutationErr`). Rules: unknown name; `isGroup` cannot be applied (error **names the assignable children** — AGENT-UX graft); retired cannot be **newly** applied (already-applied retired labels carry through — required, since `labelIds` is a full-set write); at most one child per group among the *selected* set. | Locality: the API's rejection text won't say "pick one of: Platform, Mobile". Live item L3 confirms our policy matches the server's. |
| 8 | **Write mechanics** | Atomic `ProjectUpdateInput.LabelIds *[]string` on the **existing single `UpdateProject` call** — the scalarEdit fork, explicitly NOT reconcileLinks (labels are one set-semantics input array; reconcileLinks exists for per-pair link mutations). Pointer-or-omit: untouched flush sends nil; clear sends `&[]string{}`. Diff as **ID sets**, order-insensitive. Zero `MutationClient` change. | All three designs and all three judges converged here. |
| 9 | **Clear semantics** | Delete-the-line clears (key absent + current non-empty → empty set), matching `initiatives:` and issue `labels:`. Gated on live item **L2** (`labelIds: []` accepted — strongly implied by the absence of `removedLabelIds`, unverified). If L2 fails, clear becomes EINVAL-with-explanation, not a workaround. | Uniform mount contract; contested item for grilling (#1). |
| 10 | **Prune policy** | Full-table prune licensed by the complete workspace drain, cutoff = the `pruneCutoff` **already taken at the top of `syncWorkspace`** (worker.go:473 — earlier-than-fetch is strictly conservative for `synced_at < cutoff`; MINIMALIST graft). Gated on live item **L1**: a retired label must appear in the default drain. Fallback if L1 fails: `prune: nil` (states precedent) — one-line policy swap, pre-agreed. | Schema evidence favors safety (retired ≠ archived; `ProjectLabelFilter` has no retired comparator, i.e. the API doesn't segregate retired labels), but the failure mode — sync eats exactly the rows retirement requires us to keep — is bad enough to verify first. |
| 11 | **`retiredAt` `[Internal]` hazard** | Live item **L0, run first**: confirm `retiredAt` is selectable by API-key clients. Fallback (MINIMALIST's V1b, grafted by all three judges): fragment selects `retiredBy { id }` instead and retired-ness derives from its presence; the `retired_at` column then stores the sync-observation time and the catalog renders `retired: true` without a date. | This is the one failure mode that bricks the entire catalog query every cycle; only one design planned for it. Decided now (not "decide at that point" — the risk judge's flaw in MINIMALIST) so the column shape is settled either way. |
| 12 | **Duplicate catalog names** | Deterministic resolver preference instead of map-build-order nondeterminism: on a bare-name tie, prefer **(a) a label already in the project's current `labelIds`, then (b) active over retired, then (c) error listing the candidate IDs**. **No** `Parent/Child` qualified-input grammar (speculative new syntax with no demonstrated need; a label literally named "A/B" would collide with it). | Synthesized from the risk judge's duplicate-name gap + AGENT-UX's prefer-current rule, minus its grammar. (a) guarantees untouched files round-trip; (c) keeps ambiguity loud rather than silently assigning the wrong sibling — the ID-passthrough path is the documented disambiguation. |
| 13 | **Stale-blob clobber guard** | Before computing the label diff, if the parsed frontmatter changes labels **and** `p.project.LabelIds` is empty, do one `GetProject` fetch to refresh current state first. | Closes the corner all three designs missed (risk-judge graft): a project blob predating the `LabelIds` field reads current as empty; an agent *adding* one label would full-set-write and silently wipe the project's real labels in Linear — undetectable by the WriteBack divergence check (fresh == sent). One cheap fetch, only in the at-risk window (≤1 sync cycle after upgrade). |
| 14 | **`ProjectFields` fragment consolidation** | **Deferred.** `labelIds` is added by hand to the three inlined project field lists (`queryTeamProjects`, `queryProject`, `mutationCreateProject` echo). | Pre-existing drift site; folding it in smears the slice's diff (both winning judges agreed). Flagged as its own follow-up commit. Grilling item #5. |
| 15 | **Where the new code lives** | One new pure file `internal/fs/projectlabels.go`: `projectLabelsMarkdown`, `resolveProjectLabels`, `validateProjectLabelSelection`, `sameIDSet`. All unit-testable with a literal catalog slice — no mount, no interface. | Change concentration; the panel penalized MINIMALIST's waffling on this. |

## Architecture

### 1. Schema DDL (`internal/db/schema.sql`)

```sql
-- Project labels (Linear "ProjectLabel"). WORKSPACE-scoped: the schema has no
-- team edge, so unlike `labels` there is no team_id column at all. Deliberately
-- a twin of `labels`, not a kind-column extension of it: scoping, prune regime,
-- and lifecycle (retirement) all differ -- see CONTEXT.md "Project-label selection".
-- Retired labels are KEPT (retirement is keep-but-not-newly-assignable, not
-- deletion) so name resolution on existing projects keeps working.
CREATE TABLE IF NOT EXISTS project_labels (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    color TEXT,
    description TEXT,
    is_group INTEGER NOT NULL DEFAULT 0,   -- groups cannot be applied directly
    parent_id TEXT,                        -- group parent; NULL = top-level
    retired_at DATETIME,                   -- NULL = active (see L0 fallback note)
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_project_labels_name ON project_labels(name);
```

No parent index, per decision #4. Migration for existing DBs: none needed — schema.sql is applied as `CREATE IF NOT EXISTS` at `Open` (store.go), so the table appears on next service start; existing project rows self-heal (see §4).

`internal/db/queries.sql` (then `make sqlc`; no apostrophes in SQL comments — sqlc v1.30):

```sql
-- name: ListProjectLabels :many
SELECT * FROM project_labels ORDER BY name COLLATE NOCASE;

-- name: UpsertProjectLabel :exec
INSERT INTO project_labels (id, name, color, description, is_group, parent_id,
    retired_at, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, color=excluded.color,
    description=excluded.description, is_group=excluded.is_group,
    parent_id=excluded.parent_id, retired_at=excluded.retired_at,
    created_at=excluded.created_at, updated_at=excluded.updated_at,
    synced_at=excluded.synced_at, data=excluded.data;

-- Workspace-wide prune, licensed ONLY by a complete drain of Query.projectLabels
-- that includes retired labels (live-verified, item L1; fallback = nil prune).
-- name: PruneProjectLabels :exec
DELETE FROM project_labels WHERE synced_at < ?;
```

### 2. Conversion (`internal/db/convert.go`)

- `APIProjectLabelToDBProjectLabel(l api.ProjectLabel) (UpsertProjectLabelParams, error)` — marshal the whole struct into `data`; extract columns; `parent_id` from `l.Parent.ID` when non-nil; **`SyncedAt: Now()` stamped internally** per the existing converter convention (no caller-passed timestamp — MINIMALIST's `(l, now)` deviation rejected; internal `Now()` at upsert time is strictly after the pre-fetch prune cutoff, so row survival never depends on timestamp equality).
- `DBProjectLabelToAPIProjectLabel(l ProjectLabel) api.ProjectLabel` — **hydrate-then-overlay** per the reverse-conversion contract at `DBMilestoneToAPIProjectMilestone`: best-effort blob unmarshal (corrupt blob → zero struct; one bad row can't poison the catalog), then overlay every column; `Parent` **strictly from the `parent_id` column** as `&api.ProjectLabel{ID: ...}` (the `DBLabelToAPILabel` never-trust-the-blob's-relationship rule); timestamps only via `db.ParseSQLiteTime`.
- `DBProjectLabelsToAPIProjectLabels` slice mapper.
- **`api.Project.LabelIds` needs zero converter work**: `DBProjectToAPIProject` is a pure blob unmarshal. Known, self-healing consequence: blobs predating the field read as empty `LabelIds` for ≤1 sync cycle — the write-path guard (decision #13) covers the one dangerous window.

### 3. API layer (`internal/api`)

**Types** (`types.go`):

```go
// ProjectLabel is a WORKSPACE-scoped label applied to projects. Deliberately
// not unified with Label (IssueLabel): no team edge, group/retirement
// lifecycle, disjoint mutations. See CONTEXT.md "Project-label selection".
// IsGroup labels are containers and cannot be applied directly; only one
// child per group may be applied. RetiredAt != nil (or RetiredBy, per the L0
// fallback) means not newly assignable but valid on existing projects.
type ProjectLabel struct {
    ID          string        `json:"id"`
    Name        string        `json:"name"`
    Color       string        `json:"color"`
    Description string        `json:"description"`
    IsGroup     bool          `json:"isGroup"`
    Parent      *ProjectLabel `json:"parent,omitempty"` // id from wire; Name stitched by the repo read
    RetiredAt   *string       `json:"retiredAt,omitempty"`
    CreatedAt   string        `json:"createdAt"`
    UpdatedAt   string        `json:"updatedAt"`
}
```

- `Project` gains `LabelIds []string \`json:"labelIds"\``.
- `ProjectUpdateInput` gains `LabelIds *[]string \`json:"labelIds,omitempty"\`` (pointer-or-omit; `&[]string{}` clears).

**Fragment + query** (`queries.go`) — fragment exists from day one so level-3 mutations project through it (CLAUDE.md rule):

```graphql
fragment ProjectLabelFields on ProjectLabel {
  id name color description isGroup retiredAt createdAt updatedAt
  parent { id }
}

query ProjectLabelsPage($cursor: String) {
  projectLabels(first: 250, after: $cursor) {
    nodes { ...ProjectLabelFields }
    pageInfo { hasNextPage endCursor }
  }
}
```

(`retiredAt` swaps to `retiredBy { id }` if L0 fails. `lastAppliedAt`/`creator` deliberately unfetched — no reader; blob-first storage makes adding them a fragment edit later.)

- `labelIds` (one scalar token) added by hand to `queryTeamProjects`, `queryProject` (the WriteBack verify fetch — required for the divergence check), and `mutationCreateProject`'s echo.
- `client.GetProjectLabels(ctx) ([]ProjectLabel, error)` — full drain via the existing `paginate` module (`drainFrom`/`fetchAll` pattern), no `filter:` (the drain must include retired and group labels; completeness licenses the prune).
- **Rate budget** (`ratebudget.go`): `"ProjectLabelsPage": pSkeleton` in `opBaseTier` — FS-shape metadata beside `WorkspaceLabelsPage`; without the entry it defaults to `pList` and misprices at `defaultPredictedCost` until first measurement.
- **MutationClient: unchanged.** `UpdateProject(ctx, id, ProjectUpdateInput)` already exists; the input struct grew a field. `mockmutation.UpdateProject`'s `projEdit` overlay extends to record/apply `LabelIds` (test plumbing).

### 4. Sync (`internal/sync/worker.go`)

One new spec at the end of `syncWorkspace` — once per cycle, workspace pass, before the per-team loop; isolated log-and-continue so a catalog failure never blocks users/initiatives (and vice versa). Reuses the existing `pruneCutoff` from the top of the function:

```go
// Project-label catalog (workspace-scoped; see CONTEXT.md). Reuses pruneCutoff:
// taken before ANY fetch this pass, so it is strictly conservative for the
// synced_at < cutoff prune (converter stamps SyncedAt at upsert time, after it).
plabels, err := w.client.GetProjectLabels(ctx)
if err != nil {
    log.Printf("[sync] project labels fetch failed: %v", err) // no prune this pass
} else {
    syncCollection(ctx, syncCollectionSpec[api.ProjectLabel]{
        label: "project-label",
        items: plabels, // complete drain (retired + groups included) licenses the prune
        upsert: func(ctx context.Context, l api.ProjectLabel) error {
            params, err := db.APIProjectLabelToDBProjectLabel(l)
            if err != nil { return err }
            return w.store.Queries().UpsertProjectLabel(ctx, params)
        },
        prune: func(ctx context.Context) error {
            return w.store.Queries().PruneProjectLabels(ctx, pruneCutoff)
        },
    })
}
```

Prune-contract statement for the spec: *clean = every catalog row upserted; the drain is the completeness set; retired labels are members of the drained set every cycle (L1), so retirement never reads as removal — only deletion/archival does.* Fallback if L1 fails: `prune: nil` (states precedent, worker.go:568-580); truly-deleted labels then linger as a documented limitation. `Project.labelIds` freshness needs no sync work — it rides the existing per-team project pass into the blob.

### 5. Repo + LinearFS read/resolve

- `repo/sqlite.go`: `GetProjectLabels(ctx) ([]api.ProjectLabel, error)` = `ListProjectLabels` → converters → **one in-memory pass stitching `Parent.Name`** from the id→row map (concrete method; no interface — round 14 deleted them).
- `fs/linearfs.go`: `GetProjectLabels(ctx)` passthrough beside `GetInitiatives`; `projectLabelNames(ctx, ids []string) []string` — id→name over one catalog load, **unknown IDs render verbatim as the raw ID, never dropped**.
- `fs/projectlabels.go` (new, pure):
  - `resolveProjectLabels(catalog []api.ProjectLabel, names []string, currentIDs map[string]bool) (ids []string, ferr *FieldError)` — per name: case-insensitive name match with the decision-#12 tie-break (prefer current, then active-over-retired, else ambiguity error listing candidate IDs); **exact-ID passthrough** (matches a catalog ID or a current `labelIds` member — the round-trip invariant); else unknown → FieldError with "See project-labels.md for valid project labels."
  - `validateProjectLabelSelection(selected []api.ProjectLabel, currentIDs map[string]bool, catalog byID) *FieldError` — the three policy rules, with the AGENT-UX error content: group error enumerates children (`"Area" is a label group and cannot be applied directly. Apply one of its children instead: Platform, Mobile. See project-labels.md.`); retired-newly-added error notes existing assignments are unaffected; one-child-per-group names the group and both offenders.
  - `sameIDSet(a, b []string) bool`.
  - `projectLabelsMarkdown(labels []api.ProjectLabel) []byte`.

### 6. FS read surfaces

**Catalog** — `RootNode.Readdir` gains `{Name: "project-labels.md", Mode: S_IFREG}`; `RootNode.Lookup` gains a case:

```go
case "project-labels.md":
    lfs := r.lfs
    return r.lookupRenderFile(ctx, out, "project-labels.md",
        func(ctx context.Context) ([]byte, time.Time, time.Time) {
            labels, _ := lfs.GetProjectLabels(ctx) // SQLite-only; err → empty render
            return projectLabelsMarkdown(labels), minCreated(labels), maxUpdated(labels)
        }, projectLabelsCatalogIno(), inheritTimeout), 0
```

- Times: mtime = max `UpdatedAt`, ctime = min `CreatedAt`; zero when empty (renderFile's never-fabricate-now() contract). Folding RootNode onto `dirManifest` is noted, not done.
- `ino.go`: `projectLabelsCatalogIno() uint64 { return ino("project-labels-catalog", "workspace") }` in the Labels section, **manually added to `TestInodeNamespaceDistinct`'s map** (it is a hand-enumerated checklist, not automatic — both non-winning designs got this wrong).

Render shape (top key `labels:` matching the labels.md idiom; omit-when-false flags; rules prose in-file; stable when empty):

```markdown
---
labels:
  - id: aaaa-…
    name: Area
    group: true
  - id: bbbb-…
    name: Platform
    color: "#5E6AD2"
    parent: Area
  - id: dddd-…
    name: Legacy-2024
    retired: true
---
# Project Labels (workspace-wide)

Assign in any project.md frontmatter: `labels: [Platform, Q3-Bet]`
(Names are resolved case-insensitively; a raw label ID is also accepted.)

Rules:
- Labels marked `group: true` are containers and CANNOT be assigned; assign one
  of their children instead.
- At most ONE child from each group may be on a project at a time.
- Labels marked `retired: true` cannot be newly assigned; existing assignments remain.

| Name | Group | Color | Flags | ID |
|------|-------|-------|-------|-----|
| Area | — | — | group (assign a child) | aaaa-… |
| Platform | Area | #5E6AD2 | | bbbb-… |
| Legacy-2024 | — | #95A2B3 | retired | dddd-… |
```

Empty catalog: header + rules + "No project labels defined." — never ENOENT.

**project.md** — `marshal.ProjectToMarkdown(project *api.Project, labelNames []string)` emits `labels:` (flow list of names) beside `initiatives:`, only when non-empty (delete-the-line contract stays uniform). The one non-test caller (projects.go's `generateContent`) computes `labelNames` via `lfs.projectLabelNames(ctx, project.LabelIds)`. `project.meta` and `ProjectMetaToMarkdown` untouched (labels are editable-surface data).

### 7. Write path (`ProjectInfoNode.Flush`, projects.go)

Reordered so **all pure resolution/validation runs before any mutation** (labels introduce the first post-reconcile validation; hoisting closes the partial-application window — the flaw that sank AGENT-UX's flush ordering):

1. **Parse** (unchanged).
2. **Labels: parse + guard + resolve + validate (pure, no mutation).**
   - `raw, present := doc.Frontmatter["labels"]`; `desiredNames := marshal.StringSliceFromYAML(raw)`.
   - **Stale-blob guard (decision #13):** if the write would change labels (`present` with non-empty names, or absent-key-clear per next bullet) and `len(p.project.LabelIds) == 0`, refresh once via `p.lfs.verify().GetProject` and adopt `LabelIds` before diffing.
   - Key absent + current non-empty → desired = empty set (clear). Key absent + current empty → labels untouched (no-op; the stale-blob no-op corner gets an explicit test).
   - `resolveProjectLabels` then `validateProjectLabelSelection` → any `*FieldError` → `SetWriteError(p.project.ID, ...)` + `EINVAL`. No API call has happened.
   - `labelsChanged := !sameIDSet(desiredIDs, p.project.LabelIds)` — **ID sets, order-insensitive** (no name-case flapping; raw-ID round-trip diffs empty → no mutation).
3. **initiatives: reconcileLinks** (unchanged — per-pair link mutations keep their shape).
4. **Single UpdateProject:** `input := api.ProjectUpdateInput{Name: edit.name, Description: edit.desc}`; `if labelsChanged { input.LabelIds = &desiredIDs }` (`&[]string{}` for clear); call when `edit.changed() || labelsChanged`; failure → `classifyMutationErr("update project", err)` → `.error` + errno (server remains the policy backstop).
5. **WriteBack tail:** `compare` closure extended — `edit.divergences(fresh.Name, fresh.Description)` plus, **only when `labelsChanged`** (the guard MINIMALIST omitted, which would have EIO'd every plain rename), a fatal divergence when `!sameIDSet(fresh.LabelIds, desiredIDs)`. `queryProject` now selects `labelIds`; `persist` = existing `UpsertProject` (blob carries them); `p.project = *fresh` adopts server truth.
6. **Invalidation:** unchanged — existing `InvalidateUpdated(projectInfoIno)` + `InvalidateUpdated(metaIno)` pair. The catalog file is a DIRECT_IO renderFile re-rendered per read: no kernel invalidation surface exists or is needed (proven by test, not asserted — §Test plan). Error keying: existing per-project `.error` via `project.ID`; no new feedback surface.

Project **create** path: out of scope (mkdir creates by name only today); `ProjectCreateInput.labelIds` exists, so a future full-frontmatter project `_create` can take `labels:` with the same resolver — nothing forecloses it.

### 8. Marshal

- `internal/marshal/project.go`: `ProjectToMarkdown` signature grows `labelNames []string`; emits `labels:` when non-empty. Parse side stays in fs (the existing project pattern — marshal renders, Flush reads frontmatter via `StringSliceFromYAML`).
- `project_test.go`: key-set pins → `["initiatives","labels","name"]` / `["labels","name"]` / `["name"]`; a doc-case pinning "no labels → no key" (the delete-line contract). `TestProjectMetaToMarkdown` untouched.

### 9. Generated README + guard (`generateReadme`, root.go; same change per CLAUDE.md)

- `<directory_structure>`: root gains `project-labels.md  [read-only: workspace project-label catalog (groups, retired)]` near `initiatives/`.
- **New `<project_frontmatter>` block** (project.md now has three editable fields + body; it has outgrown implication-by-`<operations>`):
  ```
  ---
  name: "API Gateway"                       [editable]
  initiatives: ["Platform Modernization"]   [names; see initiatives/]
  labels: [Backend, Q3-Bet]                 [must match project-labels.md; groups
                                             cannot be applied; max one child per
                                             group; retired = existing-only; raw
                                             label IDs also accepted]
  ---
  Project description (editable — the body maps to the description)
  ```
- `<validation_errors>`: `Validated project fields: initiatives, labels` (**not** `name` — it is sent unvalidated; the docs-lens judge caught MINIMALIST's overclaim) and the Reference-files line gains `project-labels.md (valid project labels)`.
- `<claude_code_instructions>`: "Check `<mount>/project-labels.md` before editing project `labels:`".
- `<important_notes>`: one line on group/retired semantics.
- `TestGeneratedReadmeMatchesBehavior`: want-loop gains `"project-labels.md"` and a rule phrase (`"one child"`); behavior spot-checks: mounted `/project-labels.md` readable, mode 0444, contains `group`.

### 10. CONTEXT.md

New `###` entry **"Project-label selection"** after "Name→ID resolution": the workspace catalog (root renderFile + `project_labels` table + workspace-pass syncCollection with drain-licensed prune and the L1-gated nil-prune fallback); assign-by-set via `ProjectUpdateInput.labelIds` in the single project update; the selection policy as a pure function; the render-unknown-as-ID + resolve-ID-passthrough round-trip invariant; and — explicitly — **the rejected unification with issue labels and its four reasons** (scope axis, prune regime, lifecycle, fragment non-shareability) so no future round re-derives the merge. Plus: "Link reconciliation" entry gains "project labels deliberately do NOT use this (atomic array field)"; "Name→ID resolution" mentions `resolveProjectLabels` as the second multi-name resolver (pure-function-with-catalog-slice, sibling of `ResolveLabelIDs`); "Sync reconcile tail" example list gains `project-label`.

## Files touched

| File | Change |
|---|---|
| `internal/db/schema.sql` | `project_labels` table + name index (no parent index) |
| `internal/db/queries.sql` (+ sqlc regen) | `ListProjectLabels` / `UpsertProjectLabel` / `PruneProjectLabels` only |
| `internal/db/convert.go` | `APIProjectLabelToDBProjectLabel` (internal `SyncedAt: Now()`), `DBProjectLabelToAPIProjectLabel` (hydrate-then-overlay, parent-from-column), slice mapper |
| `internal/db/convert_test.go` | `TestProjectLabelRoundTrip`; `TestOverlayColumnsWin` project-label subtest |
| `internal/api/types.go` | `ProjectLabel`; `Project.LabelIds`; `ProjectUpdateInput.LabelIds *[]string` |
| `internal/api/queries.go` | `ProjectLabelFields` fragment; `ProjectLabelsPage` query; `labelIds` scalar in `queryTeamProjects`, `queryProject`, `mutationCreateProject` echo |
| `internal/api/client.go` | `GetProjectLabels` paginated drain |
| `internal/api/ratebudget.go` | `"ProjectLabelsPage": pSkeleton` in `opBaseTier` |
| `internal/sync/worker.go` | project-label `syncCollection` spec in `syncWorkspace`, reusing `pruneCutoff` |
| `internal/repo/sqlite.go` | `GetProjectLabels` (+ Parent.Name in-memory stitch) |
| `internal/fs/linearfs.go` | `GetProjectLabels` passthrough; `projectLabelNames` (unknown → verbatim ID) |
| `internal/fs/projectlabels.go` (new) | `projectLabelsMarkdown`, `resolveProjectLabels`, `validateProjectLabelSelection`, `sameIDSet` — all pure |
| `internal/fs/projectlabels_test.go` (new) | unit tables for all of the above |
| `internal/fs/root.go` | Readdir entry; Lookup case (`lookupRenderFile`); `generateReadme` sections (§9) |
| `internal/fs/ino.go` + `ino_test.go` | `projectLabelsCatalogIno`; manual addition to `TestInodeNamespaceDistinct` |
| `internal/fs/projects.go` | Flush: labels guard/resolve/validate hoisted pre-mutation, `LabelIds` on the single UpdateProject, guarded label divergence in the compare closure; `generateContent` passes resolved names |
| `internal/marshal/project.go` + `project_test.go` | `labels:` render param; key-set pins |
| `internal/testutil/fixtures/api.go` | `FixtureAPIProjectLabel` (+ `WithGroup`/`WithParent`/`WithRetired`); `FixtureAPIProject` gains `WithLabelIDs` |
| `internal/testutil/fixtures/fstest.go` | `PopulateProjectLabels`; seed mini-catalog in `populateTestFixtures` |
| `internal/testutil/mockmutation/mock.go` | `UpdateProject` `projEdit` overlay records/applies `LabelIds` |
| `internal/integration/projectlabels_test.go` (new) | catalog + assign + validation + round-trip surfaces |
| `internal/integration/readme_test.go` | guard extensions (§9) |
| `CONTEXT.md` | new entry + three cross-reference touch-ups (§10) |

## Test plan

**Live spikes FIRST (they gate design decisions; targeted queries only — no temp-mount full sync, per the 3M/hr budget lesson):**
- **L0** — `retiredAt` selectable by an API-key client despite the `[Internal]` doc tag. Fail → fragment uses `retiredBy { id }`, retired-ness = presence, `retired_at` column stores sync-observation time.
- **L1** — a retired label appears in the default `projectLabels` drain. Fail → `prune: nil` (one-line swap).
- **L2** — `projectUpdate` with `labelIds: []` clears. Fail → delete-the-line becomes EINVAL-with-explanation (no fake removal path).
- **L3** — server rejects a group-label ID on `projectUpdate` (confirms our pre-validation matches server policy; capture the server's message text for comparison).

**Unit (no mount):**
- convert: round-trip; overlay subtest (every column overlaid + columns-win; blob-only field survives; corrupt blob → columns fallback; parent strictly from column).
- `projectlabels.go` tables: resolver — case-insensitive hit / unknown / **ID passthrough (catalog ID and current-member ID)** / duplicate-name prefers-current / duplicate-name prefers-active-over-retired / duplicate-name-no-tiebreak → ambiguity error listing IDs / empty catalog; validation — clean / group (error names children) / retired-newly-added / retired-carried-through-OK / two-children-one-group / two-children-different-groups; `sameIDSet` order-insensitivity; renderer — flags, sorted, rules prose, **empty-catalog stable render**.
- marshal: key-set pins; labels absent when empty.
- flush-diff logic: set-equal → no-op; key-absent + current-empty → no-op (**the stale-blob no-op corner, explicit test**); key-absent + current-non-empty → `&[]string{}`; untouched write → `LabelIds == nil`; **stale-blob clobber guard fires the refresh fetch when current is empty and labels change**.
- divergence: guarded on `labelsChanged` (a name-only edit with untouched labels must produce zero label divergence); order difference ≠ divergence; real set mismatch → fatal.

**Fixture integration (default SQLite mode, mockmutation):**
- Seed: group "Platform" with children "Backend"/"Frontend", standalone "Bug", retired "Legacy"; project-1 pre-labeled `[Backend, Legacy]`.
- Catalog: readable at mount root, 0444, names + group/retired markers + rules text; **mid-test `testStore.Queries().UpsertProjectLabel` visible on the next read** (DIRECT_IO proof — turns the no-invalidation claim into a tested invariant).
- project.md: renders `labels: [Backend, Legacy]` (names); edit to `[Backend, Bug]` → mock receives the resolved ID set, re-read reflects it; delete the line → `LabelIds = &[]`; unknown/group/retired-new/two-per-group each → EINVAL + expected `.error` text (group error contains the children's names); retired "Legacy" carried through → success.
- Stale-ID round trip: project carrying a labelId absent from the catalog renders the verbatim ID; unchanged re-save → **no** `UpdateProject` call.
- README guard extensions per §9.

**Live end-to-end (gated `LINEARFS_LIVE_API=1 LINEARFS_WRITE_TESTS=1`, TST team):** assign a real label through the mounted file, verify read-back + `GetProject.labelIds`; exercise the empty-array clear; confirm a group-assign server rejection lands legibly in `.error` via `classifyMutationErr`.

## Level-3 forward compatibility

`projectLabelCreate/Update/Delete/Retire` all exist in the schema. Later CRUD is a root `project-labels/` directory **beside** the catalog file (the team-level `labels.md`+`labels/` coexistence precedent), built from `namedListing` + collectionTrio (`_create`/`.error`/`.last`) + the `commitCreate`/`commitWriteBack`/`commitDelete` tails, with four new `MutationClient` methods projecting through the already-defined `ProjectLabelFields` fragment (the CLAUDE.md rule is why the fragment ships now, mutation-less). The table already carries every lifecycle column including `retired_at` for a retire-instead-of-delete surface; `GetProjectLabel :one` / `DeleteProjectLabel` queries are added **then**, when readers exist. `.error`/`.last` key via `collectionErrorKey("project-labels", "workspace")`. Nothing in this slice assumes the catalog file is the only surface, and nothing needs rework.

## Effort estimate

**M — two focused sessions** (~600–800 LOC including tests), plus a short up-front live-spike pass.
- Spike pass (½ hour): L0/L1/L2/L3 as raw GraphQL against the live API — they settle the fragment shape, prune policy, and clear semantics before any code.
- Session 1: DB + sqlc + converters + API types/query/client/budget + sync spec + repo/linearfs reads + catalog render + ino (READ complete, fixture-testable).
- Session 2: write path (guard/resolve/validate/UpdateProject/WriteBack) + mock + marshal + integration tests + README/guard + CONTEXT.md.
No migration risk (additive table, blob-riding field). Reinstall of the live service after merge is warranted (new surface + sync pass) and needs explicit user authorization per house rule.

## Decisions to grill

1. **Delete-the-line clears labels** (key absent + current non-empty → full clear). *Recommendation: keep* — it matches the mount-wide contract (issue labels, initiatives), the stale-blob no-op corner is tested, and the clobber guard covers the dangerous window. The alternative (absent = no-op, clear only via explicit `labels: []`) is safer against accidental truncation but forks the mount's established semantics for one field.
2. **One-child-per-group client-side check.** The cheapest rule with the weakest justification: the server enforces it, and our check could false-reject if Linear ever relaxes the rule. *Recommendation: keep* — it is ~10 lines in an already-pure function, L3 verifies parity, and the raw GraphQL error it replaces is genuinely unhelpful; but this is the first check to delete if grilling says "server is the authority, period."
3. **Stale-blob clobber guard shape** — targeted `GetProject` refresh only when current `LabelIds` is empty and labels change, vs always refreshing before any label diff, vs no guard (accept the ≤2-min window as documented). *Recommendation: targeted guard* — one cheap fetch in exactly the at-risk state; always-refreshing adds a fetch to every labeled save forever to cover a window that only exists for one sync cycle after upgrade.
4. **No per-team symlink** (`teams/{KEY}/project-labels.md → ../../project-labels.md`). *Recommendation: ship without* — README + `<project_frontmatter>` + every `.error` message point at the root file; the symlink is a cheap additive follow-up with a concrete revisit trigger (live agent transcripts showing root-file misses). But this is the one placement facet the user explicitly never confirmed.
5. **`ProjectFields` fragment consolidation** — this slice touches all three inlined project field lists to add `labelIds`, widening the known drift site by one field each. *Recommendation: defer to its own follow-up commit* (both winning judges agreed the slice shouldn't smear into it), but if the grilling wants the CLAUDE.md fragment rule executed while the files are open, it is severable and low-risk as commit #2 of the same PR.
6. **Duplicate-name ambiguity error vs silent pick.** Decision #12 errors (listing candidate IDs) when the prefer-current/prefer-active tie-breaks don't resolve a bare name. *Recommendation: keep the error* — silently picking a sibling is exactly the wrong-label hazard the docs lens flagged; the ID-passthrough path is the documented escape hatch. The alternative (always pick deterministically, never error) is friendlier but can assign a label the agent didn't mean, invisibly.

## Grilling outcomes & live-spike results (2026-07-08)

All spikes ran as raw GraphQL against the live API (script preserved the
workspace: spike labels deleted, TST project's original label restored after a
quoting bug in the script's restore step was fixed by hand).

**Spike results:**

- **L0 — PASS.** `retiredAt` IS selectable by an API-key client despite the
  `[Internal]` doc tag. The fragment uses `retiredAt` directly; the `retiredBy`
  fallback (decision #11) is dead. `retired_at` column stores the real date.
- **L1 — PASS.** A retired label appears in the default `projectLabels` drain
  (verified: retired `zz-spike-plain` present, `retiredAt` populated). The
  full-table prune is licensed; the `prune: nil` fallback (decision #10) is dead.
  Bonus: `projectLabelDelete` works directly on a retired label — no
  restore-then-delete dance for future level-3 CRUD.
- **L2 — PASS.** `projectUpdate` with `labelIds: []` clears cleanly.
  Delete-the-line-clears (decision #9) confirmed viable and RESOLVED: keep.
- **L3a — group rejection confirmed, and the error is structured:**
  `extensions: {type: "invalid input", code: "INPUT_ERROR", statusCode: 400,
  userError: true, userPresentableMessage: "The label 'zz-spike-group' is a
  group and cannot be assigned to projects directly."}` (raw message:
  "labelIds contain parent labels"). BUILD NOTE: verify `classifyMutationErr`
  maps `INPUT_ERROR`/`userError` to EINVAL (not EIO) and prefers
  `userPresentableMessage` over the terse internal message when surfacing to
  `.error`. Our pre-validation still adds value: the server message does not
  name the assignable children.
- **L3b — SURPRISE: the server ACCEPTS assigning a retired label.**
  `projectUpdate` with a retired label ID succeeded, contradicting the schema
  docs. Retirement enforcement is evidently UI-level only.

**Grill decisions — all resolved:**

1. **Delete-the-line clears: KEEP** (as proposed). Mount-wide contract
   consistency wins; the stale-blob guard and the tested no-op corner close the
   dangerous edges.
2. **One-child-per-group check: KEEP.** Untested live (only one child was
   created), but the retired-check decision settled the principle: either the
   server enforces it (our check = parity + better message) or it does not
   (our check = docs-faithful policy, same stance as retired).
3. **Retired-label check: KEEP as docs-faithful POLICY** — this is now
   deliberately stricter than the API (per L3b). Rationale: both the type docs
   and the `projectLabelRetire` mutation description state the constraint; API
   acceptance reads as lax enforcement, not intent; an agent newly assigning a
   retired label is almost certainly a mistake; the escape hatch is restoring
   the label in Linear. Validation policy in one sentence: **we enforce what
   Linear's docs say about label assignment, even where the API is lax.**
4. **Stale-blob clobber guard: TARGETED shape** (refresh only when the write
   changes labels AND current `LabelIds` is empty). Pinned: the refresh uses
   the interactive-promoted context (`api.WithInteractive`) — synchronous read
   inside a user-blocking flush, same rule as the attachment idempotency
   re-check (PR #187).
5. **Per-team symlink: NOW IN SCOPE** (reverses the proposal's deferral, on
   codebase evidence + user's ergonomics preference). `teams/{KEY}/
   project-labels.md → ../../project-labels.md` via the existing `symlinkNode`
   deep module — the established cross-tree idiom (initiative project symlinks,
   `cycles/current`). Construction passes ZERO times (loading the catalog at
   team-Lookup to stamp symlink times is overkill; the renderFile target
   reports real times through the link on stat). Files-touched delta: the team
   directory's listing/lookup (teams.go / its dirManifest) + one README
   directory-structure line + one guard-test line.
6. **`ProjectFields` fragment consolidation: NOW IN SCOPE as commit #2 of the
   same PR** (reverses the proposal's deferral). Executes the CLAUDE.md
   fragment rule while the three inlined sites are open; severable commit
   keeps the slice's diff reviewable.
7. **Duplicate-name ambiguity error: KEEP** (as proposed). Tie-break ladder
   prefer-current → prefer-active → loud EINVAL listing candidate IDs;
   ID-passthrough is the documented disambiguation.

**Workspace facts observed during spikes:** catalog is 11 labels, one page,
no groups/parents/retired in real data — the sync pass is trivially cheap
today; group/retired fixtures must be synthetic in tests.
