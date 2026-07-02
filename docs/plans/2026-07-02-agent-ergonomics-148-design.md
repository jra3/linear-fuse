# Agent Ergonomics (#148) — Coherent Design & Scoped Tasks

## 1. The Write Contract

**Editable in, server-managed out.** Every writable surface (`issue.md`, `project.md`, `initiative.md`, and every `_create` trigger) exposes only the fields the writer may set, and a successful write never alters the bytes the writer wrote. Server-managed identity and write-volatile state (`id`, `identifier`, `url`, `slug`, `created`/`updated`, `creator`/`owner`, `branch`, workflow timestamps, API-read-only dates) live in a sibling read-only `<entity>.meta`. Feedback is symmetric and keyed identically (entity ID for singletons, `collectionKey(kind, parentID)` for collections), mounted with the same zero attr/entry timeouts + `FOPEN_DIRECT_IO` plumbing:

- Bad input → `EINVAL`, reason on `.error`.
- Backend failure → `EIO` (or `EAGAIN` when retryable), reason on `.error`.
- **Every write** sets or clears `.error`; **every _create/mkdir** additionally appends its resulting identity to `.last` as a YAML list of `{identifier, url, path, title, status}`. Edits report success via read-your-writes, not `.last`.

The frontmatter a surface emits (minus its `.meta` fields) is exactly the frontmatter its `_create` accepts — one schema, read and write. Associations are set as frontmatter fields on `_create` (`project:`, `labels:`, `parent:`, `milestone:`, `cycle:`, `assignee:`), never by choosing a magic create-location.

---

## 2. Key Decisions

### Design questions

| Question | Decision | Why |
|---|---|---|
| Is "stop self-mutation" issue-only or general? | **General.** Split write-volatile server fields into a read-only `.meta` sibling for `issue.md`, `project.md`, **and** `initiative.md`. | `project.md` (projects.go ~488) and `initiative.md` (initiatives.go ~299) inline-emit `id/slug/url/status/updated` and self-mutate identically. Issue-only scope was under-scoped. |
| Do the `.meta` split and full-object create share one schema? In what order? | **One schema, schema-first.** The `.meta` split defines the canonical editable schema; `issues/_create` consumes it verbatim. Meta split precedes create. | Avoids a second "shape create ignores." |
| How many ways to make an issue survive? | **Exactly two, clearly roled.** `mkdir "Title"` = quick path (title only). `echo spec > issues/_create` = full path (frontmatter + body, single document). Both report identity to `.last`. No create verbs anywhere else. | Kills create-verb proliferation (four ways → two). |
| Add create-in-context to `projects/{slug}/`, `by/label/`, `by/status/`? | **Dropped.** No create verbs on filter/association views. Association-at-create is frontmatter on `issues/_create`. `children/` mkdir stays as the sole path-based precedent. | `by/*` are read-only symlink views; born-Done / unassigned / labels-are-not-containers edge cases; frontmatter `project:` already covers association. |
| Is `.last` scalar or list-shaped? Does it cover edits? | **List-shaped from day one** — a capped YAML array of `{identifier, url, path, title, status}`, create-scoped only. | A multi-create session recovers all N identities in one read; symmetric with `.error`; `path` cures typed-name-vs-slug friction. |
| Does a declarative `_bulk` manifest survive? | **Dropped.** Batch is achieved by N atomic `echo spec > _create` calls, each self-reporting to `.last`. | A `_bulk` reconciler is RPC-in-a-file: needs unused transaction/rollback machinery (`WithTx` wraps only SQLite; Linear has no batch mutation) and partial-failure semantics that fight the sync worker. Its results value is covered by list-shaped `.last`; its round-trip value by sequential atomic creates. |
| Do `.last`, `my/created/`, `recent/` overlap harmfully? | **No — named, non-overlapping roles.** `.last` = "what THIS session's writes just produced" (create-scoped, collection-local). `my/created/` = "everything I authored" (user-scoped, exists). `recent/` = "the team's newest" (team-scoped, ordered). | Overlap is fine when roles are named. |
| Is multi-document batch `_create` kept? | **Dropped from `_create`.** One write = one document = one atomic, self-reporting create. | The `---`-splitter collides with legitimate markdown horizontal rules and body lines (`Note:` after a `---`), and "body may not contain `---`" silently corrupts bodies — violating the loud-failure premise. Sequential atomic creates are more legible and lose nothing. |
| Can the success half be tested offline? | **Yes, via a new fixture-mode mock mutation client (T0).** | Fixture mode uses a real `api.Client` with a dummy key; every create/edit fails before reaching `ClearWriteError`/`AppendWriteSuccess`. Without a mutation seam the entire success contract is CI-unguarded. |
| `_create` schema strictness? | **Tolerant parse** on both edit and create — silently ignore unknown/read-only keys, matching existing `MarkdownTo*Update`. | Lets a writer copy `.meta` fields back into the editable file without error. |
| Is `.meta` frontmatter or bare YAML? | **YAML frontmatter block** (leading/trailing `---`, no body). | `parseFrontmatter` works uniformly across issue/project/initiative meta and test helpers. |
| Is `.meta` cache policy KEEP_CACHE or DIRECT_IO? | **`FOPEN_DIRECT_IO` + zero attr/entry timeouts**, matching `.error`/`.last`. | Freshness is automatic after a Flush; no per-Flush meta-inode invalidation, no stale-cache bug class. |

### Fate of each filed issue

| Issue | Fate | Notes |
|---|---|---|
| **#149** (`.last` sidecar) | **Kept** as T1, upgraded to list-shaped `{identifier,url,path,title,status}`, create-scoped, symmetric with `.error`, present-and-empty before first create. |
| **#150** (stop `issue.md` self-mutation) | **Kept & generalized** across T2 (`issue.meta`) + T3 (`project.meta`, `initiative.meta`). Requires extracting `marshal/project.go` + `marshal/initiative.go`. |
| **#151** (full-object `issues/_create`) | **Kept** as T4, re-anchored to consume the T2 editable schema. Single-document only (batch dropped). |
| **#152** (create-in-context association) | **Dropped as filed, merged into T4.** Association becomes frontmatter on `_create`; no create verbs on read-only views. |
| **#153** (team-scoped `recent/`) | **Kept** as T5, unchanged in intent, lowest priority, orthogonal to the write contract. |
| **#154** (declarative `_bulk`) | **Dropped.** Results value absorbed by list-shaped `.last`; round-trip value by sequential atomic `_create`. |

