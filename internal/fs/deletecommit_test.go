package fs

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// fakeDeleteSink records every interaction the delete tail can have. It
// satisfies deleteSink.
type fakeDeleteSink struct {
	fakeSink // .error interactions (editcommit_test.go)

	invalidateDir uint64
	invalidateNam string
	invalidates   int
}

func (f *fakeDeleteSink) InvalidateDeleted(dirIno uint64, name string) {
	f.invalidateDir, f.invalidateNam = dirIno, name
	f.invalidates++
}

// okDeleteSpec returns a spec whose find/mutate succeed; tests override the
// parts they exercise.
func okDeleteSpec(target *ent, mutations, forgets, extras *int) deleteSpec[ent] {
	return deleteSpec[ent]{
		op:   "delete ent",
		key:  "K",
		find: func(context.Context) (*ent, error) { return target, nil },
		mutate: func(context.Context, *ent) error {
			*mutations++
			return nil
		},
		forget: func(context.Context, *ent) error {
			*forgets++
			return nil
		},
		dir:  42,
		name: "the-entry",
		invalidateExtra: func(*ent) {
			*extras++
		},
	}
}

func TestCommitDelete_Success(t *testing.T) {
	sink := &fakeDeleteSink{}
	mutations, forgets, extras := 0, 0, 0

	errno := commitDelete(context.Background(), sink, okDeleteSpec(&ent{title: "x"}, &mutations, &forgets, &extras))

	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	if mutations != 1 || forgets != 1 || extras != 1 {
		t.Errorf("mutations=%d forgets=%d extras=%d, want 1 each", mutations, forgets, extras)
	}
	if sink.clears != 1 || sink.clearKey != "K" {
		t.Errorf("ClearWriteError: calls=%d key=%q, want 1 call on K", sink.clears, sink.clearKey)
	}
	if sink.setCalls != 0 {
		t.Errorf("SetWriteError calls = %d, want 0", sink.setCalls)
	}
	if sink.invalidates != 1 || sink.invalidateDir != 42 || sink.invalidateNam != "the-entry" {
		t.Errorf("InvalidateDeleted: calls=%d dir=%d name=%q, want (1, 42, the-entry)",
			sink.invalidates, sink.invalidateDir, sink.invalidateNam)
	}
}

func TestCommitDelete_NotFound(t *testing.T) {
	sink := &fakeDeleteSink{}
	mutations, forgets, extras := 0, 0, 0
	spec := okDeleteSpec(nil, &mutations, &forgets, &extras)
	spec.find = func(context.Context) (*ent, error) { return nil, nil }

	errno := commitDelete(context.Background(), sink, spec)

	if errno != syscall.ENOENT {
		t.Fatalf("errno = %v, want ENOENT", errno)
	}
	if sink.setCalls != 1 || !strings.Contains(sink.setMsg, "no such entry") {
		t.Errorf(".error should note the unknown name; calls=%d msg=%q", sink.setCalls, sink.setMsg)
	}
	if mutations != 0 || forgets != 0 || sink.invalidates != 0 || extras != 0 {
		t.Errorf("tail ran on not-found: mutations=%d forgets=%d invalidates=%d extras=%d",
			mutations, forgets, sink.invalidates, extras)
	}
}

