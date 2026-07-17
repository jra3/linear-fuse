package fs

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeRenameEntity is a synthetic entity so the commitRename tail can be
// exercised generically without a real api.Label/api.Document.
type fakeRenameEntity struct {
	ID   string
	Name string
}

// recordingCommitRenameSpec builds a renameSpec[fakeRenameEntity] whose
// find/mutate/persist closures record their calls, so a test can assert which
// stages ran and what arguments they saw.
type recordingCommitRenameSpec struct {
	spec renameSpec[fakeRenameEntity]

	findCalls int
	findRet   *fakeRenameEntity
	findErr   error

	mutateCalls   int
	mutateNewName string // the newName that reached mutate (asserts the parse)
	mutateRet     *fakeRenameEntity
	mutateErr     error

	persistCalls int
	persistGot   *fakeRenameEntity // the entity persist was handed (the normalization contract)
	persistErr   error
}

func newRecordingCommitRenameSpec() *recordingCommitRenameSpec {
	r := &recordingCommitRenameSpec{
		findRet:   &fakeRenameEntity{ID: "ent-1", Name: "foo"},
		mutateRet: &fakeRenameEntity{ID: "ent-1", Name: "new name"},
	}
	r.spec = renameSpec[fakeRenameEntity]{
		kind:   "label",
		errKey: "labels:TEAM",
		dirIno: 0, // matches the zero-value renameParent inode (same directory)
		find: func(ctx context.Context) (*fakeRenameEntity, error) {
			r.findCalls++
			return r.findRet, r.findErr
		},
		mutate: func(ctx context.Context, target *fakeRenameEntity, newName string) (*fakeRenameEntity, error) {
			r.mutateCalls++
			r.mutateNewName = newName
			return r.mutateRet, r.mutateErr
		},
		persist: func(ctx context.Context, fresh *fakeRenameEntity) error {
			r.persistCalls++
			r.persistGot = fresh
			return r.persistErr
		},
	}
	return r
}

