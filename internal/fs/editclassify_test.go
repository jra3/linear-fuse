package fs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// These tests pin the edit-mutation failure model: an edit site's mutation
// failure routes through classifyMutationErr (the same classifier the create
// and delete tails use), so a rate-limited edit returns EAGAIN with a retry
// hint in .error, and a structured Linear input rejection (userError: true)
// returns EINVAL carrying the server's user-presentable message — never the
// old flat EIO the hand-rolled sites returned.

// failingMutator satisfies MutationClient by embedding the interface (nil) and
// overriding only the update methods under test; anything else panics, which
// is fine — these tests exercise the mutation-error path exclusively.
type failingMutator struct {
	MutationClient
	err error
}

func (f failingMutator) UpdateComment(ctx context.Context, commentID, body string) (*api.Comment, error) {
	return nil, f.err
}

func (f failingMutator) UpdateLabel(ctx context.Context, id string, input map[string]any) (*api.Label, error) {
	return nil, f.err
}

// newEditTestLFS builds the minimal LinearFS an edit error path touches: the
// writeFeedback store (.error) plus the injected failing mutation client.
func newEditTestLFS(t *testing.T, err error) *LinearFS {
	t.Helper()
	lfs := &LinearFS{writeFeedback: newWriteFeedback(nil)}
	lfs.InjectTestMutationClient(failingMutator{err: err})
	return lfs
}

func TestCommentEditFlush_RateLimitedIsEAGAIN(t *testing.T) {
	rl := &api.GraphQLError{Message: "Rate limit exceeded", Code: "RATELIMITED"}
	lfs := newEditTestLFS(t, rl)

	n := &CommentNode{
		BaseNode: BaseNode{lfs: lfs},
		issueID:  "issue-1",
		comment:  api.Comment{ID: "c-1", Body: "old body"},
	}
	n.content = []byte("new body")
	n.dirty = true

	errno := n.Flush(context.Background(), nil)

	if errno != syscall.EAGAIN {
		t.Fatalf("Flush errno = %v, want EAGAIN", errno)
	}
	we := lfs.GetWriteError(collectionErrorKey("comments", "issue-1"))
	if we == nil {
		t.Fatal(".error not set for rate-limited comment edit")
	}
	if !strings.Contains(we.Message, "rate-limited") || !strings.Contains(we.Message, "retry") {
		t.Errorf(".error = %q, want a rate-limited retry hint", we.Message)
	}
	if !strings.Contains(we.Message, "update comment") {
		t.Errorf(".error = %q, want the op name 'update comment'", we.Message)
	}
}

func TestLabelEditFlush_UserErrorIsEINVALWithPresentableMessage(t *testing.T) {
	rejection := &api.GraphQLError{
		Message:                "labelIds contain parent labels",
		Code:                   "INPUT_ERROR",
		UserError:              true,
		UserPresentableMessage: "The label 'X' is a group and cannot be assigned directly.",
	}
	lfs := newEditTestLFS(t, rejection)

	n := &LabelFileNode{
		BaseNode: BaseNode{lfs: lfs},
		label:    api.Label{ID: "l-1", Name: "Old Name"},
		teamID:   "team-1",
	}
	n.content = []byte("---\nname: New Name\n---\n")
	n.dirty = true

	errno := n.Flush(context.Background(), nil)

	if errno != syscall.EINVAL {
		t.Fatalf("Flush errno = %v, want EINVAL", errno)
	}
	we := lfs.GetWriteError(collectionErrorKey("labels", "team-1"))
	if we == nil {
		t.Fatal(".error not set for rejected label edit")
	}
	if !strings.Contains(we.Message, rejection.UserPresentableMessage) {
		t.Errorf(".error = %q, want the server's user-presentable message %q",
			we.Message, rejection.UserPresentableMessage)
	}
}

func TestLabelEditFlush_BackendFailureStaysEIO(t *testing.T) {
	lfs := newEditTestLFS(t, &api.GraphQLError{Message: "internal server error"})

	n := &LabelFileNode{
		BaseNode: BaseNode{lfs: lfs},
		label:    api.Label{ID: "l-1", Name: "Old Name"},
		teamID:   "team-1",
	}
	n.content = []byte("---\nname: New Name\n---\n")
	n.dirty = true

	if errno := n.Flush(context.Background(), nil); errno != syscall.EIO {
		t.Fatalf("Flush errno = %v, want EIO for an unclassified backend failure", errno)
	}
}

