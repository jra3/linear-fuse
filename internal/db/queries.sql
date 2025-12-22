-- name: GetIssueByID :one
SELECT * FROM issues WHERE id = ?;

-- name: GetIssueByIdentifier :one
SELECT * FROM issues WHERE identifier = ?;

-- name: ListTeamIssues :many
SELECT * FROM issues WHERE team_id = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByState :many
SELECT * FROM issues WHERE team_id = ? AND state_id = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByStateName :many
SELECT * FROM issues WHERE team_id = ? AND state_name = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByStateType :many
SELECT * FROM issues WHERE team_id = ? AND state_type = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByAssignee :many
SELECT * FROM issues WHERE team_id = ? AND assignee_id = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByAssigneeEmail :many
SELECT * FROM issues WHERE team_id = ? AND assignee_email = ? ORDER BY updated_at DESC;

-- name: ListTeamUnassignedIssues :many
SELECT * FROM issues WHERE team_id = ? AND assignee_id IS NULL ORDER BY updated_at DESC;

-- name: ListTeamIssuesByPriority :many
SELECT * FROM issues WHERE team_id = ? AND priority = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByProject :many
SELECT * FROM issues WHERE team_id = ? AND project_id = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByProjectName :many
SELECT * FROM issues WHERE team_id = ? AND project_name = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByCycle :many
SELECT * FROM issues WHERE team_id = ? AND cycle_id = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByCycleName :many
SELECT * FROM issues WHERE team_id = ? AND cycle_name = ? ORDER BY updated_at DESC;

-- name: ListTeamIssuesByParent :many
SELECT * FROM issues WHERE parent_id = ? ORDER BY updated_at DESC;

-- name: ListUserAssignedIssues :many
SELECT * FROM issues WHERE assignee_id = ? ORDER BY updated_at DESC;

-- name: ListUserAssignedIssuesByEmail :many
SELECT * FROM issues WHERE assignee_email = ? ORDER BY updated_at DESC;

-- name: ListUserActiveIssues :many
SELECT * FROM issues WHERE assignee_id = ? AND state_type NOT IN ('completed', 'canceled') ORDER BY updated_at DESC;

-- name: ListProjectIssues :many
SELECT * FROM issues WHERE project_id = ? ORDER BY updated_at DESC;

-- name: ListCycleIssues :many
SELECT * FROM issues WHERE cycle_id = ? ORDER BY updated_at DESC;

