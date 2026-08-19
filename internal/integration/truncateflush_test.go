package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// #454 is a two-sided bug, and this file holds both sides at the mount, because
// only the mount produces the kernel op sequence that causes either.
//
// Side one — the report: a flush arriving between an O_TRUNC and the first write
// resurrects the previous file image, and the write lands at offset 0 of it.
// Side two — the cure's own hazard: whatever marks the truncation must not
// outlive the writer that asked for it, or the next ordinary write to the file
// is clipped instead. Both persist wrong bytes to Linear with exit 0 and no
// `.error`, so a fix for one that reopens the other is not a fix.

// truncMarker is the tail every probe body carries. A rewrite shorter than the
// body must not leave it behind, on any surface.
const truncMarker = "ZZZZ-TAIL-MARKER-454"

// seedTruncateProbeIssue creates a throwaway issue whose body is long enough
// that a shorter rewrite has plenty of tail to leave behind, and returns the
// path to its issue.md plus its id.
//
// It seeds its OWN uniquely-named row rather than naming TST-1, so the file's
// skipIfLiveAPI guards are about the fake mutator's payload and nothing else.
// The relations are seeded too: they are what a corrupted write silently clears
// (see TestRejectedTruncateDoesNotClipTheNextWrite).
func seedTruncateProbeIssue(t *testing.T, tag string) (path, issueID string) {
	t.Helper()
	const longBody = "AAAA BBBB CCCC DDDD EEEE FFFF GGGG HHHH IIII JJJJ KKKK\n\nLong tail: " + truncMarker + "."

	ctx := context.Background()
	team := fixtures.FixtureAPITeam()
	user := fixtures.FixtureAPIUser()
	uniq := time.Now().UnixNano()
	issueID = fmt.Sprintf("trunc-%s-%d", tag, uniq)
	identifier := fmt.Sprintf("TST-%d", 30000+uniq%10000)
	issue := fixtures.FixtureAPIIssue(
		fixtures.WithIssueID(issueID, identifier),
		fixtures.WithTitle("Truncate flush probe"),
		fixtures.WithDescription(longBody),
		fixtures.WithTeam(&team),
		fixtures.WithAssignee(&user),
		fixtures.WithLabels(fixtures.FixtureAPILabels()[0]),
	)
	due := "2030-01-01"
	issue.DueDate = &due
	row, err := db.APIIssueToDBIssue(issue)
	if err != nil {
		t.Fatalf("convert seed: %v", err)
	}
	if err := testStore.Queries().UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	t.Cleanup(func() { _ = testStore.Queries().DeleteIssue(context.Background(), issueID) })
	return mountPoint + "/teams/" + testTeamKey + "/issues/" + identifier + "/issue.md", issueID
}

// interveningFlush performs the fd dance a shell does while setting a `>`
// redirection up: duplicate the descriptor, then close the copy. The close emits
// a FLUSH on a buffer the O_TRUNC has already emptied, which is the whole
// trigger for #454 — os.OpenFile/Write/Close alone sends OPEN, SETATTR, WRITE,
// FLUSH and never produces it, which is why a plain O_TRUNC+write test passes
// against the bug.
//
// The EINVAL that close returns IS the proof the flush fired and met the
// empty-write rejection, so it is asserted rather than logged: if the dance ever
// stops producing that flush, these tests must fail loudly instead of quietly
// degrading into the plain O_TRUNC+write case.
func interveningFlush(t *testing.T, f *os.File) {
	t.Helper()
	dup, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		_ = f.Close()
		t.Fatalf("dup: %v", err)
	}
	err = syscall.Close(dup)
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("close(dup) = %v, want EINVAL — the intervening flush did not reach the empty-write rejection, "+
			"so this test is no longer exercising #454", err)
	}
}

// truncateWriteViaShellSequence performs the write a shell `>` redirect
// performs: O_TRUNC, then a FLUSH before any bytes arrive, then the write, then
// close. Returns the error close reported, which is what the shell's exit status
// reflects.
func truncateWriteViaShellSequence(t *testing.T, path, content string) error {
	t.Helper()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open %s O_TRUNC: %v", path, err)
	}
	interveningFlush(t, f)

	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		t.Fatalf("write %s: %v", path, err)
	}
	return f.Close()
}

