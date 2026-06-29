package integration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// =============================================================================
// Write-contract suite (#142)
//
// These tests codify, in CI, the invariants every writable surface must uphold
// to be friendly to LLM/editor tools. They emulate the I/O patterns Claude
// Code's Edit/Write tools and fsync-ing editors (vim, VS Code) use: open, write,
// fsync, close — and temp-file + rename.
//
// The contract (per surface):
//
//  1. fsync is supported — write+fsync never returns ENOTSUP (#139).
//     - TestClaudeToolFsyncSupportedOnWritableFiles (editable files)
//     - TestWriteContractFsyncOnCreateFiles (_create trigger files)
//  2. Read-your-writes — after a successful write, a re-read reflects the
//     written content; a silent revert/truncation is surfaced, never hidden
//     (#141/#136).
//     - TestReadYourWritesLargeBody (live) + writeback_test.go (unit)
//  3. Loud invalid input — bad input yields EINVAL + a populated .error, never
//     a bare EIO and never silent success (#140).
//     - TestWriteInvalidInputIsLoud, TestMkdirIssueFailureIsLegible
//     - TestErrorFileExposedOnWritableSurfaces (every surface exposes .error)
//  4. Atomic-rename / overwrite writes don't corrupt the node (#137).
//     - TestOverwriteDocKeepsNodeReadable, TestWriteContractAtomicRenameNoCorruption
//
// Layers: fixture mode (default `make test`, no API) covers the structural
// invariants (1)(3)(4); live write mode (LINEARFS_WRITE_TESTS=1) covers the
// persistence invariant (2).
// =============================================================================

// errorVisibilityWait bounds how long a test polls for a freshly-set .error to
// become visible. Setting an error invalidates the kernel inode asynchronously
// (InodeNotify), so under full-suite load the new content can take a beat to
// surface; a generous deadline keeps the assertion robust without flaking.
const errorVisibilityWait = 3 * time.Second

// claudeToolFsync opens path read-write and calls fsync, returning the fsync
// error. This isolates the exact syscall #139 was about: ENOTSUP is returned
// by the FUSE layer when a node lacks a Fsync implementation, independent of
// whether bytes were written first. A nil result means fsync is supported.
func claudeToolFsync(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// claudeToolWrite emulates the full Claude Write/Edit tool save: truncate, write
// the new contents, fsync, close. It fails the test if fsync returns an error
// (the #139 regression) or if the close-time commit fails.
func claudeToolWrite(t *testing.T, path string, content []byte) {
	t.Helper()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open %s for write: %v", path, err)
	}

	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		t.Fatalf("write %s: %v", path, err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.ENOTSUP) {
			t.Fatalf("fsync returned ENOTSUP on %s (issue #139 regression): %v", path, err)
		}
		t.Fatalf("fsync %s: %v", path, err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("close/commit %s: %v", path, err)
	}
}

// claudeToolSaveExpectingError emulates the atomic save-via-rename that Claude
// Code's Edit/Write tools and editors (vim, VS Code) actually use: write a
// sibling temp file, then rename it over the target. It returns the error the
// rename surfaces. Unlike a raw truncate+write+close, the rename routes the bytes
// straight through the directory's Rename handler, which runs the target's Flush
// (and its validation) inline and returns the errno directly — so a rejected
// write fails loudly and deterministically, instead of having the verdict masked
// when the kernel serves an O_TRUNC+write entirely from a primed page cache.
// Used to assert that invalid writes fail loudly rather than succeeding silently.
func claudeToolSaveExpectingError(t *testing.T, path string, content []byte) error {
	t.Helper()

	tmp := path + ".tmp.999.deadbeef"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("create temp file for %s: %v", path, err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		t.Fatalf("write temp file for %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		t.Fatalf("close temp file for %s: %v", path, err)
	}
	err = os.Rename(tmp, path)
	_ = os.Remove(tmp) // best-effort cleanup if the rename did not consume it
	return err
}

// firstWritableFile returns the first non-hidden, non-_create entry in dir.
func firstWritableFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "_create" || strings.HasPrefix(name, ".") {
			continue
		}
		return filepath.Join(dir, name), nil
	}
	return "", fmt.Errorf("no writable file found in %s", dir)
}

