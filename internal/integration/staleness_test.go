package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
)

// TestRemoteUpdateVisibleAfterKernelRevalidation is the decisive experiment
// for the captured-entity staleness question (round-15 review, candidate 1):
// when a REMOTE change lands in SQLite (the sync worker's job — it never
// notifies the kernel), freshness relies on the kernel re-asking userspace
// via Lookup after its caches drop. go-fuse then reuses the already-known
// node for a stable ino (bridge.addNewChild: "child is ignored"), so if the
// serving nodes bake the entity at first Lookup, the re-Lookup serves the
// OLD data anyway — stale for as long as the kernel remembers the inode.
//
// The test simulates the full revalidation cycle deterministically: read
// (populate nodes) → upsert a changed row (simulate sync) → drop the kernel
// dentries/attrs explicitly (what timeout expiry does, without waiting 30s)
// → re-read. Fresh content passing means the reuse hazard is compensated;
// stale content is the latent bug, confirmed end-to-end.
func TestRemoteUpdateVisibleAfterKernelRevalidation(t *testing.T) {
	ctx := context.Background()
	path := mountPoint + "/teams/TST/issues/TST-1/issue.md"

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if !strings.Contains(string(before), "Test Issue 1") {
		t.Fatalf("fixture issue not served, got:\n%s", before)
	}

	// Pin the inode chain: an open descriptor keeps the kernel from
	// FORGETting the file inode and its ancestor dentries during the wait,
	// so the post-expiry re-Lookups hit the ALREADY-KNOWN nodes — the
	// go-fuse reuse path this test exists to guard (without the pin, the
	// kernel may forget everything and build fresh nodes, which was fresh
	// even before the nodeRefresher seam existed).
	pin, err := os.Open(path)
	if err != nil {
		t.Fatalf("pin open: %v", err)
	}
	defer pin.Close()

	// Simulate the sync worker landing a remote edit: same issue, new title,
	// newer updatedAt — written straight to the store, no kernel notification
	// (faithful to production sync).
	team := fixtures.FixtureAPITeam()
	renamed := fixtures.FixtureAPIIssue(
		fixtures.WithIssueID("issue-1", "TST-1"),
		fixtures.WithTitle("Renamed By Remote Sync"),
		fixtures.WithTeam(&team),
	)
	renamed.UpdatedAt = time.Now()
	row, err := db.APIIssueToDBIssue(renamed)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if testStore == nil {
		t.Skip("store-backed staleness simulation requires fixture mode")
	}
	if err := testStore.Queries().UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Let the kernel's caches expire for real — the production freshness
	// mechanism. The sync worker never notifies the kernel; entry timeouts
	// (30s) make the next path walk re-Lookup every component, and each
	// re-Lookup runs the nodeRefresher seam. (Targeted EntryNotify calls
	// can't stand in for this: several ancestor dirs use auto-assigned inos,
	// so their kernel ids aren't derivable from the ino namespace.)
	if testing.Short() {
		t.Skip("waits out the 30s kernel entry timeout; skipped with -short")
	}
	time.Sleep(31 * time.Second)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !strings.Contains(string(after), "Renamed By Remote Sync") {
		t.Errorf("STALE: issue.md still serves first-Lookup content after remote update + full kernel revalidation.\ngot:\n%s", after)
	}

	// The reported mtime should likewise follow the remote update.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if age := time.Since(st.ModTime()); age > time.Hour {
		t.Errorf("STALE ATTRS: issue.md mtime %v predates the remote update (age %v)", st.ModTime(), age)
	}

	// Layer isolation: issue.meta's render closure RE-FETCHES by identifier on
	// every read (the freshestByID pattern) instead of trusting the captured
	// entity. If .meta is fresh while issue.md is stale, the mechanism is
	// pinned: go-fuse node reuse + entity captured at first Lookup.
	meta, err := os.ReadFile(mountPoint + "/teams/TST/issues/TST-1/issue.meta")
	if err != nil {
		t.Fatalf("read issue.meta: %v", err)
	}
	if !strings.Contains(string(meta), "Renamed By Remote Sync") {
		// .meta doesn't render the title — check the updated timestamp instead.
		if !strings.Contains(string(meta), time.Now().Format("2006-01-02")) {
			t.Logf("NOTE: issue.meta also stale (updated stamp not today):\n%s", meta)
		} else {
			t.Logf("CONTRAST: issue.meta reflects the remote update (re-fetching closure) while issue.md does not:\n%s", meta)
		}
	}
}
