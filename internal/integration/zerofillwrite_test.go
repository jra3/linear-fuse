package integration

import (
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

// TestZeroFilledWriteIsRejected pins #472 at the mount: a write that leaves a
// zero-filled HOLE in an editable .md must be refused, not persisted.
//
// The hole itself is correct filesystem behaviour — a write starting past EOF,
// or an ftruncate that grows the file, fills the gap with NUL — but the document
// it manufactures does not start with `---`, so marshal.Parse reads it as empty
// frontmatter with the whole string as body. Measured before the fix, both
// sequences below reached Linear as a description of NUL bytes AND cleared
// assignee, due date, parent, project, milestone, cycle and labels in the same
// mutation, with close() returning success and nothing in .error: the #397
// empty-write guard tested bytes.TrimSpace, which does not strip NUL.
//
// The assertion that matters is the audited payload — no update was SENT at all
// — since the stored row alone cannot tell a rejected write from one that
// happened to persist the same value (#415).
func TestZeroFilledWriteIsRejected(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds a row and audits the fake mutator's payload")

	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}
	mock := enableMockMutations(t)

	for _, tc := range []struct {
		name string
		// write performs the zero-filling sequence on an open O_TRUNC fd and
		// returns the error close() reported.
		write func(t *testing.T, path string) error
	}{
		{
			// ftruncate(fd, 20) after O_TRUNC: the whole file is hole.
			name: "ftruncate grow",
			write: func(t *testing.T, path string) error {
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
				if err != nil {
					t.Fatalf("open O_TRUNC: %v", err)
				}
				if err := f.Truncate(20); err != nil {
					_ = f.Close()
					t.Fatalf("ftruncate(20): %v", err)
				}
				return f.Close()
			},
		},
		{
			// pwrite(fd, "hello", 10) after O_TRUNC: hole, then real bytes.
			name: "pwrite past EOF",
			write: func(t *testing.T, path string) error {
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
				if err != nil {
					t.Fatalf("open O_TRUNC: %v", err)
				}
				if _, err := f.WriteAt([]byte("hello"), 10); err != nil {
					_ = f.Close()
					t.Fatalf("pwrite at offset 10: %v", err)
				}
				return f.Close()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issueID, identifier, body := seedZeroFillProbe(t)
			path := issueFilePath(testTeamKey, identifier)
			if _, err := os.ReadFile(path); err != nil {
				t.Fatalf("prime read: %v", err)
			}

			cerr := tc.write(t, path)
			if !errors.Is(cerr, syscall.EINVAL) {
				t.Errorf("close after a zero-filled write returned %v, want EINVAL — a hole is not a "+
					"document the writer composed", cerr)
			}

			for _, u := range updatesFor(mock, issueID) {
				sent := "<nil>"
				if u.Body != nil {
					sent = fmt.Sprintf("%d NULs in %q", strings.Count(*u.Body, "\x00"), *u.Body)
				}
				t.Errorf("a zero-filled write reached the API (body: %s); the same mutation clears every "+
					"removable field the issue had", sent)
			}

			// The rejection is legible, and it names the cause rather than
			// claiming the file was empty — it was not, the offset was wrong.
			errPath := issueDirPath(testTeamKey, identifier) + "/.error"
			data := readFileUntilContains(t, errPath, "Zero-filled write rejected", errorVisibilityWait)
			for _, want := range []string{"Zero-filled write rejected", "NUL", "Nothing was written", "offset 0"} {
				if !strings.Contains(string(data), want) {
					t.Errorf(".error does not mention %q after a zero-filled write, got:\n%s", want, data)
				}
			}

			// Nothing was applied, and the file still serves its contents — the
			// recovery the .error prescribes is "re-read the file".
			fresh, err := getIssueFromSQLite(issueID)
			if err != nil {
				t.Fatalf("issue not readable after the rejected write: %v", err)
			}
			if strings.TrimSpace(fresh.Description) != strings.TrimSpace(body) {
				t.Errorf("description = %q after a rejected zero-filled write, want the original %q",
					fresh.Description, body)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("re-read after the rejection: %v", err)
			}
			if strings.Contains(string(after), "\x00") || !strings.Contains(string(after), "---") {
				t.Errorf("issue.md still serves the hole after the rejection:\n%q", after)
			}
		})
	}
}

// seedZeroFillProbe upserts one issue with a real body and returns its ID,
// identifier, and body. Each subtest gets its own row so a restored buffer from
// one cannot colour the next.
func seedZeroFillProbe(t *testing.T) (issueID, identifier, body string) {
	t.Helper()

	const probeBody = "a body a zero-filled write must not replace with NUL bytes"

	ctx := context.Background()
	team := fixtures.FixtureAPITeam()
	uniq := time.Now().UnixNano()
	issueID = fmt.Sprintf("zero-fill-%d", uniq)
	identifier = fmt.Sprintf("TST-%d", 40000+uniq%10000)
	row, err := db.APIIssueToDBIssue(fixtures.FixtureAPIIssue(
		fixtures.WithIssueID(issueID, identifier),
		fixtures.WithTitle("Zero fill probe"),
		fixtures.WithDescription(probeBody),
		fixtures.WithTeam(&team),
	))
	if err != nil {
		t.Fatalf("convert seed: %v", err)
	}
	if err := testStore.Queries().UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	t.Cleanup(func() { _ = testStore.Queries().DeleteIssue(context.Background(), issueID) })
	return issueID, identifier, probeBody
}

// updatesFor filters the fake mutator's audit log down to one entity.
func updatesFor(mock *mockmutation.Client, id string) []mockmutation.UpdateCall {
	var out []mockmutation.UpdateCall
	for _, u := range mock.Updates() {
		if u.ID == id {
			out = append(out, u)
		}
	}
	return out
}