// firstInitiativeDir returns the path to the first initiative directory.
func firstInitiativeDir() (string, error) {
	entries, err := os.ReadDir(initiativesPath())
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			return initiativePath(e.Name()), nil
		}
	}
	return "", fmt.Errorf("no initiative directory found")
}

// TestClaudeToolFsyncSupportedOnWritableFiles is the core #139 regression guard.
// It runs in the default fixture mode (no API key, no network): for each kind of
// writable file Claude's Edit tool touches, it verifies fsync does not return
// ENOTSUP. Opening without writing keeps the node clean (dirty=false), so close
// is a no-op and no API call is made.
func TestClaudeToolFsyncSupportedOnWritableFiles(t *testing.T) {
	cases := []struct {
		name string
		path func(t *testing.T) string
	}{
		{
			name: "issue.md",
			path: func(t *testing.T) string { return issueFilePath(testTeamKey, "TST-1") },
		},
		{
			name: "project.md",
			path: func(t *testing.T) string {
				return filepath.Join(projectsPath(testTeamKey), "test-project", "project.md")
			},
		},
		{
			name: "initiative.md",
			path: func(t *testing.T) string {
				dir, err := firstInitiativeDir()
				if err != nil {
					t.Skipf("no initiative fixture: %v", err)
				}
				return filepath.Join(dir, "initiative.md")
			},
		},
		{
			name: "comment",
			path: func(t *testing.T) string {
				p, err := firstWritableFile(commentsPath(testTeamKey, "TST-1"))
				if err != nil {
					t.Skipf("no comment fixture: %v", err)
				}
				return p
			},
		},
		{
			name: "label",
			path: func(t *testing.T) string {
				p, err := firstWritableFile(labelsPath(testTeamKey))
				if err != nil {
					t.Skipf("no label fixture: %v", err)
				}
				return p
			},
		},
		{
			name: "document",
			path: func(t *testing.T) string {
				p, err := firstWritableFile(docsPath(testTeamKey, "TST-1"))
				if err != nil {
					t.Skipf("no document fixture: %v", err)
				}
				return p
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path(t)
			if err := claudeToolFsync(path); err != nil {
				if errors.Is(err, syscall.ENOTSUP) {
					t.Fatalf("fsync returned ENOTSUP on %s (issue #139 regression): %v", path, err)
				}
				t.Fatalf("fsync %s: %v", path, err)
			}
		})
	}
}

// TestWriteContractFsyncOnCreateFiles extends the #139 fsync guarantee to the
// write-only _create trigger files (#142 contract item 1). Editors that
// write-then-fsync must be able to save through a _create file; fsync must
// never return ENOTSUP. _create is mode 0200, so it is opened write-only.
func TestWriteContractFsyncOnCreateFiles(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"comments/_create", newCommentPath(testTeamKey, "TST-1")},
		{"docs/_create", newDocPath(testTeamKey, "TST-1")},
		{"labels/_create", filepath.Join(labelsPath(testTeamKey), "_create")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.OpenFile(tc.path, os.O_WRONLY, 0200)
			if err != nil {
				t.Fatalf("open %s write-only: %v", tc.path, err)
			}
			defer f.Close()
			if err := f.Sync(); err != nil {
				if errors.Is(err, syscall.ENOTSUP) {
					t.Fatalf("fsync returned ENOTSUP on %s (issue #139 regression): %v", tc.path, err)
				}
				t.Fatalf("fsync %s: %v", tc.path, err)
			}
		})
	}
}

