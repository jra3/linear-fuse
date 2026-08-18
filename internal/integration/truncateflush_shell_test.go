package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// shellStep runs one command through a real /bin/sh and logs it as a transcript
// line, so a -v run of this test reads like the terminal session in the bug
// report rather than like Go plumbing.
func shellStep(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput()
	status := "0"
	if err != nil {
		status = err.Error()
	}
	t.Logf("$ %s\n%s(exit: %s)", script, indentOutput(string(out)), status)
	return string(out)
}

func indentOutput(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  | " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// persistedDescription returns the description that reached the store — what a
// later `curl` against the API would read back in the bug report, modelled here
// by the fake mutator's upsert.
func persistedDescription(t *testing.T, issueID string) string {
	t.Helper()
	row, err := testStore.Queries().GetIssueByID(context.Background(), issueID)
	if err != nil {
		t.Fatalf("read persisted issue %s: %v", issueID, err)
	}
	return row.Description.String
}

// TestShellRedirectTruncateLeavesNoResidue drives the literal reproduction from
// #454 — `printf ... > issue.md` typed at a shell, twice, with each write
// shorter than the last — against the mounted filesystem.
//
// The sibling tests in truncateflush_test.go reconstruct the shell's fd dance
// with dup(2)+close(2) because that is the precise trigger. This one spends a
// real /bin/sh to prove the reconstruction was faithful: it is the sequence an
// agent or script actually emits, and the report's whole point is that the
// obvious thing to type silently corrupted the description and persisted it.
func TestShellRedirectTruncateLeavesNoResidue(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds a row and reads the persisted description back")
	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	enableMockMutations(t)

	path, issueID := seedTruncateProbeIssue(t, "shell")

	t.Logf("--- before: the issue as it stands on the mount ---")
	before := shellStep(t, fmt.Sprintf("cat %q", path))
	if !strings.Contains(before, "ZZZZ-TAIL-MARKER-454") {
		t.Fatalf("seeded issue.md has no tail to leave behind:\n%s", before)
	}

	writes := []struct {
		body string
		doc  string
	}{
		{
			body: "AAAA BBBB CCCC DDDD EEEE FFFF GGGG HHHH IIII JJJJ",
			doc:  `---\ntitle: "THROWAWAY"\n---\nAAAA BBBB CCCC DDDD EEEE FFFF GGGG HHHH IIII JJJJ\n`,
		},
		{
			body: "SHORT",
			doc:  `---\ntitle: "THROWAWAY"\n---\nSHORT\n`,
		},
	}

	for i, w := range writes {
		t.Logf("--- write %d: shell `>` redirect, body %q ---", i+1, w.body)
		shellStep(t, fmt.Sprintf("printf -- %q > %q", w.doc, path))

		got := shellStep(t, fmt.Sprintf("cat %q", path))
		persisted := persistedDescription(t, issueID)
		t.Logf("persisted description (what a later API read returns):\n%s", indentOutput(persisted))

		if strings.Contains(got, "ZZZZ-TAIL-MARKER-454") {
			t.Errorf("write %d: the previous file image survived the truncate on read:\n%s", i+1, got)
		}
		if strings.Contains(persisted, "ZZZZ-TAIL-MARKER-454") {
			t.Errorf("write %d: the previous description survived the truncate and was persisted:\n%s", i+1, persisted)
		}
		if strings.Contains(persisted, "itle:") || strings.Contains(persisted, "title:") {
			t.Errorf("write %d: frontmatter leaked into the persisted description:\n%s", i+1, persisted)
		}
		if trimmed := strings.TrimSpace(persisted); trimmed != w.body {
			t.Errorf("write %d: persisted description = %q, want exactly %q", i+1, trimmed, w.body)
		}
		if i > 0 && strings.Contains(persisted, writes[i-1].body) {
			t.Errorf("write %d: the previous write's body is still bleeding through:\n%s", i+1, persisted)
		}
	}

	// The report's final symptom: a shorter write left a LONGER file behind.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	t.Logf("--- sizes: %d bytes before, %d bytes after two shortening writes ---", len(before), len(after))
	if len(after) >= len(before) {
		t.Errorf("file is %d bytes after two shortening writes from %d — the truncate did not stick",
			len(after), len(before))
	}
}

// TestShellRedirectGuardRailsAfterTruncateFix walks the behaviours that sit
// either side of the #454 fix, at the shell, in the order a user would meet
// them: the #397 promise the fix had to preserve (a rejection still leaves a
// readable entity behind, not zero bytes), that a real write following the
// rejection still lands cleanly, and that the temp-file+rename save the README
// documents is untouched.
func TestShellRedirectGuardRailsAfterTruncateFix(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds a row and reads the persisted description back")
	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	enableMockMutations(t)

	path, issueID := seedTruncateProbeIssue(t, "guardrail")
	dir := strings.TrimSuffix(path, "/issue.md")

	t.Logf("--- 1. emptying the file is still rejected, and a read still shows the issue (#397) ---")
	shellStep(t, fmt.Sprintf(": > %q", path))
	afterEmptying := shellStep(t, fmt.Sprintf("cat %q", path))
	if strings.TrimSpace(afterEmptying) == "" {
		t.Error("emptying issue.md left the file serving zero bytes — #397's restore promise is gone")
	}
	if !strings.Contains(afterEmptying, "ZZZZ-TAIL-MARKER-454") {
		t.Errorf("read after an empty write lost the issue body:\n%s", afterEmptying)
	}
	t.Logf("$ cat %q/.error", dir)
	if b, err := os.ReadFile(dir + "/.error"); err == nil {
		t.Logf("%s", indentOutput(string(b)))
	}
	if persisted := persistedDescription(t, issueID); !strings.Contains(persisted, "ZZZZ-TAIL-MARKER-454") {
		t.Errorf("the rejected empty write reached the store anyway: %q", persisted)
	}

	t.Logf("--- 2. a real `>` write straight after that rejection still lands clean ---")
	shellStep(t, fmt.Sprintf(`printf -- '---\ntitle: "THROWAWAY"\n---\nAFTER REJECTION\n' > %q`, path))
	shellStep(t, fmt.Sprintf("cat %q", path))
	persisted := persistedDescription(t, issueID)
	t.Logf("persisted description:\n%s", indentOutput(persisted))
	if got := strings.TrimSpace(persisted); got != "AFTER REJECTION" {
		t.Errorf("persisted description = %q, want %q", got, "AFTER REJECTION")
	}

	t.Logf("--- 3. the documented temp-file + rename save still works ---")
	tmp := dir + "/.probe.tmp"
	shellStep(t, fmt.Sprintf(`printf -- '---\ntitle: "THROWAWAY"\n---\nVIA RENAME\n' > %q && mv %q %q`, tmp, tmp, path))
	shellStep(t, fmt.Sprintf("cat %q", path))
	persisted = persistedDescription(t, issueID)
	t.Logf("persisted description:\n%s", indentOutput(persisted))
	if got := strings.TrimSpace(persisted); got != "VIA RENAME" {
		t.Errorf("persisted description = %q, want %q", got, "VIA RENAME")
	}
}
