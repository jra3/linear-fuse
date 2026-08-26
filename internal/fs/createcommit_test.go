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

// fakeCreateSink records every interaction the create tail can have so a test
// can assert what it did without a LinearFS. It satisfies createSink.
type fakeCreateSink struct {
	fakeSink // .error interactions (editcommit_test.go)

	appendKey     string
	appendResult  WriteResult
	appends       int
	failAppendKey string
	failAppendMsg string
	failAppends   int
	invalidateDir uint64
	invalidateNam string
	invalidates   int
}

func (f *fakeCreateSink) AppendWriteSuccess(key string, r WriteResult) {
	f.appendKey, f.appendResult = key, r
	f.appends++
}

func (f *fakeCreateSink) AppendWriteFailure(key, msg string) {
	f.failAppendKey, f.failAppendMsg = key, msg
	f.failAppends++
}

func (f *fakeCreateSink) InvalidateCreated(dirIno uint64, name string) {
	f.invalidateDir, f.invalidateNam = dirIno, name
	f.invalidates++
}

// okSpec returns a spec whose mutate succeeds; tests override the parts they
// exercise. The counters record the success tail's side effects.
func okSpec(created *ent, persists, extras *int) createSpec[ent] {
	return createSpec[ent]{
		op:  "create ent",
		key: "K",
		mutate: func(context.Context) (*ent, error) {
			return created, nil
		},
		result: func(e *ent) WriteResult {
			return WriteResult{Title: e.title, Path: "on-disk-name"}
		},
		persist:   func(context.Context, *ent) error { *persists++; return nil },
		dir:       42,
		entryName: func(e *ent) string { return "on-disk-name" },
		invalidateExtra: func(*ent) {
			*extras++
		},
	}
}

func TestCommitCreate_Success(t *testing.T) {
	sink := &fakeCreateSink{}
	persists, extras := 0, 0
	created := &ent{title: "made"}

	got, errno := commitCreate(context.Background(), sink, okSpec(created, &persists, &extras))

	if errno != 0 || got != created {
		t.Fatalf("got (%v, %v), want (%v, 0)", got, errno, created)
	}
	if sink.clears != 1 || sink.clearKey != "K" {
		t.Errorf("ClearWriteError: calls=%d key=%q, want 1 call on K", sink.clears, sink.clearKey)
	}
	if sink.setCalls != 0 {
		t.Errorf("SetWriteError calls = %d, want 0", sink.setCalls)
	}
	if sink.appends != 1 || sink.appendKey != "K" || sink.appendResult.Title != "made" {
		t.Errorf(".last append: calls=%d key=%q result=%+v", sink.appends, sink.appendKey, sink.appendResult)
	}
	if sink.failAppends != 0 {
		t.Errorf("AppendWriteFailure ran on success: calls=%d, want 0", sink.failAppends)
	}
	if persists != 1 {
		t.Errorf("persist calls = %d, want 1", persists)
	}
	if sink.invalidates != 1 || sink.invalidateDir != 42 || sink.invalidateNam != "on-disk-name" {
		t.Errorf("InvalidateCreated: calls=%d dir=%d name=%q, want (1, 42, on-disk-name)",
			sink.invalidates, sink.invalidateDir, sink.invalidateNam)
	}
	if extras != 1 {
		t.Errorf("invalidateExtra calls = %d, want 1", extras)
	}
}

