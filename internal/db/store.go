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
	connStr := "file:" + escapedPath + "?_time_format=sqlite"
	db, err := sql.Open("sqlite", connStr)
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

// ListIssuesByLabel returns issues that have a specific label
// Labels are stored in the JSON data column as {"labels": {"nodes": [...]}}
func (s *Store) ListIssuesByLabel(ctx context.Context, teamID, labelName string) ([]Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT * FROM issues
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
			&i.SyncedAt, &i.Data,
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
