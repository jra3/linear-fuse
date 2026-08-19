package db

// Schedule keys that more than one package writes.
//
// sync_schedule is a generic key/value table of last-run instants, and most of
// its keys belong to the single package that owns them (the full-cycle stamp
// and the projects-probe watermark are internal/sync's). This one is written
// from two places — the sync worker's full-cycle metadata drain and the
// repository's read-path label refresh — and internal/sync and internal/repo
// deliberately do not import one another, so the factory lives here, where
// both already depend on it.

// scheduleKeyTeamLabelsPrefix keys one team's label-catalog freshness stamp:
// "labels_catalog:<teamID>", last_run = when the catalog was last drained
// cleanly for that team.
const scheduleKeyTeamLabelsPrefix = "labels_catalog:"

// TeamLabelsScheduleKey returns the label-catalog freshness key for one team.
//
// The stamp is deliberately NOT derived from the labels rows themselves. A
// team's catalog is served as its own labels PLUS the workspace labels
// (team_id NULL), and those workspace rows are shared: an aggregate over that
// union lets a refresh triggered by team A re-stamp the shared rows and so
// declare team B fresh, suppressing the very refresh #475 exists to fire. The
// same rows also cannot record a catalog that is legitimately EMPTY — a fetch
// returning zero labels upserts nothing, so a row-derived signal reads
// "never synced" forever and re-fires on every browse (the per-browse API
// loop detail_synced_at was introduced to escape). A stamp per team, written
// by whoever drained it, has neither hole.
func TeamLabelsScheduleKey(teamID string) string {
	return scheduleKeyTeamLabelsPrefix + teamID
}
