package fs

import (
	"context"
	"syscall"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// milestoneEditNode builds a dirty MilestoneFileNode whose buffer holds an edit
// (a changed description) ready to Flush.
func milestoneEditNode(t *testing.T, lfs *LinearFS, projectID string) *MilestoneFileNode {
	t.Helper()
	n := &MilestoneFileNode{
		BaseNode:  BaseNode{lfs: lfs},
		milestone: api.ProjectMilestone{ID: "ms-1", Name: "Alpha", Description: "orig desc"},
		projectID: projectID,
	}
	edited := n.milestone
	edited.Description = "new desc ZZZ"
	content, err := marshal.MilestoneToMarkdown(&edited)
	if err != nil {
		t.Fatalf("render milestone: %v", err)
	}
	n.content = content
	n.dirty = true
	return n
}

// TestMilestoneEditPersistFailureFailsLoud is the #285(a) regression: a milestone
// edit whose SQLite reflection fails must fail loud (EIO), not report a clean save
// that then silently reverts on the next re-Lookup. The bug was that milestone was
// the only editFlush entity setting writeBack.persist = nil, relying on
// LinearFS.UpdateProjectMilestone's log-and-swallowed upsert, so a wedged write
// returned 0 with an empty .error.
func TestMilestoneEditPersistFailureFailsLoud(t *testing.T) {
	zeroRetryBackoff(t)
	lfs, store := linkTestLFS(t)
	n := milestoneEditNode(t, lfs, "proj-1")

	// Close the store so the reflection upsert fails while the mock mutation still
	// succeeds — the #276 confirmed-reflection wedge condition.
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	if errno := n.Flush(context.Background(), nil); errno != syscall.EIO {
		t.Fatalf("milestone Flush on a failed persist: errno = %v, want EIO", errno)
	}
	key := collectionErrorKey("milestones", n.projectID)
	if e := lfs.GetWriteError(key); e == nil {
		t.Errorf(".error must be set on an unconfirmed milestone reflection")
	}
}

// TestMilestoneEditPreservesProjectAssociation is the #285(b) regression: a
// milestone edit must upsert under the node's known projectID, not a fallible
// GetProjectMilestone lookup that (on a store miss) yielded projectID="" and
// dropped the milestone from its project listing entirely. Here the milestone row
// is NOT pre-seeded, so the old recovery would have clobbered the association.
func TestMilestoneEditPreservesProjectAssociation(t *testing.T) {
	lfs, store := linkTestLFS(t)
	const projectID = "proj-xyz"
	n := milestoneEditNode(t, lfs, projectID)

	if errno := n.Flush(context.Background(), nil); errno != 0 {
		t.Fatalf("milestone Flush: errno = %v, want 0", errno)
	}

	got, err := store.Queries().ListProjectMilestones(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListProjectMilestones: %v", err)
	}
	found := false
	for _, m := range got {
		if m.ID == n.milestone.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("edited milestone %q not associated with project %q (clobbered to \"\")", n.milestone.ID, projectID)
	}
}
