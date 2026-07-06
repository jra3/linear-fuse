package sync

import (
	"context"
	"errors"
	"testing"
)

// syncCollection is pure of the store and API — it drives only the spec's
// upsert/prune closures. These tests feed it recording fakes and assert the
// prune-safety invariant: prune runs iff every upsert succeeded, and a nil
// prune (the states case) is honored.

// recordingCollection records which items were upserted and whether prune fired,
// and can be told to fail specific items to exercise the clean/unclean gate.
type recordingCollection struct {
	upserted []string        // item keys passed to upsert, in order
	pruned   bool            // set when prune fired
	failOn   map[string]bool // item keys whose upsert returns an error
	pruneErr error           // if set, prune returns it
}

func (r *recordingCollection) spec(items []string) syncCollectionSpec[string] {
	return syncCollectionSpec[string]{
		label: "thing",
		items: items,
		upsert: func(_ context.Context, item string) error {
			r.upserted = append(r.upserted, item)
			if r.failOn[item] {
				return errors.New("upsert failed: " + item)
			}
			return nil
		},
		prune: func(_ context.Context) error {
			r.pruned = true
			return r.pruneErr
		},
	}
}

func TestSyncCollectionUpsertsAllThenPrunes(t *testing.T) {
	r := &recordingCollection{}
	syncCollection(context.Background(), r.spec([]string{"a", "b", "c"}))
	if len(r.upserted) != 3 {
		t.Errorf("upserted = %v, want all 3 items", r.upserted)
	}
	if !r.pruned {
		t.Error("prune should fire when every upsert succeeds")
	}
}

func TestSyncCollectionUpsertFailureSuppressesPrune(t *testing.T) {
	r := &recordingCollection{failOn: map[string]bool{"b": true}}
	syncCollection(context.Background(), r.spec([]string{"a", "b", "c"}))
	// A failed upsert does not abort the loop — every item is still attempted.
	if len(r.upserted) != 3 {
		t.Errorf("upserted = %v, want all 3 attempted (log-and-continue)", r.upserted)
	}
	if r.pruned {
		t.Error("prune must be skipped when any upsert fails — a partial fetch would read as removals")
	}
}

func TestSyncCollectionNilPruneSkipsPrune(t *testing.T) {
	// The states case: upsert-only, no prune closure. Must not panic.
	var upserted []string
	syncCollection(context.Background(), syncCollectionSpec[string]{
		label: "state",
		items: []string{"todo", "done"},
		upsert: func(_ context.Context, item string) error {
			upserted = append(upserted, item)
			return nil
		},
		// prune nil
	})
	if len(upserted) != 2 {
		t.Errorf("upserted = %v, want both states", upserted)
	}
}

func TestSyncCollectionEmptyItemsStillPrunes(t *testing.T) {
	// An empty (but complete) fetch is clean — the prune removes everything
	// stale, which is exactly right: the collection really is empty now.
	r := &recordingCollection{}
	syncCollection(context.Background(), r.spec(nil))
	if len(r.upserted) != 0 {
		t.Errorf("upserted = %v, want none", r.upserted)
	}
	if !r.pruned {
		t.Error("prune should fire on an empty-but-complete fetch")
	}
}

func TestSyncCollectionPruneErrorDoesNotPanic(t *testing.T) {
	// A prune failure is logged and swallowed — sync is best-effort.
	r := &recordingCollection{pruneErr: errors.New("prune boom")}
	syncCollection(context.Background(), r.spec([]string{"a"}))
	if !r.pruned {
		t.Error("prune should have been attempted")
	}
}
