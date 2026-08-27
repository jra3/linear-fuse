package integration

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// #447 end-to-end: the userspace surface a caller actually sees when the failure
// is an HTTP STATUS rather than a message Linear wrote. The unit tests pin
// api.HTTPError's predicates and classifyMutationErr in isolation; these drive a
// real create through the mount and assert what an agent experiences — the errno
// the syscall returns, and the wording .error carries.
//
// The mutator injected here is the REAL *api.Client, aimed at a local server that
// answers with a chosen status. That is deliberate: it exercises the whole chain
// the fix rests on — response status -> *api.HTTPError minted in query() ->
// predicate -> classifyMutationErr arm -> errno at the syscall -> .error text. A
// hand-built &api.HTTPError{} would skip the half of the change that lives in
// client.go and would keep passing if those two sites reverted to fmt.Errorf.
//
// Reverting client.go's two sites to fmt.Errorf and re-running this file shows
// what the fix is worth: the five status cases all collapse to EIO — which the
// generated README teaches as a retryable backend fault, so an agent retries a
// revoked key forever and re-creates after a 5xx without checking — and the
// 503-naming-a-missing-entity case lands on ENOENT, telling the agent to stop
// when waiting is the fix. The two 400 cases are unaffected either way; they are
// here as the boundary, not the bug.

// injectStatusMutator points LinearFS's mutation seam at a real *api.Client
// whose endpoint answers every request with status/body. Inert in live mode,
// where injecting anything would swap out the API the test names — every caller
// is behind a skipIfLiveAPI guard, but the coupling lives here rather than in
// each caller's discipline (same rationale as injectMutator).
func injectStatusMutator(t *testing.T, status int, body string) {
	t.Helper()
	if liveAPIMode {
		return
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	client := api.NewClient("lin_api_stub_key")
	client.SetAPIURL(srv.URL)
	lfs.InjectTestMutationClient(client)
	t.Cleanup(func() { lfs.InjectTestMutationClient(nil) })
}

// TestOffline_HTTPStatusVerdictsReachTheMount asserts the three verdicts #447
// settles, each through a real mkdir against the mount.
func TestOffline_HTTPStatusVerdictsReachTheMount(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: replaces the mutation client with one aimed at a local stub server to model an HTTP status; live it would mask the real API and this mkdir would create a real issue")

	cases := []struct {
		name      string
		status    int
		body      string
		wantErrno syscall.Errno
		wantIn    []string
		wantNotIn []string
	}{
		{
			// A revoked or mistyped key. EACCES, not EIO: no retry clears it, and
			// .error must name the key a HUMAN has to replace — not echo the
			// proxy's HTML, which says nothing Linear wrote.
			name:      "401 is EACCES naming the key",
			status:    http.StatusUnauthorized,
			body:      `<html><head><title>401 Unauthorized</title></head><body>nginx</body></html>`,
			wantErrno: syscall.EACCES,
			wantIn:    []string{"CREDENTIALS", "LINEAR_API_KEY", "did NOT take effect", "will NOT help"},
			wantNotIn: []string{"<html>", "nginx"},
		},
		{
			name:      "403 is EACCES too",
			status:    http.StatusForbidden,
			body:      `{"errors":[{"message":"Access denied"}]}`,
			wantErrno: syscall.EACCES,
			wantIn:    []string{"CREDENTIALS", "403"},
		},
		{
			// Linear's own side failing. EAGAIN (waiting is the fix), but the
			// outcome is UNKNOWN — a 500 may have applied the create and lost the
			// response, so .error must send the caller to check before retrying.
			name:      "500 is EAGAIN with an unknown outcome",
			status:    http.StatusInternalServerError,
			body:      `{"errors":[{"message":"Internal server error"}]}`,
			wantErrno: syscall.EAGAIN,
			wantIn:    []string{"UNKNOWN", "duplicate", "500"},
			wantNotIn: []string{"did not take effect"},
		},
		{
			name:      "503 is EAGAIN with an unknown outcome",
			status:    http.StatusServiceUnavailable,
			body:      `<html><body>upstream connect error</body></html>`,
			wantErrno: syscall.EAGAIN,
			wantIn:    []string{"UNKNOWN", "503"},
			wantNotIn: []string{"did not take effect"},
		},
		{
			// A bare 429 — body naming neither "RATELIMITED" nor "rate limit", so
			// only the status identifies it. It is refused at admission and never
			// reaches a mutation handler, so it earns the STRONGER promise.
			name:      "bare 429 is EAGAIN and does promise no effect",
			status:    http.StatusTooManyRequests,
			body:      `<html><body>Too Many Requests</body></html>`,
			wantErrno: syscall.EAGAIN,
			wantIn:    []string{"before it was sent", "did not take effect"},
			wantNotIn: []string{"UNKNOWN"},
		},
		{
			// Collision: the status arms sit ABOVE the text arms, so a status the
			// arms own wins over a body that would satisfy a message predicate.
			// Reporting a transient failure as permanently gone tells an agent to
			// stop when waiting is exactly what fixes it.
			name:      "503 whose body also names a missing entity stays EAGAIN",
			status:    http.StatusServiceUnavailable,
			body:      `{"errors":[{"message":"Entity not found: Issue - Could not find referenced Issue."}]}`,
			wantErrno: syscall.EAGAIN,
			wantNotIn: []string{"no longer exists on Linear"},
		},
		{
			// The other side of that boundary: a status in NEITHER set must leave
			// the text arms untouched. Linear reports a workspace over its plan
			// limit as HTTP 400, and that must still be EDQUOT (#409) — which is
			// only true if IsUsageLimited still matches THROUGH the new type. The
			// quoted "API error (status 400): …" below is that proof at the
			// product surface: HTTPError.Error() reproducing the historical
			// rendering is what keeps the three message predicates hooked up.
			name:      "400 usage-limit envelope is still EDQUOT",
			status:    http.StatusBadRequest,
			body:      `{"errors":[{"message":"usage limit exceeded"}]}`,
			wantErrno: syscall.EDQUOT,
			wantIn: []string{
				"plan/usage limit", "will NOT help",
				`API error (status 400): {"errors":[{"message":"usage limit exceeded"}]}`,
			},
		},
		{
			name:      "400 length-cap envelope is still EMSGSIZE",
			status:    http.StatusBadRequest,
			body:      `{"errors":[{"message":"title must be shorter than or equal to 255 characters."}]}`,
			wantErrno: syscall.EMSGSIZE,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			injectStatusMutator(t, tc.status, tc.body)

			dir := filepath.Join(issuesPath(testTeamKey), fmt.Sprintf("HTTP Status Probe %d", i))
			err := os.Mkdir(dir, 0o755)
			if !errors.Is(err, tc.wantErrno) {
				t.Fatalf("mkdir under HTTP %d = %v, want %v (%q)", tc.status, err, tc.wantErrno, tc.wantErrno)
			}
			// A rejected create leaves nothing behind.
			if _, serr := os.Stat(dir); serr == nil {
				t.Errorf("rejected create left %s in the listing", dir)
			}

			reason, rerr := os.ReadFile(issuesErrorPath(testTeamKey))
			if rerr != nil {
				t.Fatalf("read issues/.error: %v", rerr)
			}
			t.Logf("mkdir %q -> %v\nissues/.error:\n%s", filepath.Base(dir), err, reason)

			for _, want := range tc.wantIn {
				if !strings.Contains(string(reason), want) {
					t.Errorf(".error = %q, missing %q", reason, want)
				}
			}
			for _, notWant := range tc.wantNotIn {
				if strings.Contains(string(reason), notWant) {
					t.Errorf(".error = %q, must not contain %q", reason, notWant)
				}
			}
		})
	}
}