---

## 3. Scoped Tasks

> **Cross-cutting testability fact:** in fixture mode (default `make test`), `lfs.client` is a real `api.Client` with a dummy key and `InjectTestStore` injects only the store — so *today* no create/edit can succeed offline. **T0 introduces the mutation seam that makes the success half of the contract provable in `make test`.** Every "success → `.last` carries identity" and "byte-stable across a successful write" acceptance below depends on T0; without T0 those lines are live-only (`LINEARFS_WRITE_TESTS=1`).

---

### [T0] Fixture-mode mock mutation client — make the success contract CI-provable offline

- **Effort:** M
- **Dependencies:** none (blocks T1, T4, T6)

**Problem.** Fixture mode has no mutation seam: `lfs.client` is a concrete `*api.Client` built from `cfg.APIKey="fixture-mode-key"` (integration_test.go:124) pointing at the real Linear API, and `InjectTestStore` injects only the store. Every `CreateIssue`/`CreateComment`/`CreateDocument`/`CreateLabel`/… fails at the network call before reaching `ClearWriteError`/`AppendWriteSuccess` (proven by `TestMkdirIssueFailureIsLegible` expecting failure, and by every persistence test gating on `skipIfNoWriteTests`). Consequently the *success* half of the write contract — the headline deliverable — cannot be regression-guarded by `make test`.

**Solution.** Introduce a narrow mutation-client interface and an injectable in-memory fake.

1. Define a `MutationClient` interface in `internal/fs` (or `internal/api`) covering exactly the mutation methods the write handlers call: `CreateIssue`, `UpdateIssue`, `CreateComment`, `UpdateComment`, `CreateDocument`, `UpdateDocument`, `CreateLabel`, `UpdateLabel`, `CreateProject`, `CreateMilestone`, `CreateProjectUpdate`, `CreateAttachment`, `CreateRelation`, plus the deletes the delete handlers use. The concrete `*api.Client` already satisfies it.
2. Change `LinearFS` to hold the mutation surface behind that interface (keep the field typed as the interface; production wiring stays `*api.Client`).
3. Add `InjectTestMutationClient(mc MutationClient)` beside `InjectTestStore`.
4. Provide `internal/testutil/mockmutation` (or reuse `repo.MockRepository` conventions): a fake that returns a canned persisted entity (echoes input with a generated `id`/`identifier`/`url`/`slug`) and upserts into the injected store so reads see it. It must exercise the *real* handler tail (resolve → API-call → upsert → invalidate → `ClearWriteError` → `AppendWriteSuccess`).

**File touchpoints.**
- `internal/fs/linearfs.go`: change the mutation-client field to the interface type; add `InjectTestMutationClient`.
- `internal/fs/mutationclient.go` (NEW): `MutationClient` interface definition.
- `internal/testutil/mockmutation/mock.go` (NEW): fake returning canned persisted entities and upserting to the store.
- `internal/integration/integration_test.go`: wire `InjectTestMutationClient` into `setupSQLiteFixtures` (opt-in flag so existing "create fails offline" tests can still assert failure where they intend to).

**Acceptance.**
- With the fake injected, a fixture-mode `mkdir "Title"` / `echo spec > _create` runs the full handler tail to success and upserts the entity into the store; the new entity is subsequently readable.
- The concrete `*api.Client` satisfies `MutationClient` unchanged; production behavior is identical.
- Tests can choose per-case whether mutations succeed (fake) or fail (no fake, real client + dummy key), so both the success and loud-failure halves are exercisable offline.
- `go build ./...` and `make test` pass.

**Out of scope.** Mocking *reads* (the store already backs reads); changing the API client's real network behavior; any `.last`/`.meta` logic (T1/T2).

**Test plan.** Unit test the fake in isolation (input echoed with generated identity; store upsert observable). One fixture-mode smoke test: inject fake, create an issue, assert it appears via the repository. All existing loud-failure tests continue to run without the fake.

---

### [T1] `.last` success sidecar — list-shaped read-only mirror of `.error`, wired into every create tail

- **Effort:** M
- **Dependencies:** T0 (to prove the populated success path in fixture mode)

**Problem.** Every writable collection has a `.error` failure sidecar (internal/fs/errorfile.go) but no success channel. After a create there is no deterministic place reporting the new entity's identity; an agent must re-list, read `my/created/`, or grep `issue.md`. The feedback contract is half-built. The characterization test `TestIssue148_CreateHandsBackNoIdentifier` currently PASSES asserting no `.last` exists.

**Solution.** Mirror `internal/fs/errorfile.go` in a new `internal/fs/successfile.go`.

- **Store:** `WriteResult{Identifier, URL, Path, Title, Status string; Timestamp time.Time}`; `writeSuccesses map[string][]*WriteResult` + `writeSuccessesMu sync.RWMutex` on `LinearFS`. Methods `AppendWriteSuccess(key, r)` (append, cap ~50 newest-last, then `InvalidateUpdated(successIno(key))`), `GetWriteSuccess(key)`, `ClearWriteSuccess(key)` (symmetry/tests).
- **Keys:** `collectionSuccessKey(kind, parentID)` returns the *same* `kind + ":" + parentID` string as `collectionErrorKey` — shared namespace, distinct maps and inodes. Sub-issue success keys on the parent issue ID (same as `.error`).
- **Inode:** `successIno(key)` = fnv64a over `"last:" + key`, never colliding with `errorIno` (`"error:"+key`).
- **Node:** `SuccessFileNode` copied from `ErrorFileNode` — `Getattr` (mode 0444, size = rendered length), `Open` (`FOPEN_DIRECT_IO`), `Read`. `lookupSuccessFile` copied from `lookupErrorFile`: mode 0444, zero `SetAttrTimeout`/`SetEntryTimeout`. **`.last` is mounted always-present (present-and-empty before any create)**, mirroring `ErrorFileNode`, so structural tests can find it offline.
- **Rendering:** YAML list, one map per entry with keys `identifier, url, path, title, status` (blank where the entity lacks them). `path` = the real on-disk name (`issue.Identifier`, project slug, or entity filename). Capture what **persisted** (the returned entity), never what was sent.
- **Wiring** (append immediately after each existing `ClearWriteError`, using the returned entity): `IssuesNode.Mkdir`, `ChildrenNode.Mkdir` (key = parent issue ID), `ProjectsNode.Mkdir`, and the `_create` Flush handlers `NewCommentNode`/`NewDocumentNode`/`NewLabelNode`/`NewMilestoneNode`.
- **Mounting:** add a `.last` case beside every `.error` case in the Lookup + Readdir sites for these surfaces. **Do NOT extend `editcommit.go` `commitWriteBack`** — `.last` is create-scoped; the create handlers call `AppendWriteSuccess` directly.

