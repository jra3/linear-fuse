package integration

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/fs"
	"github.com/jra3/linear-fuse/internal/marshal"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// What a failed write leaves behind, at the mount (#494). The unit tests pin
// the two failure arms in isolation and TestRejectedSaveRestoresReadableContent
// walks the unsent arm through a project; these walk the arm the errno cannot
// reach on its own — a mutation that REACHED Linear and came back a failure —
// and the one consequence an agent feels immediately: a later read of a
// rejected file must not re-send the write.
//
// That was #418's measurement: dirty is BUFFER-level, so a buffer left dirty
// after a refused save turned every subsequent close(2) — including a pure
// reader's, which is what `cat` does — back into a flush that re-ran the doomed
// front half. Twenty repeats from one bad write.

// countingGoneMutator is Linear rejecting an issue update because the entity is
// gone, counting the mutations it received. The count is the assertion: the
// stored value cannot tell one rejected write from five.
type countingGoneMutator struct {
	*mockmutation.Client
	calls *atomic.Int32
}

func (m countingGoneMutator) UpdateIssue(ctx context.Context, issueID string, input map[string]any) error {
	m.calls.Add(1)
	return &api.GraphQLError{Message: "Entity not found: Issue - Could not find referenced Issue."}
}

// TestOffline_ServerRejectedSaveIsNotReattemptedByLaterReads: after Linear
// rejects a save, reading the file must stay a read. Before #494 the buffer
// stayed dirty and each of these reads re-sent the same failing mutation.
func TestOffline_ServerRejectedSaveIsNotReattemptedByLaterReads(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: needs the injected mutator to model Linear's not-found rejection, and seeds its own probe row")

	probe := seedIssueProbe(t, "sentfail", "Sent Failure Probe", "Body before the rejected save.")
	var calls atomic.Int32
	injectMutator(t, func(m *mockmutation.Client) fs.MutationClient {
		return countingGoneMutator{Client: m, calls: &calls}
	})

	before, err := os.ReadFile(probe.Path)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	t.Logf("$ cat issue.md\n%s", before)

	edited, err := modifyFrontmatter(before, "title", "Renamed while gone upstream")
	if err != nil {
		t.Fatalf("modify frontmatter: %v", err)
	}
	// Saved IN PLACE, not through a rename: this is the path whose buffer
	// outlives the save, so it is the one where a dirty flag would be re-flushed
	// by whoever opens the file next. close(2) carries the verdict.
	f, err := os.OpenFile(probe.Path, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open issue.md for write: %v", err)
	}
	if _, err := f.Write(edited); err != nil {
		_ = f.Close()
		t.Fatalf("write issue.md: %v", err)
	}
	saveErr := f.Close()
	t.Logf("$ save issue.md (in place)  -> %v", saveErr)
	if !errors.Is(saveErr, syscall.ENOENT) {
		t.Fatalf("save under Linear's not-found = %v, want ENOENT", saveErr)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("mutations sent for the save = %d, want exactly 1", got)
	}
	t.Logf("$ cat .error\n%s", readIssueError(t, probe.Identifier))

	// The reads an agent makes next. Each is an open/read/close, and the close
	// is a FLUSH: with the buffer left dirty it re-entered the write path.
	for i := 1; i <= 5; i++ {
		content, err := os.ReadFile(probe.Path)
		if err != nil {
			t.Fatalf("read #%d after the rejected save failed with %v — the close re-attempted the write (#418)", i, err)
		}
		if i == 5 {
			t.Logf("$ cat issue.md  (read #%d after the rejection, %d bytes)", i, len(content))
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("mutations sent = %d after five reads, want 1 — a rejected write is re-attempted by readers", got)
	}
	t.Logf("mutations Linear received across the save and five later reads: %d", calls.Load())
}

// TestOffline_RejectedAtomicSaveKeepsItsScratchFile: the corrected-re-save
// affordance the in-place path gave up when a failed write stopped leaving its
// buffer dirty (#494 / #406). A refused save does not consume the scratch file,
// so the writer's un-retyped text is still there and a corrected rename saves
// it — and that is the path vim, VS Code, and the agent edit tools all take.
func TestOffline_RejectedAtomicSaveKeepsItsScratchFile(t *testing.T) {
	skipIfLiveAPI(t, fixtureWriteContract)

	mock := enableMockMutations(t)
	probe := seedIssueProbe(t, "scratch", "Scratch Survival Probe", "Body before the rejected save.")

	original, err := os.ReadFile(probe.Path)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}

	// A document validation rejects: a label the workspace does not have. The
	// name resolves nowhere, so nothing is ever sent — this is the unsent arm.
	if !strings.Contains(string(original), "- Bug") {
		t.Fatalf("probe render lost its seeded label; the rejection below would not be a resolve failure:\n%s", original)
	}
	rejected := strings.Replace(string(original), "- Bug", "- __no_such_label__", 1)
	scratch := probe.Path + ".tmp.4242"
	if err := os.WriteFile(scratch, []byte(rejected), 0644); err != nil {
		t.Fatalf("write scratch: %v", err)
	}
	saveErr := os.Rename(scratch, probe.Path)
	t.Logf("$ save issue.md via scratch rename -> %v", saveErr)
	if !errors.Is(saveErr, syscall.EINVAL) {
		t.Fatalf("save with an unknown label = %v, want EINVAL", saveErr)
	}
	if sent := updatesFor(mock, probe.ID); len(sent) != 0 {
		t.Errorf("a validation rejection sent %d mutation(s) to Linear; it must never leave the process", len(sent))
	}

	// The file itself is back to the entity's render — the rejection does not
	// park the writer's document in it.
	served, err := os.ReadFile(probe.Path)
	if err != nil {
		t.Fatalf("re-read issue.md: %v", err)
	}
	if strings.Contains(string(served), "__no_such_label__") {
		t.Errorf("issue.md still serves the rejected document:\n%s", served)
	}
	t.Logf("$ cat issue.md  (after the rejection: the entity's render, %d bytes, no rejected label)", len(served))

	// The un-retyped text is in the scratch file, which the refused save left
	// alone.
	kept, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatalf("the scratch file did not survive the refused save: %v", err)
	}
	if string(kept) != rejected {
		t.Errorf("scratch file no longer holds the writer's document:\n%s", kept)
	}
	t.Logf("$ cat issue.md.tmp.4242  (%d bytes — the writer's document, still there)", len(kept))

	// Correct it in the scratch file and rename again: this is the documented
	// recovery, and it must persist.
	corrected := strings.Replace(rejected, "- __no_such_label__", "- Bug", 1)
	corrected = strings.Replace(corrected, "title: Scratch Survival Probe", "title: Corrected By Re-rename", 1)
	if err := os.WriteFile(scratch, []byte(corrected), 0644); err != nil {
		t.Fatalf("rewrite scratch: %v", err)
	}
	if err := os.Rename(scratch, probe.Path); err != nil {
		t.Fatalf("corrected rename failed: %v", err)
	}
	t.Logf("$ save corrected issue.md via scratch rename -> ok")

	if sent := updatesFor(mock, probe.ID); len(sent) != 1 {
		t.Fatalf("mutations sent for this issue = %d, want 1 (only the corrected save)", len(sent))
	}
	after, err := os.ReadFile(probe.Path)
	if err != nil {
		t.Fatalf("read issue.md after the corrected save: %v", err)
	}
	if !strings.Contains(string(after), "Corrected By Re-rename") {
		t.Errorf("corrected save did not land:\n%s", after)
	}
	t.Logf("$ cat issue.md  (after the corrected save)\n%s", after)

	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("stat scratch after a save that persisted = %v, want it consumed", err)
	}
}