// TestOffline_HTTPStatusVerdictsReadableFromAShell is the same contract seen the
// way an agent driving this filesystem actually sees it: the errno rendered by
// strerror in a command's own message. The distinction #447 buys is only useful
// if it survives to that surface — "Permission denied" and "Resource temporarily
// unavailable" prescribe opposite next actions, and both used to read
// "Input/output error".
func TestOffline_HTTPStatusVerdictsReadableFromAShell(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: replaces the mutation client with one aimed at a local stub server to model an HTTP status; live it would mask the real API and this mkdir would create a real issue")
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh unavailable: %v", err)
	}

	cases := []struct {
		name    string
		status  int
		body    string
		wantMsg string // what strerror renders for the errno this must earn
	}{
		{"401", http.StatusUnauthorized, `<html><title>401 Unauthorized</title></html>`, "Permission denied"},
		{"503", http.StatusServiceUnavailable, `<html><body>upstream connect error</body></html>`, "Resource temporarily unavailable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			injectStatusMutator(t, tc.status, tc.body)

			issues := issuesPath(testTeamKey)
			title := "Shell Probe " + tc.name
			// LC_ALL=C pins strerror's wording; the transcript is the assertion.
			script := fmt.Sprintf(
				"export LC_ALL=C; cd %q; mkdir %q; echo \"exit=$?\"; echo '--- .error ---'; cat .error",
				issues, title)
			out, _ := exec.Command("/bin/sh", "-c", script).CombinedOutput()
			t.Logf("$ mkdir %q   # Linear answers HTTP %d\n%s", title, tc.status, out)

			if !strings.Contains(string(out), tc.wantMsg) {
				t.Errorf("mkdir under HTTP %d did not report %q to the shell:\n%s", tc.status, tc.wantMsg, out)
			}
			if strings.Contains(string(out), "Input/output error") {
				t.Errorf("HTTP %d still surfaces as the EIO fallthrough:\n%s", tc.status, out)
			}
			if strings.Contains(string(out), "exit=0") {
				t.Errorf("mkdir reported success under HTTP %d:\n%s", tc.status, out)
			}
		})
	}
}