func TestCommitDelete_Classification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantErrno syscall.Errno
		wantIn    string
	}{
		{"rate limit is EAGAIN", errors.New("rate limit exceeded"), syscall.EAGAIN, "rate-limited"},
		{"deadline is EAGAIN", context.DeadlineExceeded, syscall.EAGAIN, "retry"},
		{"usage limit is EDQUOT", &api.GraphQLError{Message: "usage limit exceeded"}, syscall.EDQUOT, "plan/usage limit"},
		{"anything else is EIO", errors.New("boom"), syscall.EIO, "boom"},
	}
	for _, tc := range cases {
		t.Run("mutate: "+tc.name, func(t *testing.T) {
			sink := &fakeDeleteSink{}
			mutations, forgets, extras := 0, 0, 0
			spec := okDeleteSpec(&ent{title: "x"}, &mutations, &forgets, &extras)
			spec.mutate = func(context.Context, *ent) error { return tc.err }

			errno := commitDelete(context.Background(), sink, spec)

			if errno != tc.wantErrno {
				t.Fatalf("errno = %v, want %v", errno, tc.wantErrno)
			}
			if sink.setCalls != 1 || !strings.Contains(sink.setMsg, tc.wantIn) {
				t.Errorf(".error = %q (calls=%d), want it to contain %q", sink.setMsg, sink.setCalls, tc.wantIn)
			}
			if sink.clears != 0 || forgets != 0 || sink.invalidates != 0 || extras != 0 {
				t.Errorf("success tail ran on failure: clears=%d forgets=%d invalidates=%d extras=%d",
					sink.clears, forgets, sink.invalidates, extras)
			}
		})
	}

	// find failures classify the same way.
	t.Run("find: backend failure is EIO", func(t *testing.T) {
		sink := &fakeDeleteSink{}
		mutations, forgets, extras := 0, 0, 0
		spec := okDeleteSpec(nil, &mutations, &forgets, &extras)
		spec.find = func(context.Context) (*ent, error) { return nil, errors.New("store down") }

		if errno := commitDelete(context.Background(), sink, spec); errno != syscall.EIO {
			t.Fatalf("errno = %v, want EIO", errno)
		}
		if mutations != 0 {
			t.Error("mutate ran after a find failure")
		}
		if sink.setCalls != 1 || !strings.Contains(sink.setMsg, "store down") {
			t.Errorf(".error = %q, want the find failure cause", sink.setMsg)
		}
	})

	// A find that fails because Linear no longer has the entity is not the
	// already-gone path (that gate covers mutate only), so it does classify:
	// ENOENT rather than the EIO fallthrough (#445).
	t.Run("find: server not-found is ENOENT", func(t *testing.T) {
		sink := &fakeDeleteSink{}
		mutations, forgets, extras := 0, 0, 0
		spec := okDeleteSpec(nil, &mutations, &forgets, &extras)
		spec.find = func(context.Context) (*ent, error) {
			return nil, &api.GraphQLError{Message: "Entity not found: Issue - Could not find referenced Issue."}
		}

		if errno := commitDelete(context.Background(), sink, spec); errno != syscall.ENOENT {
			t.Fatalf("errno = %v, want ENOENT", errno)
		}
		if mutations != 0 || forgets != 0 {
			t.Errorf("tail ran after a find failure: mutations=%d forgets=%d", mutations, forgets)
		}
		if sink.setCalls != 1 || !strings.Contains(sink.setMsg, "no longer exists on Linear") {
			t.Errorf(".error = %q, want the not-found cause", sink.setMsg)
		}
	})
}

// TestCommitDelete_ForgetFailureFailsLoud: a SQLite forget that survives the
// retries is fatal (#278). The delete is on Linear, but the phantom row lingers
// in the listing, so the tail fails loud (EIO + a "re-run rm" .error) and skips
// the coherence policy (invalidating would only repopulate the phantom). The
// forget is retried first (the stress-tested failure was a transient SQLITE_BUSY
// racing the sync worker).
func TestCommitDelete_ForgetFailureFailsLoud(t *testing.T) {
	zeroRetryBackoff(t)
	sink := &fakeDeleteSink{}
	mutations, forgets, extras := 0, 0, 0
	spec := okDeleteSpec(&ent{title: "x"}, &mutations, &forgets, &extras)
	spec.forget = func(context.Context, *ent) error { forgets++; return errors.New("db down") }

	if errno := commitDelete(context.Background(), sink, spec); errno != syscall.EIO {
		t.Fatalf("errno = %v, want EIO (an unforgotten delete leaves a phantom)", errno)
	}
	if forgets != len(sqliteRetryBackoff) {
		t.Errorf("forget attempts = %d, want %d (retried before giving up)", forgets, len(sqliteRetryBackoff))
	}
	// .error cleared (delete succeeded on Linear) then set to the forget failure.
	if sink.setCalls != 1 || sink.setKey != "K" {
		t.Errorf("SetWriteError: calls=%d key=%q, want 1 on K", sink.setCalls, sink.setKey)
	}
	for _, want := range []string{"SUCCEEDED on Linear", "Re-run rm", "db down"} {
		if !strings.Contains(sink.setMsg, want) {
			t.Errorf(".error = %q, want it to contain %q", sink.setMsg, want)
		}
	}
	// The phantom is still present, so the coherence policy must NOT run.
	if sink.invalidates != 0 || extras != 0 {
		t.Errorf("coherence ran on forget failure: invalidates=%d extras=%d, want 0", sink.invalidates, extras)
	}
}

// TestCommitDelete_ForgetRetrySucceeds: a transient forget failure (SQLITE_BUSY)
// recovers on retry — no phantom row, no error surfaced.
func TestCommitDelete_ForgetRetrySucceeds(t *testing.T) {
	zeroRetryBackoff(t)
	sink := &fakeDeleteSink{}
	mutations, forgets, extras := 0, 0, 0
	spec := okDeleteSpec(&ent{title: "x"}, &mutations, &forgets, &extras)
	attempts := 0
	spec.forget = func(context.Context, *ent) error {
		attempts++
		if attempts == 1 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	}

	if errno := commitDelete(context.Background(), sink, spec); errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	if attempts != 2 {
		t.Errorf("forget attempts = %d, want 2 (fail once, succeed on retry)", attempts)
	}
}

