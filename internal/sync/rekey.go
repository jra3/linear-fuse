package sync

import (
	"context"
	"fmt"
	"log"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// An issue's identifier is "<teamKey>-<number>", minted server-side. LinearFS
// caches it as a column AND inside the issue's data blob, and serves it
// verbatim, so a team-key rename in Linear invalidates every one of that
// team's cached identifiers at once — without touching any issue's updatedAt.
// The incremental cursor therefore can never see the change (worker.go's
// sync-until-unchanged walk stops on the first unchanged page), while the
// team row takes the new key on the very next cycle: a renamed team directory
// full of old-prefixed issue directories, surviving restarts. That is #427.
//
// The repair below is deliberately a LEVEL check, not an edge one. It reads
// the invariant — every cached issue's identifier prefix equals its team's
// current key — rather than the rename event, so it re-arms every cycle and
// repairs a cache that was damaged long before this code existed, where no
// rename event will ever fire again.

// maybeRebuildRenamedTeam runs the drift check for one team and, on a hit,
// rebuilds it. The cost in the healthy case is one indexed local COUNT per
// team per full cycle and no API calls at all.
func (w *Worker) maybeRebuildRenamedTeam(ctx context.Context, team api.Team) {
	if team.Key == "" {
		return
	}
	// The trailing hyphen is load-bearing: without it key TS matches
	// identifier TST-1 and a real rename reads as healthy.
	prefix := team.Key + "-"
	stale, err := w.store.Queries().CountTeamIssuesWithForeignIdentifier(ctx, db.CountTeamIssuesWithForeignIdentifierParams{
		TeamID:    team.ID,
		KeyPrefix: prefix,
	})
	if err != nil {
		log.Printf("[sync] team %s: identifier drift check failed: %v", team.Key, err)
		return
	}
	if stale == 0 {
		return
	}
	log.Printf("[sync] team %s: %d cached issues carry a foreign identifier prefix (team key renamed?), rebuilding the team",
		team.Key, stale)
	w.rebuildTeamIssues(ctx, team)
}

// rebuildTeamIssues drops a team's cached issues, everything keyed off them,
// and its incremental watermark. The caller's ordinary syncTeam then refills
// the team from the server in the same cycle: with no watermark that call is
// already a full walk that queues detail sync for everything it sees, which is
// exactly what a rebuild is, so there is no second sync path to keep honest.
//
// Repair is delete-and-refill rather than a local rewrite of the identifiers.
// Nothing stale can survive a delete, which removes the whole class of
// partial-repair bugs, and the refilled identifiers come from the server
// instead of being invented in a namespace we do not own (the issue's number,
// the other half of an identifier, is not even selected by any query). It is
// scoped to the one affected team: wiping the cache would empty every other
// team's directories for the length of a cold start, and an empty directory is
// a more confident lie than a stale prefix.
//
// ORDERING IS LOAD-BEARING. The watermark goes first, and it is the only thing
// that re-arms a rebuild that dies partway — the drift check cannot, because
// once the rows are deleted no stale prefix remains for it to count. A team
// left with no watermark gets walked in full by the next cycle regardless.
func (w *Worker) rebuildTeamIssues(ctx context.Context, team api.Team) {
	q := w.store.Queries()
	rows, err := q.ListTeamIssueIDs(ctx, team.ID)
	if err != nil {
		log.Printf("[sync] team %s: rebuild aborted, listing cached issues failed: %v", team.Key, err)
		return
	}
	if err := q.DeleteSyncMeta(ctx, team.ID); err != nil {
		// Abort with the rows intact: the drift check fires again next cycle.
		log.Printf("[sync] team %s: rebuild aborted, dropping the watermark failed: %v", team.Key, err)
		return
	}
	for _, row := range rows {
		db.DeleteIssueCascade(ctx, q, row.ID, func(family string, err error) {
			log.Printf("[sync] team %s: rebuild cleanup: %s for issue %s: %v", team.Key, family, row.ID, err)
		})
	}
	log.Printf("[sync] team %s: rebuild dropped %d cached issues; refilling from the server", team.Key, len(rows))
}

// identifierHolder names the cached issue currently holding the incoming
// issue's identifier, formatted for a log line, or "" when nothing holds it
// (so the failure was something other than the UNIQUE(identifier) collision).
// An issue that is merely its own previous row is not a holder — that is the
// ordinary update path, which conflicts on id and succeeds.
func (w *Worker) identifierHolder(ctx context.Context, issue api.Issue) string {
	q := w.store.Queries()
	holder, err := q.GetIssueByIdentifier(ctx, issue.Identifier)
	if err != nil || holder.ID == issue.ID {
		return ""
	}
	if key, err := q.GetIssueTeamKey(ctx, holder.ID); err == nil && key != "" {
		return fmt.Sprintf("issue %s (team %s)", holder.ID, key)
	}
	return fmt.Sprintf("issue %s", holder.ID)
}

// teamKeyHolder names the cached team currently holding the incoming team's
// key, or "" when nothing else holds it. This is the departed-team squatting
// case: a key freed by a rename and taken by a genuinely new team, whose row
// can never be inserted while the old holder is cached. LinearFS has no team
// eviction (deliberately - see the "no prune either" note on RefreshTeams), so
// naming the holder is all this ticket does about it.
func (w *Worker) teamKeyHolder(ctx context.Context, team api.Team) string {
	holder, err := w.store.Queries().GetTeamByKey(ctx, team.Key)
	if err != nil || holder.ID == team.ID {
		return ""
	}
	return fmt.Sprintf("team %s (%s)", holder.ID, holder.Name)
}
