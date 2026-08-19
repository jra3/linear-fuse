package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/fs"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// #445 end-to-end: the userspace surface a caller actually sees when LINEAR is
// the one saying the referenced entity is gone. The unit tests pin
// classifyMutationErr and api.IsNotFound in isolation; these two drive a real
// edit through the mount and assert what an agent experiences — the errno the
// write returns, and the wording .error carries.
//
// The pair is deliberate. Both mutators fail an edit with text containing
// "Entity not found", and the two must land on OPPOSITE verdicts:
//
//   - goneMutator: Linear's own rejection, which OPENS the message -> ENOENT
//     plus "retrying will NOT help", not the EIO fallthrough the generated
//     README teaches as a retryable backend fault.
//   - echoedNameMutator: a validation rejection that merely QUOTES a caller
//     -supplied name -> the EINVAL its fixable input earns, with no gone
//     verdict in .error.

// goneMutator is the mock mutation client with UpdateIssue failing the way
// Linear does when the entity was archived or deleted between the read that
// produced the listing and this write.
type goneMutator struct {
	*mockmutation.Client
}

func (goneMutator) UpdateIssue(ctx context.Context, issueID string, input map[string]any) error {
	return &api.GraphQLError{Message: "Entity not found: Issue - Could not find referenced Issue."}
}

// echoedNameMutator is the hazard the anchoring exists to close: Linear renders
// user-supplied entity names back into its rejections, so a workspace that owns
// a label named "Entity not found" draws validation messages that merely quote
// the phrase mid-sentence.
type echoedNameMutator struct {
	*mockmutation.Client
}

func (echoedNameMutator) UpdateIssue(ctx context.Context, issueID string, input map[string]any) error {
	return &api.GraphQLError{
		Message:   "The label 'Entity not found' is a group and cannot be assigned to projects directly.",
		UserError: true,
	}
}

// throttledMutator is the third spelling: a 429 envelope that names a missing
// entity alongside the throttle. Waiting is exactly what fixes it, so it must
// stay EAGAIN and must not be reported permanently unfixable.
type throttledMutator struct {
	*mockmutation.Client
}

func (throttledMutator) UpdateIssue(ctx context.Context, issueID string, input map[string]any) error {
	return errors.New(`API error (status 429): {"errors":[{"message":"RATELIMITED"},{"message":"Entity not found"}]}`)
}

// injectMutator swaps in a wrapper over the in-memory fake for one test.
func injectMutator(t *testing.T, wrap func(*mockmutation.Client) fs.MutationClient) {
	t.Helper()
	mock := mockmutation.New(
		mockmutation.WithTeamKey(testTeamKey),
		mockmutation.WithStore(lfs.GetStore()),
	)
	lfs.InjectTestMutationClient(wrap(mock))
	t.Cleanup(func() { lfs.InjectTestMutationClient(nil) })
}

// editIssueTitle rewrites issue.md with a new title and returns the error the
// write surfaces — the errno an editor, a shell redirect, or an agent sees.
//
// It saves through claudeToolAtomicSave rather than a raw os.WriteFile because
// the read above primes the kernel page cache for this path: an O_TRUNC+write
// against a primed cache can be served without ever reaching Flush, so the
// rejection under test would never reach the caller and the assertion would
// pass or fail on timing. The rename routes the bytes through the directory's
// Rename handler, which runs Flush inline and returns the errno directly — the
// only save form whose verdict is synchronous, and the one every other
// failing-write test in this package already uses.
func editIssueTitle(t *testing.T, identifier, title string) error {
	t.Helper()
	path := issueFilePath(testTeamKey, identifier)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	modified, err := modifyFrontmatter(content, "title", title)
	if err != nil {
		t.Fatalf("modify frontmatter: %v", err)
	}
	return claudeToolAtomicSave(t, path, modified)
}

