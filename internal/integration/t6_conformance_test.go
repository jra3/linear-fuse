package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// T6 (#157): additive write-contract conformance. These tests assert the unified
// contract holds across entities end-to-end; they add no production code.

// parseLastSidecar reads a .last file and returns its YAML list (empty allowed).
func parseLastSidecar(t *testing.T, path string) []map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	var entries []map[string]string
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("%s is not a YAML list: %v\n%s", path, err, data)
	}
	return entries
}

// assertEditableOnly fails if the editable file at path contains any forbidden
// (server-managed) frontmatter field.
func assertEditableOnly(t *testing.T, path string, forbidden ...string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, f := range forbidden {
		if _, ok := doc.Frontmatter[f]; ok {
			t.Errorf("%s should not contain server field %q (belongs in .meta)", filepath.Base(path), f)
		}
	}
}

// TestWriteContractMetaSplitGeneralizes: the editable-in/server-out rule holds
// for issue.md, project.md, AND initiative.md — each editable file is free of
// volatile server fields and has a sibling .meta carrying them.
func TestWriteContractMetaSplitGeneralizes(t *testing.T) {
	forbidden := []string{"id", "slug", "url", "updated"}

	// issue
	assertEditableOnly(t, issueFilePath(testTeamKey, "TST-1"), append(forbidden, "identifier")...)
	assertMetaHasFields(t, issueMetaPath(testTeamKey, "TST-1"), "id", "identifier", "url", "updated")

	// project
	projMD := filepath.Join(projectsPath(testTeamKey), "test-project", "project.md")
	assertEditableOnly(t, projMD, forbidden...)
	assertMetaHasFields(t, projectMetaPath(testTeamKey, "test-project"), "id", "slug", "url", "updated")

	// initiative (skip if none)
	inits, err := os.ReadDir(initiativesPath())
	if err == nil && firstRealEntry(inits) != "" {
		slug := firstRealEntry(inits)
		assertEditableOnly(t, filepath.Join(initiativesPath(), slug, "initiative.md"), forbidden...)
		assertMetaHasFields(t, initiativeMetaPath(slug), "id", "slug", "url", "updated")
	}
}

// firstItemFile returns the first "{base}.md" collection item in dir (skipping
// the _create/.error/.last trio and the .meta sidecars), or "" if none.
func firstItemFile(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no %s: %v", dir, err)
	}
	for _, e := range entries {
		if isControlFile(e.Name()) || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		return e.Name()
	}
	return ""
}

// TestWriteContractMetaSplitCollections extends the meta split to the four
// small collection entities: every item .md is free of server-managed fields
// (its edits used to be silently discarded — a no-op with no .error), every
// item has an openable read-only "{base}.meta" carrying them, and the sidecar
// can be neither written nor rm'd on its own.
func TestWriteContractMetaSplitCollections(t *testing.T) {
	surfaces := []struct {
		name      string
		dir       string
		forbidden []string
		metaHas   []string
	}{
		{
			name:      "docs",
			dir:       docsPath(testTeamKey, "TST-1"),
			forbidden: []string{"id", "url", "created", "updated", "creator", "slug"},
			metaHas:   []string{"id", "url", "created", "updated"},
		},
		{
			name:      "labels",
			dir:       labelsPath(testTeamKey),
			forbidden: []string{"id"},
			metaHas:   []string{"id"},
		},
		{
			name:      "milestones",
			dir:       filepath.Join(projectsPath(testTeamKey), "test-project", "milestones"),
			forbidden: []string{"id"},
			metaHas:   []string{"id"},
		},
	}

	for _, tc := range surfaces {
		t.Run(tc.name, func(t *testing.T) {
			mdName := firstItemFile(t, tc.dir)
			if mdName == "" && tc.name == "milestones" && !liveAPIMode {
				// The fixture ships no milestone: seed one via the mock so the
				// sidecar contract isn't vacuously green here.
				enableMockMutations(t)
				if err := os.WriteFile(filepath.Join(tc.dir, "_create"), []byte("MetaSplit Probe\nprobe milestone"), 0200); err != nil {
					t.Fatalf("seed milestone: %v", err)
				}
				mdName = firstItemFile(t, tc.dir)
			}
			if mdName == "" {
				t.Skipf("no %s item in fixture", tc.name)
			}
			assertEditableOnly(t, filepath.Join(tc.dir, mdName), tc.forbidden...)

			metaName := strings.TrimSuffix(mdName, ".md") + ".meta"
			metaPath := filepath.Join(tc.dir, metaName)
			assertMetaHasFields(t, metaPath, tc.metaHas...)

			// The sidecar is listed alongside its .md (listed⇔openable).
			entries, err := os.ReadDir(tc.dir)
			if err != nil {
				t.Fatalf("read %s: %v", tc.dir, err)
			}
			listed := false
			for _, e := range entries {
				if e.Name() == metaName {
					listed = true
				}
			}
			if !listed {
				t.Errorf("%s resolves but is not listed in the directory", metaName)
			}

			// Read-only: writes are rejected, and rm of the sidecar alone is too
			// (it vanishes with its entity, via rm of the .md).
			if info, err := os.Stat(metaPath); err == nil && info.Mode().Perm() != 0444 {
				t.Errorf("%s mode = %v, want 0444", metaName, info.Mode().Perm())
			}
			if err := os.WriteFile(metaPath, []byte("x"), 0644); err == nil {
				t.Errorf("%s is writable, want read-only", metaName)
			}
			if err := os.Remove(metaPath); err == nil {
				t.Errorf("rm %s succeeded, want EPERM (the sidecar vanishes with its .md)", metaName)
			}
		})
	}

	// Comments: the .md is the PURE body (no frontmatter at all); id/author/
	// timestamps live in the sidecar.
	t.Run("comments", func(t *testing.T) {
		dir := commentsPath(testTeamKey, "TST-1")
		mdName := firstItemFile(t, dir)
		if mdName == "" {
			t.Skip("no comment in fixture")
		}
		content, err := os.ReadFile(filepath.Join(dir, mdName))
		if err != nil {
			t.Fatalf("read %s: %v", mdName, err)
		}
		if strings.HasPrefix(string(content), "---") {
			t.Errorf("comment .md carries frontmatter, want pure body:\n%s", content)
		}
		metaName := strings.TrimSuffix(mdName, ".md") + ".meta"
		assertMetaHasFields(t, filepath.Join(dir, metaName), "id", "created", "updated")
	})
}

