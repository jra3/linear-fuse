package fs

import (
	"context"
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
