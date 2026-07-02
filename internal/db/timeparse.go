package db

import "time"

// SQLite and Linear's API disagree on time formats: the driver is configured
// with _time_format=sqlite, which returns space-separated timestamps that make
// a bare time.Parse(time.RFC3339, s) fail silently. These helpers are the one
// place that knows every format a timestamp can arrive in; repo and sync both
// parse through them (each used to carry its own copy of the format list).

// sqliteTimeFormats lists every layout a timestamp can arrive in - RFC3339
// from the API, space-separated variants from SQLite.
var sqliteTimeFormats = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00", // SQLite format with timezone
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05", // SQLite format without timezone
}

// ParseSQLiteTime parses a time string from SQLite or the API, trying every
// known format. Returns the zero time when nothing matches.
func ParseSQLiteTime(s string) time.Time {
	for _, layout := range sqliteTimeFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ParseSQLiteTimeAny converts an interface{} from SQLite (typically a MAX()/
// MIN() aggregate, which sqlc types as interface{}) to time.Time. Returns the
// zero time for nil (no rows) and unrecognized types/formats.
func ParseSQLiteTimeAny(v interface{}) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		return ParseSQLiteTime(t)
	default:
		return time.Time{}
	}
}