// TestWriteContractLastSidecarShape: .last on a collection is read-only (0444)
// and parses as a YAML list of {identifier,url,path,title,status}. Creates one
// issue first (via the mock) so the key assertions aren't vacuous on an empty log.
func TestWriteContractLastSidecarShape(t *testing.T) {
	info, err := os.Stat(issuesLastPath(testTeamKey))
	if err != nil {
		t.Fatalf("stat issues/.last: %v", err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Errorf(".last should be read-only; mode=%v", info.Mode())
	}

	if !liveAPIMode {
		enableMockMutations(t)
		if err := os.Mkdir(issueDirPath(testTeamKey, "Last Shape Probe"), 0755); err != nil {
			t.Fatalf("seed create: %v", err)
		}
	}

	entries := parseLastSidecar(t, issuesLastPath(testTeamKey))
	if !liveAPIMode && len(entries) == 0 {
		t.Fatal("expected at least one .last entry after a create")
	}
	for _, e := range entries {
		for _, k := range []string{"identifier", "url", "path", "title", "status"} {
			if _, ok := e[k]; !ok {
				t.Errorf(".last entry missing key %q: %v", k, e)
			}
		}
	}
}

// TestWriteContractCreateTrioUniform: every _create-bearing collection exposes
// the full feedback trio — _create, .error, .last — in both Lookup and the
// directory listing, and the two surfaces the #148 design deferred (attachments,
// relations) now report their creates to .last like every other surface.
func TestWriteContractCreateTrioUniform(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode; uses the mock mutator")
	}
	enableMockMutations(t)

	issueDir := issueDirPath(testTeamKey, "TST-1")
	relationsDir := filepath.Join(issueDir, "relations")
	projectUpdatesDir := filepath.Join(projectsPath(testTeamKey), "test-project", "updates")
	surfaces := map[string]string{
		"issues":             issuesPath(testTeamKey),
		"comments":           commentsPath(testTeamKey, "TST-1"),
		"docs":               docsPath(testTeamKey, "TST-1"),
		"labels":             labelsPath(testTeamKey),
		"milestones":         filepath.Join(projectsPath(testTeamKey), "test-project", "milestones"),
		"attachments":        attachmentsPath(testTeamKey, "TST-1"),
		"relations":          relationsDir,
		"project-updates":    projectUpdatesDir,
		"initiative-updates": filepath.Join(initiativePath("test-initiative"), "updates"),
	}
	for name, dir := range surfaces {
		t.Run(name, func(t *testing.T) {
			// Lookup resolves each of the trio.
			for _, f := range []string{"_create", ".error", ".last"} {
				if _, err := os.Lstat(filepath.Join(dir, f)); err != nil {
					t.Errorf("%s/%s not resolvable: %v", name, f, err)
				}
			}
			// The listing shows the trio too (guards Readdir wiring).
			d, err := os.Open(dir)
			if err != nil {
				t.Fatalf("open %s: %v", dir, err)
			}
			names, err := d.Readdirnames(-1)
			_ = d.Close()
			if err != nil {
				t.Fatalf("readdirnames %s: %v", dir, err)
			}
			listed := map[string]bool{}
			for _, n := range names {
				listed[n] = true
			}
			for _, f := range []string{"_create", ".error", ".last"} {
				if !listed[f] {
					t.Errorf("%s listing missing %s", name, f)
				}
			}
		})
	}

	// A relation create reports its identity to relations/.last.
	if err := os.WriteFile(filepath.Join(relationsDir, "_create"), []byte("related TST-2"), 0200); err != nil {
		t.Fatalf("create relation: %v", err)
	}
	rfound := false
	for _, e := range parseLastSidecar(t, filepath.Join(relationsDir, ".last")) {
		if e["path"] == "related-TST-2.rel" {
			rfound = true
		}
	}
	if !rfound {
		t.Error("relations/.last has no entry after a relation create")
	}

	// An attachment create reports its identity to attachments/.last.
	attURL := "https://example.com/trio-probe/1"
	if err := os.WriteFile(filepath.Join(attachmentsPath(testTeamKey, "TST-1"), "_create"), []byte(attURL+" Trio Probe"), 0200); err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	afound := false
	for _, e := range parseLastSidecar(t, filepath.Join(attachmentsPath(testTeamKey, "TST-1"), ".last")) {
		if e["url"] == attURL {
			afound = true
		}
	}
	if !afound {
		t.Error("attachments/.last has no entry after an attachment create")
	}

	// Updates were the last surface hand-rolling the create tail with no
	// .error/.last at all. An unrecognized health value is rejected (EINVAL),
	// not silently coerced to onTrack, and the reason lands in updates/.error.
	if err := os.WriteFile(filepath.Join(projectUpdatesDir, "_create"), []byte("---\nhealth: critical\n---\nOn fire"), 0200); err == nil {
		t.Error("invalid health accepted; want EINVAL")
	}
	errContent, err := os.ReadFile(filepath.Join(projectUpdatesDir, ".error"))
	if err != nil {
		t.Fatalf("read updates/.error: %v", err)
	}
	if !strings.Contains(string(errContent), "Field: health") {
		t.Errorf("updates/.error missing health rejection, got: %s", errContent)
	}

	// A successful update create clears .error and reports to updates/.last.
	if err := os.WriteFile(filepath.Join(projectUpdatesDir, "_create"), []byte("---\nhealth: atRisk\n---\nTrio probe update"), 0200); err != nil {
		t.Fatalf("create project update: %v", err)
	}
	ufound := false
	for _, e := range parseLastSidecar(t, filepath.Join(projectUpdatesDir, ".last")) {
		if e["status"] == "atRisk" && e["title"] == "Trio probe update" {
			ufound = true
		}
	}
	if !ufound {
		t.Error("updates/.last has no entry after an update create")
	}
	if errContent, err := os.ReadFile(filepath.Join(projectUpdatesDir, ".error")); err != nil {
		t.Fatalf("read updates/.error after success: %v", err)
	} else if len(errContent) != 0 {
		t.Errorf("updates/.error not cleared by a successful create: %s", errContent)
	}

	// Consecutive creates through the same _create path both land: the write
	// buffer lives on the per-open handle, so the kernel reusing the cached
	// node for a second open-write-close cycle must not swallow the create
	// (the old per-node buffers latched after the first success and silently
	// no-op'd every create after it).
	if err := os.WriteFile(filepath.Join(projectUpdatesDir, "_create"), []byte("Back-to-back probe"), 0200); err != nil {
		t.Fatalf("second consecutive update create: %v", err)
	}
	bfound := false
	for _, e := range parseLastSidecar(t, filepath.Join(projectUpdatesDir, ".last")) {
		if e["title"] == "Back-to-back probe" {
			bfound = true
		}
	}
	if !bfound {
		t.Error("second consecutive create through the same _create node was swallowed (no .last entry)")
	}
}