**File touchpoints.**
- `internal/fs/successfile.go` (NEW): `WriteResult`; `AppendWriteSuccess`/`GetWriteSuccess`/`ClearWriteSuccess`; `collectionSuccessKey`; `successIno`; `lookupSuccessFile`; `SuccessFileNode`.
- `internal/fs/linearfs.go`: add `writeSuccesses` map + mutex beside `writeErrors`; init alongside.
- `internal/fs/issues.go`: `AppendWriteSuccess` in `IssuesNode.Mkdir` and `ChildrenNode.Mkdir`; `.last` in `IssuesNode.Readdir`/`Lookup` and `IssueDirectoryNode.Readdir`/`Lookup`.
- `internal/fs/comments.go`, `documents.go`, `labels.go`, `milestones.go`, `projects.go`: `AppendWriteSuccess` after `ClearWriteError`; `.last` in Readdir/Lookup.
- `internal/fs/root.go`: update embedded README/tree docs to describe `.last`.
- `internal/integration/issue148_test.go`: **own the inversion** of `TestIssue148_CreateHandsBackNoIdentifier` — assert `.last` EXISTS alongside `.error` on issues/comments/docs/labels; with T0's fake, perform a create and assert the newest `.last` entry carries the identity.

**Acceptance.**
- A read-only `.last` (mode 0444, `FOPEN_DIRECT_IO`, zero timeouts) appears in every create surface that exposes `.error`: `issues/`, `issues/{ID}/` (sub-issues), `comments/`, `docs/`, `labels/`, `projects/{slug}/milestones/`, `projects/`. It is **present-and-empty before any create**.
- With T0's fake, after a create, `.last` is a YAML list whose newest entry carries `identifier`/`url` (where applicable) and `path` = the addressable on-disk name; multiple creates accumulate as a capped list (newest-last, ~50).
- `.last` is create-scoped: editing any file never appends to or clears it (`commitWriteBack` untouched).
- `successIno` never collides with `errorIno`; `collectionSuccessKey` shares the `kind:parentID` string with `collectionErrorKey`.
- Inverted `TestIssue148_CreateHandsBackNoIdentifier` passes in fixture mode.
- `go build ./...` and `go test ./internal/fs/... ./internal/integration/...` pass.

**Out of scope.** `.meta` split (T2/T3); full-object `_create` (T4); `recent/` (T5); wiring `.last` into the edit tail. **Known gap (acknowledged, follow-up pass):** `attachments/_create` and `relations/_create` have `.error` but are *not* given `.last` here — their identity is a URL/relation string, lower value. **Placement wart (mirrors `.error`):** `ChildrenNode.Mkdir` keys on the parent issue ID and its `.last` surfaces at `issues/{ID}/.last`, not inside `children/`.

**Test plan.** (1) Invert `TestIssue148_CreateHandsBackNoIdentifier`: assert existence on all four surfaces; with T0's fake, create and assert identity. (2) `internal/fs/successfile_test.go` (unit): append→get newest-last; cap enforced; `Read` renders valid YAML list; `successIno != errorIno`; `ClearWriteSuccess` empties. (3) Extend `claude_tools_test.go`: after a create (fake), sibling `.error` empty AND `.last` reports identity; an edit does NOT append to collection `.last`. (4) Regression: `go test ./internal/fs/... ./internal/integration/...`.

---

### [T2] `issue.meta` split — make `issue.md` editable-only; move server fields to read-only `issue.meta`; expose the reusable meta node

- **Effort:** M
- **Dependencies:** none

**Problem.** `issue.md` colocates editable fields with server-managed, write-volatile fields. On a successful write, `IssueFileNode.Flush` re-fetches with a bumped `updated:` and the next Read re-marshals different bytes under the editor's read-before-write cache → "modified since read." There is also no stable definition of "the editable schema" for T4 to consume. The parse side (`MarkdownToIssueUpdate`) already ignores server fields, so this is an emission/mounting change.

