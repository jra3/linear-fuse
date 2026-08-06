package fs

import (
	"context"
	"log"

	"github.com/jra3/linear-fuse/internal/api"
)

// Freshest-persisted-copy reads.
//
// A directory node caches the entity it was built with. That copy is correct
// when the node is built and can be stale at any moment after: a write through
// the entity's own file node adopts the fresh value onto the FILE node and
// upserts SQLite, and the background sync writes SQLite without touching any
// node at all. Nothing pushes either back down to the directory.
//
// Reads already account for this — every <entity>.meta closure re-reads and
// falls back to its snapshot (freshestByID). The WRITE side did not, which is
// #415: an atomic save built its transient file node from the directory's
// snapshot and diffed against it, so a save that restored what an in-place save
// had replaced looked like no change at all and sent no mutation.
//
// These are that same read-through for the write path, one per entity that has
// an editable canonical .md. They read SQLite, not the API: the in-place save
// that staled the snapshot upserted there before returning (commitWriteBack's
// persist), so the local store already holds what an API round-trip would fetch,
// at no rate-budget cost. Every one degrades to the caller's snapshot — an
// unconfigured store or a failed read leaves the caller exactly where it was
// before the read-through existed, never worse.

// freshestIssue returns the freshest persisted copy of have, or have itself when
// the store cannot serve it.
func (lfs *LinearFS) freshestIssue(ctx context.Context, have api.Issue) api.Issue {
	if lfs.repo == nil {
		return have
	}
	fresh, err := lfs.repo.GetIssueByID(ctx, have.ID)
	if err != nil || fresh == nil {
		if err != nil && lfs.debug {
			log.Printf("freshestIssue(%s): %v — diffing against the node's snapshot", have.Identifier, err)
		}
		return have
	}
	return *fresh
}

// freshestProject returns the freshest persisted copy of have, or have itself
// when the store cannot serve it.
func (lfs *LinearFS) freshestProject(ctx context.Context, have api.Project) api.Project {
	if lfs.repo == nil {
		return have
	}
	fresh, err := lfs.repo.GetProjectByID(ctx, have.ID)
	if err != nil || fresh == nil {
		if err != nil && lfs.debug {
			log.Printf("freshestProject(%s): %v — diffing against the node's snapshot", have.Name, err)
		}
		return have
	}
	return *fresh
}

// freshestInitiative returns the freshest persisted copy of have, or have itself
// when the store cannot serve it. Initiatives have no by-ID repository read, so
// this scans the workspace listing — exactly what initiative.meta's read-through
// closure does (initiatives.go).
func (lfs *LinearFS) freshestInitiative(ctx context.Context, have api.Initiative) api.Initiative {
	if lfs.repo == nil {
		return have
	}
	inits, err := lfs.repo.GetInitiatives(ctx)
	if err != nil {
		if lfs.debug {
			log.Printf("freshestInitiative(%s): %v — diffing against the node's snapshot", have.Name, err)
		}
		return have
	}
	return freshestByID(inits, have.ID, func(i api.Initiative) string { return i.ID }, have)
}
