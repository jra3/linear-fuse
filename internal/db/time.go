package db

import "time"

// SQLite time formats. The database is opened with _time_format=sqlite (see
// openDB), so the driver returns space-separated timestamps ("2006-01-02
// 15:04:05...") instead of RFC3339's 'T' separator. time.Parse(time.RFC3339, s)
// fails silently on those, so every read of a stored timestamp must try this
// ordered list of layouts. This is the single home of that invariant: it lives
// beside the driver config that causes it.
var sqliteTimeFormats = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00", // SQLite format with timezone
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05", // SQLite format without timezone
}

// ParseTime converts a value read from SQLite into a time.Time. It accepts the
// several shapes a timestamp can arrive as: a nil (no row / never synced), an
// already-parsed time.Time, or a string in any of the sqliteTimeFormats layouts
// (the common case, and what MAX()/MIN() aggregates return as interface{}).
// Anything it cannot interpret — nil, an unparseable string, an unexpected type —
// yields the zero time, which callers read as "never".
func ParseTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		for _, layout := range sqliteTimeFormats {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}
