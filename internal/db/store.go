package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Store wraps database operations for linear-fuse
type Store struct {
	db      *sql.DB
	queries *Queries
}

// Open opens or creates a SQLite database at the given path.
// If the existing database has an incompatible schema, it is deleted and recreated.
func Open(dbPath string) (*Store, error) {
	store, err := openDB(dbPath)
	if err != nil {
		// Check if this is a schema error (e.g., missing column)
		if strings.Contains(err.Error(), "no such column") ||
			strings.Contains(err.Error(), "no such table") ||
			strings.Contains(err.Error(), "SQL logic error") {
			// Schema mismatch - delete and recreate
			if removeErr := os.Remove(dbPath); removeErr != nil && !os.IsNotExist(removeErr) {
				return nil, fmt.Errorf("remove incompatible cache: %w", removeErr)
			}
			// Also remove WAL and SHM files
			os.Remove(dbPath + "-wal")
			os.Remove(dbPath + "-shm")
			// Retry with fresh database
			return openDB(dbPath)
		}
		return nil, err
	}
	return store, nil
}

// openDB is the internal function that opens the database
func openDB(dbPath string) (*Store, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	// Use file: URI format to properly handle paths with spaces and query params
	// Escape spaces in path for URI format
	escapedPath := strings.ReplaceAll(dbPath, " ", "%20")
	// The pragmas ride the DSN because database/sql pools connections: a
	// `db.Exec("PRAGMA …")` runs on one pooled connection and leaves the rest
	// unconfigured. busy_timeout in particular must cover every connection —
	// without it a write that races the sync worker fails instantly with
	// SQLITE_BUSY (a delete's forget losing that race left a phantom row that
	// resurrected the deleted file). journal_mode=WAL is persistent per
	// database but is harmless to re-apply per connection.
	connStr := "file:" + escapedPath + "?_time_format=sqlite" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Initialize schema
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}

	// Migrate pre-existing databases (CREATE TABLE IF NOT EXISTS leaves an
	// old table untouched, so new columns need an explicit ALTER).
	if err := migrateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return &Store{
		db:      db,
		queries: New(db),
	}, nil
}

// migrateSchema applies idempotent bootstrap-ALTER migrations to a database
// created by an older schema.sql. This is the project's first migration and
// the precedent for the next one: probe the column via PRAGMA table_info,
// ALTER TABLE ADD COLUMN if missing. A numbered user_version framework was
// deliberately rejected as framework-building for one column — extract one
// when full columnization needs it.
func migrateSchema(db *sql.DB) error {
	hasColumn, err := tableHasColumn(db, "issues", "detail_synced_at")
	if err != nil {
		return err
	}
	if !hasColumn {
		if _, err := db.Exec("ALTER TABLE issues ADD COLUMN detail_synced_at DATETIME"); err != nil {
			return fmt.Errorf("add issues.detail_synced_at: %w", err)
		}
	}
	return nil
}

