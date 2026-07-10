# Project & Initiative external-link surface (issue #249)

**Branch:** `feat/249-project-attachments` · **Worktree:** `linear-fuse-249-project-attachments`

## Summary

Issue #249 asks to expose an `attachments/` surface on projects (and ideally
initiatives), mirroring the per-issue `attachments/` directory. Schema
investigation shows the underlying Linear entity is **not** `Attachment`
(issue-only; `AttachmentCreateInput.issueId` is required) but a distinct entity,
**`EntityExternalLink`** — the "Links / Resources" section on projects and
initiatives.

Decision: name the FS surface **`links/`** (honest to `EntityExternalLink`,
whose field is `label` not `title`) and cover **both projects and initiatives**
(same entity + mutations, small marginal cost).

```
teams/{KEY}/projects/{slug}/links/
  _create      # echo "https://notes.granola.ai/... [Onboarding Sync]" > _create
  .error
  .last
  *.link
initiatives/{slug}/links/
  _create
  .error
  .last
  *.link
```

Write contract identical to issue attachments: write `"URL [label]"` to
`_create`, read back `.last`/`.error`, `rm *.link` to delete. Reuses the generic
collection machinery (`collectionTrio`, `commitCreate`, `commitDelete`).

## Linear API facts (verified against docs/linear-schema.graphql)

- **Entity:** `type EntityExternalLink { id, label: String!, url: String!, sortOrder, creator, project, initiative, createdAt, updatedAt }`
- **Read:** `project.externalLinks(...)` → `EntityExternalLinkConnection!`;
  `initiative.links(...)` → `EntityExternalLinkConnection!`
- **Create:** `entityExternalLinkCreate(input: EntityExternalLinkCreateInput)` → `EntityExternalLinkPayload!`
  - Input: `{ url: String!, label: String!, projectId?, initiativeId?, cycleId?, teamId?, sortOrder?, id? }` — supply exactly one parent id.
- **Update:** `entityExternalLinkUpdate(id, input)` (label/url/sortOrder)
- **Delete:** `entityExternalLinkDelete(id)` → payload with `success`
- No dedup-by-url semantics on the server (unlike issue attachments, where
  `attachmentLinkURL` upserts on duplicate URL). So the idempotency pre-check
  in the create path must be our own (query existing links, match by url).

## Implementation layers

Mirrors the issue-attachment code paths noted in the exploration. Reference
points (in the issue plumbing) to copy from:

### 1. `internal/api` — new EntityExternalLink entity
- **types.go:** add `EntityExternalLink` struct (mirror `Attachment` at types.go:360; fields: ID, Label, URL, SortOrder, Creator, CreatedAt, UpdatedAt).
- **queries.go:**
  - `EntityExternalLinkFieldsFragment` (mirror `AttachmentFieldsFragment` queries.go:240).
  - `queryProjectExternalLinks` (`project(id).externalLinks(first:100)`), `queryInitiativeExternalLinks` (`initiative(id).links(first:100)`).
  - `mutationCreateEntityExternalLink`, `mutationDeleteEntityExternalLink` (+ update if we want edit later). Project each through the fragment (per CLAUDE.md fragment rule).
- **client.go:** `GetProjectLinks(ctx, projectID)`, `GetInitiativeLinks(ctx, initiativeID)`, `CreateEntityExternalLink(ctx, parentKind, parentID, url, label)`, `DeleteEntityExternalLink(ctx, id)`. Use `fetchNodes[EntityExternalLink]` for the reads (mirror `GetIssueAttachments` client.go:889).

### 2. `internal/api/mutationclient.go` — extend the mutation seam
- Add `CreateEntityExternalLink` + `DeleteEntityExternalLink` to the `MutationClient` interface and the real impl (mirror `LinkURL`/`DeleteAttachment` mutationclient.go:64).
- Update `internal/testutil/mockmutation` accordingly.

