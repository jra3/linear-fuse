package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
)

// TestRejectedWriteVerdictReachesTheWriter pins where a rejected save's verdict
// goes, which is the contract behind #455.
//
// The filesystem returns the errno at close(2), not at write(2) — the save runs
// in Flush. Whether a caller SEES it is therefore a property of the writer, not
// of the mount: a writer that checks close reports the failure, and a shell
// builtin performing `>` redirection discards it, which is why a rejected write
// through `printf > issue.md` reports success.
//
// This is a documented-behavior test, not an aspiration: it asserts what the
// contract is today so a change to it is deliberate. #455 tracks the separate
// question of giving callers a verdict channel that does not depend on the
// writer's close handling.
func TestRejectedWriteVerdictReachesTheWriter(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: seeds throwaway rows and drives rejected writes against them")
	if testStore == nil {
		t.Fatal("fixture mode left no test store")
	}

	// Invalid YAML: an unquoted @-scalar cannot start a token. Rejected in parse,
	// before any mutation goes out.
	const badDoc = "---\ntitle: \"x\"\nlabels: [@errands]\n---\nbody\n"

	seed := func(t *testing.T, tag string) string {
		t.Helper()
		ctx := context.Background()
		team := fixtures.FixtureAPITeam()
		uniq := time.Now().UnixNano()
		issueID := fmt.Sprintf("verdict-%s-%d", tag, uniq)
		identifier := fmt.Sprintf("TST-%d", 20000+uniq%10000)
		row, err := db.APIIssueToDBIssue(fixtures.FixtureAPIIssue(
			fixtures.WithIssueID(issueID, identifier),
			fixtures.WithTitle("Verdict probe"),
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
		return mountPoint + "/teams/" + testTeamKey + "/issues/" + identifier + "/issue.md"
	}

	// close(2) carries the verdict, and Go checks it.
	t.Run("close_returns_the_errno", func(t *testing.T) {
		enableMockMutations(t)
		path := seed(t, "close")

		f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if _, err := f.Write([]byte(badDoc)); err != nil {
			_ = f.Close()
			t.Fatalf("write returned an error, but the save runs in Flush: %v", err)
		}
		if err := f.Close(); err == nil {
			t.Fatal("close returned nil for a rejected write; the verdict is not reaching close(2)")
		}
	})

	// A successful write clears .error — it is not sticky on this path.
	t.Run("successful_write_clears_error_file", func(t *testing.T) {
		enableMockMutations(t)
		path := seed(t, "clear")
		errPath := strings.TrimSuffix(path, "/issue.md") + "/.error"

		f, _ := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
		_, _ = f.Write([]byte(badDoc))
		_ = f.Close()

		if b, err := os.ReadFile(errPath); err != nil || len(strings.TrimSpace(string(b))) == 0 {
			t.Fatalf(".error should describe the rejection; got %q (err %v)", b, err)
		}

		g, _ := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
		_, _ = g.Write([]byte("---\ntitle: \"Verdict probe\"\n---\nclean body\n"))
		if err := g.Close(); err != nil {
			t.Fatalf("valid write was rejected: %v", err)
		}

		b, err := os.ReadFile(errPath)
		if err == nil && len(strings.TrimSpace(string(b))) != 0 {
			t.Errorf(".error survived a successful write: %q", b)
		}
	})

	// The writer matrix: who preserves the close-time verdict and who drops it.
	t.Run("writer_matrix", func(t *testing.T) {
		shell := "/bin/sh"
		if _, err := os.Stat(shell); err != nil {
			t.Skipf("%s unavailable", shell)
		}

		runScript := func(t *testing.T, script string) string {
			t.Helper()
			out, _ := exec.Command(shell, "-c", script).CombinedOutput()
			return strings.TrimSpace(string(out))
		}

		// A shell builtin's `>` redirection does not check close(2): the write is
		// rejected, the mount reports it, and the shell still says 0.
		t.Run("builtin_redirection_loses_the_verdict", func(t *testing.T) {
			enableMockMutations(t)
			path := seed(t, "builtin")
			got := runScript(t, fmt.Sprintf("printf -- '%%s' %q > %q; echo EXIT=$?", badDoc, path))
			if !strings.Contains(got, "EXIT=0") {
				t.Errorf("expected the builtin redirect to report success despite rejection; got %q", got)
			}
		})

		// Writers that check close report the failure.
		for name, tmpl := range map[string]string{
			"external_printf": "/usr/bin/printf -- '%%s' %q > %q; echo EXIT=$?",
			"tee":             "printf -- '%%s' %q | tee %q >/dev/null; echo EXIT=$?",
			"dd":              "printf -- '%%s' %q | dd of=%q status=none; echo EXIT=$?",
		} {
			t.Run(name+"_reports_the_verdict", func(t *testing.T) {
				enableMockMutations(t)
				path := seed(t, name)
				got := runScript(t, fmt.Sprintf(tmpl, badDoc, path))
				if strings.Contains(got, "EXIT=0") {
					t.Errorf("%s should surface the close-time rejection; got %q", name, got)
				}
			})
		}
	})
}
