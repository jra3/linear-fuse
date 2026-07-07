package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIssueSubdirsReportIssueTimes is the mounted, kernel-level proof of the
// "Attr construction" contract: every entity subdirectory reports the issue's
// times (mtime = updatedAt), not time.Now(). Before the nodeAttr refactor,
// DocsNode/AttachmentsNode.Getattr returned time.Now(), so these stats drifted
// off the issue and reshuffled on every call. The check is fixture-agnostic: it
// only asserts each subdir agrees with the issue's own mtime (issue.md, whose
// mtime is updatedAt), so it holds whatever the fixture's timestamps are.
func TestIssueSubdirsReportIssueTimes(t *testing.T) {
	const issueID = "TST-1"

	issueInfo, err := os.Stat(issueFilePath(testTeamKey, issueID))
	if err != nil {
		t.Fatalf("stat issue.md: %v", err)
	}
	want := issueInfo.ModTime()

	base := issueDirPath(testTeamKey, issueID)
	subdirs := map[string]string{
		"comments":    commentsPath(testTeamKey, issueID),
		"docs":        docsPath(testTeamKey, issueID),
		"attachments": attachmentsPath(testTeamKey, issueID),
		"relations":   filepath.Join(base, "relations"),
		"children":    filepath.Join(base, "children"),
	}

	for name, path := range subdirs {
		t.Run(name, func(t *testing.T) {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s: %v", name, err)
			}
			if !info.IsDir() {
				t.Fatalf("%s is not a directory", name)
			}
			if got := info.ModTime(); !got.Equal(want) {
				t.Errorf("%s mtime = %v, want issue's %v (subdir must report the issue's times, not time.Now())", name, got, want)
			}
		})
	}
}

// TestRenderFileServesFreshContent is the mounted, kernel-level proof of the
// renderFileNode volatility contract on cycle.md — the file whose FOPEN_KEEP_CACHE
// staleness bug motivated the module. It reads through the real kernel READ path
// (DIRECT_IO) and asserts the content is present and the times are sensible
// (non-zero, deterministic across stats). The unit tests cover the read-through
// mechanism; this proves it survives the kernel round-trip.
func TestRenderFileServesFreshContent(t *testing.T) {
	// Find the cycle directory (the non-"current" entry under cycles/).
	entries, err := os.ReadDir(cyclesPath(testTeamKey))
	if err != nil {
		t.Fatalf("read cycles dir: %v", err)
	}
	var cycleDir string
	for _, e := range entries {
		if e.Name() != "current" {
			cycleDir = e.Name()
			break
		}
	}
	if cycleDir == "" {
		t.Skip("no cycle in fixture")
	}
	path := filepath.Join(cyclesPath(testTeamKey), cycleDir, "cycle.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cycle.md: %v", err)
	}
	// Fresh render must carry the frontmatter and a wall-clock-derived status.
	for _, want := range []string{"id: cycle-1", "status:", "progress:"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("cycle.md missing %q in:\n%s", want, data)
		}
	}

	// Times: non-zero and deterministic across two stats (the DIRECT_IO/zero-timeout
	// policy must not fabricate a fresh time.Now() per stat).
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cycle.md (1): %v", err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cycle.md (2): %v", err)
	}
	if first.ModTime().IsZero() {
		t.Error("cycle.md mtime is zero — render node reported no time")
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Errorf("cycle.md mtime not stable across stats: %v vs %v", first.ModTime(), second.ModTime())
	}
}

// TestIssueSubdirStatIsDeterministic stats the same subdirectory twice and
// requires an identical mtime. The old time.Now() Getattr failed this by
// construction — two stats microseconds apart returned different times, which
// reshuffled `ls -lt`. This is the observable form of "a Lookup answer and a
// later stat can never disagree".
func TestIssueSubdirStatIsDeterministic(t *testing.T) {
	base := issueDirPath(testTeamKey, "TST-1")
	for _, name := range []string{"docs", "attachments", "comments", "relations"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(base, name)
			first, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s (1): %v", name, err)
			}
			second, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s (2): %v", name, err)
			}
			if !first.ModTime().Equal(second.ModTime()) {
				t.Errorf("%s mtime not stable across stats: %v vs %v", name, first.ModTime(), second.ModTime())
			}
		})
	}
}