// bodySentFor returns the last description the fake mutator received for an id.
func bodySentFor(mock *mockmutation.Client, issueID string) string {
	var sent string
	for _, u := range updatesFor(mock, issueID) {
		if u.Body != nil {
			sent = *u.Body
		}
	}
	return sent
}

// TestTruncatingWriteWithInterveningFlush pins #454: a flush arriving between an
// O_TRUNC and the first write must not resurrect the previous file image.
//
// editFlush's empty-write guard (#397) meets that flush with an emptied buffer,
// rejects it, and restores the entity's render so the canonical node will not
// serve zero bytes for the rest of its life. Those bytes are the entity's, not
// the writer's; without saying so, the write that follows lands at offset 0 of
// the restored image, overwrites only a prefix, and the closing flush persists
// the splice to Linear.
func TestTruncatingWriteWithInterveningFlush(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds a row and audits the fake mutator's payload")

	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	mock := enableMockMutations(t)

	path, issueID := seedTruncateProbeIssue(t, "basic")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("prime read: %v", err)
	}

	if err := truncateWriteViaShellSequence(t, path, "---\ntitle: \"SHORT\"\n---\nSHORT\n"); err != nil {
		t.Fatalf("close/commit: %v", err)
	}

	sent := bodySentFor(mock, issueID)
	if strings.Contains(sent, truncMarker) {
		t.Errorf("previous image survived the truncate and was sent to the API:\n%s", sent)
	}
	if strings.Contains(sent, "title:") {
		t.Errorf("frontmatter was spliced into the description body:\n%s", sent)
	}
	if got := strings.TrimSpace(sent); got != "SHORT" {
		t.Errorf("sent description = %q, want %q", got, "SHORT")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(after) >= len(before) {
		t.Errorf("file is %d bytes after a write shorter than the original %d — the truncate did not stick",
			len(after), len(before))
	}
}

// TestRejectedTruncateDoesNotClipTheNextWrite is the other side of #454, and the
// reason the pending truncation is scoped to the FILE HANDLE rather than to the
// buffer.
//
// A `: > issue.md` truncates and then closes without writing. That is the case
// #397 was built for: the empty write is refused with EINVAL, nothing is sent,
// and the file reads back intact. What must not happen is that the refused
// truncate stays armed on the node — the very next ordinary write, on a fresh
// descriptor that truncated nothing, then finds its buffer cleared under it.
//
// The damage is worse than a mangled body, which is why this test audits the
// whole mutation and not just the description. A NUL-padded document does not
// start with `---`, so marshal.Parse reads all of it as body with EMPTY
// frontmatter, and MarkdownToIssueUpdate sends `nil` for every removable field
// absent from frontmatter: one save silently clears assignee, due date, parent,
// project, milestone, cycle and labels. The #397 guard cannot catch it either —
// bytes.TrimSpace does not strip NUL, so the buffer does not look empty.
func TestRejectedTruncateDoesNotClipTheNextWrite(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds a row and audits the fake mutator's payload")

	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	mock := enableMockMutations(t)

	path, issueID := seedTruncateProbeIssue(t, "reject")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("prime read: %v", err)
	}

	// `: > issue.md` — truncate, no write. #397 refuses it and restores.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open O_TRUNC: %v", err)
	}
	interveningFlush(t, f)
	_ = f.Close()

	mid, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read after the rejected truncate: %v", err)
	}
	if !bytes.Equal(mid, before) {
		t.Fatalf("the rejected empty write did not leave the file readable:\ngot  %q\nwant %q", mid, before)
	}
	if n := len(mock.Updates()); n != 0 {
		t.Fatalf("a refused empty write sent %d mutation(s); it must send none", n)
	}

	// Now an ordinary append on a fresh descriptor. It truncated nothing.
	a, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := a.Write([]byte("\nAppended line.\n")); err != nil {
		_ = a.Close()
		t.Fatalf("append write: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close/commit the append: %v", err)
	}

	sent := bodySentFor(mock, issueID)
	if n := bytes.Count([]byte(sent), []byte{0}); n > 0 {
		t.Errorf("the append was written into a cleared buffer: the API received %d NUL bytes:\n%q", n, sent)
	}
	if !strings.Contains(sent, truncMarker) {
		t.Errorf("the append lost the body it was appending to; the API received:\n%q", sent)
	}
	if !strings.HasSuffix(strings.TrimSpace(sent), "Appended line.") {
		t.Errorf("sent description does not end with the appended line:\n%q", sent)
	}

	// The relation fields are the silent half. A document that lost its
	// frontmatter clears all of them in the same mutation.
	var audited bool
	for _, u := range updatesFor(mock, issueID) {
		audited = true
		for _, key := range []string{
			"assigneeId", "dueDate", "parentId", "projectId",
			"projectMilestoneId", "cycleId", "labelIds", "estimate",
		} {
			if v, present := u.Input[key]; present && v == nil {
				t.Errorf("the write cleared %s (sent nil) — it named no such change; "+
					"the document reaching marshal.Parse had no frontmatter", key)
			}
		}
	}
	if !audited {
		t.Fatal("no issue update reached the mutator; the append was dropped")
	}
}