// TestWriteContractDeleteTail: a deleted entry vanishes from the listing
// immediately. This is the resurrection-bug discriminator: SQLite is the
// listing source of truth, and label deletes never removed the row, so before
// the delete tail a removed label reappeared on the next readdir until the
// sync worker reconciled. Also asserts a successful delete clears .error and
// an unknown name fails with ENOENT.
func TestWriteContractDeleteTail(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode; uses the mock mutator")
	}
	enableMockMutations(t)

	// Create a label to delete.
	if err := os.WriteFile(filepath.Join(labelsPath(testTeamKey), "_create"),
		[]byte("---\nname: delete-tail-probe\ncolor: \"#ff0000\"\n---\n"), 0200); err != nil {
		t.Fatalf("create label: %v", err)
	}
	var labelFile string
	for _, e := range parseLastSidecar(t, filepath.Join(labelsPath(testTeamKey), ".last")) {
		if e["title"] == "delete-tail-probe" {
			labelFile = e["path"]
		}
	}
	if labelFile == "" {
		t.Fatal("labels/.last has no entry for the probe label")
	}

	if err := os.Remove(filepath.Join(labelsPath(testTeamKey), labelFile)); err != nil {
		t.Fatalf("rm label: %v", err)
	}

	// Gone from the listing at once — no resurrection from a stale SQLite row.
	d, err := os.Open(labelsPath(testTeamKey))
	if err != nil {
		t.Fatalf("open labels/: %v", err)
	}
	names, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil {
		t.Fatalf("readdirnames labels/: %v", err)
	}
	for _, n := range names {
		if n == labelFile {
			t.Fatalf("deleted label %q still listed (SQLite row not forgotten?)", labelFile)
		}
	}

	// A successful delete clears the collection .error.
	if data, _ := os.ReadFile(filepath.Join(labelsPath(testTeamKey), ".error")); strings.TrimSpace(string(data)) != "" {
		t.Errorf("labels/.error non-empty after a successful delete: %q", data)
	}

	// Removing an unknown name fails with ENOENT (via lookup; the tail's own
	// not-found branch is unit-tested — a mounted rm never reaches Unlink
	// without a resolvable entry).
	if err := os.Remove(filepath.Join(labelsPath(testTeamKey), "no-such-label.md")); !os.IsNotExist(err) {
		t.Errorf("rm of unknown label = %v, want ENOENT", err)
	}
}