// TestClassifyMutationErr_TooLongIsEMSGSIZE pins the length-cap errno: a length-cap
// rejection is a size error, so the errno itself hints (EMSGSIZE) rather than a
// bare EINVAL — while the reason still lands in .error. A userError that is NOT
// a length limit stays EINVAL.
func TestClassifyMutationErr_TooLongIsEMSGSIZE(t *testing.T) {
	tooLong := &api.GraphQLError{
		Message:                "description must be shorter than or equal to 255 characters.",
		Code:                   "INPUT_ERROR",
		UserError:              true,
		UserPresentableMessage: "description must be shorter than or equal to 255 characters.",
	}
	msg, errno := classifyMutationErr("update project", tooLong)
	if errno != syscall.EMSGSIZE {
		t.Fatalf("errno = %v, want EMSGSIZE for a length-cap rejection", errno)
	}
	if !strings.Contains(msg, "shorter than or equal to") {
		t.Errorf(".error = %q, want the server's length message", msg)
	}

	// A non-length userError must remain EINVAL (the errno hint is specific).
	other := &api.GraphQLError{Message: "bad enum value", Code: "INPUT_ERROR", UserError: true}
	if _, errno := classifyMutationErr("update project", other); errno != syscall.EINVAL {
		t.Fatalf("errno = %v, want EINVAL for a non-length userError", errno)
	}

	// The two cases api.IsFieldTooLong documents but that the classifier could not
	// reach while the check sat inside the userError gate: the cap phrasing rides
	// only in UserPresentableMessage with the tag unset, and the rejection arrives
	// as a plain HTTP-400 envelope that is not a *GraphQLError at all. Both used to
	// fall through to EIO — "backend failure", inviting a retry of a write that
	// would be rejected identically forever.
	untagged := []struct {
		name string
		err  error
	}{
		{"cap phrasing in UserPresentableMessage, userError unset", &api.GraphQLError{
			Message:                "Argument Validation Error",
			UserPresentableMessage: "name must be at most 80 characters",
		}},
		{"plain HTTP-400 envelope", errors.New(
			`API error (status 400): {"errors":[{"message":"title must be shorter than or equal to 255 characters."}]}`)},
	}
	for _, tc := range untagged {
		t.Run(tc.name, func(t *testing.T) {
			if _, errno := classifyMutationErr("update project", tc.err); errno != syscall.EMSGSIZE {
				t.Fatalf("errno = %v, want EMSGSIZE — a length cap is a size error whether or not Linear tagged it", errno)
			}
		})
	}
}

// TestClassifyMutationErr_UsageLimitIsEDQUOT pins #409. A workspace over its plan
// limit is neither the caller's bad input (EINVAL blames a name that was fine)
// nor a backend hiccup (EIO invites a retry that cannot succeed): it is a
// capacity wall, so EDQUOT carries the meaning and .error carries the action.
//
// The load-bearing assertion is that BOTH tag states produce the same errno.
// Linear's extensions.userError for this rejection has never been observed, so
// an arm below the userError gate would make the errno depend on a bit we cannot
// predict — EINVAL if set, EIO if not. This test is what pins the arm above it.
func TestClassifyMutationErr_UsageLimitIsEDQUOT(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"userError unset", &api.GraphQLError{Message: "usage limit exceeded"}},
		{"userError set", &api.GraphQLError{Message: "usage limit exceeded", UserError: true}},
		{"wrapped", fmt.Errorf("mutation IssueCreate failed: %w", &api.GraphQLError{Message: "usage limit exceeded"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, errno := classifyMutationErr("create issue", tc.err)
			if errno != syscall.EDQUOT {
				t.Fatalf("errno = %v, want EDQUOT for a plan/usage limit", errno)
			}
			// The errno alone cannot say WHICH limit, nor that waiting is futile.
			for _, want := range []string{"plan/usage limit", "did NOT take effect", "will NOT help", "usage limit exceeded"} {
				if !strings.Contains(msg, want) {
					t.Errorf(".error = %q, missing %q", msg, want)
				}
			}
		})
	}

	// A server rate limit must NOT land here — it is retryable, and telling the
	// caller that retrying will not help would be worse than the bug being fixed.
	rateLimited := &api.GraphQLError{Message: "rate limit exceeded", Code: "RATELIMITED"}
	if _, errno := classifyMutationErr("create issue", rateLimited); errno != syscall.EAGAIN {
		t.Fatalf("errno = %v, want EAGAIN — a rate limit is not a plan wall", errno)
	}
}
