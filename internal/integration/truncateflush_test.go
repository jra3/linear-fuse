package integration

import (
	"os"
	"strings"
	"syscall"
	"testing"
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
// The fix is editBuffer's pending-truncation flag: the restore stays a read-side
// convenience, and the write that follows re-applies the truncation at its own
// offset, so this test now runs unskipped.
func TestTruncatingWriteWithInterveningFlush(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds a row and audits the fake mutator's payload")

	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	mock := enableMockMutations(t)

	const longBody = "AAAA BBBB CCCC DDDD EEEE FFFF GGGG HHHH IIII JJJJ KKKK\n\nLong tail: ZZZZ-TAIL-MARKER-454."

	probe := seedIssueProbe(t, "trunc-flush", "Truncate flush probe", longBody)

	path := probe.Path
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
	for _, u := range updatesFor(mock, probe.ID) {
		if u.Body != nil {
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
