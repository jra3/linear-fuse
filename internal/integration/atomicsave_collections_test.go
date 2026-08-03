package integration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Atomic save-via-rename inside the dynamic collections (#438).
//
// Every editor and the Claude Code Edit/Write tools save the same way: create a
// sibling temp file, write it, rename(2) it over the target. The entity
// directories have supported that since #145, but the collections rejected the
// temp-file create with EINVAL — so a save failed at its FIRST syscall, before
// the rename, and the reported workaround was a whole-file `cat >` overwrite.

// collectionDirs names every directory that holds an editable .md and therefore
// must accept the save dance. The identifiers come from the workspace
// (someIssueID / someProjectDir), never a fixture literal, so these run in live
// mode too — see #395.
func collectionDirs(t *testing.T) []struct{ name, dir string } {
	t.Helper()
	issueID := someIssueID(t)
	return []struct{ name, dir string }{
		{"docs", docsPath(testTeamKey, issueID)},
		{"comments", commentsPath(testTeamKey, issueID)},
		{"labels", labelsPath(testTeamKey)},
		{"milestones", filepath.Join(someProjectDir(t), "milestones")},
	}
}

// TestAtomicSaveTempFileAccepted is the #438 repro, on every collection: opening
// the temp file must succeed. It runs in every mode because it mutates nothing —
// a scratch file lives in memory and is discarded, so no request reaches Linear.
func TestAtomicSaveTempFileAccepted(t *testing.T) {
	for _, tc := range collectionDirs(t) {
		t.Run(tc.name, func(t *testing.T) {
			// The exact shape the Claude Code Edit tool writes:
			// <target>.tmp.<pid>.<rand>.
			tmp := filepath.Join(tc.dir, "probe.md.tmp.2838402.4b8cdf9a26b5")
			f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
			if err != nil {
				if errors.Is(err, syscall.EINVAL) {
					t.Fatalf("creating an atomic-save temp file in %s returned EINVAL (#438 regression): %v", tc.name, err)
				}
				if errors.Is(err, syscall.EROFS) {
					t.Fatalf("creating an atomic-save temp file in %s returned EROFS on an rw mount (#145 regression): %v", tc.name, err)
				}
				t.Fatalf("create temp file in %s: %v", tc.name, err)
			}

			const body = "scratch bytes the editor is about to rename into place\n"
			if _, err := f.Write([]byte(body)); err != nil {
				_ = f.Close()
				_ = os.Remove(tmp)
				t.Fatalf("write temp file in %s: %v", tc.name, err)
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(tmp)
				t.Fatalf("close temp file in %s: %v", tc.name, err)
			}

			// An editor reads its own temp file back (VS Code verifies the write
			// before renaming), so the scratch buffer has to serve what it took.
			if got, err := os.ReadFile(tmp); err != nil {
				_ = os.Remove(tmp)
				t.Fatalf("read back temp file in %s: %v", tc.name, err)
			} else if string(got) != body {
				_ = os.Remove(tmp)
				t.Fatalf("temp file in %s read back %q, want the bytes written", tc.name, got)
			}

			// The scratch file is not an item: it must not show up in the listing
			// as one, or `ls` would advertise a document that does not exist.
			entries, err := os.ReadDir(tc.dir)
			if err != nil {
				_ = os.Remove(tmp)
				t.Fatalf("readdir %s: %v", tc.name, err)
			}
			for _, e := range entries {
				if e.Name() == filepath.Base(tmp) {
					_ = os.Remove(tmp)
					t.Fatalf("%s lists the scratch temp file %s as an item", tc.name, e.Name())
				}
			}

			// An aborted save unlinks its temp file; that must succeed, or a
			// cancelled edit leaves an editor reporting a broken filesystem.
			if err := os.Remove(tmp); err != nil {
				t.Fatalf("remove abandoned temp file in %s: %v", tc.name, err)
			}
		})
	}
}

// TestAtomicSaveOntoNonItemNameIsRefused pins the one rejection this path can
// produce without touching Linear: a scratch renamed onto another non-.md name
// has nowhere to persist. It must fail loudly AND leave the reason in .error —
// the errno alone cannot say where a save is allowed to land. Mutates nothing
// (the refusal precedes any API call), so it runs in every mode.
func TestAtomicSaveOntoNonItemNameIsRefused(t *testing.T) {
	dir := docsPath(testTeamKey, someIssueID(t))

	tmp := filepath.Join(dir, "probe.md.tmp.1.aaaa")
	if err := os.WriteFile(tmp, []byte("body\n"), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmp) }()

	err := os.Rename(tmp, filepath.Join(dir, "probe.md.tmp.2.bbbb"))
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("rename onto a non-.md name = %v, want EINVAL", err)
	}

	reason, rerr := os.ReadFile(filepath.Join(dir, ".error"))
	if rerr != nil {
		t.Fatalf("read docs/.error after the refused rename: %v", rerr)
	}
	for _, want := range []string{"probe.md.tmp.1.aaaa", "<name>.md"} {
		if !strings.Contains(string(reason), want) {
			t.Errorf(".error after a refused save does not mention %q:\n%s", want, reason)
		}
	}

	// The scratch survives a refusal, so the editor's corrected rename can still
	// save the bytes it is holding.
	if _, err := os.ReadFile(tmp); err != nil {
		t.Errorf("the scratch file did not survive a refused rename: %v", err)
	}
}

