package fs

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// The recheck hint (#477): a mutation tail that Linear answers "entity not
// found" hands the repo layer the one piece of evidence it has — this local row
// is an orphan — instead of leaving the mount to keep listing it, keep
// accepting writes into it, and keep failing identically until an unrelated
// read or a sync cycle rediscovers the truth.
//
// Two things are worth pinning and they are different: WHEN the hint fires
// (serverSaysGone, which must not drift from the classifier that decides the
// errno), and THAT the tails fire it on exactly that verdict.

// serverGoneCases are the rejections Linear itself answers with "entity not
// found", in both shapes the classifier documents: the typed GraphQL error and
// the plain HTTP-400 envelope.
var serverGoneCases = []struct {
	name string
	err  error
}{
	{"typed GraphQL not-found", &api.GraphQLError{Message: "Entity not found: Issue - Could not find referenced Issue."}},
	{"plain envelope not-found", errors.New(`API error (status 400): {"errors":[{"message":"Entity not found: Comment - Could not find referenced Comment."}]}`)},
}

// notGoneCases are rejections that must NOT trigger a recheck, each for its own
// reason — two of them wearing the same ENOENT the real thing does.
var notGoneCases = []struct {
	name string
	err  error
	why  string
}{
	{
		"local notFoundError",
		&notFoundError{FieldError{Field: "identifier", Value: "ENG-999", Message: "unknown issue"}},
		"ENOENT, but nothing upstream was asked: the caller named something the local catalog does not have, so there is no stale row to prune",
	},
	{
		"throttled envelope that also names a missing entity",
		fmt.Errorf("rate limit exceeded: %w", errors.New("Entity not found: Issue")),
		"EAGAIN — waiting is what fixes it, and a recheck here adds a fetch during the window Linear is asking us to back off",
	},
	{
		"field error",
		&FieldError{Field: "name", Value: "x", Message: "bad"},
		"EINVAL: the caller's input, not a statement about the entity",
	},
	{
		"usage limit",
		&api.GraphQLError{Message: "usage limit exceeded"},
		"EDQUOT: a capacity wall, and the entity is fine",
	},
	{
		"backend fault",
		errors.New("boom"),
		"EIO: Linear said nothing about whether the entity exists",
	},
}

// TestServerSaysGoneAgreesWithTheClassifier is the drift guard promised in
// serverSaysGone's doc comment. The predicate and classifyMutationErr answer
// two different questions off the same error, and they can only stay consistent
// if the agreement is asserted: a hint that fires where the classifier does not
// say ENOENT would be rechecking on a verdict that says nothing about the
// entity, and the exclusions below are the two ways ENOENT arrives without
// meaning "the cached row is stale".
func TestServerSaysGoneAgreesWithTheClassifier(t *testing.T) {
	t.Parallel()

	for _, tc := range serverGoneCases {
		t.Run("gone/"+tc.name, func(t *testing.T) {
			if !serverSaysGone(tc.err) {
				t.Fatalf("serverSaysGone = false, want true for %v", tc.err)
			}
			if _, errno := classifyMutationErr("op", tc.err); errno != syscall.ENOENT {
				t.Errorf("classifier errno = %v, want ENOENT — the hint fires on a verdict the classifier no longer calls not-found", errno)
			}
		})
	}

	for _, tc := range notGoneCases {
		t.Run("not-gone/"+tc.name, func(t *testing.T) {
			if serverSaysGone(tc.err) {
				t.Errorf("serverSaysGone = true, want false: %s", tc.why)
			}
		})
	}
}