func TestCommitRename_Contract(t *testing.T) {
	// The persist-wedge case retries through sqliteRetryBackoff; zero the sleeps
	// so the whole table stays fast.
	orig := sqliteRetryBackoff
	sqliteRetryBackoff = []time.Duration{0} // one immediate attempt, no sleeps
	t.Cleanup(func() { sqliteRetryBackoff = orig })

	cases := []struct {
		name string
		// input
		oldName string
		newName string
		dirIno  uint64 // set nonzero to force a cross-directory rename
		findErr error
		findNil bool
		mutErr  error
		perErr  error
		// expectations
		wantErrno       syscall.Errno
		wantFind        bool
		wantMutate      bool
		wantPersist     bool
		wantSets        int
		wantClears      int
		wantInvalidates int
		wantErrSubstr   string // substring the set .error must contain (if wantSets>0)
	}{
		{
			name:            "1 _create is not renamable",
			oldName:         "_create",
			newName:         "whatever.md",
			wantErrno:       syscall.EPERM,
			wantInvalidates: 0,
		},
		{
			name:            "2 meta sidecar is not renamable",
			oldName:         "foo.meta",
			newName:         "bar.md",
			wantErrno:       syscall.EPERM,
			wantInvalidates: 0,
		},
		{
			name:            "3 cross-directory rejected",
			oldName:         "foo.md",
			newName:         "bar.md",
			dirIno:          7,
			wantErrno:       syscall.EXDEV,
			wantInvalidates: 0,
		},
		{
			name:            "4 target lacks .md suffix sets a helpful error",
			oldName:         "foo.md",
			newName:         "bar",
			wantErrno:       syscall.EINVAL,
			wantSets:        1,
			wantErrSubstr:   ".md",
			wantInvalidates: 0,
		},
		{
			name:            "5 find error is classified",
			oldName:         "foo.md",
			newName:         "bar.md",
			findErr:         errors.New("boom"),
			wantErrno:       syscall.EIO,
			wantFind:        true,
			wantSets:        1,
			wantInvalidates: 0,
		},
		{
			name:            "6 find nil is ENOENT",
			oldName:         "foo.md",
			newName:         "bar.md",
			findNil:         true,
			wantErrno:       syscall.ENOENT,
			wantFind:        true,
			wantSets:        1,
			wantErrSubstr:   "no such entry",
			wantInvalidates: 0,
		},
		{
			name:            "7 mutate error is classified, no invalidation",
			oldName:         "foo.md",
			newName:         "bar.md",
			mutErr:          errors.New("api down"),
			wantErrno:       syscall.EIO,
			wantFind:        true,
			wantMutate:      true,
			wantSets:        1,
			wantInvalidates: 0,
		},
		{
			// find/mutate errors flow through classifyMutationErr; rows 5/7 use a
			// plain error (default EIO). These pin the classified branches so a
			// hardcoded `return msg, EIO` would fail (#291).
			name:            "5a find rate-limit is EAGAIN",
			oldName:         "foo.md",
			newName:         "bar.md",
			findErr:         errors.New("rate limit exceeded"),
			wantErrno:       syscall.EAGAIN,
			wantFind:        true,
			wantSets:        1,
			wantErrSubstr:   "rate-limited",
			wantInvalidates: 0,
		},
		{
			name:            "5b find FieldError is EINVAL",
			oldName:         "foo.md",
			newName:         "bar.md",
			findErr:         &FieldError{Field: "name", Value: "bar", Message: "bad name"},
			wantErrno:       syscall.EINVAL,
			wantFind:        true,
			wantSets:        1,
			wantErrSubstr:   "bad name",
			wantInvalidates: 0,
		},
		{
			name:            "7a mutate rate-limit is EAGAIN",
			oldName:         "foo.md",
			newName:         "bar.md",
			mutErr:          errors.New("rate limit exceeded"),
			wantErrno:       syscall.EAGAIN,
			wantFind:        true,
			wantMutate:      true,
			wantSets:        1,
			wantErrSubstr:   "rate-limited",
			wantInvalidates: 0,
		},
		{
			name:            "7b mutate FieldError is EINVAL",
			oldName:         "foo.md",
			newName:         "bar.md",
			mutErr:          &FieldError{Field: "name", Value: "bar", Message: "name taken"},
			wantErrno:       syscall.EINVAL,
			wantFind:        true,
			wantMutate:      true,
			wantSets:        1,
			wantErrSubstr:   "name taken",
			wantInvalidates: 0,
		},
		{
			name:            "7c mutate not-found is ENOENT",
			oldName:         "foo.md",
			newName:         "bar.md",
			mutErr:          &notFoundError{FieldError{Field: "id", Value: "ent-1", Message: "gone"}},
			wantErrno:       syscall.ENOENT,
			wantFind:        true,
			wantMutate:      true,
			wantSets:        1,
			wantErrSubstr:   "gone",
			wantInvalidates: 0,
		},
		{
			name:            "8 persist wedge fails loud, no invalidation",
			oldName:         "foo.md",
			newName:         "bar.md",
			perErr:          errors.New("sqlite busy"),
			wantErrno:       syscall.EIO,
			wantFind:        true,
			wantMutate:      true,
			wantPersist:     true,
			wantSets:        1,
			wantErrSubstr:   "SUCCEEDED on Linear",
			wantInvalidates: 0,
		},
		{
			name:            "9 success clears error and fires both pairs",
			oldName:         "foo.md",
			newName:         "new-name.md",
			wantErrno:       0,
			wantFind:        true,
			wantMutate:      true,
			wantPersist:     true,
			wantClears:      1,
			wantInvalidates: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &renameRecorder{}
			rec := newRecordingCommitRenameSpec()
			rec.spec.dirIno = tc.dirIno
			rec.findErr = tc.findErr
			if tc.findNil {
				rec.findRet = nil
			}
			rec.mutateErr = tc.mutErr
			rec.persistErr = tc.perErr

			errno := commitRename(context.Background(), sink, tc.oldName,
				&renameParent{}, tc.newName, rec.spec)

			if errno != tc.wantErrno {
				t.Errorf("errno = %v, want %v", errno, tc.wantErrno)
			}
			if (rec.findCalls > 0) != tc.wantFind {
				t.Errorf("find called = %v (%d), want %v", rec.findCalls > 0, rec.findCalls, tc.wantFind)
			}
			if (rec.mutateCalls > 0) != tc.wantMutate {
				t.Errorf("mutate called = %v (%d), want %v", rec.mutateCalls > 0, rec.mutateCalls, tc.wantMutate)
			}
			if (rec.persistCalls > 0) != tc.wantPersist {
				t.Errorf("persist called = %v (%d), want %v", rec.persistCalls > 0, rec.persistCalls, tc.wantPersist)
			}
			if sink.sets != tc.wantSets {
				t.Errorf("SetWriteError calls = %d, want %d (msg=%q)", sink.sets, tc.wantSets, sink.setMsg)
			}
			if sink.clears != tc.wantClears {
				t.Errorf("ClearWriteError calls = %d, want %d", sink.clears, tc.wantClears)
			}
			if len(sink.invalidates) != tc.wantInvalidates {
				t.Fatalf("invalidates = %v, want %d", sink.invalidates, tc.wantInvalidates)
			}
			if tc.wantSets > 0 && tc.wantErrSubstr != "" && !strings.Contains(sink.setMsg, tc.wantErrSubstr) {
				t.Errorf(".error %q does not contain %q", sink.setMsg, tc.wantErrSubstr)
			}
			// Any .error the tail sets must be keyed to the spec's errKey.
			if tc.wantSets > 0 && sink.setKey != rec.spec.errKey {
				t.Errorf("SetWriteError key = %q, want %q", sink.setKey, rec.spec.errKey)
			}
		})
	}
}