// TestSuccessiveTruncatingWritesDoNotCompound pins the second half of the
// report: each shorter rewrite built on the last, so the damage accumulated and
// the file grew instead of shrinking. The Nth write's result must depend only on
// the Nth write.
func TestSuccessiveTruncatingWritesDoNotCompound(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds a row and audits the fake mutator's payload")
	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	mock := enableMockMutations(t)

	path, issueID := seedTruncateProbeIssue(t, "compound")

	// Each write is shorter than the one before it.
	for _, body := range []string{
		"FIRST BODY, the longest of the three by a clear margin",
		"SECOND BODY, shorter",
		"THIRD",
	} {
		doc := fmt.Sprintf("---\ntitle: \"THROWAWAY\"\n---\n%s\n", body)
		if err := truncateWriteViaShellSequence(t, path, doc); err != nil {
			t.Fatalf("write %q: %v", body, err)
		}
		if got := strings.TrimSpace(bodySentFor(mock, issueID)); got != body {
			t.Fatalf("after writing %q the API received %q — a previous write is still bleeding through", body, got)
		}
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(after), "THIRD") || strings.Contains(string(after), "SECOND") {
		t.Errorf("file after three shortening writes = %q, want only the third", after)
	}
}

// TestTruncatingWriteWithInterveningFlushClearsError pins a contract this
// sequence puts under unusual pressure: the intervening flush DOES record an
// empty-write rejection in .error, and only the closing flush's success clears
// it (#400). The transient window is real, so the invariant worth holding is
// that it never outlives the write.
//
// It is deliberately NOT billed as a regression test for #454, and this is worth
// stating because the obvious reading is wrong: it passes against the unfixed
// code too. On broken code the closing flush still succeeds — it just persists
// spliced content — so .error is cleared either way. #455 reports a spurious
// "Empty write rejected" surviving a successful `>` write, and that symptom does
// NOT reproduce here in either direction; whatever produces it is a different
// sequence, still unexplained.
func TestTruncatingWriteWithInterveningFlushClearsError(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds a row and reads the entity's .error back")
	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	enableMockMutations(t)

	path, _ := seedTruncateProbeIssue(t, "err")
	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("prime read: %v", err)
	}
	if err := truncateWriteViaShellSequence(t, path, "---\ntitle: \"SHORT\"\n---\nSHORT\n"); err != nil {
		t.Fatalf("close/commit: %v", err)
	}

	errPath := strings.TrimSuffix(path, "/issue.md") + "/.error"
	b, err := os.ReadFile(errPath)
	if err != nil {
		if os.IsNotExist(err) {
			return // no .error at all is the cleanest possible outcome
		}
		t.Fatalf("read %s: %v", errPath, err)
	}
	if got := strings.TrimSpace(string(b)); got != "" {
		t.Errorf(".error survived a successful truncating write: %q", got)
	}
}