func TestCommitCreate_Classification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantErrno syscall.Errno
		wantIn    string // substring of the .error message
	}{
		{
			name:      "FieldError is EINVAL with Field/Value/Error detail",
			err:       &FieldError{Field: "name", Value: "x", Message: "bad"},
			wantErrno: syscall.EINVAL,
			wantIn:    "Field: name",
		},
		{
			name:      "notFoundError is ENOENT",
			err:       &notFoundError{FieldError{Field: "identifier", Value: "ENG-999", Message: "unknown issue"}},
			wantErrno: syscall.ENOENT,
			wantIn:    "unknown issue",
		},
		{
			name:      "deadline is EAGAIN with a retry hint",
			err:       context.DeadlineExceeded,
			wantErrno: syscall.EAGAIN,
			wantIn:    "retry",
		},
		{
			name:      "rate limit is EAGAIN with a retry hint",
			err:       errors.New("rate limit exceeded"),
			wantErrno: syscall.EAGAIN,
			wantIn:    "rate-limited",
		},
		{
			// #399: a request cancelled AFTER the POST went out is still EAGAIN,
			// but its outcome is genuinely unknown — Linear may have applied it
			// and lost the response. The .error must say so, because the caller's
			// next move differs: check before retrying, or risk a duplicate.
			name:      "in-flight cancellation is EAGAIN but does not claim no-effect",
			err:       fmt.Errorf("failed to execute request: %w (%w)", context.Canceled, api.ErrInFlight),
			wantErrno: syscall.EAGAIN,
			wantIn:    "UNKNOWN",
		},
		{
			// The pre-send twin, for contrast: this one CAN promise no effect.
			name:      "pre-send deferral says the operation did not take effect",
			err:       fmt.Errorf("deferred: %w", api.ErrDeferred),
			wantErrno: syscall.EAGAIN,
			wantIn:    "did not take effect",
		},
		{
			// #409, using the exact error observed in the first live write
			// dispatch — where it failed 42 of 45 creates while reading as either
			// bad input or a backend fault, never as a quota.
			name:      "usage limit is EDQUOT naming the plan limit",
			err:       &api.GraphQLError{Message: "usage limit exceeded"},
			wantErrno: syscall.EDQUOT,
			wantIn:    "plan/usage limit",
		},
		{
			// #445: the server-side twin of the notFoundError row above. Linear
			// says the referenced entity is gone — the same condition, so the same
			// errno, not the EIO fallthrough that reads as a retryable backend
			// fault. Reachable whenever the local catalog is ahead of the
			// workspace.
			name:      "server not-found is ENOENT, not a retryable EIO",
			err:       &api.GraphQLError{Message: "Entity not found: Issue - Could not find referenced Issue."},
			wantErrno: syscall.ENOENT,
			wantIn:    "no longer exists on Linear",
		},
		{
			// The plain-string form (an HTTP-400 envelope) classifies the same:
			// the predicate, not the error type, is what decides.
			name:      "server not-found in a plain envelope is ENOENT",
			err:       errors.New(`API error (status 400): {"errors":[{"message":"Entity not found: Comment - Could not find referenced Comment."}]}`),
			wantErrno: syscall.ENOENT,
			wantIn:    "retrying will NOT help",
		},
		{
			// #445 arm ORDER, first of three. api.IsNotFound answers on message
			// TEXT, and *FieldError renders the caller's frontmatter value
			// verbatim — so with the not-found arm above this one, a caller who
			// wrote `status: Entity not found` picked their own errno and got
			// ENOENT plus "retrying will NOT help" for a typo that a corrected
			// status fixes. Structural arms outrank textual ones.
			name:      "a FieldError whose VALUE is the phrase stays EINVAL",
			err:       &FieldError{Field: "status", Value: "Entity not found", Message: "unknown state. See states.md"},
			wantErrno: syscall.EINVAL,
			wantIn:    "unknown state",
		},
		{
			// Second: Linear echoes user-supplied entity names into
			// UserPresentableMessage, so a workspace owning a label named
			// "Entity not found" must still get the EINVAL its fixable input
			// rejection earns. Pinned here as well as on the predicate because
			// this arm sits above the userError gate by design (#409) — ordering
			// alone cannot save it, only the predicate's anchoring can.
			name:      "an echoed entity name in a userError stays EINVAL",
			err:       &api.GraphQLError{Message: "The label 'Entity not found' is a group and cannot be assigned", UserError: true},
			wantErrno: syscall.EINVAL,
			wantIn:    "is a group",
		},
		{
			// Third, and the worst of them: a retryable throttle reported to an
			// agent as permanently unfixable. Waiting is exactly what fixes this,
			// so the EAGAIN arm must claim it first.
			name:      "a throttle whose envelope also names a missing entity is EAGAIN",
			err:       errors.New(`API error (status 429): {"errors":[{"message":"RATELIMITED"},{"message":"Entity not found"}]}`),
			wantErrno: syscall.EAGAIN,
			wantIn:    "Wait a few seconds and retry",
		},
		{
			name:      "anything else is EIO carrying the cause",
			err:       errors.New("boom"),
			wantErrno: syscall.EIO,
			wantIn:    "boom",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &fakeCreateSink{}
			persists, extras := 0, 0
			spec := okSpec(nil, &persists, &extras)
			spec.mutate = func(context.Context) (*ent, error) { return nil, tc.err }

			got, errno := commitCreate(context.Background(), sink, spec)

			if got != nil || errno != tc.wantErrno {
				t.Fatalf("got (%v, %v), want (nil, %v)", got, errno, tc.wantErrno)
			}
			if sink.setCalls != 1 || sink.setKey != "K" {
				t.Errorf("SetWriteError: calls=%d key=%q, want 1 call on K", sink.setCalls, sink.setKey)
			}
			if !strings.Contains(sink.setMsg, tc.wantIn) {
				t.Errorf(".error = %q, want it to contain %q", sink.setMsg, tc.wantIn)
			}
			if !strings.Contains(sink.setMsg, "Operation: create ent") &&
				(tc.wantErrno == syscall.EAGAIN || tc.wantErrno == syscall.EIO ||
					tc.wantErrno == syscall.EDQUOT) {
				t.Errorf(".error = %q, want the op name in API-failure messages", sink.setMsg)
			}
			// A clean failure appends one countable outcome to .last (#370),
			// carrying the same reason .error got.
			if sink.failAppends != 1 || sink.failAppendKey != "K" || sink.failAppendMsg != sink.setMsg {
				t.Errorf("AppendWriteFailure: calls=%d key=%q msg=%q, want 1 on K carrying the .error msg",
					sink.failAppends, sink.failAppendKey, sink.failAppendMsg)
			}
			// The failure path must not run any of the success tail.
			if sink.clears != 0 || sink.appends != 0 || persists != 0 || sink.invalidates != 0 || extras != 0 {
				t.Errorf("success tail ran on failure: clears=%d appends=%d persists=%d invalidates=%d extras=%d",
					sink.clears, sink.appends, persists, sink.invalidates, extras)
			}
		})
	}
}

