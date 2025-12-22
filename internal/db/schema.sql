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