// TestCommitRename_ParseReachesMutate proves the "foo-bar.md" -> "foo bar"
// dash-to-space parse runs before mutate, so the API sees the human name.
func TestCommitRename_ParseReachesMutate(t *testing.T) {
	orig := sqliteRetryBackoff
	sqliteRetryBackoff = []time.Duration{0}
	t.Cleanup(func() { sqliteRetryBackoff = orig })

	sink := &renameRecorder{}
	rec := newRecordingCommitRenameSpec()

	errno := commitRename(context.Background(), sink, "foo.md",
		&renameParent{}, "new-name.md", rec.spec)

	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	if rec.mutateNewName != "new name" {
		t.Errorf("mutate saw newName %q, want %q (dashes should become spaces, .md stripped)", rec.mutateNewName, "new name")
	}
}

// TestCommitRename_PersistsMutateReturn proves the normalization contract: the
// tail persists the entity mutate RETURNED (the server-normalized value), not
// the pre-rename target it found nor the parsed filename. If a refactor
// persisted `target`, the local cache would silently diverge from Linear and
// every other case would still pass (#291). The mutate return carries a name
// distinct from both the found "foo" and the parsed "bar" to make it sharp.
func TestCommitRename_PersistsMutateReturn(t *testing.T) {
	orig := sqliteRetryBackoff
	sqliteRetryBackoff = []time.Duration{0}
	t.Cleanup(func() { sqliteRetryBackoff = orig })

	sink := &renameRecorder{}
	rec := newRecordingCommitRenameSpec()
	rec.findRet = &fakeRenameEntity{ID: "ent-1", Name: "foo"}
	rec.mutateRet = &fakeRenameEntity{ID: "ent-1", Name: "Server Normalized"}

	errno := commitRename(context.Background(), sink, "foo.md",
		&renameParent{}, "bar.md", rec.spec)

	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	if rec.persistGot == nil {
		t.Fatal("persist was never handed an entity")
	}
	if rec.persistGot.Name != "Server Normalized" {
		t.Errorf("persist got Name %q, want %q (the mutate return, not the found target %q or parsed %q)",
			rec.persistGot.Name, "Server Normalized", "foo", "bar")
	}
}

// TestCommitRename_SuccessInvalidatesBothPairs pins the exact old->new names for
// the .md pair and its .meta twin on the success path.
func TestCommitRename_SuccessInvalidatesBothPairs(t *testing.T) {
	orig := sqliteRetryBackoff
	sqliteRetryBackoff = []time.Duration{0}
	t.Cleanup(func() { sqliteRetryBackoff = orig })

	sink := &renameRecorder{}
	rec := newRecordingCommitRenameSpec()
	// dirIno stays 0 to match the zero-value renameParent inode (same
	// directory), exactly as renamesave_test.go pins its invalidation strings.

	errno := commitRename(context.Background(), sink, "foo.md",
		&renameParent{}, "bar.md", rec.spec)

	if errno != 0 {
		t.Fatalf("errno = %v, want 0", errno)
	}
	wantMD := `renamed(0,"foo.md","bar.md",0)`
	wantMeta := `renamed(0,"foo.meta","bar.meta",0)`
	if len(sink.invalidates) != 2 {
		t.Fatalf("invalidates = %v, want 2", sink.invalidates)
	}
	if sink.invalidates[0] != wantMD {
		t.Errorf("md invalidate = %q, want %q", sink.invalidates[0], wantMD)
	}
	if sink.invalidates[1] != wantMeta {
		t.Errorf("meta invalidate = %q, want %q", sink.invalidates[1], wantMeta)
	}
}