// TestClassifyMutationErr_NotFoundJoin pins the seam where the #445 arm appends
// its own sentence to Linear's message. Linear's canonical not-found text ends
// in a full stop, so joining onto it raw rendered "...referenced Issue.. The
// referenced entity no longer exists..." — a typo in the one sentence the arm
// exists to make an agent trust. Both spellings must read as one sentence
// followed by another, and both must keep the verdict's substrings.
func TestClassifyMutationErr_NotFoundJoin(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string // the join, rendered exactly
	}{
		{
			name: "a server message ending in a full stop gets exactly one",
			err:  &api.GraphQLError{Message: "Entity not found: Issue - Could not find referenced Issue."},
			want: "referenced Issue. The referenced entity no longer exists on Linear",
		},
		{
			name: "a server message with no trailing stop still gets its separator",
			err:  &api.GraphQLError{Message: "Entity not found: Issue"},
			want: "Entity not found: Issue. The referenced entity no longer exists on Linear",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, errno := classifyMutationErr("update issue", tc.err)

			if errno != syscall.ENOENT {
				t.Fatalf("errno = %v, want ENOENT", errno)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf(".error = %q, want it to contain %q", msg, tc.want)
			}
			if strings.Contains(msg, "..") {
				t.Errorf(".error = %q, want no doubled full stop", msg)
			}
			for _, keep := range []string{"no longer exists on Linear", "retrying will NOT help"} {
				if !strings.Contains(msg, keep) {
					t.Errorf(".error = %q, want it to contain %q", msg, keep)
				}
			}
		})
	}
}

// TestClassifyMutationErr_EIODetail pins #446 part 1: the EIO fallthrough
// renders the server's user-presentable message when it sent one, whether or
// not Linear tagged the rejection userError. #409's lesson is the reason —
// extensions.userError is the server's choice about how to LABEL a rejection,
// not a fact about which text is useful, so no arm should let the tag decide
// what the caller gets to read.
//
// The errno is deliberately untouched: reclassifying untagged validation
// phrasings as EINVAL is #446 part 2 and needs its own judgement.
func TestClassifyMutationErr_EIODetail(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantIn    string
		wantNotIn string
	}{
		{
			// The live shape, from CI run 30578999501 (documents_test.go): five
			// occurrences of a rejection whose Message says nothing and whose
			// UserPresentableMessage carries the field-specific reason.
			//
			// The reason is deliberately NOT a length complaint. #409 already
			// hoisted the cap phrasings ("must be at most", "shorter than or
			// equal to") into the EMSGSIZE arm above, which classifies on the
			// CONDITION rather than the tag — so a too-long rejection never
			// reaches this fallthrough regardless of how Linear tagged it. What
			// lands here is every OTHER untagged validation rejection, which had
			// no arm reading its user-presentable text at all.
			name: "untagged rejection prefers the user-presentable message",
			err: &api.GraphQLError{
				Message:                "Argument Validation Error",
				UserPresentableMessage: "color must be a valid hex color code",
			},
			wantIn:    "color must be a valid hex color code",
			wantNotIn: "Argument Validation Error",
		},
		{
			// No user-presentable text to prefer, so the terse message stands —
			// but without Error()'s "GraphQL error: " wrapper, matching every
			// other arm that renders through serverDetail.
			name:      "untagged rejection with only a message drops the wrapper",
			err:       &api.GraphQLError{Message: "Something broke server-side"},
			wantIn:    "Something broke server-side",
			wantNotIn: "GraphQL error:",
		},
		{
			// serverDetail falls through to err.Error() for anything that is not
			// a *api.GraphQLError, so non-GraphQL failures render exactly as before.
			name:   "a non-GraphQL error is unchanged",
			err:    errors.New("dial tcp: connection refused"),
			wantIn: "dial tcp: connection refused",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, errno := classifyMutationErr("update document", tc.err)

			if errno != syscall.EIO {
				t.Fatalf("errno = %v, want EIO", errno)
			}
			if !strings.Contains(msg, tc.wantIn) {
				t.Errorf(".error = %q, want it to contain %q", msg, tc.wantIn)
			}
			if tc.wantNotIn != "" && strings.Contains(msg, tc.wantNotIn) {
				t.Errorf(".error = %q, want it NOT to contain %q", msg, tc.wantNotIn)
			}
		})
	}
}