// tableHasColumn reports whether table already has the named column.
func tableHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("table_info %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// Queries returns the sqlc queries interface
func (s *Store) Queries() *Queries {
	return s.queries
}

// DB returns the underlying database connection for raw queries
func (s *Store) DB() *sql.DB {
	return s.db
}

// WithTx executes a function within a transaction
func (s *Store) WithTx(ctx context.Context, fn func(*Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}

	return tx.Commit()
}

// ListIssuesByLabel returns issues that have a specific label
// Labels are stored in the JSON data column as {"labels": {"nodes": [...]}}
// The column list is explicit (not SELECT *) because a migrated database has
// detail_synced_at appended at the END of the table (ALTER TABLE ADD COLUMN),
// while a fresh one has it in schema.sql order — positional scanning over *
// would misalign on one of them.
func (s *Store) ListIssuesByLabel(ctx context.Context, teamID, labelName string) ([]Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, identifier, team_id, title, description,
			state_id, state_name, state_type,
			assignee_id, assignee_email, creator_id, creator_email, priority,
			project_id, project_name, cycle_id, cycle_name,
			parent_id, due_date, estimate, url, branch_name,
			created_at, updated_at, started_at, completed_at, canceled_at, archived_at,
			synced_at, detail_synced_at, data
		FROM issues
		WHERE team_id = ?
		AND EXISTS (
			SELECT 1 FROM json_each(json_extract(data, '$.labels.nodes'))
			WHERE json_extract(value, '$.name') = ?
		)
		ORDER BY updated_at DESC
	`, teamID, labelName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanIssues(rows)
}

// scanIssues scans rows into Issue structs
func scanIssues(rows *sql.Rows) ([]Issue, error) {
	var issues []Issue
	for rows.Next() {
		var i Issue
		if err := rows.Scan(
			&i.ID, &i.Identifier, &i.TeamID, &i.Title, &i.Description,
			&i.StateID, &i.StateName, &i.StateType,
			&i.AssigneeID, &i.AssigneeEmail, &i.CreatorID, &i.CreatorEmail, &i.Priority,
			&i.ProjectID, &i.ProjectName, &i.CycleID, &i.CycleName,
			&i.ParentID, &i.DueDate, &i.Estimate, &i.Url, &i.BranchName,
			&i.CreatedAt, &i.UpdatedAt, &i.StartedAt, &i.CompletedAt, &i.CanceledAt, &i.ArchivedAt,
			&i.SyncedAt, &i.DetailSyncedAt, &i.Data,
		); err != nil {
			return nil, err
		}
		issues = append(issues, i)
	}
	return issues, rows.Err()
}

// UpsertIssueParams creates parameters for UpsertIssue from an api.Issue-like structure
// This is a convenience function for use with the sync worker
type IssueData struct {
	ID            string
	Identifier    string
	TeamID        string
	Title         string
	Description   *string
	StateID       *string
	StateName     *string
	StateType     *string
	AssigneeID    *string
	AssigneeEmail *string
	CreatorID     *string
	CreatorEmail  *string
	Priority      int
	ProjectID     *string
	ProjectName   *string
	CycleID       *string
	CycleName     *string
	ParentID      *string
	DueDate       *string
	Estimate      *float64
	URL           *string
	BranchName    *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
	CanceledAt    *time.Time
	ArchivedAt    *time.Time
	Data          json.RawMessage
}

// ToUpsertParams converts IssueData to UpsertIssueParams
func (d *IssueData) ToUpsertParams() UpsertIssueParams {
	return UpsertIssueParams{
		ID:            d.ID,
		Identifier:    d.Identifier,
		TeamID:        d.TeamID,
		Title:         d.Title,
		Description:   toNullString(d.Description),
		StateID:       toNullString(d.StateID),
		StateName:     toNullString(d.StateName),
		StateType:     toNullString(d.StateType),
		AssigneeID:    toNullString(d.AssigneeID),
		AssigneeEmail: toNullString(d.AssigneeEmail),
		CreatorID:     toNullString(d.CreatorID),
		CreatorEmail:  toNullString(d.CreatorEmail),
		Priority:      sql.NullInt64{Int64: int64(d.Priority), Valid: true},
		ProjectID:     toNullString(d.ProjectID),
		ProjectName:   toNullString(d.ProjectName),
		CycleID:       toNullString(d.CycleID),
		CycleName:     toNullString(d.CycleName),
		ParentID:      toNullString(d.ParentID),
		DueDate:       toNullString(d.DueDate),
		Estimate:      toNullFloat64(d.Estimate),
		Url:           toNullString(d.URL),
		BranchName:    toNullString(d.BranchName),
		CreatedAt:     d.CreatedAt,
		UpdatedAt:     d.UpdatedAt,
		StartedAt:     toNullTimePtr(d.StartedAt),
		CompletedAt:   toNullTimePtr(d.CompletedAt),
		CanceledAt:    toNullTimePtr(d.CanceledAt),
		ArchivedAt:    toNullTimePtr(d.ArchivedAt),
		SyncedAt:      Now(),
		Data:          d.Data,
	}
}

func toNullString(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func toNullFloat64(f *float64) sql.NullFloat64 {
	if f == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *f, Valid: true}
}

func toNullTimePtr(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

// ToNullTime converts a time.Time to sql.NullTime
func ToNullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// Now returns the current time formatted for SQLite storage.
// It uses UTC and strips the monotonic clock reading to produce
// clean RFC3339 timestamps that SQLite datetime functions understand.
func Now() time.Time {
	return time.Now().UTC().Round(0)
}

// ToNullInt64 converts an int64 to sql.NullInt64
func ToNullInt64(i int64) sql.NullInt64 {
	return sql.NullInt64{Int64: i, Valid: true}
}

// DefaultDBPath returns the default database path
func DefaultDBPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.Getenv("HOME")
	}
	return filepath.Join(configDir, "linearfs", "cache.db")
}
