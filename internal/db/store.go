package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Open opens or creates a SQLite database at the given path
func Open(dbPath string) (*Store, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable WAL mode for better concurrent access
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Initialize schema
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}

	return &Store{
		db:      db,
		queries: New(db),
	}, nil
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
	defer tx.Rollback()

	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}

	return tx.Commit()
}

// SearchIssues performs full-text search on issues
// This uses raw SQL since sqlc doesn't support FTS5
func (s *Store) SearchIssues(ctx context.Context, query string) ([]Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT issues.* FROM issues
		JOIN issues_fts ON issues.rowid = issues_fts.rowid
		WHERE issues_fts MATCH ?
		ORDER BY rank
	`, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanIssues(rows)
}

// SearchTeamIssues performs full-text search on issues within a team
func (s *Store) SearchTeamIssues(ctx context.Context, query string, teamID string) ([]Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT issues.* FROM issues
		JOIN issues_fts ON issues.rowid = issues_fts.rowid
		WHERE issues_fts MATCH ? AND issues.team_id = ?
		ORDER BY rank
	`, query, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanIssues(rows)
}

// ListIssuesByLabel returns issues that have a specific label
// Labels are stored in the JSON data column
func (s *Store) ListIssuesByLabel(ctx context.Context, teamID, labelName string) ([]Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT * FROM issues
		WHERE team_id = ?
		AND EXISTS (
			SELECT 1 FROM json_each(json_extract(data, '$.labels'))
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
			&i.AssigneeID, &i.AssigneeEmail, &i.Priority,
			&i.ProjectID, &i.ProjectName, &i.CycleID, &i.CycleName,
			&i.ParentID, &i.DueDate, &i.Estimate, &i.Url,
			&i.CreatedAt, &i.UpdatedAt, &i.SyncedAt, &i.Data,
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
	Priority      int
	ProjectID     *string
	ProjectName   *string
	CycleID       *string
	CycleName     *string
	ParentID      *string
	DueDate       *string
	Estimate      *float64
	URL           *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
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
		Priority:      sql.NullInt64{Int64: int64(d.Priority), Valid: true},
		ProjectID:     toNullString(d.ProjectID),
		ProjectName:   toNullString(d.ProjectName),
		CycleID:       toNullString(d.CycleID),
		CycleName:     toNullString(d.CycleName),
		ParentID:      toNullString(d.ParentID),
		DueDate:       toNullString(d.DueDate),
		Estimate:      toNullFloat64(d.Estimate),
		Url:           toNullString(d.URL),
		CreatedAt:     d.CreatedAt,
		UpdatedAt:     d.UpdatedAt,
		SyncedAt:      time.Now(),
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

// DefaultDBPath returns the default database path
func DefaultDBPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.Getenv("HOME")
	}
	return filepath.Join(configDir, "linearfs", "cache.db")
}
