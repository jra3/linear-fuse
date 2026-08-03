package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/marshal"
)

// Team moves via issue.md's `team:` field (#429).
//
// An issue's team used to be read-only on the reasoning that it is fixed; it is
// not. A real workflow — a team key rename plus a new team reusing the old key —
// needed 34 issues moved, and the only way was raw GraphQL against the API,
// because the filesystem could not express the operation at all.
//
// The move is the one edit whose PATH changes: Linear re-numbers the issue into
// the destination team's sequence, so the file it was written through ceases to
// exist and a differently-named one appears under the other team. That is what
// these tests pin — not merely that a mutation was sent.

// TestOffline_TeamMoveRelocatesTheIssue drives the whole path: render → edit →
// resolve → mutate → write-back → re-cohere, and asserts the observable
// consequences on the mount rather than the request.
func TestOffline_TeamMoveRelocatesTheIssue(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode offline move check; uses the mock mutator and the seeded TST/SUB team pair")
	enableMockMutations(t)

	identifier := createRefreshTestIssue(t, "Team Move Probe")
	path := issueFilePath(testTeamKey, identifier)

	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	// The field has to be readable before it can be written: a writer edits what
	// the file shows, and team: renders from the issue's current team.
	if !strings.Contains(string(orig), "team: "+testTeamKey) {
		t.Fatalf("issue.md does not render the editable team key:\n%s", orig)
	}

	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse issue.md: %v", err)
	}
	doc.Frontmatter["team"] = "SUB"
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render issue.md: %v", err)
	}
	claudeToolWrite(t, path, edited)

	// Linear re-numbers a moved issue, so the destination path is discovered
	// from the destination team's listing, not predicted.
	moved := findIssueInTeam(t, "SUB", "Team Move Probe")
	movedDir := issueDirPath("SUB", moved)
	content, err := readFileWithRetry(filepath.Join(movedDir, "issue.md"), defaultWaitTime)
	if err != nil {
		t.Fatalf("read the moved issue at SUB/%s: %v", moved, err)
	}

	// The move must be clean — an .error means the write-back's team check found
	// the issue somewhere other than where it was sent. Read at the DESTINATION:
	// .error is keyed by the issue's UUID (unchanged by a move), and the source
	// path no longer resolves, so reading it there would report "clean" for the
	// uninteresting reason that the file is gone.
	if e, rerr := os.ReadFile(filepath.Join(movedDir, ".error")); rerr == nil && len(strings.TrimSpace(string(e))) > 0 {
		t.Fatalf("team move left an error on the moved issue:\n%s", e)
	}
	if !strings.Contains(string(content), "team: SUB") {
		t.Errorf("the moved issue still renders its old team:\n%s", content)
	}

	// The identifier really changed — the whole reason the path moves. A move
	// that kept the identifier would mean the re-numbering was not reflected,
	// and every cached path would silently point at the wrong team (#427).
	if moved == identifier {
		t.Errorf("moved issue kept its identifier %s; the re-numbering did not land", moved)
	}

	// And the old path is gone. This is the assertion that catches a move which
	// updated the row but left the kernel serving the old listing: the source
	// team must stop offering an issue it no longer owns.
	if _, err := os.Stat(issueDirPath(testTeamKey, identifier)); err == nil {
		t.Errorf("%s still resolves under team %s after the move", identifier, testTeamKey)
	}
	for _, e := range mustReadDir(t, issuesPath(testTeamKey)) {
		if e.Name() == identifier {
			t.Errorf("team %s still lists %s after the move", testTeamKey, identifier)
		}
	}
}

// TestOffline_TeamMoveToUnknownTeamIsLegible pins the rejection: an unknown team
// key must fail the write loudly and say where to look, not silently drop the
// key the way an unknown frontmatter field does (#426) — which is exactly the
// trap this feature removes, and would be worse now that team: looks writable.
func TestOffline_TeamMoveToUnknownTeamIsLegible(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode offline validation check; uses the mock mutator")
	enableMockMutations(t)

	identifier := createRefreshTestIssue(t, "Team Move Reject Probe")
	path := issueFilePath(testTeamKey, identifier)

	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse issue.md: %v", err)
	}
	doc.Frontmatter["team"] = "NOSUCHTEAM"
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render issue.md: %v", err)
	}

	// The atomic-save path returns the flush errno directly; a raw truncate+write
	// can have its verdict masked by the page cache (see claudeToolAtomicSave).
	if err := claudeToolAtomicSave(t, path, edited); err == nil {
		t.Fatal("moving to an unknown team succeeded; want a rejected write")
	}

	reason := readIssueError(t, identifier)
	for _, want := range []string{"team", "NOSUCHTEAM", "teams/"} {
		if !strings.Contains(reason, want) {
			t.Errorf(".error after an unknown-team move does not mention %q:\n%s", want, reason)
		}
	}
	// The issue stayed put.
	if _, err := os.Stat(issueDirPath(testTeamKey, identifier)); err != nil {
		t.Errorf("a rejected move moved the issue anyway: %v", err)
	}
}

// findIssueInTeam returns the identifier of the issue in teamKey whose issue.md
// carries titleFragment. A moved issue's new identifier is assigned by the
// server, so it can only be discovered.
func findIssueInTeam(t *testing.T, teamKey, titleFragment string) string {
	t.Helper()
	for _, e := range mustReadDir(t, issuesPath(teamKey)) {
		if !e.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(issuesPath(teamKey), e.Name(), "issue.md"))
		if err != nil {
			continue
		}
		if strings.Contains(string(content), titleFragment) {
			return e.Name()
		}
	}
	t.Fatalf("team %s lists no issue titled %q", teamKey, titleFragment)
	return ""
}
