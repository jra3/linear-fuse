-- name: GetIssueByID :one
SELECT * FROM issues WHERE id = ?;

-- name: GetIssueByIdentifier :one
SELECT * FROM issues WHERE identifier = ?;

-- name: ListTeamIssues :many
SELECT * FROM issues WHERE team_id = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByState :many
SELECT * FROM issues WHERE team_id = ? AND state_id = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByAssignee :many
SELECT * FROM issues WHERE team_id = ? AND assignee_id = ? ORDER BY updated_at DESC;

-- name: ListTeamUnassignedIssues :many
SELECT * FROM issues WHERE team_id = ? AND assignee_id IS NULL ORDER BY updated_at DESC;

-- name: ListTeamIssuesByParent :many
SELECT * FROM issues WHERE parent_id = ? ORDER BY updated_at DESC;

-- name: SetIssueParent :exec
UPDATE issues SET parent_id = ? WHERE id = ?;

-- name: ListUserAssignedIssues :many
SELECT * FROM issues WHERE assignee_id = ? ORDER BY updated_at DESC;

-- name: ListUserActiveIssues :many
SELECT * FROM issues WHERE assignee_id = ? AND state_type NOT IN ('completed', 'canceled') ORDER BY updated_at DESC;

-- name: ListUserCreatedIssues :many
SELECT * FROM issues WHERE creator_id = ? ORDER BY updated_at DESC;

-- name: ListProjectIssues :many
SELECT * FROM issues WHERE project_id = ? ORDER BY updated_at DESC;

-- name: ListCycleIssues :many
SELECT * FROM issues WHERE cycle_id = ? ORDER BY updated_at DESC;

-- name: UpsertIssue :exec
-- detail_synced_at is deliberately absent from the column list and the
-- conflict SET clause: NULL on insert, preserved on every sync upsert. The
-- stamp is owned by the detail-sync paths (StampIssueDetailSynced), never
-- by the entity upsert.
INSERT INTO issues (
    id, identifier, team_id, title, description,
    state_id, state_name, state_type,
    assignee_id, assignee_email, creator_id, creator_email, priority,
    project_id, project_name, cycle_id, cycle_name,
    parent_id, due_date, estimate, url, branch_name,
    created_at, updated_at, started_at, completed_at, canceled_at, archived_at,
    synced_at, data
) VALUES (
    ?, ?, ?, ?, ?,
    ?, ?, ?,
    ?, ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?,
    ?, ?
) ON CONFLICT(id) DO UPDATE SET
    identifier = excluded.identifier,
    team_id = excluded.team_id,
    title = excluded.title,
    description = excluded.description,
    state_id = excluded.state_id,
    state_name = excluded.state_name,
    state_type = excluded.state_type,
    assignee_id = excluded.assignee_id,
    assignee_email = excluded.assignee_email,
    creator_id = excluded.creator_id,
    creator_email = excluded.creator_email,
    priority = excluded.priority,
    project_id = excluded.project_id,
    project_name = excluded.project_name,
    cycle_id = excluded.cycle_id,
    cycle_name = excluded.cycle_name,
    parent_id = excluded.parent_id,
    due_date = excluded.due_date,
    estimate = excluded.estimate,
    url = excluded.url,
    branch_name = excluded.branch_name,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    started_at = excluded.started_at,
    completed_at = excluded.completed_at,
    canceled_at = excluded.canceled_at,
    archived_at = excluded.archived_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteIssue :exec
DELETE FROM issues WHERE id = ?;

-- name: GetTeamIssueCount :one
SELECT COUNT(*) FROM issues WHERE team_id = ?;

-- name: GetLatestTeamIssueUpdatedAt :one
SELECT MAX(updated_at) FROM issues WHERE team_id = ?;

-- Sync metadata queries

-- name: GetSyncMeta :one
SELECT * FROM sync_meta WHERE team_id = ?;

-- name: UpsertSyncMeta :exec
INSERT INTO sync_meta (team_id, last_synced_at, last_issue_updated_at, issue_count)
VALUES (?, ?, ?, ?)
ON CONFLICT(team_id) DO UPDATE SET
    last_synced_at = excluded.last_synced_at,
    last_issue_updated_at = excluded.last_issue_updated_at,
    issue_count = excluded.issue_count;

-- Sync schedule queries

-- name: GetSyncSchedule :one
SELECT last_run FROM sync_schedule WHERE key = ?;

-- name: UpsertSyncSchedule :exec
INSERT INTO sync_schedule (key, last_run)
VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET last_run = excluded.last_run;