// TestCommitCreate_PersistFailureFailsLoud confirms a SQLite upsert failure is
// fatal to the create (#276): the entity is live on Linear but unconfirmed
// locally, so the tail must return EIO, write a de-dupe .error naming the
// entity, and NOT advertise the create via .last or run the coherence policy.
func TestCommitCreate_PersistFailureFailsLoud(t *testing.T) {
	sink := &fakeCreateSink{}
	persists, extras := 0, 0
	spec := okSpec(&ent{title: "Fix bug"}, &persists, &extras)
	spec.result = func(e *ent) WriteResult { return WriteResult{Identifier: "ENG-5567", Title: e.title} }
	spec.persist = func(context.Context, *ent) error { return errors.New("db down") }

	got, errno := commitCreate(context.Background(), sink, spec)

	if errno != syscall.EIO || got != nil {
		t.Fatalf("got (%v, %v), want (nil, EIO) on unconfirmed reflection", got, errno)
	}
	if sink.setCalls != 1 || sink.setKey != "K" {
		t.Errorf("SetWriteError: calls=%d key=%q, want 1 call on K", sink.setCalls, sink.setKey)
	}
	for _, want := range []string{"SUCCEEDED on Linear", "ENG-5567", "do NOT recreate", "db down"} {
		if !strings.Contains(sink.setMsg, want) {
			t.Errorf(".error = %q, want it to contain %q", sink.setMsg, want)
		}
	}
	// A create the local cache can't serve must not be advertised or cohered —
	// and, crucially, must not be logged to .last as a failure either: it
	// SUCCEEDED on Linear (#276), so a failure entry would misreport it.
	if sink.appends != 0 || sink.failAppends != 0 || sink.clears != 0 || sink.invalidates != 0 || extras != 0 {
		t.Errorf("success/failure tail ran on unconfirmed reflection: appends=%d failAppends=%d clears=%d invalidates=%d extras=%d",
			sink.appends, sink.failAppends, sink.clears, sink.invalidates, extras)
	}
}

// TestCommitCreate_UnknowableEntryName confirms a nil entryName (comments,
// relations: the on-disk name needs a re-list) still refreshes the dir listing.
func TestCommitCreate_UnknowableEntryName(t *testing.T) {
	sink := &fakeCreateSink{}
	persists, extras := 0, 0
	spec := okSpec(&ent{title: "x"}, &persists, &extras)
	spec.entryName = nil
	spec.invalidateExtra = nil

	_, errno := commitCreate(context.Background(), sink, spec)

	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	if sink.invalidates != 1 || sink.invalidateNam != "" {
		t.Errorf("InvalidateCreated: calls=%d name=%q, want (1, \"\")", sink.invalidates, sink.invalidateNam)
	}
}

// TestCommitCreate_BoundsTheMutation confirms the module owns the create
// timeout: the ctx handed to mutate carries a deadline even when the caller's
// context has none (#131 legibility for rate-limited creates).
func TestCommitCreate_BoundsTheMutation(t *testing.T) {
	sink := &fakeCreateSink{}
	persists, extras := 0, 0
	spec := okSpec(&ent{title: "x"}, &persists, &extras)
	sawDeadline := false
	inner := spec.mutate
	spec.mutate = func(ctx context.Context) (*ent, error) {
		_, sawDeadline = ctx.Deadline()
		return inner(ctx)
	}

	if _, errno := commitCreate(context.Background(), sink, spec); errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	if !sawDeadline {
		t.Error("mutate ran without a deadline; commitCreate must bound the create")
	}
}
