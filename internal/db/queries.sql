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