// TestWriteContractAtomicRenameNoCorruption covers #142 contract item 4 for the
// failure direction: writing a temp file and rename()-ing it over an existing
// doc must never leave the target corrupted (unreadable/EACCES) — the #137
// failure mode. In fixture mode the rename itself may not succeed (no API), but
// the target document node must remain readable either way.
func TestWriteContractAtomicRenameNoCorruption(t *testing.T) {
	target, err := firstWritableFile(docsPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Skipf("no document fixture: %v", err)
	}

	orig, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target doc: %v", err)
	}
	// Restore the target's in-memory content so this test doesn't pollute others.
	defer func() {
		if f, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			_, _ = f.Write(orig)
			_ = f.Close()
		}
	}()

	// Write a temp file in the same directory, then rename it over the target.
	tmp := filepath.Join(docsPath(testTeamKey, "TST-1"), "atomic-rename-tmp.md")
	if f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); err == nil {
		_, _ = f.Write([]byte("---\ntitle: Atomic\n---\n\nrenamed body\n"))
		_ = f.Close()
	}
	_ = os.Rename(tmp, target) // may fail (EXDEV/ENOENT/EIO); must not corrupt target
	_ = os.Remove(tmp)         // best-effort cleanup if the rename did not consume it

	// The target must still be readable — never EACCES (the #137 corruption).
	if _, err := os.ReadFile(target); err != nil {
		if errors.Is(err, syscall.EACCES) {
			t.Fatalf("target doc became unreadable after rename-over (#137 regression): %v", err)
		}
		t.Fatalf("target doc not readable after rename-over: %v", err)
	}
}

// TestWriteContractAtomicRenameCreateNoEROFS covers #145 symptom #1 on every
// single-editable-file surface: an editor's atomic save (create a sibling temp
// file, write it, rename it over the editable file) must not be rejected with a
// misleading EROFS ("read-only file system") on an rw mount. The decisive
// assertion is that *creating* the temp file in the directory succeeds — that
// was the EROFS the Claude Code Edit tool, vim, and VS Code all hit. The rename
// itself may fail in fixture mode (no live API), but it must never leave the
// target unreadable/corrupted.
func TestWriteContractAtomicRenameCreateNoEROFS(t *testing.T) {
	cases := []struct {
		name    string
		dirFile func(t *testing.T) (dir, file string) // directory + editable filename
	}{
		{
			name: "issue",
			dirFile: func(t *testing.T) (string, string) {
				return issueDirPath(testTeamKey, "TST-1"), "issue.md"
			},
		},
		{
			name: "project",
			dirFile: func(t *testing.T) (string, string) {
				return filepath.Join(projectsPath(testTeamKey), "test-project"), "project.md"
			},
		},
		{
			name: "initiative",
			dirFile: func(t *testing.T) (string, string) {
				dir, err := firstInitiativeDir()
				if err != nil {
					t.Skipf("no initiative fixture: %v", err)
				}
				return dir, "initiative.md"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, editable := tc.dirFile(t)
			target := filepath.Join(dir, editable)

			orig, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read %s: %v", editable, err)
			}

			// Create a sibling temp file the way an atomic-save editor does. Before
			// #145 this returned EROFS even though the mount is rw and the editable
			// file is writable.
			tmp := filepath.Join(dir, editable+".tmp.999.deadbeef")
			f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
			if err != nil {
				if errors.Is(err, syscall.EROFS) {
					t.Fatalf("creating an atomic-save temp file returned EROFS on an rw mount (#145 regression): %v", err)
				}
				t.Fatalf("create temp file in %s dir: %v", tc.name, err)
			}
			if _, err := f.Write(orig); err != nil {
				_ = f.Close()
				t.Fatalf("write temp file: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("close temp file: %v", err)
			}

			// The temp file must read back what we wrote while it's still a scratch file.
			if got, err := os.ReadFile(tmp); err != nil {
				t.Fatalf("read scratch temp file: %v", err)
			} else if string(got) != string(orig) {
				t.Fatalf("scratch temp file content mismatch: wrote %d bytes, read %d", len(orig), len(got))
			}

			// Rename it over the editable file. In fixture mode the persisting write
			// fails (no API), so the rename may return an error; what matters is no
			// corruption.
			_ = os.Rename(tmp, target)
			_ = os.Remove(tmp) // best-effort cleanup if the rename did not consume it

			if _, err := os.ReadFile(target); err != nil {
				if errors.Is(err, syscall.EACCES) {
					t.Fatalf("%s became unreadable after rename-over (#145 corruption): %v", editable, err)
				}
				t.Fatalf("%s not readable after rename-over: %v", editable, err)
			}
		})
	}
}

