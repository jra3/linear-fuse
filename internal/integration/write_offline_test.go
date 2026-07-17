package integration

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// These tests exercise the create/archive/rename/delete FUSE write paths
// OFFLINE, through the in-memory mutation fake (enableMockMutations). Before
// them the project/label/issue/comment mkdir·rmdir·rename·unlink handlers were
// only reachable under LINEARFS_WRITE_TESTS=1 against the live API (they go
// through createTestIssue, which mkdir's against the real backend), so the
// default fixture-mode suite left every one of them at 0% coverage even though
// the fake already implements the whole mutation surface. Each test drives one
// entity's full lifecycle and asserts the mount reflects it immediately (the
// handlers upsert/forget SQLite and invalidate the kernel cache after the
// mutation), which is exactly the per-entity front half the commitCreate /
// commitDelete tails cannot cover on their own.

// writeToWriteOnly opens a write-only control file (a _create trigger), writes
// content, and returns the error surfaced at close, where Flush/create runs.
func writeToWriteOnly(t *testing.T, path, content string) error {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0o200)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		t.Fatalf("write %s: %v", path, err)
	}
	return f.Close()
}

// lastEntryByTitle scans a .last YAML list for the newest entry whose title
// matches. The mount is shared, so match by title, never by position.
func lastEntryByTitle(t *testing.T, lastPath, title string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(lastPath)
	if err != nil {
		t.Fatalf("read %s: %v", lastPath, err)
	}
	var entries []map[string]string
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("%s is not a YAML list: %v\n%s", lastPath, err, data)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i]["title"] == title {
			return entries[i]
		}
	}
	return nil
}

// TestOffline_ProjectCreateAndArchive drives ProjectsNode.Mkdir (commitCreate)
// then ProjectsNode.Rmdir (commitDelete): the created project is visible in the
// listing with a populated project.meta, and after archive it is gone — the
// forget-from-SQLite that stops an archived project resurrecting on the next
// readdir (#149) is what this asserts.
func TestOffline_ProjectCreateAndArchive(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline write-path check; uses the mock mutator")
	}
	enableMockMutations(t)

	const title = "Offline Mock Project Probe"
	if err := os.Mkdir(filepath.Join(projectsPath(testTeamKey), title), 0o755); err != nil {
		t.Fatalf("mkdir project should succeed with mock mutator: %v", err)
	}

	// projects/.last reports the new identity (the trio degrades to .error/.last
	// because projects are created by mkdir, not a _create trigger).
	last := lastEntryByTitle(t, filepath.Join(projectsPath(testTeamKey), ".last"), title)
	if last == nil {
		t.Fatalf("projects/.last has no entry titled %q", title)
	}
	dir := last["path"]
	if dir == "" || last["identifier"] == "" {
		t.Fatalf("projects/.last entry missing identity: %v", last)
	}

	// The created project directory is present and carries a server-populated
	// project.meta (proof the mkdir upserted it to SQLite for the listing).
	if !dirContains(projectsPath(testTeamKey), dir) {
		t.Fatalf("created project %q not in projects listing", dir)
	}
	assertMetaHasFields(t, filepath.Join(projectsPath(testTeamKey), dir, "project.meta"), "id", "url", "status")

	// Archive via rmdir; the project must vanish from the listing immediately.
	if err := os.Remove(filepath.Join(projectsPath(testTeamKey), dir)); err != nil {
		t.Fatalf("rmdir (archive) project should succeed: %v", err)
	}
	if dirContains(projectsPath(testTeamKey), dir) {
		t.Errorf("archived project %q still in listing (resurrection — forget failed)", dir)
	}
}

