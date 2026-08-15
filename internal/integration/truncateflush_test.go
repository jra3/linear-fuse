package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
)

// TestTruncatingWriteWithInterveningFlush pins #454: a flush that arrives
// between an O_TRUNC and the first write must not resurrect the previous file
// image.
//
// The kernel's sequence for a shell `>` redirect, from a `-d` trace of a live
// mount, is OPEN, SETATTR(size 0), FLUSH, WRITE, FLUSH — the shell emits that
// middle FLUSH by closing a duplicated descriptor. editFlush's empty-write
// guard (#397) sees the emptied buffer, records an empty-write rejection, and
// RESTORES the buffer to the entity's canonical content, which is correct when
// a writer truly emptied the file and wrong here: the write that follows lands
// at offset 0 of the restored content, overwriting only a prefix, and the
// closing flush persists the splice to Linear.
//
// Go's OpenFile/Write/Close never emits the intervening flush, which is why a
// plain O_TRUNC+write test passes against the bug. dup(2) + close(2) reproduces
// it exactly, and is the whole difference between this test and a passing one.
//
// SKIPPED: this asserts the FIXED behavior and fails today. Deleting the skip
// is the first step of fixing #454; the assertions below are its acceptance
// criteria.
func TestTruncatingWriteWithInterveningFlush(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds a row and audits the fake mutator's payload")
	t.Skip("pins #454 (unfixed): a flush between O_TRUNC and the first write restores the buffer, splicing the old image into the save. Remove this skip to fix it.")

	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	mock := enableMockMutations(t)

	const longBody = "AAAA BBBB CCCC DDDD EEEE FFFF GGGG HHHH IIII JJJJ KKKK\n\nLong tail: ZZZZ-TAIL-MARKER-454."

	ctx := context.Background()
	team := fixtures.FixtureAPITeam()
	uniq := time.Now().UnixNano()
	issueID := fmt.Sprintf("trunc-flush-%d", uniq)
	identifier := fmt.Sprintf("TST-%d", 30000+uniq%10000)
	row, err := db.APIIssueToDBIssue(fixtures.FixtureAPIIssue(
		fixtures.WithIssueID(issueID, identifier),
		fixtures.WithTitle("Truncate flush probe"),
		fixtures.WithDescription(longBody),
		fixtures.WithTeam(&team),
	))
	if err != nil {
		t.Fatalf("convert seed: %v", err)
	}
	if err := testStore.Queries().UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	t.Cleanup(func() { _ = testStore.Queries().DeleteIssue(context.Background(), issueID) })

	path := mountPoint + "/teams/" + testTeamKey + "/issues/" + identifier + "/issue.md"
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("prime read: %v", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("open O_TRUNC: %v", err)
	}

	// The shell's fd dance: dup, then close the copy. The close emits a FLUSH
	// while the buffer is empty from the truncate — the trigger for #454.
	dup, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	if err := syscall.Close(dup); err != nil {
		t.Logf("close(dup): %v", err)
	}

	if _, err := f.Write([]byte("---\ntitle: \"SHORT\"\n---\nSHORT\n")); err != nil {
		_ = f.Close()
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close/commit: %v", err)
	}

	var sent string
	for _, u := range mock.Updates() {
		if u.ID == issueID && u.Body != nil {
			sent = *u.Body
		}
	}

	if strings.Contains(sent, "ZZZZ-TAIL-MARKER-454") {
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
	if len(after) == len(before) {
		t.Errorf("file size unchanged at %d bytes after a shorter write — the truncate did not stick", len(after))
	}
}