// TestErrorFileExposedOnWritableSurfaces is the #140 structural guard: every
// writable entity directory must expose a readable `.error` feedback file so a
// failed write is legible to an LLM/script instead of a bare errno. It asserts
// the file exists and is readable on every surface; that a failed write
// *populates* it is covered by TestWriteInvalidInputIsLoud and
// TestMkdirIssueFailureIsLegible. (Emptiness is not asserted: in the shared-mount
// suite another test may have left a collection .error populated.)
func TestErrorFileExposedOnWritableSurfaces(t *testing.T) {
	cases := []struct {
		name string
		dir  func(t *testing.T) string
	}{
		{
			name: "issue",
			dir:  func(t *testing.T) string { return issueDirPath(testTeamKey, "TST-1") },
		},
		{
			name: "project",
			dir: func(t *testing.T) string {
				return filepath.Join(projectsPath(testTeamKey), "test-project")
			},
		},
		{
			name: "initiative",
			dir: func(t *testing.T) string {
				dir, err := firstInitiativeDir()
				if err != nil {
					t.Skipf("no initiative fixture: %v", err)
				}
				return dir
			},
		},
		{
			name: "comments",
			dir:  func(t *testing.T) string { return commentsPath(testTeamKey, "TST-1") },
		},
		{
			name: "docs",
			dir:  func(t *testing.T) string { return docsPath(testTeamKey, "TST-1") },
		},
		{
			name: "labels",
			dir:  func(t *testing.T) string { return labelsPath(testTeamKey) },
		},
		{
			name: "milestones",
			dir: func(t *testing.T) string {
				dir := filepath.Join(projectsPath(testTeamKey), "test-project", "milestones")
				if _, err := os.Stat(dir); err != nil {
					t.Skipf("no milestones fixture: %v", err)
				}
				return dir
			},
		},
		{
			name: "attachments",
			dir:  func(t *testing.T) string { return attachmentsPath(testTeamKey, "TST-1") },
		},
		{
			name: "relations",
			dir: func(t *testing.T) string {
				dir := filepath.Join(issueDirPath(testTeamKey, "TST-1"), "relations")
				if _, err := os.Stat(dir); err != nil {
					t.Skipf("no relations dir: %v", err)
				}
				return dir
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errPath := filepath.Join(tc.dir(t), ".error")
			if _, err := os.ReadFile(errPath); err != nil {
				t.Fatalf("read %s: %v", errPath, err)
			}
		})
	}
}

// TestWriteInvalidInputIsLoud is the #140/#142 "loud invalid input" guard:
// writing an unresolvable initiative to project.md must fail with EINVAL and
// leave a populated .error naming the bad value — never a bare EIO and never
// silent success. The unknown-initiative check resolves against local SQLite,
// so this runs in fixture mode with no network.
func TestWriteInvalidInputIsLoud(t *testing.T) {
	dir := filepath.Join(projectsPath(testTeamKey), "test-project")
	path := filepath.Join(dir, "project.md")
	errPath := filepath.Join(dir, ".error")

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project.md: %v", err)
	}
	// Restore original content (clears .error on a clean flush).
	defer func() {
		if f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			_, _ = f.Write(orig)
			_ = f.Close()
		}
	}()

	const badValue = "__no_such_initiative__"
	bad := []byte("---\nname: Test Project\ninitiatives: [\"" + badValue + "\"]\n---\n\nbody\n")

	werr := claudeToolSaveExpectingError(t, path, bad)
	if !errors.Is(werr, syscall.EINVAL) {
		t.Fatalf("expected EINVAL writing unknown initiative, got %v", werr)
	}

	// The .error becomes visible once the kernel's attr-cache invalidation
	// (InodeNotify) propagates; poll briefly. An agent reads .error well after
	// this window, so eventual visibility is the real-world contract.
	data := readFileUntilContains(t, errPath, badValue, errorVisibilityWait)
	if !strings.Contains(string(data), badValue) {
		t.Fatalf(".error should name the rejected value %q, got %q", badValue, data)
	}
}

