package fs

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
)

// fakeSink records the .error interactions so a test can assert what the tail did
// without a LinearFS. It satisfies errorSink.
type fakeSink struct {
	setKey   string
	setMsg   string
	setCalls int
	clearKey string
	clears   int
}

func (f *fakeSink) SetWriteError(key, message string) {
	f.setKey, f.setMsg = key, message
	f.setCalls++
}
func (f *fakeSink) ClearWriteError(key string) {
	f.clearKey = key
	f.clears++
}

// ent is a stand-in entity type; the tail is generic, so any T works.
type ent struct{ title string }

func TestCommitWriteBack(t *testing.T) {
	fresh := &ent{title: "persisted"}

	cases := []struct {
		name string
		spec writeBackSpec[ent]
		// expectations
		wantErrno    syscall.Errno
		wantFreshNil bool
		wantSets     int
		wantClears   int
		wantPersist  int
	}{
		{
			name: "faithful write clears error and returns success",
			spec: writeBackSpec[ent]{
				errKey:  "K",
				fetch:   func(context.Context) (*ent, error) { return fresh, nil },
				compare: func(*ent) []writeBackResult { return nil },
			},
			wantErrno:  0,
			wantClears: 1,
		},
		{
			name: "fatal divergence sets error and returns EIO",
			spec: writeBackSpec[ent]{
				errKey:  "K",
				fetch:   func(context.Context) (*ent, error) { return fresh, nil },
				compare: func(*ent) []writeBackResult { return []writeBackResult{{message: "reverted", fatal: true}} },
			},
			wantErrno: syscall.EIO,
			wantSets:  1,
		},
		{
			name: "benign reformat notes error but returns success",
			spec: writeBackSpec[ent]{
				errKey:  "K",
				fetch:   func(context.Context) (*ent, error) { return fresh, nil },
				compare: func(*ent) []writeBackResult { return []writeBackResult{{message: "reformatted", fatal: false}} },
			},
			wantErrno: 0,
			wantSets:  1,
		},
		{
			name: "fetch failure clears error, returns nil fresh, never compares",
			spec: writeBackSpec[ent]{
				errKey: "K",
				fetch:  func(context.Context) (*ent, error) { return nil, errors.New("network") },
				compare: func(*ent) []writeBackResult {
					t.Error("compare must not run when fetch fails")
					return nil
				},
			},
			wantErrno:    0,
			wantFreshNil: true,
			wantClears:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &fakeSink{}
			persist := 0
			tc.spec.persist = func(context.Context, *ent) error { persist++; return nil }

			got, errno := commitWriteBack(context.Background(), sink, tc.spec)

			if errno != tc.wantErrno {
				t.Errorf("errno = %v, want %v", errno, tc.wantErrno)
			}
			if (got == nil) != tc.wantFreshNil {
				t.Errorf("fresh nil = %v, want %v", got == nil, tc.wantFreshNil)
			}
			if sink.setCalls != tc.wantSets {
				t.Errorf("SetWriteError calls = %d, want %d", sink.setCalls, tc.wantSets)
			}
			if sink.clears != tc.wantClears {
				t.Errorf("ClearWriteError calls = %d, want %d", sink.clears, tc.wantClears)
			}
		})
	}
}

// TestCommitWriteBack_NilPersist confirms an absent persist closure (used by
// milestone, whose repo path already upserted) is simply skipped.
func TestCommitWriteBack_NilPersist(t *testing.T) {
	sink := &fakeSink{}
	fresh := &ent{title: "x"}
	got, errno := commitWriteBack(context.Background(), sink, writeBackSpec[ent]{
		errKey:  "K",
		fetch:   func(context.Context) (*ent, error) { return fresh, nil },
		persist: nil,
		compare: func(*ent) []writeBackResult { return nil },
	})
	if errno != 0 || got != fresh {
		t.Fatalf("got (%v, %v), want (%v, 0)", got, errno, fresh)
	}
	if sink.clears != 1 {
		t.Errorf("ClearWriteError calls = %d, want 1", sink.clears)
	}
}

// TestCommitWriteBack_PersistFailureFailsLoud confirms a SQLite upsert failure
// that survives the retries is fatal (#278): the write is on Linear but its
// reflection is unconfirmed (a wedge), so the tail must return EIO with a
// "re-saving is safe" .error — while still returning `fresh` so the caller can
// adopt correct data into the live fd.
func TestCommitWriteBack_PersistFailureFailsLoud(t *testing.T) {
	zeroRetryBackoff(t)
	sink := &fakeSink{}
	fresh := &ent{title: "x"}
	persists := 0
	got, errno := commitWriteBack(context.Background(), sink, writeBackSpec[ent]{
		errKey:  "K",
		fetch:   func(context.Context) (*ent, error) { return fresh, nil },
		persist: func(context.Context, *ent) error { persists++; return errors.New("db down") },
		compare: func(*ent) []writeBackResult { return nil },
	})
	if errno != syscall.EIO {
		t.Errorf("errno = %v, want EIO on unconfirmed reflection", errno)
	}
	if got != fresh {
		t.Errorf("fresh = %v, want it returned for adopt even on EIO", got)
	}
	if persists != len(sqliteRetryBackoff) {
		t.Errorf("persist attempts = %d, want %d (retried before giving up)", persists, len(sqliteRetryBackoff))
	}
	if sink.setCalls != 1 || sink.setKey != "K" {
		t.Errorf("SetWriteError: calls=%d key=%q, want 1 on K", sink.setCalls, sink.setKey)
	}
	for _, want := range []string{"SUCCEEDED on Linear", "Re-saving is safe", "db down"} {
		if !strings.Contains(sink.setMsg, want) {
			t.Errorf(".error = %q, want it to contain %q", sink.setMsg, want)
		}
	}
	if sink.clears != 0 {
		t.Errorf("ClearWriteError calls = %d, want 0 on a loud failure", sink.clears)
	}
}

// TestCommitWriteBack_RealDivergence wires the real writeBackDivergence helper
// through the tail to confirm a silent revert is classified fatal end-to-end.
func TestCommitWriteBack_RealDivergence(t *testing.T) {
	sink := &fakeSink{}
	// want "new body", but the fresh fetch returned the pre-write value -> revert.
	fresh := &ent{title: "old body"}
	_, errno := commitWriteBack(context.Background(), sink, writeBackSpec[ent]{
		errKey: "ENG-1",
		fetch:  func(context.Context) (*ent, error) { return fresh, nil },
		compare: func(f *ent) []writeBackResult {
			return []writeBackResult{writeBackDivergence("body", "new body", f.title, "old body")}
		},
	})
	if errno != syscall.EIO {
		t.Errorf("errno = %v, want EIO for a silent revert", errno)
	}
	if sink.setCalls != 1 || sink.setKey != "ENG-1" {
		t.Errorf("expected SetWriteError(ENG-1, …); got calls=%d key=%q", sink.setCalls, sink.setKey)
	}
}