// TestWriteContractAgentLoop exercises the end-to-end shape an agent uses:
// batch create via _create (recover ids from .last), no-op rewrites that stay
// byte-stable, and a failure that leaves the success log intact.
func TestWriteContractAgentLoop(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode agent-loop; uses the mock mutator")
	}
	enableMockMutations(t)

	// (a) Batch create via N sequential _create writes; recover every id from .last.
	marker := "agentloop-batch"
	for i := 0; i < 2; i++ {
		spec := fmt.Sprintf("---\ntitle: %s-%d\npriority: medium\n---\nbody %d\n", marker, i, i)
		if err := writeCreateSpec(t, spec); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	entries := parseLastSidecar(t, issuesLastPath(testTeamKey))
	found := 0
	for _, e := range entries {
		if strings.HasPrefix(e["title"], marker) {
			found++
			if _, err := os.ReadFile(issueFilePath(testTeamKey, e["path"])); err != nil {
				t.Errorf("created issue %q from .last not reachable: %v", e["path"], err)
			}
		}
	}
	if found < 2 {
		t.Fatalf("expected >=2 %q entries in .last, found %d", marker, found)
	}

	// (a2) A non-issue create surface also reports to its .last: create a comment
	// on TST-1 and confirm comments/.last gains an entry (guards the other five
	// AppendWriteSuccess producers beyond team-issue creates).
	commentBody := "agentloop comment marker"
	if err := os.WriteFile(newCommentPath(testTeamKey, "TST-1"), []byte(commentBody), 0200); err != nil {
		t.Fatalf("create comment: %v", err)
	}
	commentsLast := filepath.Join(commentsPath(testTeamKey, "TST-1"), ".last")
	cfound := false
	for _, e := range parseLastSidecar(t, commentsLast) {
		if strings.Contains(e["title"], "agentloop comment marker") {
			cfound = true
		}
	}
	if !cfound {
		t.Errorf("comments/.last has no entry after a comment create")
	}

	// (b) No-op rewrite of an editable file is byte-stable and clears .error.
	noopByteStable(t, issueFilePath(testTeamKey, "TST-1"), issueDirPath(testTeamKey, "TST-1"))

	// (c) Same byte-stability for project.md and initiative.md. Unlike (b) —
	// which short-circuits at len(updates)==0 before the tail — a project/
	// initiative no-op still runs the full edit-commit tail. The mock implements
	// fs's verify seam (read-your-writes served from the store), so offline the
	// tail genuinely executes fetch → persist → compare rather than taking the
	// "unverified" early return. TestWriteContractEditVerifiesOffline below
	// exercises the actual-edit direction.
	projDir := filepath.Join(projectsPath(testTeamKey), "test-project")
	noopByteStable(t, filepath.Join(projDir, "project.md"), projDir)
	inits, err := os.ReadDir(initiativesPath())
	if err == nil && firstRealEntry(inits) != "" {
		initDir := filepath.Join(initiativesPath(), firstRealEntry(inits))
		noopByteStable(t, filepath.Join(initDir, "initiative.md"), initDir)
	}

	// (d) A subsequent EINVAL create must not wipe the earlier successes from .last.
	before := len(parseLastSidecar(t, issuesLastPath(testTeamKey)))
	if err := writeCreateSpec(t, "---\ntitle: Doomed\npriority: critical\n---\nx\n"); err == nil {
		t.Fatal("expected EINVAL for invalid priority")
	}
	after := parseLastSidecar(t, issuesLastPath(testTeamKey))
	if len(after) < before {
		t.Errorf(".last shrank after a failed create: %d -> %d (append log should survive)", before, len(after))
	}
}