-- Teams queries

-- name: ListTeams :many
SELECT * FROM teams ORDER BY name;

-- name: UpsertTeam :exec
INSERT INTO teams (id, key, name, icon, created_at, updated_at, synced_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    key = excluded.key,
    name = excluded.name,
    icon = excluded.icon,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at;

-- Full-text search queries are handled with raw SQL (FTS5 not supported by sqlc)
-- See internal/db/search.go for FTS implementation

-- Bulk operations for sync

-- name: GetIssueUpdatedAt :one
SELECT updated_at FROM issues WHERE id = ?;

-- name: StampIssueDetailSynced :exec
-- The one per-issue detail-freshness fact: set clean-gated by syncDetails
-- (worker) and refreshIssueDetails (repo SWR path) after every detail family
-- persisted without error.
UPDATE issues SET detail_synced_at = ? WHERE id = ?;

-- name: GetIssueDetailFreshness :one
-- Both inputs of the issue-details staleness decision in one fetch:
-- stale iff detail_synced_at is NULL or updated_at > detail_synced_at.
SELECT updated_at, detail_synced_at FROM issues WHERE id = ?;

-- name: DeleteIssueHistoryCache :exec
DELETE FROM issue_history_cache WHERE issue_id = ?;

-- name: ListTeamIssueIDs :many
SELECT id, updated_at FROM issues WHERE team_id = ? ORDER BY updated_at DESC;

-- Label-based queries (labels stored in JSON data column)
-- These require extracting from JSON - keeping simple queries here,
-- complex label queries will be done in Go code

-- =============================================================================
-- States queries
-- =============================================================================

-- name: GetState :one
SELECT * FROM states WHERE id = ?;

-- name: GetStateByName :one
SELECT * FROM states WHERE team_id = ? AND name = ?;

-- name: ListTeamStates :many
SELECT * FROM states WHERE team_id = ? ORDER BY position;

-- name: UpsertState :exec
INSERT INTO states (id, team_id, name, type, color, position, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    team_id = excluded.team_id,
    name = excluded.name,
    type = excluded.type,
    color = excluded.color,
    position = excluded.position,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- =============================================================================
-- Labels queries
-- =============================================================================

-- name: GetLabel :one
SELECT * FROM labels WHERE id = ?;

-- name: GetLabelByName :one
SELECT * FROM labels WHERE (team_id = ? OR team_id IS NULL) AND name = ?;

-- name: ListTeamLabels :many
SELECT * FROM labels WHERE team_id = ? OR team_id IS NULL ORDER BY name;

-- name: UpsertLabel :exec
INSERT INTO labels (id, team_id, name, color, description, parent_id, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    team_id = excluded.team_id,
    name = excluded.name,
    color = excluded.color,
    description = excluded.description,
    parent_id = excluded.parent_id,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteLabel :exec
DELETE FROM labels WHERE id = ?;

-- =============================================================================
-- Project labels queries (workspace-scoped catalog; see schema.sql)
-- =============================================================================

-- name: ListProjectLabels :many
SELECT * FROM project_labels ORDER BY name COLLATE NOCASE;

-- name: UpsertProjectLabel :exec
INSERT INTO project_labels (id, name, color, description, is_group, parent_id,
    retired_at, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    color = excluded.color,
    description = excluded.description,
    is_group = excluded.is_group,
    parent_id = excluded.parent_id,
    retired_at = excluded.retired_at,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- Workspace-wide prune, licensed ONLY by a complete drain of Query.projectLabels.
-- The drain includes retired labels (live-verified 2026-07-08), so retirement
-- never reads as removal here; only true deletion/archival does.
-- name: PruneProjectLabels :exec
DELETE FROM project_labels WHERE synced_at < ?;

-- =============================================================================
-- Users queries
-- =============================================================================

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: ListUsers :many
SELECT * FROM users WHERE active = 1 ORDER BY name;

-- name: UpsertUser :exec
INSERT INTO users (id, email, name, display_name, avatar_url, active, admin, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    email = excluded.email,
    name = excluded.name,
    display_name = excluded.display_name,
    avatar_url = excluded.avatar_url,
    active = excluded.active,
    admin = excluded.admin,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- =============================================================================
-- Team Members queries
-- =============================================================================

-- name: ListTeamMembers :many
SELECT u.* FROM users u
JOIN team_members tm ON u.id = tm.user_id
WHERE tm.team_id = ?
ORDER BY u.name;

-- name: UpsertTeamMember :exec
INSERT INTO team_members (team_id, user_id, synced_at)
VALUES (?, ?, ?)
ON CONFLICT(team_id, user_id) DO UPDATE SET
    synced_at = excluded.synced_at;

-- =============================================================================
-- Cycles queries
-- =============================================================================

-- name: ListTeamCycles :many
SELECT * FROM cycles WHERE team_id = ? ORDER BY number DESC;

-- name: UpsertCycle :exec
INSERT INTO cycles (id, team_id, number, name, description, starts_at, ends_at, completed_at, progress, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    team_id = excluded.team_id,
    number = excluded.number,
    name = excluded.name,
    description = excluded.description,
    starts_at = excluded.starts_at,
    ends_at = excluded.ends_at,
    completed_at = excluded.completed_at,
    progress = excluded.progress,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- =============================================================================
-- Projects queries
-- =============================================================================

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY name;

-- name: ListTeamProjects :many
SELECT p.* FROM projects p
JOIN project_teams pt ON p.id = pt.project_id
WHERE pt.team_id = ?
ORDER BY p.name;

-- name: UpsertProject :exec
INSERT INTO projects (id, slug_id, name, description, icon, color, state, progress, start_date, target_date, lead_id, url, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    slug_id = excluded.slug_id,
    name = excluded.name,
    description = excluded.description,
    icon = excluded.icon,
    color = excluded.color,
    state = excluded.state,
    progress = excluded.progress,
    start_date = excluded.start_date,
    target_date = excluded.target_date,
    lead_id = excluded.lead_id,
    url = excluded.url,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ?;

-- =============================================================================
-- Project-Team associations
-- =============================================================================

-- name: UpsertProjectTeam :exec
INSERT INTO project_teams (project_id, team_id, synced_at)
VALUES (?, ?, ?)
ON CONFLICT(project_id, team_id) DO UPDATE SET
    synced_at = excluded.synced_at;

-- Delete a team's associations the metadata sync no longer sees. Only safe
-- against a provably complete (drained) projects fetch, with the cutoff
-- taken before the sync's upserts, so rows written mid-sync survive.
-- name: PruneProjectTeams :exec
DELETE FROM project_teams WHERE team_id = ? AND synced_at < ?;

-- Prune the team metadata rows the drained (complete) metadata fetch no
-- longer returned: renamed or deleted labels, cycles, and departed members.
-- Same contract as PruneProjectTeams, only safe against a complete fetch with
-- the cutoff taken before the sync upserts. A label's team_id follows its own
-- team, so workspace labels are stored team_id=NULL and sit outside this
-- team-scoped prune entirely (only genuine team labels are removed here).
-- name: PruneTeamLabels :exec
DELETE FROM labels WHERE team_id = ? AND synced_at < ?;

-- name: PruneTeamCycles :exec
DELETE FROM cycles WHERE team_id = ? AND synced_at < ?;

-- name: PruneTeamMembers :exec
DELETE FROM team_members WHERE team_id = ? AND synced_at < ?;

-- name: DeleteProjectTeams :exec
DELETE FROM project_teams WHERE project_id = ?;

-- name: GetProjectPrimaryTeamKey :one
-- The canonical team for a project that spans teams: first by key order.
-- This is the one place that rule lives; symlink targets and any future
-- "which team dir hosts this project" consumer must go through it.
SELECT t.key FROM teams t
JOIN project_teams pt ON t.id = pt.team_id
WHERE pt.project_id = ?
ORDER BY t.key
LIMIT 1;

-- =============================================================================
-- Project Milestones queries
-- =============================================================================

-- name: GetProjectMilestone :one
SELECT * FROM project_milestones WHERE id = ?;

-- name: ListProjectMilestones :many
SELECT * FROM project_milestones WHERE project_id = ? ORDER BY sort_order;

-- name: UpsertProjectMilestone :exec
INSERT INTO project_milestones (id, project_id, name, description, target_date, sort_order, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    project_id = excluded.project_id,
    name = excluded.name,
    description = excluded.description,
    target_date = excluded.target_date,
    sort_order = excluded.sort_order,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteProjectMilestone :exec
DELETE FROM project_milestones WHERE id = ?;

-- name: DeleteProjectMilestones :exec
DELETE FROM project_milestones WHERE project_id = ?;

-- =============================================================================
-- Comments queries
-- =============================================================================

-- name: ListIssueComments :many
SELECT * FROM comments WHERE issue_id = ? ORDER BY created_at;

-- name: UpsertComment :exec
INSERT INTO comments (id, issue_id, body, body_data, user_id, user_name, user_email, edited_at, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    issue_id = excluded.issue_id,
    body = excluded.body,
    body_data = excluded.body_data,
    user_id = excluded.user_id,
    user_name = excluded.user_name,
    user_email = excluded.user_email,
    edited_at = excluded.edited_at,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteComment :exec
DELETE FROM comments WHERE id = ?;

-- name: DeleteIssueComments :exec
DELETE FROM comments WHERE issue_id = ?;

-- Prune*: delete rows the details sync no longer sees for an issue. Scoped by
-- issue and a synced_at cutoff taken before the sync's upserts, so only rows
-- the fresh fetch did NOT touch are removed (e.g. a comment deleted in Linear,
-- or a phantom left by a delete whose SQLite forget failed).
-- name: PruneIssueComments :exec
DELETE FROM comments WHERE issue_id = ? AND synced_at < ?;

-- =============================================================================
-- Documents queries
-- =============================================================================

-- name: ListIssueDocuments :many
SELECT * FROM documents WHERE issue_id = ? ORDER BY title;

-- name: ListProjectDocuments :many
SELECT * FROM documents WHERE project_id = ? ORDER BY title;

-- name: UpsertDocument :exec
INSERT INTO documents (id, slug_id, title, icon, color, content, content_data, issue_id, project_id, initiative_id, team_id, creator_id, url, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    slug_id = excluded.slug_id,
    title = excluded.title,
    icon = excluded.icon,
    color = excluded.color,
    content = excluded.content,
    content_data = excluded.content_data,
    issue_id = excluded.issue_id,
    project_id = excluded.project_id,
    initiative_id = excluded.initiative_id,
    team_id = excluded.team_id,
    creator_id = excluded.creator_id,
    url = excluded.url,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteDocument :exec
DELETE FROM documents WHERE id = ?;

-- name: PruneIssueDocuments :exec
DELETE FROM documents WHERE issue_id = ? AND synced_at < ?;

-- name: DeleteIssueDocuments :exec
DELETE FROM documents WHERE issue_id = ?;

-- name: DeleteProjectDocuments :exec
DELETE FROM documents WHERE project_id = ?;

-- name: GetProjectDocumentsSyncedAt :one
SELECT MAX(synced_at) FROM documents WHERE project_id = ?;

-- name: ListInitiativeDocuments :many
SELECT * FROM documents WHERE initiative_id = ? ORDER BY title;

-- name: DeleteInitiativeDocuments :exec
DELETE FROM documents WHERE initiative_id = ?;

-- name: GetInitiativeDocumentsSyncedAt :one
SELECT MAX(synced_at) FROM documents WHERE initiative_id = ?;

-- name: ListTeamDocuments :many
SELECT * FROM documents WHERE team_id = ? ORDER BY title;

-- name: DeleteTeamDocuments :exec
DELETE FROM documents WHERE team_id = ?;

-- name: GetTeamDocumentsSyncedAt :one
SELECT MAX(synced_at) FROM documents WHERE team_id = ?;

-- =============================================================================
-- Initiatives queries
-- =============================================================================

-- name: GetInitiative :one
SELECT * FROM initiatives WHERE id = ?;

-- name: ListInitiatives :many
SELECT * FROM initiatives ORDER BY sort_order, name;

-- name: UpsertInitiative :exec
INSERT INTO initiatives (id, slug_id, name, description, icon, color, status, sort_order, target_date, owner_id, url, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    slug_id = excluded.slug_id,
    name = excluded.name,
    description = excluded.description,
    icon = excluded.icon,
    color = excluded.color,
    status = excluded.status,
    sort_order = excluded.sort_order,
    target_date = excluded.target_date,
    owner_id = excluded.owner_id,
    url = excluded.url,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteInitiative :exec
DELETE FROM initiatives WHERE id = ?;

-- =============================================================================
-- Initiative-Project associations
-- =============================================================================

-- name: UpsertInitiativeProject :exec
INSERT INTO initiative_projects (initiative_id, project_id, synced_at)
VALUES (?, ?, ?)
ON CONFLICT(initiative_id, project_id) DO UPDATE SET
    synced_at = excluded.synced_at;

-- Delete an initiative's junction rows the workspace sync no longer sees.
-- Only safe against a provably complete (drained) initiative projects
-- fetch, with the cutoff taken before the sync's upserts: a link the user
-- creates mid-sync (persistInitiativeProjectLink stamps a fresh synced_at)
-- survives.
-- name: PruneInitiativeProjects :exec
DELETE FROM initiative_projects WHERE initiative_id = ? AND synced_at < ?;

-- name: DeleteInitiativeProject :exec
DELETE FROM initiative_projects WHERE initiative_id = ? AND project_id = ?;

-- name: DeleteInitiativeProjects :exec
DELETE FROM initiative_projects WHERE initiative_id = ?;

-- name: DeleteInitiativeProjectsByProject :exec
DELETE FROM initiative_projects WHERE project_id = ?;

-- =============================================================================
-- Project Updates queries
-- =============================================================================

-- name: ListProjectUpdates :many
SELECT * FROM project_updates WHERE project_id = ? ORDER BY created_at DESC;

-- name: UpsertProjectUpdate :exec
INSERT INTO project_updates (id, project_id, body, body_data, health, user_id, user_name, url, edited_at, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    project_id = excluded.project_id,
    body = excluded.body,
    body_data = excluded.body_data,
    health = excluded.health,
    user_id = excluded.user_id,
    user_name = excluded.user_name,
    url = excluded.url,
    edited_at = excluded.edited_at,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteProjectUpdates :exec
DELETE FROM project_updates WHERE project_id = ?;

-- name: GetProjectUpdatesSyncedAt :one
SELECT MAX(synced_at) FROM project_updates WHERE project_id = ?;

-- =============================================================================
-- Initiative Updates queries
-- =============================================================================

-- name: ListInitiativeUpdates :many
SELECT * FROM initiative_updates WHERE initiative_id = ? ORDER BY created_at DESC;

-- name: UpsertInitiativeUpdate :exec
INSERT INTO initiative_updates (id, initiative_id, body, body_data, health, user_id, user_name, url, edited_at, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    initiative_id = excluded.initiative_id,
    body = excluded.body,
    body_data = excluded.body_data,
    health = excluded.health,
    user_id = excluded.user_id,
    user_name = excluded.user_name,
    url = excluded.url,
    edited_at = excluded.edited_at,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteInitiativeUpdates :exec
DELETE FROM initiative_updates WHERE initiative_id = ?;

-- name: GetInitiativeUpdatesSyncedAt :one
SELECT MAX(synced_at) FROM initiative_updates WHERE initiative_id = ?;

-- =============================================================================
-- Attachments queries (external links: GitHub PRs, Slack, etc.)
-- =============================================================================

-- name: ListIssueAttachments :many
-- The id tiebreaker keeps the order deterministic on equal created_at, so
-- attachmentListing dedup suffixes stay stable across calls.
SELECT * FROM attachments WHERE issue_id = ? ORDER BY created_at, id;

-- name: UpsertAttachment :exec
INSERT INTO attachments (id, issue_id, title, subtitle, url, source_type, metadata, creator_id, creator_name, creator_email, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    issue_id = excluded.issue_id,
    title = excluded.title,
    subtitle = excluded.subtitle,
    url = excluded.url,
    source_type = excluded.source_type,
    metadata = excluded.metadata,
    creator_id = excluded.creator_id,
    creator_name = excluded.creator_name,
    creator_email = excluded.creator_email,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteAttachment :exec
DELETE FROM attachments WHERE id = ?;

-- name: PruneIssueAttachments :exec
DELETE FROM attachments WHERE issue_id = ? AND synced_at < ?;

-- name: DeleteIssueAttachments :exec
DELETE FROM attachments WHERE issue_id = ?;

-- =============================================================================
-- Entity external links queries (project/initiative "Links / Resources")
-- =============================================================================

-- name: ListProjectLinks :many
-- The id tiebreaker keeps the order deterministic on equal sort_order, so
-- linkListing dedup suffixes stay stable across calls.
SELECT * FROM entity_external_links WHERE project_id = ? ORDER BY sort_order, id;

-- name: ListInitiativeLinks :many
SELECT * FROM entity_external_links WHERE initiative_id = ? ORDER BY sort_order, id;

-- name: GetProjectLinksSyncedAt :one
SELECT MAX(synced_at) FROM entity_external_links WHERE project_id = ?;

-- name: GetInitiativeLinksSyncedAt :one
SELECT MAX(synced_at) FROM entity_external_links WHERE initiative_id = ?;

-- name: UpsertEntityExternalLink :exec
INSERT INTO entity_external_links (id, project_id, initiative_id, label, url, sort_order, creator_id, creator_name, creator_email, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    project_id = excluded.project_id,
    initiative_id = excluded.initiative_id,
    label = excluded.label,
    url = excluded.url,
    sort_order = excluded.sort_order,
    creator_id = excluded.creator_id,
    creator_name = excluded.creator_name,
    creator_email = excluded.creator_email,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteEntityExternalLink :exec
DELETE FROM entity_external_links WHERE id = ?;

-- name: DeleteProjectLinks :exec
DELETE FROM entity_external_links WHERE project_id = ?;

-- name: DeleteInitiativeLinks :exec
DELETE FROM entity_external_links WHERE initiative_id = ?;

-- =============================================================================
-- Embedded Files queries (images, PDFs from Linear CDN)
-- =============================================================================

-- name: ListIssueEmbeddedFiles :many
-- The id tiebreaker keeps the order deterministic on equal filenames (the
-- dedup case), so which duplicate gets the (2) suffix stays stable.
SELECT * FROM embedded_files WHERE issue_id = ? ORDER BY filename, id;

-- name: UpsertEmbeddedFile :exec
INSERT INTO embedded_files (id, issue_id, url, filename, mime_type, file_size, cache_path, source, created_at, synced_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    issue_id = excluded.issue_id,
    url = excluded.url,
    filename = excluded.filename,
    mime_type = excluded.mime_type,
    file_size = COALESCE(excluded.file_size, embedded_files.file_size),
    cache_path = COALESCE(excluded.cache_path, embedded_files.cache_path),
    source = excluded.source,
    synced_at = excluded.synced_at;

-- name: UpdateEmbeddedFileCache :exec
UPDATE embedded_files SET cache_path = ?, file_size = ? WHERE id = ?;

-- name: DeleteIssueEmbeddedFiles :exec
DELETE FROM embedded_files WHERE issue_id = ?;

-- =============================================================================
-- Issue Relations queries
-- =============================================================================

-- name: ListIssueRelations :many
SELECT * FROM issue_relations WHERE issue_id = ? ORDER BY type, related_issue_id;

-- name: ListIssueInverseRelations :many
SELECT * FROM issue_relations WHERE related_issue_id = ? ORDER BY type, issue_id;

-- name: UpsertIssueRelation :exec
INSERT INTO issue_relations (id, issue_id, related_issue_id, type, created_at, updated_at, synced_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    issue_id = excluded.issue_id,
    related_issue_id = excluded.related_issue_id,
    type = excluded.type,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at;

-- name: DeleteIssueRelation :exec
DELETE FROM issue_relations WHERE id = ?;

-- name: DeleteIssueRelations :exec
DELETE FROM issue_relations WHERE issue_id = ?;

-- name: PruneIssueRelations :exec
-- Scoped to the OWNING issue (issue_id): only the owning side's drained
-- fetch is a completeness set for its rows. Inverse upserts refresh rows
-- owned by other issues and must never license their deletion.
DELETE FROM issue_relations WHERE issue_id = ? AND synced_at < ?;

-- =============================================================================
-- Issue History Cache
-- =============================================================================

-- name: UpsertIssueHistoryCache :exec
INSERT INTO issue_history_cache (issue_id, synced_at, data)
VALUES (?, ?, ?)
ON CONFLICT(issue_id) DO UPDATE SET
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: GetIssueHistoryCache :one
SELECT issue_id, synced_at, data FROM issue_history_cache WHERE issue_id = ?;

-- =============================================================================
-- Viewer Cache
-- =============================================================================

-- name: GetViewerUserID :one
SELECT user_id FROM viewer_cache LIMIT 1;

-- name: SetViewerUserID :exec
INSERT INTO viewer_cache (singleton, user_id, synced_at)
VALUES (1, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET
    user_id = excluded.user_id,
    synced_at = excluded.synced_at;

-- =============================================================================
-- Pending Detail Sync Queue
-- =============================================================================

-- name: UpsertPendingDetailSync :exec
INSERT INTO pending_detail_sync (issue_id, identifier, queued_at)
VALUES (?, ?, ?)
ON CONFLICT(issue_id) DO UPDATE SET queued_at = excluded.queued_at;

-- name: DeletePendingDetailSync :exec
DELETE FROM pending_detail_sync WHERE issue_id = ?;

-- name: ListPendingDetailSync :many
SELECT issue_id, identifier FROM pending_detail_sync ORDER BY queued_at;

-- name: CountPendingDetailSync :one
SELECT COUNT(*) FROM pending_detail_sync;