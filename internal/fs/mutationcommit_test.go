package fs

import (
	"context"
	"errors"
	"syscall"
	"testing"
)

// commitMutation reuses the fakeSink defined in editcommit_test.go (same package).

func TestCommitMutation_SuccessPersistsThenInvalidates(t *testing.T) {
	sink := &fakeSink{}
	persisted, invalidated := 0, 0

	errno := commitMutation(context.Background(), sink, mutationSpec{
		errKey:     "K",
		op:         "create comment",
		persist:    func(context.Context) error { persisted++; return nil },
		invalidate: func() { invalidated++ },
	}, nil)

	if errno != 0 {
		t.Errorf("errno = %v, want 0", errno)
	}
	if sink.clears != 1 || sink.setCalls != 0 {
		t.Errorf("want clear once and no set; got clears=%d sets=%d", sink.clears, sink.setCalls)
	}
	if persisted != 1 {
		t.Errorf("persist calls = %d, want 1", persisted)
	}
	if invalidated != 1 {
		t.Errorf("invalidate calls = %d, want 1", invalidated)
	}
}

func TestCommitMutation_FailureDefaultsToEIO(t *testing.T) {
	sink := &fakeSink{}
	persisted, invalidated := 0, 0

	errno := commitMutation(context.Background(), sink, mutationSpec{
		errKey:     "K",
		op:         "create comment",
		persist:    func(context.Context) error { persisted++; return nil },
		invalidate: func() { invalidated++ },
	}, errors.New("backend exploded"))

	if errno != syscall.EIO {
		t.Errorf("errno = %v, want EIO", errno)
	}
	if sink.setCalls != 1 || sink.clears != 0 {
		t.Errorf("want set once and no clear; got sets=%d clears=%d", sink.setCalls, sink.clears)
	}
	if sink.setKey != "K" {
		t.Errorf("SetWriteError key = %q, want K", sink.setKey)
	}
	// Message independently spells out the contract format, not recomputed from code.
	want := "Operation: create comment\nError: backend exploded"
	if sink.setMsg != want {
		t.Errorf("SetWriteError msg = %q, want %q", sink.setMsg, want)
	}
	if persisted != 0 || invalidated != 0 {
		t.Errorf("failure must skip persist/invalidate; got persist=%d invalidate=%d", persisted, invalidated)
	}
}

func TestCommitMutation_OnErrorOverridesErrnoAndMessage(t *testing.T) {
	sink := &fakeSink{}

	errno := commitMutation(context.Background(), sink, mutationSpec{
		errKey: "K",
		op:     "create issue", // ignored because onError is set
		onError: func(error) (string, syscall.Errno) {
			return "rate-limited, wait and retry", syscall.EAGAIN
		},
	}, errors.New("rate limit exceeded"))

	if errno != syscall.EAGAIN {
		t.Errorf("errno = %v, want EAGAIN", errno)
	}
	if sink.setMsg != "rate-limited, wait and retry" {
		t.Errorf("SetWriteError msg = %q, want the onError message", sink.setMsg)
	}
}

// A SQLite persist failure must not fail a mutation the API already accepted, and
// must not skip invalidation — the API is the source of truth; sync reconciles the
// cache. (For a delete this means the row can briefly reappear until sync; accepted.)
func TestCommitMutation_PersistFailureNonFatal(t *testing.T) {
	sink := &fakeSink{}
	invalidated := 0

	errno := commitMutation(context.Background(), sink, mutationSpec{
		errKey:     "K",
		op:         "delete label",
		persist:    func(context.Context) error { return errors.New("db down") },
		invalidate: func() { invalidated++ },
	}, nil)

	if errno != 0 {
		t.Errorf("errno = %v, want 0 (persist failure must be non-fatal)", errno)
	}
	if sink.clears != 1 {
		t.Errorf("ClearWriteError calls = %d, want 1", sink.clears)
	}
	if invalidated != 1 {
		t.Errorf("invalidate must still run after a non-fatal persist failure; got %d", invalidated)
	}
}

func TestCommitMutation_NilPersistSkipped(t *testing.T) {
	sink := &fakeSink{}
	invalidated := 0

	errno := commitMutation(context.Background(), sink, mutationSpec{
		errKey:     "K",
		op:         "create comment",
		persist:    nil,
		invalidate: func() { invalidated++ },
	}, nil)

	if errno != 0 {
		t.Errorf("errno = %v, want 0", errno)
	}
	if sink.clears != 1 || invalidated != 1 {
		t.Errorf("want clear and invalidate to run with nil persist; got clears=%d invalidate=%d", sink.clears, invalidated)
	}
}

// The success tail must run clear → persist → invalidate in that order, so the
// kernel's refreshed readdir (invalidate) hits already-updated SQLite (persist),
// and a stale .error is gone before either.
func TestCommitMutation_OrderClearThenPersistThenInvalidate(t *testing.T) {
	sink := &fakeSink{}
	var seq []string

	commitMutation(context.Background(), sink, mutationSpec{
		errKey: "K",
		op:     "create comment",
		persist: func(context.Context) error {
			if sink.clears != 1 {
				t.Errorf("persist ran before ClearWriteError (clears=%d)", sink.clears)
			}
			seq = append(seq, "persist")
			return nil
		},
		invalidate: func() { seq = append(seq, "invalidate") },
	}, nil)

	if len(seq) != 2 || seq[0] != "persist" || seq[1] != "invalidate" {
		t.Errorf("sequence = %v, want [persist invalidate]", seq)
	}
}