// TestWriteContractEditVerifiesOffline proves the edit-commit tail runs against
// fake state in fixture mode (the F9 fix): an actual project.md edit goes through
// the mock's read-your-writes verify seam, so the change persists and survives a
// re-read with a clean .error. In the pre-seam behavior the verify fetch 401'd,
// commitWriteBack took its "unverified" early return, persist was skipped, and
// the edit was lost offline — so the marker's survival is a genuine discriminator.
func TestWriteContractEditVerifiesOffline(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode; exercises the mock verify seam")
	}
	enableMockMutations(t)

	projDir := filepath.Join(projectsPath(testTeamKey), "test-project")
	projMD := filepath.Join(projDir, "project.md")
	orig, err := os.ReadFile(projMD)
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}

	// Append a marker to the body (the project description) and save.
	marker := "verify-seam-marker-847"
	edited := append(append([]byte{}, orig...), []byte("\n"+marker+"\n")...)
	claudeToolWrite(t, projMD, edited)

	// The read-your-writes verify ran against the mock's recorded edit and found
	// no divergence, so .error is clean.
	if data, _ := os.ReadFile(filepath.Join(projDir, ".error")); strings.TrimSpace(string(data)) != "" {
		t.Fatalf("project .error non-empty after a verified edit: %q", data)
	}

	// The edit persisted (the tail ran fetch → persist), so it survives a re-read.
	after, err := readFileWithRetry(projMD, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read project.md: %v", err)
	}
	if !strings.Contains(string(after), marker) {
		t.Errorf("edited description marker %q lost after re-read (verify tail did not persist offline):\n%s", marker, after)
	}
}

// noopByteStable writes an editable file's exact bytes back and asserts the
// re-read frontmatter is byte-identical (no self-mutation) and .error is empty.
func noopByteStable(t *testing.T, filePath, dirPath string) {
	t.Helper()
	orig, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read %s: %v", filePath, err)
	}
	if frontmatterOf(string(orig)) == "" {
		t.Fatalf("%s has no frontmatter block — byte-stability check would be vacuous", filepath.Base(filePath))
	}
	claudeToolWrite(t, filePath, orig) // fails the test if close/commit errors

	after, err := readFileWithRetry(filePath, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read %s: %v", filePath, err)
	}
	if frontmatterOf(string(orig)) != frontmatterOf(string(after)) {
		t.Errorf("%s frontmatter changed across a no-op rewrite (self-mutation):\n--- before ---\n%s\n--- after ---\n%s",
			filepath.Base(filePath), frontmatterOf(string(orig)), frontmatterOf(string(after)))
	}
	if data, _ := os.ReadFile(filepath.Join(dirPath, ".error")); strings.TrimSpace(string(data)) != "" {
		t.Errorf("%s: .error non-empty after a faithful no-op write: %q", filepath.Base(filePath), data)
	}
}

// frontmatterOf returns the YAML frontmatter block (between the --- fences).
func frontmatterOf(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return ""
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return ""
	}
	return rest[:end]
}
