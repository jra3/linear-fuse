package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// entityGoneMutator wraps the offline mutation fake and fails the write paths
// #445 is about the way Linear fails them when the referenced entity was
// deleted upstream between the read and the write: an "Entity not found"
// rejection. Everything else still runs through the fake.
type entityGoneMutator struct {
	*mockmutation.Client
	err    error
	docErr error // same rejection, named for a document, for the rename path
}

func (m *entityGoneMutator) UpdateIssue(context.Context, string, map[string]any) error {
	return m.err
}

func (m *entityGoneMutator) CreateIssue(context.Context, map[string]any) (*api.Issue, error) {
	return nil, m.err
}

func (m *entityGoneMutator) DeleteComment(context.Context, string) error {
	return m.err
}

func (m *entityGoneMutator) UpdateDocument(context.Context, string, map[string]any) (*api.Document, error) {
	return nil, m.docErr
}

// TestServerEntityNotFoundIsENOENT is the end-to-end guard for #445: a
// Linear-side "Entity not found" on a save, a create or a rename must reach the
// writer as ENOENT ("no such file or directory"), not EIO — and .error must say
// a retry will not help, because EIO is what taught callers to retry a write
// that can never succeed.
//
// It exercises the real mount: the errno a writer sees at close(2), the shell
// transcript an agent sees, and the .error text it reads next. It also pins the
// two things that must NOT move with it — an already-gone delete stays
// idempotent success, and the generated README names the new cause.
func TestServerEntityNotFoundIsENOENT(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: injects a mutation client that rejects writes with Linear's \"Entity not found\"; live, the server decides and the write would mutate a real workspace")
	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}

	gone := &api.GraphQLError{Message: "Entity not found: Issue - Could not find referenced Issue."}
	mock := mockmutation.New(
		mockmutation.WithTeamKey(testTeamKey),
		mockmutation.WithStore(lfs.GetStore()),
	)
	lfs.InjectTestMutationClient(&entityGoneMutator{
		Client: mock,
		err:    gone,
		docErr: &api.GraphQLError{Message: "Entity not found: Document - Could not find referenced Document."},
	})
	t.Cleanup(func() { lfs.InjectTestMutationClient(nil) })

	// A row that exists locally (a stale read) but is gone on Linear — the exact
	// state #445 describes.
	ctx := context.Background()
	team := fixtures.FixtureAPITeam()
	uniq := time.Now().UnixNano()
	issueID := fmt.Sprintf("gone-%d", uniq)
	identifier := fmt.Sprintf("TST-%d", 30000+uniq%10000)
	row, err := db.APIIssueToDBIssue(fixtures.FixtureAPIIssue(
		fixtures.WithIssueID(issueID, identifier),
		fixtures.WithTitle("Deleted upstream probe"),
		fixtures.WithDescription("original body"),
		fixtures.WithTeam(&team),
	))
	if err != nil {
		t.Fatalf("convert seed: %v", err)
	}
	if err := testStore.Queries().UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	t.Cleanup(func() { _ = testStore.Queries().DeleteIssue(context.Background(), issueID) })

	issuePath := issueFilePath(testTeamKey, identifier)
	issueErrPath := strings.TrimSuffix(issuePath, "/issue.md") + "/.error"

	// The save path: close(2) carries the verdict, and it must be ENOENT.
	t.Run("save_returns_ENOENT_and_says_not_to_retry", func(t *testing.T) {
		f, err := os.OpenFile(issuePath, os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if _, err := f.Write([]byte("---\ntitle: \"Edited after deletion\"\n---\nnew body\n")); err != nil {
			_ = f.Close()
			t.Fatalf("write: %v", err)
		}
		closeErr := f.Close()
		if closeErr == nil {
			t.Fatal("close returned nil; the rejection is not reaching the writer")
		}
		t.Logf("close(2) error: %v", closeErr)
		if !errors.Is(closeErr, syscall.ENOENT) {
			t.Errorf("close errno = %v, want ENOENT (#445: an upstream-deleted entity is not an I/O fault)", closeErr)
		}
		if errors.Is(closeErr, syscall.EIO) {
			t.Errorf("close errno is EIO, the pre-#445 misclassification: %v", closeErr)
		}

		data := readFileUntilContains(t, issueErrPath, "Entity not found", errorVisibilityWait)
		t.Logf("issue .error:\n%s", data)
		for _, want := range []string{"update issue", "Entity not found", "retrying will not help"} {
			if !strings.Contains(string(data), want) {
				t.Errorf(".error should contain %q; got:\n%s", want, data)
			}
		}
	})

	// The create path: the same rejection through issues/_create.
	t.Run("create_returns_ENOENT_and_says_not_to_retry", func(t *testing.T) {
		err := writeCreateSpec(t, "---\ntitle: Create against a deleted parent\nparent: "+identifier+"\n---\nbody\n")
		if err == nil {
			t.Fatal("create should have failed")
		}
		t.Logf("close(2) error: %v", err)
		if !errors.Is(err, syscall.ENOENT) {
			t.Errorf("create errno = %v, want ENOENT", err)
		}
		data := readFileUntilContains(t, issuesErrorPath(testTeamKey), "Entity not found", errorVisibilityWait)
		t.Logf("issues/.error:\n%s", data)
		for _, want := range []string{"Entity not found", "retrying will not help"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("issues/.error should contain %q; got:\n%s", want, data)
			}
		}
	})

	// What an agent at a shell actually sees. dd checks close(2), so the strerror
	// it prints is the classification under test.
	t.Run("shell_transcript", func(t *testing.T) {
		if _, err := os.Stat("/bin/sh"); err != nil {
			t.Skipf("/bin/sh unavailable: %v", err)
		}
		script := fmt.Sprintf(`cat <<'DOC' | dd of=%q status=none
---
title: "Edited after deletion"
---
new body
DOC
echo "EXIT=$?"
echo '--- .error ---'
cat %q`, issuePath, issueErrPath)
		out, _ := exec.Command("/bin/sh", "-c", script).CombinedOutput()
		t.Logf("$ cat <edited issue.md> | dd of=<mount>/teams/%s/issues/%s/issue.md\n%s", testTeamKey, identifier, out)
		if !strings.Contains(string(out), "No such file or directory") {
			t.Errorf("shell writer should report ENOENT's strerror; got:\n%s", out)
		}
		if strings.Contains(string(out), "Input/output error") {
			t.Errorf("shell writer reported EIO, the pre-#445 misclassification:\n%s", out)
		}
	})

	// The rename path: renaming a doc file is a title update, and the same
	// rejection there is the third write shape #445 names.
	t.Run("rename_returns_ENOENT", func(t *testing.T) {
		doc := fixtures.FixtureAPIIssueDocument(issueID, 1)
		if err := fixtures.PopulateDocuments(ctx, testStore, []api.Document{doc}); err != nil {
			t.Fatalf("seed document: %v", err)
		}
		t.Cleanup(func() { _ = testStore.Queries().DeleteDocument(context.Background(), doc.ID) })

		dir := docsPath(testTeamKey, identifier)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir docs: %v", err)
		}
		name := firstRealEntry(entries)
		if name == "" {
			t.Fatal("seeded document did not appear in the listing")
		}

		out, mvErr := exec.Command("/bin/mv", dir+"/"+name, dir+"/renamed-after-deletion.md").CombinedOutput()
		t.Logf("$ mv <mount>/teams/%s/issues/%s/docs/%s renamed-after-deletion.md\n%s(exit err: %v)", testTeamKey, identifier, name, out, mvErr)
		if mvErr == nil {
			t.Fatal("rename should have failed")
		}
		if !strings.Contains(string(out), "No such file or directory") {
			t.Errorf("mv should report ENOENT's strerror; got:\n%s", out)
		}
		if strings.Contains(string(out), "Input/output error") {
			t.Errorf("mv reported EIO, the pre-#445 misclassification:\n%s", out)
		}
		data := readFileUntilContains(t, dir+"/.error", "Entity not found", errorVisibilityWait)
		t.Logf("docs/.error:\n%s", data)
		if !strings.Contains(string(data), "retrying will not help") {
			t.Errorf("docs/.error should say a retry will not help; got:\n%s", data)
		}
	})

	// The delete tail must NOT have moved: it intercepts the same rejection
	// earlier (remoteAlreadyGone), where "already gone" means the delete already
	// happened. rm therefore still succeeds and the row is forgotten.
	t.Run("delete_of_an_already_gone_entity_is_still_success", func(t *testing.T) {
		commentID := fmt.Sprintf("comment-gone-%d", uniq)
		user := fixtures.FixtureAPIUser()
		if err := fixtures.PopulateComments(ctx, testStore, issueID, []api.Comment{{
			ID:        commentID,
			Body:      "a comment Linear no longer has",
			CreatedAt: time.Now().Add(-time.Hour),
			UpdatedAt: time.Now().Add(-time.Hour),
			User:      &user,
		}}); err != nil {
			t.Fatalf("seed comment: %v", err)
		}
		t.Cleanup(func() { _ = testStore.Queries().DeleteComment(context.Background(), commentID) })

		dir := commentsPath(testTeamKey, identifier)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir comments: %v", err)
		}
		name := firstRealEntry(entries)
		if name == "" {
			t.Fatal("seeded comment did not appear in the listing")
		}

		out, rmErr := exec.Command("/bin/rm", dir+"/"+name).CombinedOutput()
		t.Logf("$ rm <mount>/teams/%s/issues/%s/comments/%s\n%s(exit err: %v)", testTeamKey, identifier, name, out, rmErr)
		if rmErr != nil {
			t.Fatalf("rm of an already-gone comment should succeed (idempotent delete), got %v: %s", rmErr, out)
		}
		after, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir comments after rm: %v", err)
		}
		if left := firstRealEntry(after); left != "" {
			t.Errorf("comment %q survived the rm; the phantom row was not forgotten", left)
		}
	})

	// The generated README is the doc an agent reads to learn the failure model,
	// so the new cause has to be named there too — an errno nothing documents is
	// the same trap EIO was.
	t.Run("generated_readme_names_the_cause", func(t *testing.T) {
		data, err := os.ReadFile(mountPoint + "/README.md")
		if err != nil {
			t.Fatalf("read mounted README.md: %v", err)
		}
		// The ENOENT entry is a two-line bullet; print it whole.
		lines := strings.Split(string(data), "\n")
		var enoent []string
		for i, line := range lines {
			if !strings.Contains(line, "-> ENOENT") {
				continue
			}
			if i > 0 && !strings.HasPrefix(strings.TrimSpace(line), "- ") {
				enoent = append(enoent, lines[i-1]) // the bullet wraps; keep its first line
			}
			enoent = append(enoent, line)
		}
		t.Logf("README.md failure model, ENOENT entry:\n%s", strings.Join(enoent, "\n"))
		if !strings.Contains(strings.Join(enoent, " "), "deleted upstream between a read and a write") {
			t.Errorf("README's ENOENT entry should name the upstream-deletion cause; got:\n%s", strings.Join(enoent, "\n"))
		}
	})
}