**Solution.**
1. Split emission in `internal/marshal/issue.go`. Narrow `IssueToMarkdown` to emit only editable fields + body (title, status, priority, assignee, labels, due, estimate, team, project, milestone, parent, cycle, description) — drop `id/identifier/url/created/updated/creator/branch`, workflow timestamps, links, relations, and **drop the attachments variadic from its signature**. Add `IssueMetaToMarkdown(issue, attachments...) ([]byte, error)` emitting the server set (`id, identifier, url, created, updated, creator, branch`, the four workflow timestamps, external links, forward + inverted relations via `invertRelationType`). Leave `MarkdownToIssueUpdate` untouched (tolerant parse).
2. **Introduce the shared, reusable meta node** used by T3. Add `MetaFileNode` constructible from arbitrary pre-generated `[]byte`, and `lookupMetaFile(entityID, content []byte)` that mounts it read-only (mode 0444) with **`FOPEN_DIRECT_IO` + zero attr/entry timeouts** (matching `.error`/`.last`, NOT `HistoryFileNode`'s KEEP_CACHE). With DIRECT_IO + zero timeouts, freshness after a Flush is automatic — no per-Flush meta-inode invalidation needed. `.meta` content is a **YAML frontmatter block** (leading/trailing `---`, no body) so `parseFrontmatter` works uniformly.
3. Mount `issue.meta`. Add `metaIno(issueID)` (fnv, alongside `historyIno`/`errorIno`). In `IssueDirectoryNode.Readdir` add `{Name: "issue.meta", Mode: S_IFREG}`; in `IssueDirectoryNode.Lookup` add `case "issue.meta"`: fetch attachments via `GetIssueAttachments`, call `IssueMetaToMarkdown`, mount via `lookupMetaFile` with `metaIno(issue.ID)` and times from the issue.
4. **Fix both existing `IssueToMarkdown` callers** for the dropped variadic: the `issue.md` Lookup case (issues.go ~320) and `IssueFileNode.ensureContent` (issues.go ~573). **Delete the `attachments, _ := n.lfs.GetIssueAttachments(...)` fetch lines** at both (not just the arg) or the package won't compile (unused variable).

Net effect: a successful write no longer changes the bytes of `issue.md`. The narrowed `IssueToMarkdown` output IS the canonical editable schema T4 consumes.

**File touchpoints.**
- `internal/marshal/issue.go`: narrow `IssueToMarkdown` (drop server fields, links, relations, attachments variadic); add `IssueMetaToMarkdown`; leave `MarkdownToIssueUpdate`.
- `internal/fs/issues.go`: add `metaIno`; add `MetaFileNode` + `lookupMetaFile` (DIRECT_IO, zero timeouts, from `[]byte`); `issue.meta` in `IssueDirectoryNode.Readdir`/`Lookup`; **delete the attachments fetch at the `issue.md` Lookup case and at `ensureContent`**.
- `internal/marshal/issue_test.go`: `TestIssueToMarkdown` and `TestIssueToMarkdownWithAttachments` assert server/link fields present — **re-point the attachment call to `IssueMetaToMarkdown`**; move server/link want-strings to a new `TestIssueMetaToMarkdown`; keep editable want-strings on `TestIssueToMarkdown`.
- `internal/integration/issue148_test.go`: **own the inversion** of `TestIssue148_EditableFileColocatesVolatileServerFields` — `issue.meta` EXISTS, `issue.md` has editable fields and NOT `updated/url/id/identifier`, `issue.meta` carries the server set.
- `internal/integration/read_test.go`: `requiredFields` (line ~361) asserts `id/identifier/url/created/updated` in `issue.md` — **relocate**: editable fields checked on `issue.md`, server fields read from `issue.meta`.
- `internal/integration/fixture_read_test.go`: `requiredFields` (line ~77) same relocation.
- `internal/integration/testdata_test.go` / other helpers: `createTestIssue`/`parseFilesystemIssue`/`getIssueFromFilesystem` recover `id`/`identifier` from `issue.md` — **re-source identity from `issue.meta` (or `.last`)**. Also `cache_test.go:201`, `symlink_test.go:448` (skip in fixture; fix for live).

**Acceptance.**
- `IssueToMarkdown` output contains the editable set + body and none of `id/identifier/url/created/updated/creator/branch/started/completed/canceled/archived/links/relations`.
- `IssueMetaToMarkdown` output contains exactly the server set (fields present-only), wrapped as a YAML frontmatter block.
- `issue.meta` appears in Readdir and resolves read-only (mode 0444) with a stable `metaIno`; it uses `FOPEN_DIRECT_IO` + zero timeouts.
- `issue.md` no longer contains `updated:`/`url`/`id`/`identifier`; a faithful no-op rewrite is byte-identical (self-mutation eliminated). *(Byte-stability across a real write is verified live, or in fixture mode via T0's fake.)*
- `MarkdownToIssueUpdate` unchanged; stray copied-in server keys still write successfully.
- Inverted `TestIssue148_EditableFileColocatesVolatileServerFields` passes; relocated `read_test.go`/`fixture_read_test.go` required-field checks pass.
- `go build ./...` and `go test ./internal/marshal/... ./internal/integration/...` pass in fixture mode.

**Out of scope.** `project.md`/`initiative.md` split (T3, but this task delivers the reusable `MetaFileNode`/`lookupMetaFile` T3 consumes); `.last` (T1); `issues/_create` (T4); freezing residual `issue.md` mtime bump; `MarkdownToIssueUpdate` parse changes.

**Test plan.** Unit (`marshal/issue_test.go`): editable-present/server-absent on `IssueToMarkdown`; new `TestIssueMetaToMarkdown`; migrate attachment link asserts to `IssueMetaToMarkdown`. Integration: invert `TestIssue148_EditableFileColocatesVolatileServerFields`; relocate the two `requiredFields` sets; a no-op-rewrite byte-stability assertion (live, or fixture via T0). Run `go test ./internal/marshal/... ./internal/integration/...` and `go build ./...`.

---

### [T3] Generalize the `.meta` split to `project.md` and `initiative.md`

- **Effort:** M
- **Dependencies:** T2 (reuses `MetaFileNode` + `lookupMetaFile`)

**Problem.** The "editable in, server-managed out" rule is general, but T2 fixes only `issue.md`. `project.md` (projects.go:451-512 `generateContent`) emits `id/slug/url/status/lead/startDate/targetDate/created/updated`; `initiative.md` (initiatives.go:274-327) emits `id/slug/url/status/color/icon/owner/targetDate/created/updated`. Both re-fetch on Flush, bump `updated:`, set `contentReady=false`, and regenerate bytes with a fresh timestamp — the same phantom-diff churn. Neither has a marshal module, so there is no shared, testable seam.

**Solution.**
1. Extract two marshal modules mirroring `marshal/issue.go`:
   - `internal/marshal/project.go`: `ProjectToMarkdown` (editable only: `name`, `initiatives`, body=description); `ProjectMetaToMarkdown` (`id, slug, url, status, lead, created, updated`, and API-read-only `startDate`/`targetDate` — absent from `api.ProjectUpdateInput`); `MarkdownToProjectUpdate` (tolerant parse of `name`/`description`/`initiatives`).
   - `internal/marshal/initiative.go`: `InitiativeToMarkdown` (editable only: `name`, `projects`, body); `InitiativeMetaToMarkdown` (`id, slug, url, status, color, icon, owner, created, updated`, and read-only `targetDate`); `MarkdownToInitiativeUpdate` (tolerant parse).
   Both meta emitters produce a YAML frontmatter block (matching T2).
2. Point the fs nodes at the editable emitters: `ProjectInfoNode.generateContent()` → `marshal.ProjectToMarkdown`; `InitiativeInfoNode.generateContent()` → `marshal.InitiativeToMarkdown`. This alone kills the self-mutation (`updated:` no longer appears in the editor's bytes).
3. **Wire the tolerant parsers into Flush** so they are not dead code: `ProjectInfoNode.Flush` and `InitiativeInfoNode.Flush` consume `MarkdownToProjectUpdate`/`MarkdownToInitiativeUpdate` (replacing the inline frontmatter parse at projects.go ~634 / initiatives.go).
4. Mount the meta sidecars via T2's `lookupMetaFile(entityID, content)`: `ProjectNode.Lookup` adds `case "project.meta"` with `marshal.ProjectMetaToMarkdown(&p.project)` and `projectMetaIno()`; `ProjectNode.Readdir` adds `project.meta` (bump the entry allocation). Same for `InitiativeNode.Lookup`/`Readdir` with `initiative.meta`/`initiativeMetaIno()`.

Because `lookupMetaFile` uses `FOPEN_DIRECT_IO` + zero timeouts (T2), freshness is automatic; **no per-Flush meta-inode invalidation is required** (this is the reason T2 mandates DIRECT_IO over KEEP_CACHE). Note that Flush already re-fetches into `p.project`/`i.initiative` and does the SQLite upsert + `InvalidateTeamProjects`/`InvalidateInitiatives` + full-path re-lookup, so the meta sidecar reflects fresh server state on the next resolve.

**File touchpoints.**
- `internal/marshal/project.go` (NEW), `internal/marshal/initiative.go` (NEW): the three functions each.
- `internal/fs/projects.go`: `ProjectInfoNode.generateContent` → `ProjectToMarkdown`; `ProjectInfoNode.Flush` → `MarkdownToProjectUpdate`; `ProjectNode.Lookup` `case "project.meta"`; `ProjectNode.Readdir` entry + allocation bump; add `projectMetaIno()`.
- `internal/fs/initiatives.go`: analogous changes; add `initiativeMetaIno()`.
- `internal/integration/initiatives_test.go` (line ~39), `fixture_read_test.go` (line ~378, project section), `fixture_extended_test.go` (line ~154): **own these relocations** — these fixture-mode tests hard-assert `id/slug/status` on `initiative.md`/`project.md`; move server-field expectations to `.meta`.
- `internal/integration/issue148_test.go`: add project/initiative characterization tests (assert `.meta` exists, editable file lacks `id/url/updated`).

**Acceptance.**
- Listing a project dir shows `project.md` + `project.meta`; initiative dir shows `initiative.md` + `initiative.meta`.
- `project.md` has only `name`/`initiatives` (+ body), none of `id/slug/url/status/created/updated/lead/startDate/targetDate`; `initiative.md` only `name`/`projects`, none of the server set.
- `project.meta`/`initiative.meta` are mode 0444, `FOPEN_DIRECT_IO` + zero timeouts, carry the server fields, and reject open-for-write (`EACCES`/`EPERM`).
- No-op rewrite of `project.md`/`initiative.md` is byte-identical (no `updated:` churn) — verified live, or fixture via T0.
- Editing `name`/`description`/associations still syncs via Flush (now through the tolerant parsers); stray meta keys are ignored (no `EINVAL`).
- `.error` behavior unchanged. `make build` and `make test` pass; the three relocated fixture tests pass.

**Out of scope.** `issue.md`/`issue.meta` (T2); `.last` (T1); a `projects/_create`/`initiatives/_create` surface; making `startDate`/`targetDate` writable (API input structs reject them); `recent/` (T5); `issues/_create` (T4).

**Test plan.** Fixture-mode integration in `issue148_test.go`: `TestT3_ProjectMetaSplit`, `TestT3_InitiativeMetaSplit` (skip if fixture lacks an initiative), plus meta-node open-for-write rejection (`O_WRONLY` → `EACCES`/`EPERM`). No-op-rewrite byte-stability (live, or fixture via T0). Unit tests in the new marshal packages: golden round-trips for the four emitters + tolerance tests for the two parsers. Run `go test ./internal/marshal/... ./internal/integration/...` and `make build`.

---

### [T4] Full-object `issues/_create` — frontmatter + body, name resolution, association (single document)

- **Effort:** L
- **Dependencies:** T1 (hard build dep — calls `AppendWriteSuccess`/`collectionSuccessKey`). T2 is a **sequencing/schema-coherence** preference, not a build dep (T4 reuses the existing `MarkdownToIssueUpdate` extraction, which compiles today).

**Problem.** An issue can only be created via `mkdir "Title"` (sends `{teamId, title}`, skips the resolver). There is no way to set status/assignee/labels/priority/project/milestone/cycle/parent/estimate/due/body at birth; an agent must mkdir then edit (a second Flush round-trip) and cannot associate at creation. There is no `_create` seam under `teams/{KEY}/issues/`.

**Solution.** Add a write-only `_create` trigger under `teams/{KEY}/issues/`, modeled on the comments `_create` pattern (`NewCommentNode`): `Getattr` 0200 / `Open` `FOPEN_DIRECT_IO` / `Write` buffer / `Read`→`EACCES` / `Setattr` truncate / `Fsync` no-op / `Flush`=create.

- **Parsing:** add `marshal.MarkdownToIssueCreate(content) (map[string]any, error)` reusing the same field-extraction as `MarkdownToIssueUpdate`, but emitting ALL present editable fields (not deltas) as a create-input map (names, not IDs) + `description` from body. Read-only/unknown keys ignored tolerantly.
- **`.error` format normalization (fix):** priority is validated inside marshal (`fmt.Errorf("priority: %w")`), NOT by the resolver, so a naive reuse would give a `Parse error:` shape for bad priority but a `Field/Value/Error` shape for an unresolvable status. **Normalize:** in `createIssueFromSpec`, wrap the marshal priority-parse error into the `Field: priority\nValue: %q\nError: %s` format before `SetWriteError`, so both invalid-frontmatter cases produce the same `.error` shape. Pin this shape once in acceptance/tests.
- **Resolution:** reuse `resolveIssueUpdate` verbatim against a synthetic `api.Issue{Team: &n.team}` (empty Labels/Project — the empty-labels clearing branch is a safe no-op; `removedLabelIds` is never emitted on create). A `*FieldError` → `.error` in `Field/Value/Error` format → `EINVAL`. Missing title falls back to a constant default ("Untitled issue").
- **Create + persist:** add `teamId`, call `CreateIssue`, then `UpsertIssue` + `InvalidateTeamIssues`/`InvalidateMyIssues`/`InvalidateFilteredIssues(team.ID)` (+ project/user invalidation when set) + `InvalidateCreated(issuesDirIno, identifier)` — the same tail `Mkdir` uses. On success `ClearWriteError` + `AppendWriteSuccess` with `{identifier,url,path,title,status}`; `errKey = collectionErrorKey("issues", team.ID)` so it shares `issues/.error` and `issues/.last`.
- **One document per write.** No `SplitIssueSpecs`, no batch loop. Batch = N sequential atomic `echo spec > _create` calls, each self-reporting to `.last`.
- **Converge the two verbs:** extract `createIssueFromSpec(ctx, spec) (*api.Issue, *FieldError, syscall.Errno)` used by BOTH the new `_create` Flush and `IssuesNode.Mkdir` (Mkdir passes a title-only spec). The builder returns the entity + classified error and **lets each caller decide inode construction** — `Mkdir` builds and returns an `*fs.Inode`; `_create` Flush does not. **Preserve `Mkdir`'s `retryableCreateErr`→`EAGAIN` branch and the exact `Operation: create issue …` `.error` text** that `TestMkdirIssueFailureIsLegible` asserts.

**File touchpoints.**
- `internal/fs/issues.go`: `_create` entry in `IssuesNode.Readdir`; `IssuesNode.Lookup` mounts `NewIssueCreateNode`; add `NewIssueCreateNode` (methods copied structurally from `NewCommentNode`); extract `createIssueFromSpec` returning `(*api.Issue, *FieldError, syscall.Errno)`; refactor `Mkdir` to delegate (title-only spec) while preserving EAGAIN + `.error` text.
- `internal/marshal/issue.go`: add `MarkdownToIssueCreate` (all present editable fields as names + body; `api.ValidatePriority`). No `SplitIssueSpecs`.
- `internal/fs/resolve.go`: no change (`resolveIssueUpdate` reused; confirm empty-labels branch is a no-op).
- `internal/integration/claude_tools_test.go`: fixture tests — `_create` exists and is write-only (`Read`→`EACCES`, mode 0200); invalid priority AND unresolvable status both → `EINVAL` with the **same** `Field/Value/Error` shape on `issues/.error`; a valid spec fails at API loudly when the mock is absent (mirrors `TestMkdirIssueFailureIsLegible`); with T0's fake, a valid spec creates the issue with associations and reports to `issues/.last`.
- `internal/integration/write_test.go`: live-mode (`skipIfNoWriteTests`) `TestCreateIssueViaCreateFileFullObject` (associations set, appears in listing and `.last`).

**Acceptance.**
- `teams/{KEY}/issues/` lists `_create`; it resolves to a write-only node (`Read`→`EACCES`, mode 0200).
- Writing one `---`-delimited frontmatter+body document creates one issue whose title/priority/status/assignee/labels/project/milestone/cycle/parent/estimate/due/description are set, names resolved via `resolveIssueUpdate`.
- Association is expressed only as frontmatter; no create verbs on `by/*`, `projects/{slug}/`, `cycles/`.
- Missing title → default; unknown/read-only keys ignored.
- Invalid priority AND unresolvable status/assignee/label/project/etc. both return `EINVAL` with the **same** `Field/Value/Error` message on `issues/.error` before any issue is created; backend failure → `EIO` (or `EAGAIN` retryable) with a reason.
- On success (fake in fixture, or live), `issues/.error` cleared and `{identifier,url,path,title,status}` appended to `issues/.last` (keyed `collectionSuccessKey("issues", team.ID)`).
- `mkdir "Title"` still works, now through `createIssueFromSpec`; `TestCreateIssueViaMkdir`/`TestMkdirIssueFailureIsLegible`/`TestCreateIssueInvalidatesTeamListing` still pass (EAGAIN + `.error` text preserved).
- New issues appear immediately in the kernel directory listing.

**Out of scope.** `project.md`/`initiative.md` create surfaces and `.meta` split (T2/T3); `.last` plumbing itself (T1); `recent/` (T5); create verbs on filter/association views (dropped — frontmatter-only); **multi-document batch / `_bulk` reconciler (dropped)**; `children/` creation (unchanged); generalizing `resolveIssueUpdate` to other create paths.

**Test plan.** Fixture-mode (`claude_tools_test.go`): `TestIssuesCreateSurfaceIsWriteOnly`; `TestIssuesCreateInvalidFrontmatterIsLegible` (bad priority AND unresolvable status → identical `.error` shape, "Field: priority" / "Field: status"); `TestIssuesCreateValidSpecFailsAtAPILoudly` (no fake); with T0's fake, `TestIssuesCreateValidSpecSucceedsAndReportsLast`. Unit (`internal/fs`): fake resolver + synthetic `api.Issue{Team:…}` exercises `createIssueFromSpec` for all relational fields; asserts `removedLabelIds` never emitted and nil Project/Labels don't panic. Unit (`internal/marshal`): `MarkdownToIssueCreate` returns all present editable fields as names + body. Live (`write_test.go`, `skipIfNoWriteTests`): `TestCreateIssueViaCreateFileFullObject`. Confirm the Mkdir refactor keeps the three existing mkdir tests green.

---

### [T5] Team-scoped `recent/` view — newest-first issue discovery, order-guaranteed

- **Effort:** S
- **Dependencies:** none (needs the fixture timestamp seam below, which it introduces)

**Problem.** An agent has no shell-flag-independent way to see "the team's newest issues." `my/created/` is user-scoped; `.last` is write-scoped. Plain `ls` of `teams/{KEY}/issues/` is unordered (FUSE readdir order is unspecified). There is no `recent/` under `teams/{KEY}/`.

**Solution.** Add a read-only `recent/` under `teams/{KEY}/` listing the team's issues as symlinks, newest-first (`updatedAt` DESC), capped to 50. Mirror `MyIssuesNode`/`FilterValueNode`.

- **Data source:** reuse `Repository.GetTeamIssues(ctx, teamID)` (backing `ListTeamIssues` is already `ORDER BY updated_at DESC`). **Do NOT add a redundant `ListRecentTeamIssues`.**
- **Ordering guarantee (critical):** SQL `ORDER BY` does not survive to the fs layer as a contract (`comments.go:101` sorts explicitly for this reason). In `RecentNode.Readdir`/`Lookup`, after fetching, call `sort.Slice` on `UpdatedAt` descending, then cap to N — in **both** so `ls` and `stat recent/TST-3` agree.
- **Symlink depth:** `recent/` sits at `teams/{KEY}/recent/`, so the relative target is `../issues/{identifier}`. Add `RecentIssueSymlink` (0777|S_IFLNK, `SetTimes(updatedAt, updatedAt, createdAt)`).
- **Wiring:** `TeamNode.Readdir` gets `{Name: "recent", Mode: S_IFDIR}`; `TeamNode.Lookup` gets `case "recent":` mounting `RecentNode`. No write verbs, no `_create`, no `.error` (mode 0555 dir, 0444 symlinks).
- **Fixture timestamp seam (fix — required for the ordering test):** `FixtureAPIIssue` hardcodes `UpdatedAt = fixtureTime + 24h` for every issue and there is no `WithUpdatedAt`/`WithCreatedAt` option, so all TST issues share one timestamp and `sort.Slice` is a no-op. **Add `WithUpdatedAt`/`WithCreatedAt` `IssueOption`s to `internal/testutil/fixtures/api.go` and seed the recent-test fixtures with distinct, non-monotonic-vs-insertion timestamps** so a missing `sort.Slice` fails the test.

**File touchpoints.**
- `internal/fs/recent.go` (NEW): `RecentNode` (`getIssues()` → `GetTeamIssues`; shared sort-DESC + cap `recentLimit=50`); `RecentIssueSymlink` targeting `../issues/{identifier}`.
- `internal/fs/teams.go`: `recent` entry in `TeamNode.Readdir`; `case "recent":` in `TeamNode.Lookup`.
- `internal/fs/linearfs.go`: confirm `GetTeamIssues` reachable from `RecentNode`; add a thin passthrough if only `repo` has it (mirror `GetMyCreatedIssues`).
- `internal/testutil/fixtures/api.go`: **add `WithUpdatedAt`/`WithCreatedAt` `IssueOption`s**; seed distinct timestamps for the recent-test fixtures.
- `internal/integration/issue148_test.go`: `TestIssue148_RecentViewOrdered` (characterization → flip).
- `internal/integration/helpers_test.go`: add `recentPath(teamKey)`.

**Acceptance.**
- `ls teams/{KEY}/recent/` lists issue-identifier symlinks, no flags, newest-first by `updatedAt`.
- Ordering is produced by an explicit fs-layer `sort.Slice`; **verified by seeding distinct, non-insertion-order timestamps so fs output is DESC by resolved-target lstat mtime** (a missing sort fails the test). *(Reworded from the untestable "would pass if SQL order were reversed.")*
- `recent/{ID}` resolves to `../issues/{ID}/`; `cat recent/{ID}/issue.md` reads the issue.
- Read-only: mkdir/write returns an error (`EROFS`/`EACCES`/`ENOTSUP`).
- Capped at 50; `recent/` appears in `ls teams/{KEY}/`; unknown children → `ENOENT`.
- No `ListRecentTeamIssues` added.
- `make build` and `make test` pass; the new fixture test passes.

**Out of scope.** Any write capability on `recent/`; a new SQL query/repo method; SQL-side LIMIT/pagination; cross-team/workspace-wide recent; filtering by state/assignee/label (that's `by/`); `.last` (T1) and `my/created/`; ordering by `createdAt`. **Known staleness bound:** like `my/created`/`by/` views, a just-created issue may not appear in `recent/` until the dir's attr/entry timeout expires unless create tails also `InvalidateKernelInode` the recent dir — documented, not fixed here.

**Test plan.** Fixture mode, `issue148_test.go`: `TestIssue148_RecentViewOrdered` — pre-fix assert `recentPath` absent/errors; post-fix assert ReadDir succeeds, each entry is a symlink whose `Readlink` starts `../issues/`, one resolves and its `issue.md` reads, and lstat mtimes are non-increasing given the distinct-timestamp fixtures. Assert `recent` appears in the team-dir listing. Assert `os.Mkdir(recentPath+"/foo")` errors. Run `go test -v ./internal/integration/ -run Issue148` and `make test`.

---

### [T6] Contract conformance — purely additive generalized tests + the live agent-loop

- **Effort:** M
- **Dependencies:** T1, T2, T3, T4 (and T0 for fixture-mode success coverage)

**Problem.** After T1–T4 land, the unified contract needs a single executable statement and an end-to-end agent-loop. **The per-surface characterization inversions and fixture required-field relocations are the red-green partner of the production changes and are owned by their producing tasks** (T1 owns `CreateHandsBackNoIdentifier`; T2 owns `EditableFileColocates` + `read_test.go`/`fixture_read_test.go` issue fields; T3 owns `initiatives_test.go`/`fixture_extended_test.go`/`fixture_read_test.go` project section). This keeps the tree green at every merge and removes double-ownership. T6 is therefore **purely additive**: the generalized cross-entity conformance test and the live agent-loop.

**Solution.**

*Tier A — fixture-mode structural conformance (`make test`):*
1. `TestWriteContractMetaSplitGeneralizes` (claude_tools_test.go): for `issue.md`, `project.md`, `initiative.md` — the editable file carries no volatile server fields (`id/slug/url/status/updated/created`) and a sibling `.meta` exists carrying them. The executable form of "the rule is general, not issue-only." Uses the fixture project (`PopulateProject`) and initiative (`PopulateInitiative`).
2. `TestWriteContractLastSidecarShape`: `.last` on a collection is mode 0444 and parses as a YAML list of `{identifier,url,path,title,status}` (empty list acceptable pre-create — relies on T1 mounting `.last` present-and-empty).

*Tier B — behavioral conformance (fixture via T0's fake, and/or live `skipIfNoWriteTests`):* `TestWriteContractAgentLoop`:
- (a) create N≥2 issues via N sequential `issues/_create` writes with a shared unique title marker; read `.last`, assert every marker's identifier appears with a reachable `issue.md` path. Match by marker, not list length (the append-log is shared).
- (b) no-op rewrite one created `issue.md` (fsync+close), re-read, assert the frontmatter block is byte-identical (no `updated:` churn) and `.error` empty.
- (c) same byte-stability no-op-rewrite on `project.md` and `initiative.md`.
- (d) force one `EINVAL` (unresolvable status on `_create`), assert the reason lands on `.error` while `.last` still holds the earlier successes — the create-scoped append log survives a subsequent failure.

**File touchpoints.**
- `internal/integration/claude_tools_test.go`: add `TestWriteContractMetaSplitGeneralizes`, `TestWriteContractLastSidecarShape`, and `TestWriteContractAgentLoop`.
- `internal/integration/helpers_test.go`: add path builders (`issueMetaPath`, `projectDirPath`/`projectFilePath`/`projectMetaPath`, `initiativeFilePath`/`initiativeMetaPath`); `parseLastSidecar(content) []struct{Identifier,URL,Path,Title,Status}`; `assertEditableOnly(t, path, forbidden...)` reusing `parseFrontmatter`; a `.meta` parser (reuses `parseFrontmatter` since `.meta` is a YAML frontmatter block per T2).

**Acceptance.**
- Fixture mode (`make test`): `TestWriteContractMetaSplitGeneralizes` and `TestWriteContractLastSidecarShape` pass. (The per-surface receipt inversions and required-field relocations are already green because they shipped inside T1/T2/T3.)
- With T0's fake (fixture) and/or live: `TestWriteContractAgentLoop` (a)–(d) pass.
- No production code (`internal/fs`, `internal/marshal`, API client) is modified by this task — test/helper-only.
- `make test` is green with no new skips beyond the pre-existing live-only pattern; live-mode tests skip cleanly without the env vars.

**Out of scope.** `recent/` (T5) — not a dependency; per-surface receipt inversions and fixture required-field relocations (owned by T1/T2/T3); introducing a mutation seam (that's T0); dropped surfaces (#152 create-in-context, #154 `_bulk`); the slug-vs-title receipt `TestIssue148_TypedNameNeqResultingPath` (design doesn't change slug addressing); unit tests inside `internal/fs`/`internal/marshal` (owned by T1–T4).

**Test plan.** Fixture: `TestWriteContractMetaSplitGeneralizes` (editable-only + sibling `.meta` for all three entities), `TestWriteContractLastSidecarShape` (0444 + YAML list). Behavioral: `TestWriteContractAgentLoop` under T0's fake and/or `LINEARFS_WRITE_TESTS=1`. Regression: full live suite to confirm the helper switch to sourcing identity from `.last`/`.meta` keeps `TestReadYourWritesLargeBody`, the project/initiative edit-persistence tests, and the #142 fsync/atomic-rename tests green; confirm `make test` green.

---

## 4. Build Order

The declared dependency graph is acyclic; the corrected ordering keeps the tree green at every merge by **folding each characterization inversion and fixture required-field relocation into the production task that causes the breakage** (not into a downstream T6).

1. **T0 — fixture-mode mock mutation client** (dep: none). Land first so the *success* half of the contract (populated `.last`, byte-stability across a successful write, associations set) is provable in `make test`. Also the natural home to review the fixture seam pattern T5 extends.
2. **T1 — `.last` sidecar** (dep: T0). Hard build dep of T4. Owns the `CreateHandsBackNoIdentifier` inversion and mounts `.last` present-and-empty. Green with T0's fake proving the populated path.
3. **T2 — `issue.meta` split** (dep: none). Owns `EditableFileColocates` inversion + `read_test.go`/`fixture_read_test.go` issue-field relocations + the `ensureContent`/attachment-variadic build fixes. **Exposes the reusable DIRECT_IO `lookupMetaFile` + `MetaFileNode`** that T3 consumes.
4. **T3 — `project.meta`/`initiative.meta`** (dep: T2). Consumes T2's helper; owns the `initiatives_test.go`/`fixture_extended_test.go`/`fixture_read_test.go` project/initiative relocations and wires the tolerant parsers into Flush.
5. **T4 — full-object `issues/_create`** (dep: T1 hard; T2 sequencing/schema-coherence). Single-document only. Converges `mkdir`/`_create` through `createIssueFromSpec` while preserving EAGAIN + `.error` text.
6. **T5 — `recent/` view** (dep: none; introduces the `WithUpdatedAt`/`WithCreatedAt` fixture seam). Can run in parallel from the start; only needs the fixture timestamp seam it adds.
7. **T6 — additive conformance + live agent-loop** (dep: T1–T4, T0). Lands last, purely additive: the generalized cross-entity meta-split test, the `.last` shape test, and the end-to-end agent-loop.

Because every red-green receipt inversion ships inside its producing task, there is no intermediate merge state where the tree is red.

---

## 5. What We Deliberately Did NOT Do — and Why

- **No `_bulk` manifest reconciler (#154 dropped).** It is RPC-in-a-file: needs transaction/rollback machinery Linear can't back (`WithTx` wraps only SQLite; no batch mutation) and partial-failure semantics that fight the eventually-consistent sync worker. Its results value is covered by list-shaped `.last`; its round-trip value by N sequential atomic `_create` calls, each self-reporting — strictly better per-item retry.
- **No multi-document batch inside `_create`.** The `---`-splitter collides with legitimate markdown horizontal rules and body lines, and "body may not contain `---`" silently corrupts bodies — violating the loud-failure premise. One write = one entity stays file-shaped; batch is sequential atomic creates.
- **No create verbs on filter/association views (#152 dropped as filed).** `by/status`, `by/label`, `by/assignee` are read-only symlink views by design (born-Done? unassigned? labels aren't containers). Association-at-create is frontmatter on `issues/_create`. `children/` mkdir remains the sole path-based association precedent; no `projects/{slug}/_create` sugar (frontmatter `project:` covers it).
- **No `.last` on the edit tail.** `.last` is a create-scoped append log; edits report success via read-your-writes, not `.last`. `commitWriteBack` is untouched — this keeps the create/edit roles clean.
- **No `.last` for `attachments/_create` / `relations/_create` yet.** Their identity is a URL/relation string, lower value; explicitly deferred as an acknowledged gap so the "every writable surface" claim stays honest — a follow-up pass, not a silent omission.
- **No collection-file `.meta` (comments/docs).** They carry few volatile fields and would cost extra inodes per item; deferred.
- **No writable `startDate`/`targetDate` (project) or `targetDate` (initiative).** The Linear API input structs reject them; they stay in read-only `.meta`.
- **No `ListRecentTeamIssues` SQL/repo method.** Redundant with the existing `GetTeamIssues` (already `ORDER BY updated_at DESC`); ordering is guaranteed by an explicit fs-layer `sort.Slice`, and the cap lives in the fs layer for now.
- **No change to slug-vs-title addressing.** `TestIssue148_TypedNameNeqResultingPath` stays asserting current behavior; `.last`'s `path` field surfaces the real on-disk name so an agent never has to guess the slug.
- **No RPC endpoint dressed as a file.** The retrospective's premise — FS-as-API is a good fit — is preserved: single-document `_create` mirrors the existing `comments/_create`, `.last` is a log-file idiom, `.meta` is a sibling read-only view, and every new seam reuses existing plumbing (errorfile, `MetaFileNode`, `resolveIssueUpdate`) rather than new machinery.