// TestOffline_LabelCreateRenameDelete drives the labels collection's whole
// write surface offline: createLabel (via _create), Rename (LabelsNode.Rename),
// and Unlink. Labels are a collectionDir, so this also exercises its create and
// unlink heads.
func TestOffline_LabelCreateRenameDelete(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline write-path check; uses the mock mutator")
	}
	enableMockMutations(t)

	// Create. The label filename is the name with spaces→dashes (labelFilename).
	spec := "---\nname: Offline Mock Label ZZZ\ncolor: \"#ff8800\"\ndescription: probe label\n---\n"
	if err := writeToWriteOnly(t, filepath.Join(labelsPath(testTeamKey), "_create"), spec); err != nil {
		t.Fatalf("create label via _create should succeed with mock mutator: %v", err)
	}
	const created = "Offline-Mock-Label-ZZZ.md"
	if !dirContains(labelsPath(testTeamKey), created) {
		t.Fatalf("created label %q not in labels listing", created)
	}

	// Rename in place (same-directory) — LabelsNode.Rename updates the name and
	// moves both the .md and its .meta sidecar.
	const renamed = "Offline-Renamed-Label-ZZZ.md"
	if err := os.Rename(labelFilePath(testTeamKey, created), labelFilePath(testTeamKey, renamed)); err != nil {
		t.Fatalf("rename label should succeed: %v", err)
	}
	if dirContains(labelsPath(testTeamKey), created) {
		t.Errorf("old label name %q still present after rename", created)
	}
	if !dirContains(labelsPath(testTeamKey), renamed) {
		t.Errorf("renamed label %q not present after rename", renamed)
	}

	// Delete via unlink; the label (and its .meta) must disappear.
	if err := os.Remove(labelFilePath(testTeamKey, renamed)); err != nil {
		t.Fatalf("unlink (delete) label should succeed: %v", err)
	}
	if dirContains(labelsPath(testTeamKey), renamed) {
		t.Errorf("deleted label %q still in listing (forget failed)", renamed)
	}
}

// TestOffline_IssueLifecycle drives the issue/child/comment write paths offline
// in one flow: IssuesNode.Mkdir (top-level create), ChildrenNode.Mkdir
// (sub-issue create), the comments collection's _create + Unlink, and finally
// IssuesNode.Rmdir (archive). This reaches the sub-issue create handler and the
// issue-archive forget that no offline test touched before.
func TestOffline_IssueLifecycle(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline write-path check; uses the mock mutator")
	}
	enableMockMutations(t)

	// Top-level issue via mkdir (title-only quick create).
	const title = "Offline Issue Lifecycle Probe"
	if err := os.Mkdir(filepath.Join(issuesPath(testTeamKey), title), 0o755); err != nil {
		t.Fatalf("mkdir issue should succeed with mock mutator: %v", err)
	}
	last := lastEntryByTitle(t, issuesLastPath(testTeamKey), title)
	if last == nil {
		t.Fatalf("issues/.last has no entry titled %q", title)
	}
	id := last["identifier"]
	if id == "" {
		t.Fatalf("issues/.last entry missing identifier: %v", last)
	}

	// Sub-issue via children/ mkdir — the parent carries a team, so the handler
	// resolves teamId and creates the child (issues.go ChildrenNode.Mkdir).
	childrenDir := filepath.Join(issueDirPath(testTeamKey, id), "children")
	if err := os.Mkdir(filepath.Join(childrenDir, "Offline Sub Probe"), 0o755); err != nil {
		t.Fatalf("mkdir sub-issue should succeed: %v", err)
	}
	if firstRealEntry(mustReadDir(t, childrenDir)) == "" {
		t.Errorf("children/ empty after sub-issue create")
	}

	// Comment via _create, then delete via unlink.
	if err := writeToWriteOnly(t, newCommentPath(testTeamKey, id), "[TEST] offline comment probe"); err != nil {
		t.Fatalf("create comment via _create should succeed: %v", err)
	}
	commentFile := firstRealEntry(mustReadDir(t, commentsPath(testTeamKey, id)))
	if commentFile == "" {
		t.Fatalf("no comment file after create")
	}
	if err := os.Remove(commentFilePath(testTeamKey, id, commentFile)); err != nil {
		t.Fatalf("unlink (delete) comment should succeed: %v", err)
	}
	if dirContains(commentsPath(testTeamKey, id), commentFile) {
		t.Errorf("deleted comment %q still present", commentFile)
	}

	// Archive the issue via rmdir; it must leave the team's issues listing.
	if err := os.Remove(issueDirPath(testTeamKey, id)); err != nil {
		t.Fatalf("rmdir (archive) issue should succeed: %v", err)
	}
	if dirContains(issuesPath(testTeamKey), id) {
		t.Errorf("archived issue %q still in listing (resurrection — forget failed)", id)
	}
}

