package db

import (
	"testing"
	"time"
)

// timeparseSeedCorpus seeds the no-panic target with one example of each of the
// 7 layouts ParseSQLiteTime knows, plus garbage the parser must reject cleanly
// (return the zero time, never panic).
var timeparseSeedCorpus = []string{
	"2026-01-02T15:04:05Z",                  // RFC3339
	"2026-01-02T15:04:05.017Z",              // RFC3339Nano
	"2026-01-02 15:04:05.999999999-07:00",   // SQLite w/ tz, nanos
	"2026-01-02 15:04:05.999999999Z07:00",   // SQLite w/ Z-form tz, nanos
	"2026-01-02 15:04:05-07:00",             // SQLite w/ tz
	"2026-01-02 15:04:05Z07:00",             // SQLite Z-form tz
	"2026-01-02 15:04:05",                   // SQLite no tz
	"",                                      // empty
	"not a time",                            // garbage
	"2026-13-45 99:99:99",                   // out-of-range fields
	"9999999999999999999999999999999999999", // overflow-ish
	"\x00\x00\x00",                          // NUL bytes
	"0000-00-00 00:00:00",                   // zero-ish
}

// FuzzParseSQLiteTimeNoPanic asserts ParseSQLiteTime survives arbitrary input:
// it parses these strings straight out of SQLite and the API, so the contract is
// "a time or the zero time, never a panic."
func FuzzParseSQLiteTimeNoPanic(f *testing.F) {
	for _, s := range timeparseSeedCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = ParseSQLiteTime(s) // contract: no panic
	})
}

// FuzzParseSQLiteTimeRoundTrip asserts the string-fixpoint property per layout:
// for a time built from clamped components, formatting it with a layout and
// re-parsing must format back to the identical string. This is a STRING fixpoint,
// not a time-equality one — a layout that drops the zone or sub-seconds still
// passes, because we compare the re-formatted string to the original formatted
// string under the same layout.
func FuzzParseSQLiteTimeRoundTrip(f *testing.F) {
	f.Add(int64(1767366245), int64(17000000), 0)     // 2026-01-02T15:04:05.017Z, UTC
	f.Add(int64(0), int64(0), 0)                     // epoch
	f.Add(int64(4102444800), int64(999999999), -420) // 2100, -07:00
	f.Add(int64(1000000000), int64(500000000), 330)  // +05:30 (half-hour zone)
	f.Fuzz(func(t *testing.T, sec, nsec int64, offMin int) {
		// Clamp components to the ranges a real timestamp inhabits.
		if sec < 0 {
			sec = -sec
		}
		sec %= 4102444801 // [0, 4102444800] -> ~1970..2100
		if nsec < 0 {
			nsec = -nsec
		}
		nsec %= 1_000_000_000 // [0, 1e9)
		// offMin into [-840, 840] (UTC-14 .. UTC+14).
		if offMin < 0 {
			offMin = -offMin
		}
		offMin = offMin%1681 - 840 // [-840, 840]

		zone := time.FixedZone("fuzz", offMin*60)
		base := time.Unix(sec, nsec).In(zone)

		for _, layout := range sqliteTimeFormats {
			s := base.Format(layout)
			got := ParseSQLiteTime(s)
			if got.Format(layout) != s {
				t.Fatalf("string-fixpoint broken for layout %q:\n formatted=%q\n reparsed =%q",
					layout, s, got.Format(layout))
			}
		}
	})
}
