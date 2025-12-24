-- Issues table: stores all issue data
-- Key fields are columns for efficient querying, full data stored as JSON
CREATE TABLE IF NOT EXISTS issues (
    id TEXT PRIMARY KEY,
    identifier TEXT UNIQUE NOT NULL,
    team_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    state_id TEXT,
    state_name TEXT,
    state_type TEXT,
    assignee_id TEXT,
    assignee_email TEXT,
    creator_id TEXT,
    creator_email TEXT,
    priority INTEGER DEFAULT 0,
    project_id TEXT,
    project_name TEXT,
    cycle_id TEXT,
    cycle_name TEXT,
    parent_id TEXT,
    due_date TEXT,
    estimate REAL,
    url TEXT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL  -- Full issue JSON for complex fields (labels, children, etc.)
);

CREATE INDEX IF NOT EXISTS idx_issues_team ON issues(team_id);
CREATE INDEX IF NOT EXISTS idx_issues_identifier ON issues(identifier);
CREATE INDEX IF NOT EXISTS idx_issues_updated ON issues(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_issues_state ON issues(team_id, state_id);
CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues(team_id, assignee_id);
CREATE INDEX IF NOT EXISTS idx_issues_creator ON issues(creator_id);
CREATE INDEX IF NOT EXISTS idx_issues_project ON issues(project_id);
CREATE INDEX IF NOT EXISTS idx_issues_cycle ON issues(cycle_id);
CREATE INDEX IF NOT EXISTS idx_issues_parent ON issues(parent_id);

-- Sync metadata: tracks last sync time per team
CREATE TABLE IF NOT EXISTS sync_meta (
    team_id TEXT PRIMARY KEY,
    last_synced_at DATETIME NOT NULL,
    last_issue_updated_at DATETIME,  -- Max updatedAt we've seen
    issue_count INTEGER DEFAULT 0
);

-- Teams table: cache team info
CREATE TABLE IF NOT EXISTS teams (
    id TEXT PRIMARY KEY,
    key TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    icon TEXT,
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL
);

-- Full-text search virtual table
CREATE VIRTUAL TABLE IF NOT EXISTS issues_fts USING fts5(
    identifier,
    title,
    description,
    content='issues',
    content_rowid='rowid'
);

-- Triggers to keep FTS in sync
CREATE TRIGGER IF NOT EXISTS issues_ai AFTER INSERT ON issues BEGIN
    INSERT INTO issues_fts(rowid, identifier, title, description)
    VALUES (NEW.rowid, NEW.identifier, NEW.title, NEW.description);
END;

CREATE TRIGGER IF NOT EXISTS issues_ad AFTER DELETE ON issues BEGIN
    INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description)
    VALUES('delete', OLD.rowid, OLD.identifier, OLD.title, OLD.description);
END;

CREATE TRIGGER IF NOT EXISTS issues_au AFTER UPDATE ON issues BEGIN
    INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description)
    VALUES('delete', OLD.rowid, OLD.identifier, OLD.title, OLD.description);
    INSERT INTO issues_fts(rowid, identifier, title, description)
    VALUES (NEW.rowid, NEW.identifier, NEW.title, NEW.description);
END;

