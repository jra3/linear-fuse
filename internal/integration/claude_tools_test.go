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
// Claude Code tool smoke tests
//
// These tests emulate the I/O pattern Claude Code's Edit and Write tools use
// when saving a file: open, write the new contents, fsync, then close. Other
// fsync-ing editors (vim, VS Code) follow the same pattern.
//
// Regression target: issue #139. Several writable nodes (project.md,
// initiative.md, comment .md, and the _create files) did not implement
// NodeFsyncer, so the kernel's FUSE_FSYNC was answered with ENOTSUP. Editors
// treat a failed fsync as a failed save and drop the write, which made project
// and initiative descriptions impossible to edit. The second half of #139 was
// that even on a successful flush the description was never sent to the API —
// ProjectInfoNode/InitiativeInfoNode.Flush only synced initiative associations.
// =============================================================================

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

// claudeToolWriteExpectingError emulates a save (truncate, write, close) and
// returns the error surfaced at close, where Flush runs. Used to assert that
// invalid writes fail loudly rather than succeeding silently.
func claudeToolWriteExpectingError(t *testing.T, path string, content []byte) error {
	t.Helper()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open %s for write: %v", path, err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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

// TestErrorFileExposedOnWritableSurfaces is the #140 structural guard: every
// writable entity directory must expose a readable `.error` feedback file so a
// failed write is legible to an LLM/script instead of a bare errno. It runs in
// fixture mode (no API): with no prior failed write the file reads empty.
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
			data, err := os.ReadFile(errPath)
			if err != nil {
				t.Fatalf("read %s: %v", errPath, err)
			}
			if len(data) != 0 {
				t.Fatalf("expected empty .error with no prior failed write, got %q", data)
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

	werr := claudeToolWriteExpectingError(t, path, bad)
	if !errors.Is(werr, syscall.EINVAL) {
		t.Fatalf("expected EINVAL writing unknown initiative, got %v", werr)
	}

	// The .error becomes visible once the kernel's attr-cache invalidation
	// (InodeNotify) propagates; poll briefly. An agent reads .error well after
	// this window, so eventual visibility is the real-world contract.
	data := readFileUntilContains(t, errPath, badValue, defaultWaitTime)
	if !strings.Contains(string(data), badValue) {
		t.Fatalf(".error should name the rejected value %q, got %q", badValue, data)
	}
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