// TestOffline_NamedFileCreate covers the NodeCreater variant of the collection
// create surface — creating an entity by writing a NAMED .md file (how an editor
// or the Claude Code Write tool creates one), distinct from the _create trigger.
// This reaches LabelsNode.Create and CommentsNode.Create, which the _create
// path bypasses.
func TestOffline_NamedFileCreate(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline write-path check; uses the mock mutator")
	}
	enableMockMutations(t)

	// Label via a named file. Use a name whose labelFilename is the file we
	// write, so the scratch name and the canonical name coincide.
	const labelFile = "NamedLabelProbe.md"
	labelSpec := []byte("---\nname: NamedLabelProbe\ncolor: \"#00aaff\"\n---\n")
	if err := os.WriteFile(labelFilePath(testTeamKey, labelFile), labelSpec, 0o644); err != nil {
		t.Fatalf("create label via named file should succeed: %v", err)
	}
	if !dirContains(labelsPath(testTeamKey), labelFile) {
		t.Fatalf("named-file label %q not in labels listing", labelFile)
	}
	_ = os.Remove(labelFilePath(testTeamKey, labelFile)) // best-effort cleanup

	// Comment via a named file needs an issue; create one offline first.
	const title = "Offline Named Comment Probe"
	if err := os.Mkdir(filepath.Join(issuesPath(testTeamKey), title), 0o755); err != nil {
		t.Fatalf("mkdir issue: %v", err)
	}
	last := lastEntryByTitle(t, issuesLastPath(testTeamKey), title)
	if last == nil {
		t.Fatalf("issues/.last has no entry titled %q", title)
	}
	id := last["identifier"]

	if err := os.WriteFile(commentFilePath(testTeamKey, id, "named-comment.md"), []byte("[TEST] named comment probe"), 0o644); err != nil {
		t.Fatalf("create comment via named file should succeed: %v", err)
	}
	if firstRealEntry(mustReadDir(t, commentsPath(testTeamKey, id))) == "" {
		t.Errorf("no comment present after named-file create")
	}

	_ = os.Remove(issueDirPath(testTeamKey, id)) // best-effort cleanup (archive)
}

