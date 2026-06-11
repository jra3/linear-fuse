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