// TestCommitCreate_RechecksOnlyWhenLinearSaysGone: the create tail supplies the
// hint on the server's not-found and on nothing else — including the local
// notFoundError, which carries the same ENOENT the caller sees.
func TestCommitCreate_RechecksOnlyWhenLinearSaysGone(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, mutateErr error) (rechecks int, sink *fakeCreateSink) {
		t.Helper()
		sink = &fakeCreateSink{}
		persists, extras := 0, 0
		spec := okSpec(&ent{title: "made"}, &persists, &extras)
		spec.mutate = func(context.Context) (*ent, error) { return nil, mutateErr }
		spec.recheck = func() { rechecks++ }
		commitCreate(context.Background(), sink, spec)
		return rechecks, sink
	}

	for _, tc := range serverGoneCases {
		t.Run("gone/"+tc.name, func(t *testing.T) {
			rechecks, sink := run(t, tc.err)
			if rechecks != 1 {
				t.Errorf("recheck calls = %d, want 1", rechecks)
			}
			// The hint is a background trigger; the caller's feedback must not
			// depend on it, so both sidecars are still written.
			if sink.setCalls != 1 || sink.failAppends != 1 {
				t.Errorf("SetWriteError=%d AppendWriteFailure=%d, want 1 and 1 — the hint must not displace the caller's feedback",
					sink.setCalls, sink.failAppends)
			}
		})
	}

	for _, tc := range notGoneCases {
		t.Run("not-gone/"+tc.name, func(t *testing.T) {
			if rechecks, _ := run(t, tc.err); rechecks != 0 {
				t.Errorf("recheck calls = %d, want 0: %s", rechecks, tc.why)
			}
		})
	}

	t.Run("success does not recheck", func(t *testing.T) {
		rechecks := 0
		sink := &fakeCreateSink{}
		persists, extras := 0, 0
		spec := okSpec(&ent{title: "made"}, &persists, &extras)
		spec.recheck = func() { rechecks++ }
		if _, errno := commitCreate(context.Background(), sink, spec); errno != 0 {
			t.Fatalf("errno = %v, want 0", errno)
		}
		if rechecks != 0 {
			t.Errorf("recheck calls = %d on a successful create, want 0", rechecks)
		}
	})

	t.Run("a nil recheck is not a panic", func(t *testing.T) {
		sink := &fakeCreateSink{}
		persists, extras := 0, 0
		spec := okSpec(&ent{title: "made"}, &persists, &extras)
		spec.mutate = func(context.Context) (*ent, error) { return nil, serverGoneCases[0].err }
		// recheck deliberately unset: the collections owned by a TEAM leave it
		// nil, and the tail must treat that as "nothing to hint".
		if _, errno := commitCreate(context.Background(), sink, spec); errno != syscall.ENOENT {
			t.Fatalf("errno = %v, want ENOENT", errno)
		}
	})
}

// TestCommitRename_RechecksOnBothLinearArms: a rename reaches Linear twice — a
// find that fetches and the mutation itself — and either can be the request
// that discovers the entity is gone. Both arms must supply the hint, because
// which one fires depends only on how far the rename got.
func TestCommitRename_RechecksOnBothLinearArms(t *testing.T) {
	t.Parallel()

	gone := serverGoneCases[0].err

	t.Run("find arm", func(t *testing.T) {
		rec := newRecordingCommitRenameSpec()
		rec.findErr = gone
		rechecks := 0
		rec.spec.recheck = func() { rechecks++ }

		errno := commitRename(context.Background(), &renameRecorder{}, "foo.md",
			&renameParent{}, "bar.md", rec.spec)

		if errno != syscall.ENOENT {
			t.Fatalf("errno = %v, want ENOENT", errno)
		}
		if rechecks != 1 {
			t.Errorf("recheck calls = %d, want 1", rechecks)
		}
		if rec.mutateCalls != 0 {
			t.Errorf("mutate ran after a failed find: calls = %d", rec.mutateCalls)
		}
	})

	t.Run("mutate arm", func(t *testing.T) {
		rec := newRecordingCommitRenameSpec()
		rec.mutateErr = gone
		rechecks := 0
		rec.spec.recheck = func() { rechecks++ }

		errno := commitRename(context.Background(), &renameRecorder{}, "foo.md",
			&renameParent{}, "bar.md", rec.spec)

		if errno != syscall.ENOENT {
			t.Fatalf("errno = %v, want ENOENT", errno)
		}
		if rechecks != 1 {
			t.Errorf("recheck calls = %d, want 1", rechecks)
		}
	})

	t.Run("an unknown name is not a stale row", func(t *testing.T) {
		// find returning (nil, nil) answers ENOENT without asking Linear
		// anything — the name is simply not in this collection. Nothing to
		// recheck, which is why serverSaysGone reads the error and not the errno.
		rec := newRecordingCommitRenameSpec()
		rec.findRet = nil
		rechecks := 0
		rec.spec.recheck = func() { rechecks++ }

		errno := commitRename(context.Background(), &renameRecorder{}, "foo.md",
			&renameParent{}, "bar.md", rec.spec)

		if errno != syscall.ENOENT {
			t.Fatalf("errno = %v, want ENOENT", errno)
		}
		if rechecks != 0 {
			t.Errorf("recheck calls = %d, want 0", rechecks)
		}
	})

	t.Run("a rejected rename does not recheck", func(t *testing.T) {
		rec := newRecordingCommitRenameSpec()
		rec.mutateErr = &FieldError{Field: "name", Message: "bad"}
		rechecks := 0
		rec.spec.recheck = func() { rechecks++ }

		if errno := commitRename(context.Background(), &renameRecorder{}, "foo.md",
			&renameParent{}, "bar.md", rec.spec); errno != syscall.EINVAL {
			t.Fatalf("errno = %v, want EINVAL", errno)
		}
		if rechecks != 0 {
			t.Errorf("recheck calls = %d, want 0", rechecks)
		}
	})
}
