package fs

// Entity recheck — the write path's hint hook, wired to the repository.
//
// editFlush reaches it as `refresh`: a front half whose mutation REACHED Linear
// and came back a failure must not restore the buffer from the entity it holds
// in memory, because that render is local, never fetched, and would confidently
// show pre-write content that may already be wrong upstream. The shell clears
// dirty and calls the spec's refresh hook instead, and these are what the hook
// resolves to. The create and rename tails reach the same functions as
// `recheck`, on the narrower serverSaysGone verdict — Linear itself answering
// "entity not found" about the entity that owns the collection written into.
//
// Each one triggers an EXISTING SWR spec on the repository rather than touching
// the cache here. That is the whole of #477's answer to "may a failed write
// mutate the local cache?": no — it supplies the hint, and the repo layer keeps
// ownership of the prune. It matters most for the ENOENT verdict, where Linear
// has just said the entity does not exist: the spec's orphanOnNotFound wrapper
// prunes the row, but only after re-asking Linear, so a not-found that
// api.IsNotFound matched on message text cannot delete a live entity's cache.
//
// A nil repository is tolerated. The node-seam unit tests build a LinearFS
// without one, and a refresh nobody can serve is a no-op, not a crash.

// recheckIssue re-asks Linear about an issue: the entity behind issue.md and
// every collection under issues/{ID}/.
func (lfs *LinearFS) recheckIssue(issueID string) {
	if lfs.repo != nil {
		lfs.repo.RecheckIssue(issueID)
	}
}

// recheckProject re-asks Linear about a project: the entity behind project.md
// and every collection under the project directory.
func (lfs *LinearFS) recheckProject(projectID string) {
	if lfs.repo != nil {
		lfs.repo.RecheckProject(projectID)
	}
}

// recheckInitiative re-asks Linear about an initiative: the entity behind
// initiative.md and every collection under the initiative directory.
func (lfs *LinearFS) recheckInitiative(initiativeID string) {
	if lfs.repo != nil {
		lfs.repo.RecheckInitiative(initiativeID)
	}
}

// recheckDocOwner is the one dispatch for docs/, the only collection with four
// possible owners. Both document nodes call it — the file, whose edit flush
// hooks it as refresh, and the directory, which owns the create and the retitle
// — so the four owners are enumerated once and a fifth cannot be added to one
// and missed by the other.
//
// A TEAM document is the silent arm and that is deliberate: a team is the sync
// root, not a sub-resource, so its docs spec carries no orphan handler and there
// is nothing to recheck. Those rows converge on the sync worker instead.
func (lfs *LinearFS) recheckDocOwner(issueID, projectID, initiativeID string) {
	switch {
	case issueID != "":
		lfs.recheckIssue(issueID)
	case projectID != "":
		lfs.recheckProject(projectID)
	case initiativeID != "":
		lfs.recheckInitiative(initiativeID)
	}
}
