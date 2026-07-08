package reconcile

import (
	"context"
	"errors"
	"testing"
)

// Collection is pure of the store and API — it drives only the spec's
// upsert/prune closures. These tests feed it recording fakes and assert the
// prune-safety invariant: prune runs iff every upsert succeeded, a nil
// prune (the states case) is honored, and the returned clean reports upsert
// cleanliness.

// recordingCollection records which items were upserted and whether prune fired,
// and can be told to fail specific items to exercise the clean/unclean gate.
type recordingCollection struct {
	upserted []string        // item keys passed to upsert, in order
	pruned   bool            // set when prune fired
	failOn   map[string]bool // item keys whose upsert returns an error
	pruneErr error           // if set, prune returns it
}

func (r *recordingCollection) spec(items []string) CollectionSpec[string] {
	return CollectionSpec[string]{
		Label: "thing",
		Items: items,
		Upsert: func(_ context.Context, item string) error {
			r.upserted = append(r.upserted, item)
			if r.failOn[item] {
				return errors.New("upsert failed: " + item)
			}
			return nil
		},
		Prune: func(_ context.Context) error {
			r.pruned = true
			return r.pruneErr
		},
	}
}

func TestCollectionUpsertsAllThenPrunes(t *testing.T) {
	r := &recordingCollection{}
	clean := Collection(context.Background(), r.spec([]string{"a", "b", "c"}))
	if len(r.upserted) != 3 {
		t.Errorf("upserted = %v, want all 3 items", r.upserted)
	}
	if !r.pruned {
		t.Error("prune should fire when every upsert succeeds")
	}
	if !clean {
		t.Error("clean should be true when every upsert succeeds")
	}
}

func TestCollectionUpsertFailureSuppressesPrune(t *testing.T) {
	r := &recordingCollection{failOn: map[string]bool{"b": true}}
	clean := Collection(context.Background(), r.spec([]string{"a", "b", "c"}))
	// A failed upsert does not abort the loop — every item is still attempted.
	if len(r.upserted) != 3 {
		t.Errorf("upserted = %v, want all 3 attempted (log-and-continue)", r.upserted)
	}
	if r.pruned {
		t.Error("prune must be skipped when any upsert fails — a partial fetch would read as removals")
	}
	if clean {
		t.Error("clean must be false when any upsert fails")
	}
}

func TestCollectionNilPruneSkipsPrune(t *testing.T) {
	// The states case: upsert-only, no prune closure. Must not panic, and the
	// clean return still reports upsert cleanliness.
	var upserted []string
	clean := Collection(context.Background(), CollectionSpec[string]{
		Label: "state",
		Items: []string{"todo", "done"},
		Upsert: func(_ context.Context, item string) error {
			upserted = append(upserted, item)
			return nil
		},
		// Prune nil
	})
	if len(upserted) != 2 {
		t.Errorf("upserted = %v, want both states", upserted)
	}
	if !clean {
		t.Error("clean should be true for a nil-prune collection whose upserts succeed")
	}
}

func TestCollectionNilPruneUnclean(t *testing.T) {
	// clean is about upserts, not the prune: an upsert-only collection with a
	// failing upsert still returns false.
	clean := Collection(context.Background(), CollectionSpec[string]{
		Label:  "state",
		Items:  []string{"todo"},
		Upsert: func(_ context.Context, _ string) error { return errors.New("boom") },
	})
	if clean {
		t.Error("clean must be false when an upsert fails, even with a nil prune")
	}
}

func TestCollectionEmptyItemsStillPrunes(t *testing.T) {
	// An empty (but complete) fetch is clean — the prune removes everything
	// stale, which is exactly right: the collection really is empty now.
	r := &recordingCollection{}
	clean := Collection(context.Background(), r.spec(nil))
	if len(r.upserted) != 0 {
		t.Errorf("upserted = %v, want none", r.upserted)
	}
	if !r.pruned {
		t.Error("prune should fire on an empty-but-complete fetch")
	}
	if !clean {
		t.Error("an empty-but-complete fetch is clean")
	}
}

func TestCollectionPruneErrorDoesNotPanic(t *testing.T) {
	// A prune failure is logged and swallowed — reconciliation is best-effort.
	// clean reports UPSERT cleanliness, so it stays true.
	r := &recordingCollection{pruneErr: errors.New("prune boom")}
	clean := Collection(context.Background(), r.spec([]string{"a"}))
	if !r.pruned {
		t.Error("prune should have been attempted")
	}
	if !clean {
		t.Error("a prune failure must not mark the collection unclean — clean gates on upserts")
	}
}