### 3. `internal/db` — storage
- **schema.sql:** new table `entity_external_links` (id PK, `project_id` NULL, `initiative_id` NULL, label, url, sort_order, creator_*, created_at, updated_at, synced_at, data JSON). Indexes on `project_id` and `initiative_id`. (Polymorphic parent via two nullable FKs, matching how the codebase keeps IDs globally unique.)
- **queries.sql:** `ListProjectLinks`, `ListInitiativeLinks`, `UpsertEntityExternalLink`, `DeleteEntityExternalLink`, `PruneProjectLinks`, `PruneInitiativeLinks`. Run `sqlc generate`.
- **convert.go:** `APIEntityExternalLinkToDB(...)`, `DBEntityExternalLinkToAPI(...)`, slice variants (mirror convert.go:882).

### 4. `internal/repo` — access methods
- Interface (`repo.go`) + sqlite impl (`sqlite.go`) + mock (`mock.go`):
  `GetProjectLinks(ctx, projectID)`, `GetInitiativeLinks(ctx, initiativeID)`
  (mirror `GetIssueAttachments` sqlite.go:1093).

### 5. `internal/fs` — the `links/` node
- New `LinksNode` generalized like `DocsNode` (documents.go:19) — carry
  `projectID`/`initiativeID`, dispatch on whichever is set; single parent key via
  a `linkParentID(...)` helper.
  - `Readdir`/`Lookup` mirror `AttachmentsNode` (attachments.go:30/71).
  - `trio()` → `collectionTrio{kind:"links", parentID, onFlush: createLink}`.
  - `listing()` → fetch project/initiative links, wrap in a `linkListing`
    (mirror `attachmentListing` / `attachmentlisting.go`; name = `sanitizeFilename(label)+".link"`).
  - `ExternalLinkNode` (`*.link` file) mirror `ExternalAttachmentNode`
    (attachments.go:209) incl. `Unlink` → `commitDelete`.
  - `createLink` mirror `createAttachment` (attachments.go:269) incl. our own
    url-dedup pre-check via `GetProjectLinks`/`GetInitiativeLinks`.
- **ino.go:** `linksDirIno(parentID)=ino("links", id)`, `externalLinkIno(id)=ino("extlink", id)` (guard: `TestInodeNamespaceDistinct`).
- **Wire-in:**
  - projects.go manifest (~projects.go:298): `m.subdir("links", linksDirIno(project.ID), ...)`.
  - initiatives.go manifest (~initiatives.go:155): `m.subdir("links", linksDirIno(initiative.ID), ...)`.

### 6. Sync / reconcile (optional for MVP, needed for background freshness)
- `internal/reconcile/details.go` (~:94) + `internal/sync/worker.go` — reconcile
  project/initiative links so they populate without a write. MVP can fetch
  lazily on Readdir (like `AttachmentsNode` does via repo) and defer sync
  integration; note the gap if deferred.

### 7. Generated README (REQUIRED — CLAUDE.md contract)
- Update `generateReadme` (internal/fs/root.go) to document the project/initiative
  `links/` surface (directory map + `_create` behavior + write-only note).
- Extend `TestGeneratedReadmeMatchesBehavior` (internal/integration/readme_test.go)
  to assert the `links/` surface is described and `_create` is unreadable.

### 8. Tests
- Unit: `LinksNode` readdir/lookup/create/delete; url-dedup pre-check; label
  sanitization/dedup. Mock mutation client.
- Integration: create a link on a project + on an initiative, read `.last`, `rm`.

## Open questions / risks
- **Dedup:** confirm server behavior on duplicate url per parent — schema shows no
  unique-url guarantee for `EntityExternalLink`, so our create pre-check is the
  only dedup. Verify against live API before relying on it.
- **`label` required:** `_create` line must always yield a non-empty label;
  default to the URL when no `[label]` given (matches issue-attachment title default).
- **Reconcile scope:** decide MVP (lazy fetch only) vs. full sync-worker
  integration for this branch.