// TestTruncatingWriteWithInterveningFlushOnProjectMD checks a sibling surface.
// The fix lives in the buffer and shell every editable file goes through, so all
// seven inherit it by construction — but #454 names project.md and
// initiative.md as affected by inspection, and "by construction" is a claim
// worth a test: each surface reaches editFlush through its own spec, and a spec
// that declined to set `restore` would behave differently here.
func TestTruncatingWriteWithInterveningFlushOnProjectMD(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: rewrites the seeded project and audits the fake mutator's payload")
	mock := enableMockMutations(t)

	path := projectMDPath()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("prime read: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("seeded project.md is empty; nothing to leave behind")
	}
	// The seeded project is shared with every other test in the package, and this
	// write both shortens its body and drops frontmatter keys. Put it back.
	t.Cleanup(func() {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			t.Logf("restore project.md: %v", err)
			return
		}
		_, _ = f.Write(before)
		_ = f.Close()
	})

	const short = "---\nname: test-project\n---\nSHORTPROJECTBODY\n"
	if err := truncateWriteViaShellSequence(t, path, short); err != nil {
		t.Fatalf("close/commit: %v", err)
	}

	var sent string
	for _, u := range mock.Updates() {
		if u.Kind == "project" && u.Body != nil {
			sent = *u.Body
		}
	}
	if sent == "" {
		t.Fatal("no project update reached the mutator")
	}
	if got := strings.TrimSpace(sent); got != "SHORTPROJECTBODY" {
		t.Errorf("sent description = %q, want %q — the truncate must not leave residue on project.md either",
			got, "SHORTPROJECTBODY")
	}
}

// TestTruncatingWriteWithInterveningFlushOnInitiativeMD is the other surface
// #454 names by inspection.
//
// It seeds its own initiative rather than reusing the package fixture, whose
// initiative.md renders with an empty body — there would be nothing to leave
// behind, and the test would pass while asserting nothing.
func TestTruncatingWriteWithInterveningFlushOnInitiativeMD(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds an initiative and audits the fake mutator's payload")
	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	mock := enableMockMutations(t)

	ctx := context.Background()
	uniq := time.Now().UnixNano()
	init := fixtures.FixtureAPIInitiative()
	init.ID = fmt.Sprintf("trunc-initiative-%d", uniq)
	init.Name = fmt.Sprintf("Truncate Probe %d", uniq)
	init.Slug = fmt.Sprintf("truncate-probe-%d", uniq)
	init.Content = "AAAA BBBB CCCC DDDD EEEE FFFF\n\nLong tail: " + truncMarker + "."
	init.Projects.Nodes = nil
	if err := fixtures.PopulateInitiative(ctx, testStore, init); err != nil {
		t.Fatalf("seed initiative: %v", err)
	}
	t.Cleanup(func() { _ = testStore.Queries().DeleteInitiative(context.Background(), init.ID) })

	path := initiativePath(init.Slug) + "/initiative.md"
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("prime read: %v", err)
	}
	if !strings.Contains(string(before), truncMarker) {
		t.Fatalf("seeded initiative.md has no body to leave behind:\n%s", before)
	}

	short := fmt.Sprintf("---\nname: %s\n---\nSHORTINITIATIVEBODY\n", init.Name)
	if err := truncateWriteViaShellSequence(t, path, short); err != nil {
		t.Fatalf("close/commit: %v", err)
	}

	var sent string
	for _, u := range mock.Updates() {
		if u.Kind == "initiative" && u.ID == init.ID && u.Body != nil {
			sent = *u.Body
		}
	}
	if sent == "" {
		t.Fatal("no initiative update reached the mutator")
	}
	if strings.Contains(sent, truncMarker) {
		t.Errorf("previous image survived the truncate on initiative.md:\n%s", sent)
	}
	if got := strings.TrimSpace(sent); got != "SHORTINITIATIVEBODY" {
		t.Errorf("sent description = %q, want %q", got, "SHORTINITIATIVEBODY")
	}
}