// TestMkdirIssueFailureIsLegible is the #131 guard: when creating an issue via
// mkdir fails, it must fail loudly with a populated issues/.error rather than
// hanging or silently no-opping. In fixture mode the API call fails (no auth),
// which is non-retryable, so mkdir returns an error and issues/.error explains
// it. (A rate-limited/timed-out create instead returns EAGAIN; same .error.)
func TestMkdirIssueFailureIsLegible(t *testing.T) {
	newIssueDir := filepath.Join(issuesPath(testTeamKey), "Mkdir Legibility Probe")

	err := os.Mkdir(newIssueDir, 0755)
	if err == nil {
		// Should not happen in fixture mode (no API); clean up if it somehow did.
		_ = os.Remove(newIssueDir)
		t.Skip("issue creation unexpectedly succeeded (live API?); skipping legibility check")
	}

	errPath := filepath.Join(issuesPath(testTeamKey), ".error")
	data := readFileUntilContains(t, errPath, "create issue", errorVisibilityWait)
	if !strings.Contains(string(data), "create issue") {
		t.Fatalf("issues/.error should explain the failed creation, got: %q", data)
	}
}

// TestOverwriteDocKeepsNodeReadable guards #137: overwriting an existing doc
// file in place — the truncate+write pattern cp / mv / editor save-over use —
// must leave the node readable and writable, never a write-only _create node
// that returns EACCES on every subsequent read. Runs in fixture mode: the write
// itself won't persist (no API), but the node must stay a real document node.
func TestOverwriteDocKeepsNodeReadable(t *testing.T) {
	docPath, err := firstWritableFile(docsPath(testTeamKey, "TST-1"))
	if err != nil {
		t.Skipf("no document fixture: %v", err)
	}

	orig, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("doc not readable before overwrite: %v", err)
	}
	// Restore the in-memory content so this test doesn't pollute the shared
	// fixture doc for other tests (the overwrite below does not persist without
	// an API, but it does dirty the node's cached content).
	defer func() {
		if f, err := os.OpenFile(docPath, os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			_, _ = f.Write(orig)
			_ = f.Close()
		}
	}()

	// Overwrite in place the way cp/mv/editors do: O_CREAT|O_TRUNC|O_WRONLY.
	f, err := os.OpenFile(docPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open doc for overwrite: %v", err)
	}
	_, _ = f.Write([]byte("---\ntitle: Overwritten\n---\n\nnew body\n"))
	_ = f.Close() // may fail at flush (no API in fixture mode); the node must survive

	// The node must still be readable — not EACCES (the #137 corruption signature).
	if _, err := os.ReadFile(docPath); err != nil {
		if errors.Is(err, syscall.EACCES) {
			t.Fatalf("doc became unreadable after overwrite (#137 regression): %v", err)
		}
		t.Fatalf("doc not readable after overwrite: %v", err)
	}

	// ...and still writable (open for write must not EACCES).
	wf, err := os.OpenFile(docPath, os.O_WRONLY, 0644)
	if err != nil {
		if errors.Is(err, syscall.EACCES) {
			t.Fatalf("doc became unwritable after overwrite (#137 regression): %v", err)
		}
		t.Fatalf("doc not writable after overwrite: %v", err)
	}
	_ = wf.Close()
}

// readFileUntilContains polls path until its content contains want or maxWait
// elapses, returning the last content read.
func readFileUntilContains(t *testing.T, path, want string, maxWait time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(maxWait)
	var data []byte
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return data
		}
		time.Sleep(10 * time.Millisecond)
	}
	return data
}