// TestOffline_RenamedProjectRerendersAfterDeclinedBodyClear covers #406 at the
// mount. One atomic save carries two changes — a new name and an emptied body —
// against a backend that applies the name and declines the clear. The save's
// verdict is EINVAL, but the rename DID reach Linear, so the mount must end up
// showing the new name — the refused half of the save must not take the applied
// half down with it.
//
// The rename tail used to adopt only on {0, EIO}, so this EINVAL skipped adopt
// altogether. Widening the whitelist by errno is not the fix: the same EINVAL
// comes back from a parse failure, where nothing reached Linear. Adopt now runs
// on every outcome, and copies only what the commit tail fetched back — so on a
// failure that never reached Linear it copies the directory's own baseline onto
// itself.
func TestOffline_RenamedProjectRerendersAfterDeclinedBodyClear(t *testing.T) {
	skipIfLiveAPI(t, "the declined body-clear needs a backend that ignores an empty content — "+
		"that is the mock mutator (WithEmptyContentIgnored), not Linear, which may simply apply it")
	enableMockMutations(t, mockmutation.WithEmptyContentIgnored())
	// The seeding save below pins its own bytes for serve-your-own-writes, and a
	// read inside that window is answered from them — which would hide whatever
	// the declined save left on the node. Shorten the window rather than wait out
	// the production ten seconds.
	t.Cleanup(fs.SetTestPinTTL(testPinTTL))

	slug, cleanup := createTestProject(t, "Rename Then Clear")
	defer cleanup()

	path := projectFilePath(testTeamKey, slug)
	orig, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("read new project.md: %v", err)
	}
	doc, err := parseFrontmatter(orig)
	if err != nil {
		t.Fatalf("parse project.md: %v", err)
	}
	withBody, err := marshal.Render(&marshal.Document{Frontmatter: doc.Frontmatter, Body: "a body that cannot be cleared"})
	if err != nil {
		t.Fatalf("render project.md with a body: %v", err)
	}
	if err := claudeToolAtomicSave(t, path, withBody); err != nil {
		t.Fatalf("seed a body via atomic save: %v", err)
	}
	waitForCacheExpiry()

	// One save, two changes: rename, and clear the body. The new name differs
	// only in characters the directory-name transform strips, so the project
	// keeps its directory and the canonical file's own render is what the
	// assertion can be about.
	oldName, _ := doc.Frontmatter["name"].(string)
	renamed := strings.Replace(oldName, "[TEST]", "[TEST!]", 1)
	if renamed == oldName {
		t.Fatalf("could not build a rename that keeps the directory name, from %q", oldName)
	}
	doc.Frontmatter["name"] = renamed
	both, err := marshal.Render(&marshal.Document{Frontmatter: doc.Frontmatter, Body: ""})
	if err != nil {
		t.Fatalf("render the rename+clear document: %v", err)
	}
	saveErr := claudeToolAtomicSave(t, path, both)
	t.Logf("$ save project.md (rename + body clear) -> %v", saveErr)
	if !errors.Is(saveErr, syscall.EINVAL) {
		t.Fatalf("rename + declined body clear = %v, want EINVAL", saveErr)
	}

	// Past the serve-your-own-writes window, so what the mount shows is what it
	// believes about the entity rather than the bytes the client wrote; then wait
	// for the kernel's entry for the file to lapse so the directory re-renders
	// it. The bound is generous because the wait is for a cache timeout, not for
	// anything this test drives.
	time.Sleep(pinWindowWait)
	after := readFileUntilContains(t, path, renamed, errorVisibilityWait)
	t.Logf("$ cat teams/%s/projects/%s/project.md\n%s", testTeamKey, slug, after)
	if !strings.Contains(string(after), renamed) {
		t.Errorf("project.md never picked up the name a declined save had already renamed on Linear:\n%s", after)
	}
	if !strings.Contains(string(after), "a body that cannot be cleared") {
		t.Errorf("the body the backend declined to clear is gone from project.md:\n%s", after)
	}
}