// TestOffline_AtomicSaveOnDocPersists is the other half of #438: the save must
// actually LAND, not merely be accepted. The temp-file tests above run without
// the mock mutator, so their renames cannot persist; this one drives the whole
// path — scratch create, rename onto an existing doc, flush through that
// document's own edit path, re-read through the mount.
//
// It saves onto a doc it creates under a throwaway issue, never a fixture-seeded
// one: the rename consumes the scratch node and drops the target's inode, which
// made reusing a shared fixture node unreliable across -count reruns (see
// TestOffline_AtomicRenameEditPersists), and a doc added to the shared TST-1
// would break the fixture-listing tests that count what is there. The mock also
// models an update from its own create state, so a doc it never minted would
// lose its parent association.
func TestOffline_AtomicSaveOnDocPersists(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode offline edit-persistence check; uses the mock mutator")
	enableMockMutations(t)

	dir := docsPath(testTeamKey, createRefreshTestIssue(t, "Atomic Save Doc Probe"))
	target := createProbeDoc(t, dir, "Atomic Save Target")

	const marker = "atomic save persistence probe ZZZ"
	if err := claudeToolAtomicSave(t, target, []byte("# Atomic Save Target\n\n"+marker+"\n")); err != nil {
		t.Fatalf("atomic save onto %s should persist with the mock mutator: %v", filepath.Base(target), err)
	}

	after, err := readFileWithRetry(target, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read %s: %v", filepath.Base(target), err)
	}
	if !strings.Contains(string(after), marker) {
		t.Fatalf("atomic save did not persist marker %q\n--- got ---\n%s", marker, after)
	}
}

// TestOffline_AtomicSaveOntoNewNameCreates covers the destination a collection
// has and an entity directory does not: a name that does not exist yet. The save
// must create the document, exactly as writing to docs/"Title.md" does — an
// editor saving a new file under a new name is the same operation.
//
// Like the test above it works on a throwaway issue: this one MINTS a document,
// and doing that in the shared TST-1 would change what the fixture-listing tests
// count.
func TestOffline_AtomicSaveOntoNewNameCreates(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode offline create check; uses the mock mutator")
	enableMockMutations(t)

	dir := docsPath(testTeamKey, createRefreshTestIssue(t, "Atomic Save Create Probe"))
	before := docNameSet(t, dir)

	tmp := filepath.Join(dir, "Atomic Save Newcomer.md.tmp.7.f00d")
	if err := os.WriteFile(tmp, []byte("# Atomic Save Newcomer\n\nminted by a rename\n"), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "Atomic Save Newcomer.md")); err != nil {
		_ = os.Remove(tmp)
		t.Fatalf("rename onto a new .md name should create the document: %v", err)
	}

	fresh := newDocName(t, dir, before)
	content, err := readFileWithRetry(filepath.Join(dir, fresh), defaultWaitTime)
	if err != nil {
		t.Fatalf("read the created document %s: %v", fresh, err)
	}
	if !strings.Contains(string(content), "Atomic Save Newcomer") {
		t.Errorf("created document %s does not carry the saved title:\n%s", fresh, content)
	}
}

// createProbeDoc mints a throwaway document in dir through the _create trigger
// and returns its path. The filename Linear (here, the fake) assigns is derived
// from what it stored, not from the title we sent, so it is discovered by
// diffing the listing rather than predicted.
func createProbeDoc(t *testing.T, dir, title string) string {
	t.Helper()
	before := docNameSet(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "_create"), []byte("# "+title+"\n\nseed body\n"), 0644); err != nil {
		t.Fatalf("create probe document: %v", err)
	}
	return filepath.Join(dir, newDocName(t, dir, before))
}

// docNameSet is the set of item .md files currently listed in a collection.
func docNameSet(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !isControlFile(e.Name()) && strings.HasSuffix(e.Name(), ".md") {
			names[e.Name()] = true
		}
	}
	return names
}

// newDocName returns the single .md that appeared in dir since the before set.
func newDocName(t *testing.T, dir string, before map[string]bool) string {
	t.Helper()
	for name := range docNameSet(t, dir) {
		if !before[name] {
			return name
		}
	}
	t.Fatalf("no new document appeared in %s", dir)
	return ""
}