-- name: UpsertIssue :exec
INSERT INTO issues (
    id, identifier, team_id, title, description,
    state_id, state_name, state_type,
    assignee_id, assignee_email, priority,
    project_id, project_name, cycle_id, cycle_name,
    parent_id, due_date, estimate, url,
    created_at, updated_at, synced_at, data
) VALUES (
    ?, ?, ?, ?, ?,
    ?, ?, ?,
    ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?
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
    priority = excluded.priority,
    project_id = excluded.project_id,
    project_name = excluded.project_name,
    cycle_id = excluded.cycle_id,
    cycle_name = excluded.cycle_name,
    parent_id = excluded.parent_id,
    due_date = excluded.due_date,
    estimate = excluded.estimate,
    url = excluded.url,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteIssue :exec
DELETE FROM issues WHERE id = ?;

-- name: DeleteIssueByIdentifier :exec
DELETE FROM issues WHERE identifier = ?;

-- name: GetTeamIssueCount :one
SELECT COUNT(*) FROM issues WHERE team_id = ?;

-- name: GetTotalIssueCount :one
SELECT COUNT(*) FROM issues;

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

-- name: ListSyncMeta :many
SELECT * FROM sync_meta;

-- Teams queries

-- name: GetTeam :one
SELECT * FROM teams WHERE id = ?;

-- name: GetTeamByKey :one
SELECT * FROM teams WHERE key = ?;

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

-- name: ListTeamIssueIDs :many
SELECT id, updated_at FROM issues WHERE team_id = ? ORDER BY updated_at DESC;

-- name: DeleteTeamIssues :exec
DELETE FROM issues WHERE team_id = ?;

-- Label-based queries (labels stored in JSON data column)
-- These require extracting from JSON - keeping simple queries here,
-- complex label queries will be done in Go code

-- name: ListAllIdentifiers :many
SELECT identifier, team_id FROM issues ORDER BY identifier;

-- name: ListTeamIdentifiers :many
SELECT identifier FROM issues WHERE team_id = ? ORDER BY identifier;

-- =============================================================================
-- States queries
-- =============================================================================

-- name: GetState :one
SELECT * FROM states WHERE id = ?;

-- name: GetStateByName :one
SELECT * FROM states WHERE team_id = ? AND name = ?;

-- name: ListTeamStates :many
SELECT * FROM states WHERE team_id = ? ORDER BY position;

-- name: ListTeamStatesByType :many
SELECT * FROM states WHERE team_id = ? AND type = ? ORDER BY position;

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

-- name: DeleteState :exec
DELETE FROM states WHERE id = ?;

-- name: DeleteTeamStates :exec
DELETE FROM states WHERE team_id = ?;

-- =============================================================================
-- Labels queries
-- =============================================================================

-- name: GetLabel :one
SELECT * FROM labels WHERE id = ?;

-- name: GetLabelByName :one
SELECT * FROM labels WHERE (team_id = ? OR team_id IS NULL) AND name = ?;

-- name: ListTeamLabels :many
SELECT * FROM labels WHERE team_id = ? OR team_id IS NULL ORDER BY name;

-- name: ListWorkspaceLabels :many
SELECT * FROM labels WHERE team_id IS NULL ORDER BY name;

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

-- name: DeleteTeamLabels :exec
DELETE FROM labels WHERE team_id = ?;

-- =============================================================================
-- Users queries
-- =============================================================================

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ?;

-- name: ListUsers :many
SELECT * FROM users WHERE active = 1 ORDER BY name;

-- name: ListAllUsers :many
SELECT * FROM users ORDER BY name;

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

-- name: DeleteUser :exec
DELETE FROM users WHERE id = ?;

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

-- name: DeleteTeamMember :exec
DELETE FROM team_members WHERE team_id = ? AND user_id = ?;

-- name: DeleteTeamMembers :exec
DELETE FROM team_members WHERE team_id = ?;

-- =============================================================================
-- Cycles queries
-- =============================================================================

-- name: GetCycle :one
SELECT * FROM cycles WHERE id = ?;

-- name: GetCycleByNumber :one
SELECT * FROM cycles WHERE team_id = ? AND number = ?;

-- name: GetCycleByName :one
SELECT * FROM cycles WHERE team_id = ? AND name = ?;

-- name: ListTeamCycles :many
SELECT * FROM cycles WHERE team_id = ? ORDER BY number DESC;

-- name: ListTeamActiveCycles :many
SELECT * FROM cycles WHERE team_id = ? AND ends_at > datetime('now') ORDER BY starts_at;

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

-- name: DeleteCycle :exec
DELETE FROM cycles WHERE id = ?;

-- name: DeleteTeamCycles :exec
DELETE FROM cycles WHERE team_id = ?;

-- =============================================================================
-- Projects queries
-- =============================================================================

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE slug_id = ?;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY name;

-- name: ListProjectsByState :many
SELECT * FROM projects WHERE state = ? ORDER BY name;

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

-- name: DeleteProjectTeam :exec
DELETE FROM project_teams WHERE project_id = ? AND team_id = ?;

-- name: DeleteProjectTeams :exec
DELETE FROM project_teams WHERE project_id = ?;

-- name: ListProjectTeamIDs :many
SELECT team_id FROM project_teams WHERE project_id = ?;

-- =============================================================================
-- Project Milestones queries
-- =============================================================================

-- name: GetProjectMilestone :one
SELECT * FROM project_milestones WHERE id = ?;

-- name: ListProjectMilestones :many
SELECT * FROM project_milestones WHERE project_id = ? ORDER BY sort_order;

-- name: GetMilestoneByName :one
SELECT * FROM project_milestones WHERE project_id = ? AND name = ?;

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

-- name: GetComment :one
SELECT * FROM comments WHERE id = ?;

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

-- name: GetIssueCommentsSyncedAt :one
SELECT MAX(synced_at) FROM comments WHERE issue_id = ?;

-- =============================================================================
-- Documents queries
-- =============================================================================

-- name: GetDocument :one
SELECT * FROM documents WHERE id = ?;

-- name: GetDocumentBySlug :one
SELECT * FROM documents WHERE slug_id = ?;

-- name: ListIssueDocuments :many
SELECT * FROM documents WHERE issue_id = ? ORDER BY title;

-- name: ListProjectDocuments :many
SELECT * FROM documents WHERE project_id = ? ORDER BY title;

-- name: UpsertDocument :exec
INSERT INTO documents (id, slug_id, title, icon, color, content, content_data, issue_id, project_id, creator_id, url, created_at, updated_at, synced_at, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    slug_id = excluded.slug_id,
    title = excluded.title,
    icon = excluded.icon,
    color = excluded.color,
    content = excluded.content,
    content_data = excluded.content_data,
    issue_id = excluded.issue_id,
    project_id = excluded.project_id,
    creator_id = excluded.creator_id,
    url = excluded.url,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    synced_at = excluded.synced_at,
    data = excluded.data;

-- name: DeleteDocument :exec
DELETE FROM documents WHERE id = ?;

-- name: DeleteIssueDocuments :exec
DELETE FROM documents WHERE issue_id = ?;

-- name: DeleteProjectDocuments :exec
DELETE FROM documents WHERE project_id = ?;

-- name: GetIssueDocumentsSyncedAt :one
SELECT MAX(synced_at) FROM documents WHERE issue_id = ?;

-- name: GetProjectDocumentsSyncedAt :one
SELECT MAX(synced_at) FROM documents WHERE project_id = ?;

-- =============================================================================
-- Initiatives queries
-- =============================================================================

-- name: GetInitiative :one
SELECT * FROM initiatives WHERE id = ?;

-- name: GetInitiativeBySlug :one
SELECT * FROM initiatives WHERE slug_id = ?;

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

-- name: DeleteInitiativeProject :exec
DELETE FROM initiative_projects WHERE initiative_id = ? AND project_id = ?;

-- name: DeleteInitiativeProjects :exec
DELETE FROM initiative_projects WHERE initiative_id = ?;

-- name: ListInitiativeProjectIDs :many
SELECT project_id FROM initiative_projects WHERE initiative_id = ?;

-- name: ListInitiativeProjects :many
SELECT p.* FROM projects p
JOIN initiative_projects ip ON p.id = ip.project_id
WHERE ip.initiative_id = ?
ORDER BY p.name;

-- name: ListProjectInitiativeIDs :many
SELECT initiative_id FROM initiative_projects WHERE project_id = ?;

-- =============================================================================
-- Project Updates queries
-- =============================================================================

-- name: GetProjectUpdate :one
SELECT * FROM project_updates WHERE id = ?;

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

-- name: DeleteProjectUpdate :exec
DELETE FROM project_updates WHERE id = ?;

-- name: DeleteProjectUpdates :exec
DELETE FROM project_updates WHERE project_id = ?;

-- name: GetProjectUpdatesSyncedAt :one
SELECT MAX(synced_at) FROM project_updates WHERE project_id = ?;

-- =============================================================================
-- Initiative Updates queries
-- =============================================================================

-- name: GetInitiativeUpdate :one
SELECT * FROM initiative_updates WHERE id = ?;

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

-- name: DeleteInitiativeUpdate :exec
DELETE FROM initiative_updates WHERE id = ?;

-- name: DeleteInitiativeUpdates :exec
DELETE FROM initiative_updates WHERE initiative_id = ?;

-- name: GetInitiativeUpdatesSyncedAt :one
SELECT MAX(synced_at) FROM initiative_updates WHERE initiative_id = ?;