// TestCommitDelete_RemoteAlreadyGone: deleting an entity Linear no longer has
// is a success, not EIO — the local row is forgotten and the listing re-cohered.
// This is the self-heal path for a phantom row left by an earlier failed forget.
//
// #445: both spellings of "Entity not found" must be idempotent success here —
// a plain-string envelope (HTTP 400 body) and a structured *api.GraphQLError.
// The delete tail intercepts the condition via remoteAlreadyGone BEFORE
// classifyMutationErr, so the ENOENT arm the classifier gained for
// creates/updates/renames must NOT change this path: not-found is success, not
// ENOENT, on delete. Parameterizing rather than adding a thinner second case
// keeps the whole success tail (forget, clear, invalidate) asserted for both
// spellings, which is what makes this a contract and not just an errno check.
func TestCommitDelete_RemoteAlreadyGone(t *testing.T) {
	notFoundErrs := []struct {
		name string
		err  error
	}{
		{
			name: "plain string envelope",
			err:  errors.New(`API error (status 400): {"errors":[{"message":"Entity not found: Comment - Could not find referenced Comment."}]}`),
		},
		{
			name: "structured GraphQLError",
			err:  &api.GraphQLError{Message: "Entity not found: Comment - Could not find referenced Comment."},
		},
	}
	for _, tc := range notFoundErrs {
		t.Run(tc.name, func(t *testing.T) {
			sink := &fakeDeleteSink{}
			mutations, forgets, extras := 0, 0, 0
			spec := okDeleteSpec(&ent{title: "x"}, &mutations, &forgets, &extras)
			spec.mutate = func(context.Context, *ent) error { return tc.err }

			if errno := commitDelete(context.Background(), sink, spec); errno != 0 {
				t.Fatalf("errno = %v, want 0 (already-gone delete is idempotent success)", errno)
			}
			if forgets != 1 {
				t.Errorf("forgets = %d, want 1 (the phantom row must be forgotten)", forgets)
			}
			if sink.clears != 1 || sink.setCalls != 0 {
				t.Errorf(".error handling: clears=%d sets=%d, want cleared and never set", sink.clears, sink.setCalls)
			}
			if sink.invalidates != 1 {
				t.Errorf("InvalidateDeleted calls = %d, want 1", sink.invalidates)
			}
		})
	}
}

// TestCommitDelete_EchoedNotFoundIsNotAlreadyGone: the worst case the
// already-gone gate can get wrong. Linear echoes user-supplied entity names back
// into its rejections, so a workspace that owns a label named "Entity not found"
// draws validation messages that END with the phrase ("Cannot assign label:
// Entity not found"). That is a fixable input rejection, not proof the entity is
// gone — and remoteAlreadyGone reading it as gone would report a clean rm and
// FORGET the local row for an entity Linear still has, deleting it from the
// listing until the next full sync. So it must classify instead: the delete
// fails, the row survives, and the reason lands in .error.
func TestCommitDelete_EchoedNotFoundIsNotAlreadyGone(t *testing.T) {
	sink := &fakeDeleteSink{}
	mutations, forgets, extras := 0, 0, 0
	spec := okDeleteSpec(&ent{title: "x"}, &mutations, &forgets, &extras)
	spec.mutate = func(context.Context, *ent) error {
		return &api.GraphQLError{Message: "Cannot assign label: Entity not found", UserError: true}
	}

	errno := commitDelete(context.Background(), sink, spec)

	if errno != syscall.EINVAL {
		t.Fatalf("errno = %v, want EINVAL (an echoed name is bad input, not an already-gone entity)", errno)
	}
	if forgets != 0 {
		t.Errorf("forgets = %d, want 0 (the row is for an entity Linear still has)", forgets)
	}
	if sink.clears != 0 || sink.invalidates != 0 || extras != 0 {
		t.Errorf("success tail ran on failure: clears=%d invalidates=%d extras=%d",
			sink.clears, sink.invalidates, extras)
	}
	if sink.setCalls != 1 || !strings.Contains(sink.setMsg, "Cannot assign label") {
		t.Errorf(".error = %q (calls=%d), want the server's rejection", sink.setMsg, sink.setCalls)
	}
	if strings.Contains(sink.setMsg, "no longer exists on Linear") {
		t.Errorf(".error = %q, want no gone verdict", sink.setMsg)
	}
}