-- =============================================================================
-- Workflow States (per team)
-- =============================================================================
CREATE TABLE IF NOT EXISTS states (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    name TEXT NOT NULL,
    type TEXT NOT NULL,  -- backlog, unstarted, started, completed, canceled
    color TEXT,
    position REAL,
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_states_team ON states(team_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_states_team_name ON states(team_id, name);

-- =============================================================================
-- Labels (per team or workspace-wide)
-- =============================================================================
CREATE TABLE IF NOT EXISTS labels (
    id TEXT PRIMARY KEY,
    team_id TEXT,  -- NULL for workspace labels
    name TEXT NOT NULL,
    color TEXT,
    description TEXT,
    parent_id TEXT,  -- For nested labels
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_labels_team ON labels(team_id);
CREATE INDEX IF NOT EXISTS idx_labels_name ON labels(name);

-- =============================================================================
-- Users (workspace members)
-- =============================================================================
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    display_name TEXT,
    avatar_url TEXT,
    active INTEGER NOT NULL DEFAULT 1,
    admin INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_active ON users(active);

-- =============================================================================
-- Team Memberships (M2M: teams <-> users)
-- =============================================================================
CREATE TABLE IF NOT EXISTS team_members (
    team_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    synced_at DATETIME NOT NULL,
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_team_members_user ON team_members(user_id);

-- =============================================================================
-- Cycles (sprints per team)
-- =============================================================================
CREATE TABLE IF NOT EXISTS cycles (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    number INTEGER NOT NULL,
    name TEXT,
    description TEXT,
    starts_at DATETIME,
    ends_at DATETIME,
    completed_at DATETIME,
    progress REAL,
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_cycles_team ON cycles(team_id);
CREATE INDEX IF NOT EXISTS idx_cycles_dates ON cycles(starts_at, ends_at);

-- =============================================================================
-- Projects
-- =============================================================================
CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    slug_id TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    icon TEXT,
    color TEXT,
    state TEXT,  -- planned, started, paused, completed, canceled
    progress REAL,
    start_date TEXT,
    target_date TEXT,
    lead_id TEXT,
    url TEXT,
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_projects_slug ON projects(slug_id);
CREATE INDEX IF NOT EXISTS idx_projects_state ON projects(state);
CREATE INDEX IF NOT EXISTS idx_projects_lead ON projects(lead_id);

-- =============================================================================
-- Project-Team Associations (M2M: projects <-> teams)
-- =============================================================================
CREATE TABLE IF NOT EXISTS project_teams (
    project_id TEXT NOT NULL,
    team_id TEXT NOT NULL,
    synced_at DATETIME NOT NULL,
    PRIMARY KEY (project_id, team_id)
);

CREATE INDEX IF NOT EXISTS idx_project_teams_team ON project_teams(team_id);

-- =============================================================================
-- Project Milestones
-- =============================================================================
CREATE TABLE IF NOT EXISTS project_milestones (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    target_date TEXT,
    sort_order REAL,
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_milestones_project ON project_milestones(project_id);

-- =============================================================================
-- Comments (per issue)
-- =============================================================================
CREATE TABLE IF NOT EXISTS comments (
    id TEXT PRIMARY KEY,
    issue_id TEXT NOT NULL,
    body TEXT NOT NULL,
    body_data TEXT,  -- ProseMirror JSON
    user_id TEXT,
    user_name TEXT,
    user_email TEXT,
    edited_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_id);
CREATE INDEX IF NOT EXISTS idx_comments_user ON comments(user_id);
CREATE INDEX IF NOT EXISTS idx_comments_created ON comments(issue_id, created_at);

-- =============================================================================
-- Documents (attached to issues, projects, or standalone)
-- =============================================================================
CREATE TABLE IF NOT EXISTS documents (
    id TEXT PRIMARY KEY,
    slug_id TEXT UNIQUE NOT NULL,
    title TEXT NOT NULL,
    icon TEXT,
    color TEXT,
    content TEXT,
    content_data TEXT,  -- ProseMirror JSON
    issue_id TEXT,
    project_id TEXT,
    creator_id TEXT,
    url TEXT,
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_documents_slug ON documents(slug_id);
CREATE INDEX IF NOT EXISTS idx_documents_issue ON documents(issue_id);
CREATE INDEX IF NOT EXISTS idx_documents_project ON documents(project_id);
CREATE INDEX IF NOT EXISTS idx_documents_creator ON documents(creator_id);

-- =============================================================================
-- Initiatives
-- =============================================================================
CREATE TABLE IF NOT EXISTS initiatives (
    id TEXT PRIMARY KEY,
    slug_id TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    icon TEXT,
    color TEXT,
    status TEXT,
    sort_order REAL,
    target_date TEXT,
    owner_id TEXT,
    url TEXT,
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_initiatives_slug ON initiatives(slug_id);
CREATE INDEX IF NOT EXISTS idx_initiatives_owner ON initiatives(owner_id);

-- =============================================================================
-- Initiative-Project Associations (M2M: initiatives <-> projects)
-- =============================================================================
CREATE TABLE IF NOT EXISTS initiative_projects (
    initiative_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    synced_at DATETIME NOT NULL,
    PRIMARY KEY (initiative_id, project_id)
);

CREATE INDEX IF NOT EXISTS idx_initiative_projects_project ON initiative_projects(project_id);

-- =============================================================================
-- Project Updates (status updates)
-- =============================================================================
CREATE TABLE IF NOT EXISTS project_updates (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    body TEXT NOT NULL,
    body_data TEXT,  -- ProseMirror JSON
    health TEXT,  -- onTrack, atRisk, offTrack
    user_id TEXT,
    user_name TEXT,
    url TEXT,
    edited_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_project_updates_project ON project_updates(project_id);
CREATE INDEX IF NOT EXISTS idx_project_updates_created ON project_updates(project_id, created_at DESC);

-- =============================================================================
-- Initiative Updates (status updates)
-- =============================================================================
CREATE TABLE IF NOT EXISTS initiative_updates (
    id TEXT PRIMARY KEY,
    initiative_id TEXT NOT NULL,
    body TEXT NOT NULL,
    body_data TEXT,  -- ProseMirror JSON
    health TEXT,  -- onTrack, atRisk, offTrack
    user_id TEXT,
    user_name TEXT,
    url TEXT,
    edited_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_initiative_updates_initiative ON initiative_updates(initiative_id);
CREATE INDEX IF NOT EXISTS idx_initiative_updates_created ON initiative_updates(initiative_id, created_at DESC);

-- =============================================================================
-- Attachments (external links: GitHub PRs, Slack, etc.)
-- =============================================================================
CREATE TABLE IF NOT EXISTS attachments (
    id TEXT PRIMARY KEY,
    issue_id TEXT NOT NULL,
    title TEXT NOT NULL,
    subtitle TEXT,
    url TEXT NOT NULL,
    source_type TEXT,  -- github, slack, zendesk, etc.
    metadata JSON,
    creator_id TEXT,
    creator_name TEXT,
    creator_email TEXT,
    created_at DATETIME,
    updated_at DATETIME,
    synced_at DATETIME NOT NULL,
    data JSON NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_attachments_issue ON attachments(issue_id);
CREATE INDEX IF NOT EXISTS idx_attachments_url ON attachments(url);

-- =============================================================================
-- Embedded Files (images, PDFs uploaded to Linear CDN)
-- =============================================================================
CREATE TABLE IF NOT EXISTS embedded_files (
    id TEXT PRIMARY KEY,        -- SHA256 hash of URL
    issue_id TEXT NOT NULL,
    url TEXT NOT NULL UNIQUE,   -- Linear CDN URL
    filename TEXT NOT NULL,     -- Derived filename
    mime_type TEXT,
    file_size INTEGER,          -- Bytes (NULL until downloaded)
    cache_path TEXT,            -- Local filesystem path when cached
    source TEXT NOT NULL,       -- "description" or "comment:{id}"
    created_at DATETIME NOT NULL,
    synced_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_embedded_files_issue ON embedded_files(issue_id);
CREATE INDEX IF NOT EXISTS idx_embedded_files_cached ON embedded_files(cache_path) WHERE cache_path IS NOT NULL;
