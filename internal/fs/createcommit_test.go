package fs

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
)

// fakeCreateSink records every interaction the create tail can have so a test
// can assert what it did without a LinearFS. It satisfies createSink.
type fakeCreateSink struct {
	fakeSink // .error interactions (editcommit_test.go)

	appendKey     string
	appendResult  WriteResult
	appends       int
	invalidateDir uint64
	invalidateNam string
	invalidates   int
}

func (f *fakeCreateSink) AppendWriteSuccess(key string, r WriteResult) {
	f.appendKey, f.appendResult = key, r
	f.appends++
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
				(tc.wantErrno == syscall.EAGAIN || tc.wantErrno == syscall.EIO) {
				t.Errorf(".error = %q, want the op name in API-failure messages", sink.setMsg)
			}
			// The failure path must not run any of the success tail.
			if sink.clears != 0 || sink.appends != 0 || persists != 0 || sink.invalidates != 0 || extras != 0 {
				t.Errorf("success tail ran on failure: clears=%d appends=%d persists=%d invalidates=%d extras=%d",
					sink.clears, sink.appends, persists, sink.invalidates, extras)
			}
		})
	}
}

// TestCommitCreate_PersistFailureNonFatal confirms a SQLite upsert failure does
// not fail a create Linear already accepted — and the coherence policy still runs.
func TestCommitCreate_PersistFailureNonFatal(t *testing.T) {
	sink := &fakeCreateSink{}
	persists, extras := 0, 0
	spec := okSpec(&ent{title: "x"}, &persists, &extras)
	spec.persist = func(context.Context, *ent) error { return errors.New("db down") }

	got, errno := commitCreate(context.Background(), sink, spec)

	if errno != 0 || got == nil {
		t.Fatalf("got (%v, %v), want success despite persist failure", got, errno)
	}
	if sink.appends != 1 || sink.invalidates != 1 || extras != 1 {
		t.Errorf("tail after persist failure: appends=%d invalidates=%d extras=%d, want 1 each",
			sink.appends, sink.invalidates, extras)
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
