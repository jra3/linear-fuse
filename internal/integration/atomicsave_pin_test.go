package integration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/marshal"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// #379 end-to-end, at the syscall level a real editor uses.
//
// TestOffline_AtomicSaveServesTheWrittenBytes (edit_persist_test.go) pins the
// issue.md size after a reformat that normalizeMarkdown forgives — so it fixes
// the byte count but leaves .error clear. The report's actual shape was the other
// one: Linear rewrote inline markup it does NOT forgive, so the save landed BOTH
// an informational "saved, but Linear reformatted the markdown" note AND a
// changed byte count, and the client read the pair as data loss. These tests
// cover that shape, and the two atomic-save sites the issue never touched
// (project.md, initiative.md).

// codeSpanReformat models the transform the #379 report quoted: Linear rewriting
// inline markup around a code span, here by padding it with spaces. Unlike a
// blank-line collapse it is NOT one of the reflows normalizeMarkdown forgives
// (writeback.go), so the write-back check records its non-fatal reformat note —
// and the stored body is 2 bytes LONGER than what was written, which is exactly
// the "is N bytes on disk, expected N-2" delta the report carried.
func codeSpanReformat(body string) string {
	return strings.ReplaceAll(body, "the`pin`holds", "the `pin` holds")
}

// atomicSave writes content the way every editor and the Claude Code Write tool
// do: to a scratch sibling, then rename(2) over the canonical file. It is the
// path #365's in-place serve-your-own-writes never covered.
func atomicSave(t *testing.T, path string, content []byte) {
	t.Helper()
	tmp := fmt.Sprintf("%s.tmp.%d.5aved", path, len(content))
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		t.Fatalf("write scratch temp %s: %v", filepath.Base(tmp), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		t.Fatalf("rename %s over %s: %v", filepath.Base(tmp), filepath.Base(path), err)
	}
}

// TestOffline_AtomicSaveVerifyAfterReformatNote is the report reproduced whole:
// save issue.md atomically, then make the two verification syscalls a client
// makes next (stat, then open+read) while Linear reformats the body it stores.
// Before the pin, both answered from the re-render of what PERSISTED, so the
// size was the reformatted length and the client — told by .error only that the
// markdown was reformatted — reported the successful write as possible silent
// truncation.
func TestOffline_AtomicSaveVerifyAfterReformatNote(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode check; needs the mock mutator's reformat-on-store")
	enableMockMutations(t, mockmutation.WithBodyReformat(codeSpanReformat))

	// Throwaway issue: the atomic save consumes a scratch node, so the shared
	// fixtures stay untouched (same reason as the sibling test in edit_persist).
	identifier := createRefreshTestIssue(t, "Atomic Save Verify Probe")
	path := issueFilePath(testTeamKey, identifier)
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse issue.md: %v", err)
	}
	doc.Body = strings.TrimRight(doc.Body, "\n") + "\n\nverified read-back is what the`pin`holds"
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render issue.md: %v", err)
	}

	t.Logf("$ printf %%s \"<edited>\" > %s.tmp.N && mv %s.tmp.N %s   # the atomic save every editor performs",
		filepath.Base(path), filepath.Base(path), filepath.Base(path))
	atomicSave(t, path, edited)
	t.Logf("  wrote %d bytes to %s/issue.md", len(edited), identifier)

	// No retry loop anywhere below: the point is what the very next syscalls see.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat issue.md after atomic save: %v", err)
	}
	t.Logf("$ stat -c %%s issue.md          -> %d   (client expected %d)", fi.Size(), len(edited))

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read issue.md after atomic save: %v", err)
	}
	t.Logf("$ cat issue.md | cmp - written  -> %s", map[bool]string{true: "identical", false: "DIFFERS"}[bytes.Equal(after, edited)])
	t.Logf("$ cat %s/.error\n%s", identifier, readIssueError(t, identifier))

	if fi.Size() != int64(len(edited)) {
		t.Errorf("stat size after atomic save = %d, want %d — this is the #379 report: %q",
			fi.Size(), len(edited),
			fmt.Sprintf("issue.md is %d bytes on disk, expected %d ... may have silently truncated the write",
				fi.Size(), len(edited)))
	}
	if !bytes.Equal(after, edited) {
		t.Errorf("read-back after atomic save is not what was written\n--- wrote ---\n%q\n--- got ---\n%q", edited, after)
	}
	t.Logf("client verdict: %s", map[bool]string{
		true:  "write verified byte-for-byte; the .error note is informational",
		false: "MISMATCH — reads as a possible truncated write (#379)",
	}[fi.Size() == int64(len(edited)) && bytes.Equal(after, edited)])

	// The .error note is the other half of the report: it must still SAY the
	// server reformatted (that is true and the agent needs it), while telling the
	// agent the immediate read-back is its own bytes.
	gotErr := readIssueError(t, identifier)
	if !strings.Contains(gotErr, "saved, but Linear reformatted the markdown") {
		t.Errorf(".error after a server-side reformat = %q, want the informational reformat note", gotErr)
	}
	if !strings.Contains(gotErr, "an immediate re-read still shows your own bytes") {
		t.Errorf(".error note does not describe the pinned read-back (#379): %q", gotErr)
	}
}

