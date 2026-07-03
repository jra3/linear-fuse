package fs

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
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
}

// TestCommitDelete_ForgetFailureNonFatal: a SQLite delete failure must not fail
// a delete Linear already accepted — and the coherence policy still runs. The
// forget is retried before giving up (the stress-tested failure was a
// transient SQLITE_BUSY racing the sync worker).
func TestCommitDelete_ForgetFailureNonFatal(t *testing.T) {
	sink := &fakeDeleteSink{}
	mutations, forgets, extras := 0, 0, 0
	spec := okDeleteSpec(&ent{title: "x"}, &mutations, &forgets, &extras)
	spec.forget = func(context.Context, *ent) error { forgets++; return errors.New("db down") }

	if errno := commitDelete(context.Background(), sink, spec); errno != 0 {
		t.Fatalf("errno = %v, want 0 (forget failure must be non-fatal)", errno)
	}
	if forgets != 3 {
		t.Errorf("forget attempts = %d, want 3 (retried before giving up)", forgets)
	}
	if sink.clears != 1 || sink.invalidates != 1 || extras != 1 {
		t.Errorf("tail after forget failure: clears=%d invalidates=%d extras=%d, want 1 each",
			sink.clears, sink.invalidates, extras)
	}
}

// TestCommitDelete_ForgetRetrySucceeds: a transient forget failure (SQLITE_BUSY)
// recovers on retry — no phantom row, no error surfaced.
func TestCommitDelete_ForgetRetrySucceeds(t *testing.T) {
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
func TestCommitDelete_RemoteAlreadyGone(t *testing.T) {
	sink := &fakeDeleteSink{}
	mutations, forgets, extras := 0, 0, 0
	spec := okDeleteSpec(&ent{title: "x"}, &mutations, &forgets, &extras)
	spec.mutate = func(context.Context, *ent) error {
		return errors.New(`API error (status 400): {"errors":[{"message":"Entity not found: Comment - Could not find referenced Comment."}]}`)
	}

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
}
