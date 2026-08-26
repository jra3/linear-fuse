package fs

// Entity recheck — editFlush's refresh hook, wired to the repository.
//
// A front half whose mutation REACHED Linear and came back a failure must not
// restore the buffer from the entity it holds in memory: that render is local,
// never fetched, and would confidently show pre-write content that may already
// be wrong upstream. The shell clears dirty and calls the spec's refresh hook
// instead, and these are what the hook resolves to.
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

// recheckIssue re-asks Linear about an issue (issue.md, comments/*.md, and an
// issue's docs/*.md).
func (lfs *LinearFS) recheckIssue(issueID string) {
	if lfs.repo != nil {
		lfs.repo.RecheckIssue(issueID)
	}
}

// recheckProject re-asks Linear about a project (project.md and a project's
// docs/*.md).
func (lfs *LinearFS) recheckProject(projectID string) {
	if lfs.repo != nil {
		lfs.repo.RecheckProject(projectID)
	}
}

// recheckInitiative re-asks Linear about an initiative (initiative.md and an
// initiative's docs/*.md).
func (lfs *LinearFS) recheckInitiative(initiativeID string) {
	if lfs.repo != nil {
		lfs.repo.RecheckInitiative(initiativeID)
	}
}