// TestOffline_ServerNotFoundIsLegible: an edit Linear rejects because it no
// longer has the entity must return ENOENT — the errno the mount's contract
// already gives "reference to something that doesn't exist" — and .error must
// say retrying is futile. Before #445 this fell to the EIO fallthrough, which
// the generated README teaches as a retryable backend fault, so every retry
// earned the same rejection.
func TestOffline_ServerNotFoundIsLegible(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: needs the injected mutator to model Linear's not-found rejection; live the entity exists and this edit would mutate a real issue")

	identifier := someIssueID(t)
	injectMutator(t, func(m *mockmutation.Client) fs.MutationClient { return goneMutator{m} })

	err := editIssueTitle(t, identifier, "Gone Upstream Probe")
	if !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("edit under a server not-found = %v, want ENOENT (%q)", err, syscall.ENOENT)
	}

	reason := readIssueError(t, identifier)
	// The errno says "no such file"; only .error can name WHICH reference, that
	// the write did not take effect, and what to do next.
	for _, want := range []string{
		"update issue",
		"Entity not found: Issue",
		"no longer exists on Linear",
		"retrying will NOT help",
		"re-read the directory listing",
	} {
		if !strings.Contains(reason, want) {
			t.Errorf(".error = %q, missing %q", reason, want)
		}
	}
}

// TestOffline_EchoedNotFoundStaysEINVAL: the same phrase, echoed back from the
// caller's own input, must NOT buy the gone verdict. This is what the arm order
// (structure before text) and the anchored predicate defend jointly; getting it
// wrong tells an agent that retrying will not help when fixing the field is
// exactly what fixes it.
func TestOffline_EchoedNotFoundStaysEINVAL(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: needs the injected mutator to model Linear echoing a caller-supplied name into its rejection; live the edit would mutate a real issue")

	identifier := someIssueID(t)
	injectMutator(t, func(m *mockmutation.Client) fs.MutationClient { return echoedNameMutator{m} })

	err := editIssueTitle(t, identifier, "Echoed Name Probe")
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("edit under an echoed-name rejection = %v, want EINVAL (%q)", err, syscall.EINVAL)
	}

	reason := readIssueError(t, identifier)
	if !strings.Contains(reason, "is a group and cannot be assigned") {
		t.Errorf(".error = %q, want the server's own rejection", reason)
	}
	for _, unwanted := range []string{"no longer exists on Linear", "retrying will NOT help"} {
		if strings.Contains(reason, unwanted) {
			t.Errorf(".error = %q, must not carry %q for an echoed name", reason, unwanted)
		}
	}
	// The issue is still there: an echoed name is a rejected write, not a
	// vanished entity.
	if _, err := os.Stat(filepath.Join(issueDirPath(testTeamKey, identifier), "issue.md")); err != nil {
		t.Errorf("issue.md should still be listed after a rejected edit: %v", err)
	}
}

// TestOffline_ThrottledNotFoundStaysEAGAIN: the worst misclassification of the
// three. A throttled write whose envelope also names a missing entity must keep
// the retryable verdict — EAGAIN and "wait and retry" — rather than the gone
// verdict telling an agent to stop trying.
func TestOffline_ThrottledNotFoundStaysEAGAIN(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: needs the injected mutator to model a 429 envelope naming a missing entity; live the edit would mutate a real issue")

	identifier := someIssueID(t)
	injectMutator(t, func(m *mockmutation.Client) fs.MutationClient { return throttledMutator{m} })

	err := editIssueTitle(t, identifier, "Throttle Probe")
	if !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("edit under a throttle naming a missing entity = %v, want EAGAIN (%q)", err, syscall.EAGAIN)
	}

	reason := readIssueError(t, identifier)
	if !strings.Contains(reason, "Wait a few seconds and retry") {
		t.Errorf(".error = %q, want the retryable wording", reason)
	}
	if strings.Contains(reason, "will NOT help") {
		t.Errorf(".error = %q, must not tell the caller retrying is futile", reason)
	}
}
