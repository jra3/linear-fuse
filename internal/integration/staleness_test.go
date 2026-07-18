package integration

import (
	"context"
	"fmt"
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
	if testStore == nil {
		t.Skip("store-backed staleness simulation requires fixture mode")
	}

	// Drive the remote-update dance on a THROWAWAY issue (unique per run), never
	// the shared TST-1 fixture: mutating TST-1 left the fixture readers — and
	// every -count rerun's own baseline read — seeing "Renamed By Remote Sync".
	// A store-upserted issue is served through the mount's dynamic issue Lookup,
	// and the go-fuse node-reuse path under test is identical on a fresh node.
	team := fixtures.FixtureAPITeam()
	uniq := time.Now().UnixNano()
	issueID := fmt.Sprintf("iso-issue-%d", uniq)
	identifier := fmt.Sprintf("TST-%d", 90000+uniq%10000)
	seedRow, err := db.APIIssueToDBIssue(fixtures.FixtureAPIIssue(
		fixtures.WithIssueID(issueID, identifier),
		fixtures.WithTitle("Isolation Probe Original"),
		fixtures.WithTeam(&team),
	))
	if err != nil {
		t.Fatalf("convert seed: %v", err)
	}
	if err := testStore.Queries().UpsertIssue(ctx, seedRow.ToUpsertParams()); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	t.Cleanup(func() { _ = testStore.Queries().DeleteIssue(context.Background(), issueID) })

	path := mountPoint + "/teams/" + testTeamKey + "/issues/" + identifier + "/issue.md"

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if !strings.Contains(string(before), "Isolation Probe Original") {
		t.Fatalf("throwaway issue not served, got:\n%s", before)
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
	renamed := fixtures.FixtureAPIIssue(
		fixtures.WithIssueID(issueID, identifier),
		fixtures.WithTitle("Renamed By Remote Sync"),
		fixtures.WithTeam(&team),
	)
	renamed.UpdatedAt = time.Now()
	row, err := db.APIIssueToDBIssue(renamed)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if err := testStore.Queries().UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Let the kernel's caches expire for real — the production freshness
	// mechanism. The sync worker never notifies the kernel; entry timeouts
	// (30s) make the next path walk re-Lookup every component, and each
	// re-Lookup runs the nodeRefresher seam. (The ino namespace is total now —
	// every ancestor dir has a derivable stable ino — but timeout-driven
	// revalidation remains the production mechanism, so it is what this
	// exercises.)
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
	meta, err := os.ReadFile(mountPoint + "/teams/" + testTeamKey + "/issues/" + identifier + "/issue.meta")
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

// TestRejectedSaveKeepsDirtyContentReadable pins the size half of the
// dirty-buffer-wins rule: a rejected save (EINVAL) deliberately leaves the
// user's content in the edit buffer so it can be corrected and re-saved — and
// every subsequent Lookup must report THAT buffer's size, not the fresh
// render's. newFileInode used to fill the Lookup answer from the fresh twin
// while refreshExisting let the dirty buffer keep its content, so the kernel
// clamped reads of the longer dirty content mid-file ("unclosed frontmatter"
// on project.md) — a latent mismatch the view-dir normalization surfaced once
// the whole directory chain became stably reusable.
func TestRejectedSaveKeepsDirtyContentReadable(t *testing.T) {
	if testStore == nil {
		t.Skip("store-backed simulation requires fixture mode")
	}
	enableMockMutations(t)
	ctx := context.Background()

	// A dedicated project row so the poisoned dirty state cannot leak into
	// other tests' fixtures.
	proj := fixtures.FixtureAPIProject()
	proj.ID, proj.Slug, proj.Name = "project-dirty", "dirty-project", "Dirty Project"
	if err := fixtures.PopulateProject(ctx, testStore, proj, fixtures.FixtureAPITeam().ID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testStore.DB().Exec("DELETE FROM projects WHERE id = 'project-dirty'")
		_, _ = testStore.DB().Exec("DELETE FROM project_teams WHERE project_id = 'project-dirty'")
	})

	path := mountPoint + "/teams/" + testTeamKey + "/projects/dirty-project/project.md"
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}

	// A save that validation rejects: an unknown label name. The flush returns
	// EINVAL and the buffer stays dirty with exactly this content.
	rejected := strings.Replace(string(orig), "name:", "labels: [__no_such_project_label__]\nname:", 1)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open for write: %v", err)
	}
	if _, err := f.Write([]byte(rejected)); err != nil {
		f.Close()
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err == nil {
		t.Fatal("expected the save to be rejected (EINVAL), but close succeeded")
	}

	// Force fresh Lookups through the whole chain (project children run a 0
	// entry timeout, so every walk re-Lookups), then read back: the stat size
	// and the read must both cover the FULL dirty content.
	if _, err := os.ReadDir(mountPoint + "/teams/" + testTeamKey + "/projects/dirty-project"); err != nil {
		t.Fatalf("readdir: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if int64(len(got)) != st.Size() {
		t.Errorf("stat size %d != read length %d — Lookup answered a different size than the serving buffer", st.Size(), len(got))
	}
	if string(got) != rejected {
		t.Errorf("dirty content clamped or replaced after rejected save:\nwant %d bytes:\n%s\ngot %d bytes:\n%s", len(rejected), rejected, len(got), got)
	}

	// The documented retry path: writing corrected content must succeed and
	// leave the buffer clean again.
	if err := os.WriteFile(path, orig, 0644); err != nil {
		t.Errorf("corrected re-save failed: %v", err)
	}
}

// TestRemoteTeamUpdateVisibleAfterKernelRevalidation extends the pinned-fd
// revalidation technique to the busiest reused directory node: teams/{KEY} is
// now on a stable ino (teamDirIno), so after entry-timeout expiry the kernel's
// re-Lookup dedups onto the FIRST TeamNode ever mounted. The nodeRefresher
// seam must push the freshly-fetched team snapshot into that node (and
// newDirInode must re-stamp its nodeAttr), or the directory would report
// first-Lookup times and team.md would render the old name for as long as the
// kernel remembered the inode — the exact hazard the view-dir normalization
// took on when it moved these nodes off auto-assigned inos.
func TestRemoteTeamUpdateVisibleAfterKernelRevalidation(t *testing.T) {
	ctx := context.Background()
	if testStore == nil {
		t.Skip("store-backed staleness simulation requires fixture mode")
	}

	// Drive the remote-update dance on a THROWAWAY team (unique key per run),
	// never the shared TST team: renaming TST left every -count rerun's baseline
	// read — and any later team.md reader — seeing "Renamed Team By Remote Sync".
	// TeamsNode.Lookup resolves teams dynamically from the store, so a fresh
	// upserted team is served, and the node-reuse path under test is identical.
	uniq := time.Now().UnixNano()
	teamID := fmt.Sprintf("iso-team-%d", uniq)
	teamKey := fmt.Sprintf("IS%d", uniq%100000)
	team := fixtures.FixtureAPITeam()
	team.ID = teamID
	team.Key = teamKey
	team.Name = "Isolation Team Original"
	if err := testStore.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	t.Cleanup(func() { _, _ = testStore.DB().Exec("DELETE FROM teams WHERE id = ?", teamID) })

	teamDir := mountPoint + "/teams/" + teamKey
	teamFile := teamDir + "/team.md"

	before, err := os.ReadFile(teamFile)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if !strings.Contains(string(before), "Isolation Team Original") {
		t.Fatalf("throwaway team not served, got:\n%s", before)
	}

	// Pin the team directory: an open descriptor keeps the kernel from
	// FORGETting the dir inode and its ancestor dentries, so the post-expiry
	// re-Lookup hits the ALREADY-KNOWN TeamNode — the go-fuse reuse path this
	// test exists to guard.
	pin, err := os.Open(teamDir)
	if err != nil {
		t.Fatalf("pin open: %v", err)
	}
	defer pin.Close()

	// Simulate the sync worker landing a remote team edit: same team, new
	// name, newer updatedAt — written straight to the store, no kernel
	// notification (faithful to production sync).
	team.Name = "Renamed Team By Remote Sync"
	team.UpdatedAt = time.Now()
	if err := testStore.Queries().UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("upsert team: %v", err)
	}

	// Wait out the kernel's entry/attr timeouts for real, as in the issue
	// variant above: expiry forces the next path walk to re-Lookup every
	// component, and each re-Lookup runs the nodeRefresher seam.
	if testing.Short() {
		t.Skip("waits out the 30s kernel entry timeout; skipped with -short")
	}
	time.Sleep(31 * time.Second)

	after, err := os.ReadFile(teamFile)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !strings.Contains(string(after), "Renamed Team By Remote Sync") {
		t.Errorf("STALE: team.md still serves first-Lookup content after remote team update + full kernel revalidation.\ngot:\n%s", after)
	}

	// The team directory's own attrs must follow: mtime = the team's fresh
	// updatedAt (the honest-times half of the normalization), not the fixture
	// base time the first Lookup stamped.
	st, err := os.Stat(teamDir)
	if err != nil {
		t.Fatalf("stat team dir: %v", err)
	}
	if age := time.Since(st.ModTime()); age > time.Hour {
		t.Errorf("STALE ATTRS: team dir mtime %v predates the remote update (age %v)", st.ModTime(), age)
	}
}
