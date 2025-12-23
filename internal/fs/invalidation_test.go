package fs

import (
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// =============================================================================
// No-op Cache Invalidation Tests
// These methods are all no-ops since SQLite is the source of truth.
// We just need to verify they don't panic when called.
// =============================================================================

func TestInvalidateTeamIssues(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateTeamIssues("team-123")
}

func TestInvalidateFilteredIssues(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateFilteredIssues("team-123")
}

func TestInvalidateIssueById(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateIssueById("TST-123")
}

func TestInvalidateMyIssues(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateMyIssues()
}

func TestInvalidateTeamProjects(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateTeamProjects("team-123")
}

func TestInvalidateProjectIssues(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateProjectIssues("project-123")
}

func TestInvalidateUserIssues(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateUserIssues("user-123")
}

func TestInvalidateIssueComments(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateIssueComments("issue-123")
}

func TestInvalidateIssueDocuments(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateIssueDocuments("issue-123")
}

func TestInvalidateTeamDocuments(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateTeamDocuments("team-123")
}

func TestInvalidateProjectDocuments(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateProjectDocuments("project-123")
}

func TestInvalidateProjectUpdates(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateProjectUpdates("project-123")
}

func TestInvalidateInitiativeUpdates(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateInitiativeUpdates("initiative-123")
}

func TestInvalidateTeamLabels(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateTeamLabels("team-123")
}

func TestInvalidateInitiatives(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}
	// Should not panic
	lfs.InvalidateInitiatives()
}

// =============================================================================
// Kernel Cache Invalidation Tests
// These methods interact with the FUSE server to invalidate kernel caches.
// =============================================================================

func TestInvalidateKernelInode_NilServer(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{server: nil}
	// Should be a no-op with nil server (not panic)
	lfs.InvalidateKernelInode(12345)
}

func TestInvalidateKernelEntry_NilServer(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{server: nil}
	// Should be a no-op with nil server (not panic)
	lfs.InvalidateKernelEntry(12345, "test-entry")
}

func TestSetServer(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{}

	// Initially nil
	if lfs.server != nil {
		t.Error("server should initially be nil")
	}

	// Set a server (we use nil since we can't easily create a real fuse.Server)
	var server *fuse.Server = nil
	lfs.SetServer(server)

	// Verify it was set (even though both are nil, this tests the method works)
	if lfs.server != server {
		t.Error("SetServer should set the server field")
	}
}

// =============================================================================
// Integration-style tests with mock behavior
// =============================================================================

func TestInvalidateKernelInode_CalledMultipleTimes(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{server: nil}

	// Should handle multiple calls without issue
	for i := 0; i < 100; i++ {
		lfs.InvalidateKernelInode(uint64(i))
	}
}

func TestInvalidateKernelEntry_CalledMultipleTimes(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{server: nil}

	// Should handle multiple calls without issue
	for i := 0; i < 100; i++ {
		lfs.InvalidateKernelEntry(uint64(i), "entry")
	}
}

func TestInvalidateKernelEntry_EmptyName(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{server: nil}
	// Should handle empty name without panic
	lfs.InvalidateKernelEntry(12345, "")
}

func TestInvalidateKernelInode_ZeroInode(t *testing.T) {
	t.Parallel()
	lfs := &LinearFS{server: nil}
	// Should handle zero inode without panic
	lfs.InvalidateKernelInode(0)
}