// TestOffline_AtomicSaveDoesNotPinOverRealDataLoss is the other half of the
// contract, and the reason the pin is armed only on a clean commit: when the
// server does NOT store what was written, the loss has to stay visible. Here the
// fake truncates the body, so the write-back check calls it fatal (EIO) — the
// rename must fail, .error must say the value was truncated, and the re-read must
// show the SHORT stored body, not the bytes the client wrote. A pin taken on EIO
// would echo the full text back and hide the loss behind a byte-for-byte match.
func TestOffline_AtomicSaveDoesNotPinOverRealDataLoss(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode check; needs the mock mutator's reformat-on-store")
	const kept = "refresh-retry probe body"
	// Keep a prefix rather than reverting to the previous body, so the write-back
	// check classifies this as truncation and not as the (also fatal) silent
	// revert — the same EIO outcome by the path #379 is actually about.
	enableMockMutations(t, mockmutation.WithBodyReformat(func(body string) string {
		if len(body) > 40 {
			return body[:40]
		}
		return body
	}))

	identifier := createRefreshTestIssue(t, "Atomic Save Truncation Probe")
	path := issueFilePath(testTeamKey, identifier)
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse issue.md: %v", err)
	}
	doc.Body = kept + "\n\na long paragraph of text the server is about to drop on the floor"
	edited, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render issue.md: %v", err)
	}

	tmp := fmt.Sprintf("%s.tmp.%d.5aved", path, len(edited))
	if err := os.WriteFile(tmp, edited, 0o644); err != nil {
		t.Fatalf("write scratch temp: %v", err)
	}
	renameErr := os.Rename(tmp, path)
	_ = os.Remove(tmp)
	t.Logf("$ mv issue.md.tmp.N issue.md   # wrote %d bytes -> %v", len(edited), renameErr)
	if renameErr == nil {
		t.Errorf("atomic save over a truncating server returned success; want EIO so the client sees the loss")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read issue.md after the failed save: %v", err)
	}
	t.Logf("$ cat issue.md | cmp - written  -> %s (%d bytes on disk vs %d written)",
		map[bool]string{true: "identical", false: "DIFFERS"}[bytes.Equal(after, edited)], len(after), len(edited))
	t.Logf("$ cat %s/.error\n%s", identifier, readIssueError(t, identifier))

	if bytes.Equal(after, edited) {
		t.Error("the written bytes were served back over a truncated save — a pin on EIO hides real data loss (#365/#379)")
	}
	if strings.Contains(string(after), "drop on the floor") {
		t.Errorf("re-read still shows the dropped paragraph; want the short stored body\n--- got ---\n%q", after)
	}
	if e := readIssueError(t, identifier); !strings.Contains(e, "substantially shorter") {
		t.Errorf(".error after a truncating save = %q, want the fatal truncation report", e)
	}
}

// TestOffline_AtomicSaveServesWrittenBytesForProjectAndInitiative covers the two
// atomic-save sites the #379 report never exercised. renameSave is entity-neutral
// and all three canonical files route through it, so a pin wired at only the
// issue path would leave these two reading back Linear's render — the same
// false-truncation signal, on project.md and initiative.md.
func TestOffline_AtomicSaveServesWrittenBytesForProjectAndInitiative(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode check; needs the mock mutator's reformat-on-store")
	enableMockMutations(t, mockmutation.WithBodyReformat(codeSpanReformat))

	initiativeDir, err := firstInitiativeDir()
	if err != nil {
		t.Skipf("no initiative fixture: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"project.md", projectMDPath()},
		{"initiative.md", filepath.Join(initiativeDir, "initiative.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig, err := readFileWithRetry(tc.path, defaultWaitTime)
			if err != nil {
				t.Fatalf("read %s: %v", tc.name, err)
			}
			// Restore through the SAME atomic path, not an in-place write: that
			// supersedes this test's pin with the original bytes, so a later test
			// re-looking up the file inside the pin window sees the fixture.
			t.Cleanup(func() { atomicSave(t, tc.path, orig) })

			doc, err := marshal.Parse(orig)
			if err != nil {
				t.Fatalf("parse %s: %v", tc.name, err)
			}
			doc.Body = strings.TrimRight(doc.Body, "\n") + "\n\nverified read-back is what the`pin`holds"
			edited, err := marshal.Render(doc)
			if err != nil {
				t.Fatalf("render %s: %v", tc.name, err)
			}

			atomicSave(t, tc.path, edited)
			t.Logf("$ mv %s.tmp.N %s   # wrote %d bytes", tc.name, tc.name, len(edited))

			fi, err := os.Stat(tc.path)
			if err != nil {
				t.Fatalf("stat %s after atomic save: %v", tc.name, err)
			}
			after, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("re-read %s after atomic save: %v", tc.name, err)
			}
			t.Logf("$ stat -c %%s %s -> %d (expected %d); read-back %s",
				tc.name, fi.Size(), len(edited),
				map[bool]string{true: "identical", false: "DIFFERS"}[bytes.Equal(after, edited)])

			if fi.Size() != int64(len(edited)) {
				t.Errorf("stat size after atomic save of %s = %d, want %d (the bytes written)",
					tc.name, fi.Size(), len(edited))
			}
			if !bytes.Equal(after, edited) {
				t.Errorf("read-back of %s is not what was written\n--- wrote ---\n%q\n--- got ---\n%q",
					tc.name, edited, after)
			}
		})
	}
}