// TestOffline_RelationCreateAndDelete drives the relations listingDir delete
// path: create an outgoing relation via _create, then rm the .rel file. Because
// go-fuse dispatches unlink to the parent directory node (not the file node),
// this exercises RelationsNode.Unlink — the relation must leave the listing and
// not resurrect on the next readdir. The inverse endpoint (a projection of the
// same edge) rejects deletion with EPERM.
//
// The mount is shared, so relate two freshly-minted issues (not fixture issues
// other tests also relate) — a duplicate same-named edge would mask a failed
// delete.
func TestOffline_RelationCreateAndDelete(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline write-path check; uses the mock mutator")
	}
	enableMockMutations(t)

	mkIssue := func(title string) string {
		if err := os.Mkdir(filepath.Join(issuesPath(testTeamKey), title), 0o755); err != nil {
			t.Fatalf("mkdir issue %q: %v", title, err)
		}
		last := lastEntryByTitle(t, issuesLastPath(testTeamKey), title)
		if last == nil || last["identifier"] == "" {
			t.Fatalf("issues/.last has no identifier for %q", title)
		}
		return last["identifier"]
	}
	src := mkIssue("Offline Relation Source Probe")
	dst := mkIssue("Offline Relation Target Probe")

	relationsDir := filepath.Join(issueDirPath(testTeamKey, src), "relations")
	if err := writeToWriteOnly(t, filepath.Join(relationsDir, "_create"), "blocks "+dst); err != nil {
		t.Fatalf("create relation via _create should succeed: %v", err)
	}
	rel := "blocks-" + dst + ".rel"
	if !dirContains(relationsDir, rel) {
		t.Fatalf("created relation %q not in relations listing", rel)
	}

	// The inverse endpoint on the target is the same edge seen from the other
	// side; deleting it is EPERM (delete from the owning side).
	inverseDir := filepath.Join(issueDirPath(testTeamKey, dst), "relations")
	inverse := "blocked-by-" + src + ".rel"
	if !dirContains(inverseDir, inverse) {
		t.Fatalf("inverse relation %q not present on target", inverse)
	}
	if err := os.Remove(filepath.Join(inverseDir, inverse)); !os.IsPermission(err) {
		t.Errorf("rm inverse relation: want EPERM, got %v", err)
	}
	if !dirContains(inverseDir, inverse) {
		t.Errorf("inverse relation %q vanished after a rejected rm", inverse)
	}

	// rm the outgoing .rel — the actual regression: unlink must reach the mutation
	// and the relation must leave the listing, on both endpoints.
	if err := os.Remove(filepath.Join(relationsDir, rel)); err != nil {
		t.Fatalf("unlink (delete) relation should succeed: %v", err)
	}
	if dirContains(relationsDir, rel) {
		t.Errorf("deleted relation %q still in listing (forget failed / silent no-op)", rel)
	}
	if dirContains(inverseDir, inverse) {
		t.Errorf("inverse relation %q still present after the edge was deleted", inverse)
	}

	_ = os.Remove(issueDirPath(testTeamKey, src)) // best-effort cleanup (archive)
	_ = os.Remove(issueDirPath(testTeamKey, dst))
}

// TestOffline_ProjectLinkCreateAndDelete drives the links listingDir delete path
// (LinksNode.Unlink): create a project external link via _create, then rm the
// .link file — it must leave the listing and not resurrect.
func TestOffline_ProjectLinkCreateAndDelete(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline write-path check; uses the mock mutator")
	}
	enableMockMutations(t)

	linksDir := filepath.Join(projectsPath(testTeamKey), "test-project", "links")
	if err := writeToWriteOnly(t, filepath.Join(linksDir, "_create"), "https://example.com/offline-link-probe Offline Link Probe"); err != nil {
		t.Fatalf("create link via _create should succeed: %v", err)
	}
	const link = "Offline Link Probe.link"
	if !dirContains(linksDir, link) {
		t.Fatalf("created link %q not in links listing", link)
	}

	if err := os.Remove(filepath.Join(linksDir, link)); err != nil {
		t.Fatalf("unlink (delete) link should succeed: %v", err)
	}
	if dirContains(linksDir, link) {
		t.Errorf("deleted link %q still in listing (forget failed / silent no-op)", link)
	}
}

// TestOffline_AttachmentCreateAndDelete drives the attachments listingDir delete
// path (AttachmentsNode.Unlink): link an external attachment via _create, then
// rm the .link file — it must leave the listing and not resurrect.
func TestOffline_AttachmentCreateAndDelete(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode offline write-path check; uses the mock mutator")
	}
	enableMockMutations(t)

	attDir := attachmentsPath(testTeamKey, "TST-1")
	if err := writeToWriteOnly(t, filepath.Join(attDir, "_create"), "https://example.com/offline-att-probe Offline Att Probe"); err != nil {
		t.Fatalf("create attachment via _create should succeed: %v", err)
	}
	const att = "Offline Att Probe.link"
	if !dirContains(attDir, att) {
		t.Fatalf("created attachment %q not in attachments listing", att)
	}

	if err := os.Remove(filepath.Join(attDir, att)); err != nil {
		t.Fatalf("unlink (delete) attachment should succeed: %v", err)
	}
	if dirContains(attDir, att) {
		t.Errorf("deleted attachment %q still in listing (forget failed / silent no-op)", att)
	}
}

// mustReadDir reads a directory or fails the test.
func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	return entries
}
