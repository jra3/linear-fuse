package repo

import (
	"database/sql"
	"errors"
	"fmt"
)

// queryOne owns the single-row read contract shared by every Get*By* getter —
// the repo-side sibling of the fs package's resolveByName/freshestByID
// collapses. The contract, previously restated at 17 sites: not-found is
// (nil, nil) — the read path's "no such entity", never an error — any other
// fetch failure wraps with the op label, and the row converts through
// convert, whose own error propagates unwrapped. Deviants stay hand-rolled:
// GetIssuesByLabel maps not-found to an empty slice (list semantics) and
// GetProjectPrimaryTeamKey returns a bare string.
func queryOne[R, T any](op string, fetch func() (R, error), convert func(R) (T, error)) (*T, error) {
	row, err := fetch()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	result, err := convert(row)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// pure adapts an error-free converter to queryOne's convert shape.
func pure[R, T any](convert func(R) T) func(R) (T, error) {
	return func(row R) (T, error) { return convert(row), nil }
}