// TestReadYourWritesLargeBody is the #141/#136 guard: a normal large-body write
// to issue.md must persist faithfully and must NOT trip a false-positive
// read-your-writes violation. claudeToolWrite fails the test if close returns an
// error, so a spurious EIO from the write-back check would fail here. Requires
// live-API write mode (it creates and edits a real issue).
func TestReadYourWritesLargeBody(t *testing.T) {
	skipIfNoWriteTests(t)

	issue, cleanup, err := createTestIssue("Read Your Writes Large Body")
	if err != nil {
		t.Fatalf("create test issue: %v", err)
	}
	defer cleanup()

	path := issueFilePath(testTeamKey, issue.Identifier)
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}

	// Build a >3KB unique body (the #136 threshold) and append it after the
	// existing frontmatter+content.
	marker := fmt.Sprintf("linearfs-ryw-%d", time.Now().UnixNano())
	var b strings.Builder
	b.WriteString(strings.TrimRight(string(orig), "\n"))
	b.WriteString("\n\n## " + marker + "\n\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "Line %d of the large body for %s, padded to exceed the 3KB write threshold.\n", i, marker)
	}
	claudeToolWrite(t, path, []byte(b.String())) // fails if close returns an error

	waitForCacheExpiry()

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read issue.md: %v", err)
	}
	if !strings.Contains(string(after), marker) {
		t.Fatalf("large body did not persist (read-your-writes failed); marker %q missing", marker)
	}

	// And .error must be empty — the write was faithful.
	errData, _ := os.ReadFile(filepath.Join(issueDirPath(testTeamKey, issue.Identifier), ".error"))
	if strings.TrimSpace(string(errData)) != "" {
		t.Fatalf("expected empty .error after a faithful write, got: %q", errData)
	}
}

// TestClaudeToolEditPersistsProjectDescription covers the second half of #139:
// a write+fsync to project.md must actually persist the description to Linear.
// It edits a real project and restores it, so it requires live-API write mode.
func TestClaudeToolEditPersistsProjectDescription(t *testing.T) {
	skipIfNoWriteTests(t)

	projectsDir := projectsPath(testTeamKey)
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		t.Fatalf("read projects dir: %v", err)
	}

	var path string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(projectsDir, e.Name(), "project.md")
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		t.Skip("no project with project.md in test team; skipping persistence test")
	}

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original project.md: %v", err)
	}

	// Emulate Claude's Edit tool: append a unique marker to the description body
	// and save the full new contents with write+fsync.
	marker := fmt.Sprintf("linearfs-smoke-%d", time.Now().UnixNano())
	edited := append([]byte(strings.TrimRight(string(orig), "\n")), []byte("\n\n"+marker+"\n")...)
	claudeToolWrite(t, path, edited)

	// Restore the original description regardless of the assertion outcome.
	defer claudeToolWrite(t, path, orig)

	waitForCacheExpiry()

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read project.md: %v", err)
	}
	if !strings.Contains(string(after), marker) {
		t.Fatalf("project description did not persist marker %q\n--- got ---\n%s", marker, after)
	}
}

// TestClaudeToolEditPersistsInitiativeDescription is the initiative counterpart
// to the project persistence test. It skips if the workspace has no initiative.
func TestClaudeToolEditPersistsInitiativeDescription(t *testing.T) {
	skipIfNoWriteTests(t)

	dir, err := firstInitiativeDir()
	if err != nil {
		t.Skipf("no initiative in workspace: %v", err)
	}
	path := filepath.Join(dir, "initiative.md")
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original initiative.md: %v", err)
	}

	marker := fmt.Sprintf("linearfs-smoke-%d", time.Now().UnixNano())
	edited := append([]byte(strings.TrimRight(string(orig), "\n")), []byte("\n\n"+marker+"\n")...)
	claudeToolWrite(t, path, edited)
	defer claudeToolWrite(t, path, orig)

	waitForCacheExpiry()

	after, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read initiative.md: %v", err)
	}
	if !strings.Contains(string(after), marker) {
		t.Fatalf("initiative description did not persist marker %q\n--- got ---\n%s", marker, after)
	}
}
