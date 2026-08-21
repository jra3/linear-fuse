package fs

import (
	"context"
	"strings"
)

// identifierMatchesTeamKey is the pure half of the issue-resolution
// consistency guard (#427): does the identifier a caller asked for agree with
// the current key of the team that actually owns the resolved issue?
//
// It exists because every path that turns a caller-supplied identifier into an
// issue goes through GetIssueByIdentifier, which is workspace-wide (Readdir, by
// contrast, is team-scoped), so a stale identifier left behind by a team-key
// rename can resolve to a DIFFERENT team's issue once the freed key is reused.
// That mis-resolution is not merely a wrong read: each of those paths hands the
// issue it resolved to a mutation — IssuesNode.Lookup captures the entity
// IssueFileNode.Flush writes back, ResolveIssueID feeds parentId into
// IssueUpdateInput, and RelationsNode.createRelation feeds the relation's other
// end — so a save at that path would target the other team's Linear issue.
//
// The check is deliberately NOT parent-team equality. ProjectNode.Lookup and
// ChildrenNode.Lookup both put genuinely cross-team issues under a containing
// team's issues/ directory (their listings are scoped by project_id and
// parent_id, not team), and those symlinks resolve only because Lookup is
// unscoped. Scoping would turn ordinary cross-team project members and
// sub-issues into dangling links — and cross-team parents and cross-team
// relations are legitimate for the same reason. What is being validated is the
// identifier's own internal consistency, which every legitimately-resolvable
// issue has.
//
// An unknown key (empty) admits: the owning team is not cached, so there is
// nothing authoritative to disagree with, and refusing would be inventing a
// verdict from missing data.
func identifierMatchesTeamKey(identifier, teamKey string) bool {
	if teamKey == "" {
		return true
	}
	return strings.HasPrefix(identifier, teamKey+"-")
}

// owningTeamKey returns the current key of the team that owns issueID, or ""
// when it cannot be resolved. It reads the teams table through the issue's
// team_id column rather than the team key inside the issue's data blob: on a
// rename the blob's key goes stale in lockstep with the identifier, so a guard
// reading it would compare a stale value against itself and always agree.
func (lfs *LinearFS) owningTeamKey(ctx context.Context, issueID string) string {
	if lfs.repo == nil {
		return ""
	}
	key, err := lfs.repo.GetIssueTeamKey(ctx, issueID)
	if err != nil {
		return ""
	}
	return key
}

// identifierIsStale composes the two halves above, and is the single spelling
// every caller-supplied-identifier resolution uses. One predicate rather than
// three inlined copies is the point: each new resolution path that reaches an
// issue by identifier and hands it to a mutation is a fresh instance of the
// same wrong-issue-write hazard, and they should not each decide for
// themselves what "the owning team's key" means.
func (lfs *LinearFS) identifierIsStale(ctx context.Context, identifier, issueID string) bool {
	return !identifierMatchesTeamKey(identifier, lfs.owningTeamKey(ctx, issueID))
}
