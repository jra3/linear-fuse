package fs

import (
	"fmt"
	"testing"

	"gopkg.in/yaml.v3"
)

func newSuccessTestFS() *LinearFS {
	lfs := &LinearFS{}
	lfs.writeFeedback = newWriteFeedback(lfs.InvalidateUpdated)
	return lfs
}

// TestAppendWriteSuccessCapAndOrder guards the .last append log: entries append
// newest-last and the log is capped to maxWriteResults (oldest dropped).
func TestAppendWriteSuccessCapAndOrder(t *testing.T) {
	lfs := newSuccessTestFS()
	key := collectionSuccessKey("issues", "team-1")

	total := maxWriteResults + 10
	for i := 0; i < total; i++ {
		lfs.AppendWriteSuccess(key, WriteResult{Identifier: fmt.Sprintf("TST-%d", i)})
	}

	got := lfs.GetWriteSuccess(key)
	if len(got) != maxWriteResults {
		t.Fatalf("len = %d, want capped at %d", len(got), maxWriteResults)
	}
	// Newest-last, oldest dropped: first kept entry is index (total-cap).
	wantFirst := fmt.Sprintf("TST-%d", total-maxWriteResults)
	if got[0].Identifier != wantFirst {
		t.Errorf("oldest kept = %q, want %q", got[0].Identifier, wantFirst)
	}
	wantLast := fmt.Sprintf("TST-%d", total-1)
	if got[len(got)-1].Identifier != wantLast {
		t.Errorf("newest = %q, want %q", got[len(got)-1].Identifier, wantLast)
	}
}

// TestGetWriteSuccessReturnsCopy: mutating the returned slice must not affect the
// store (the review flagged returning the internal slice as a latent race).
func TestGetWriteSuccessReturnsCopy(t *testing.T) {
	lfs := newSuccessTestFS()
	key := collectionSuccessKey("docs", "issue-1")
	lfs.AppendWriteSuccess(key, WriteResult{Identifier: "A"})

	got := lfs.GetWriteSuccess(key)
	got[0] = &WriteResult{Identifier: "MUTATED"}
	if lfs.GetWriteSuccess(key)[0].Identifier != "A" {
		t.Error("GetWriteSuccess returned the internal slice; caller mutation leaked into the store")
	}
}

// TestClearWriteSuccess empties the log for a key.
func TestClearWriteSuccess(t *testing.T) {
	lfs := newSuccessTestFS()
	key := collectionSuccessKey("labels", "team-1")
	lfs.AppendWriteSuccess(key, WriteResult{Identifier: "L1"})
	lfs.ClearWriteSuccess(key)
	if got := lfs.GetWriteSuccess(key); len(got) != 0 {
		t.Errorf("after clear, len = %d, want 0", len(got))
	}
}

// TestRenderWriteSuccessYAML: the rendered .last is a YAML list with the expected
// keys, and empty when there are no successes (mirrors an empty .error).
func TestRenderWriteSuccessYAML(t *testing.T) {
	lfs := newSuccessTestFS()
	key := collectionSuccessKey("issues", "team-1")
	if got := lfs.renderWriteSuccess(key); len(got) != 0 {
		t.Errorf("empty key should render empty, got %q", got)
	}

	lfs.AppendWriteSuccess(key, WriteResult{Identifier: "TST-1", URL: "u", Path: "TST-1", Title: "T", Status: "Todo"})
	var entries []map[string]string
	if err := yaml.Unmarshal(lfs.renderWriteSuccess(key), &entries); err != nil {
		t.Fatalf("render is not a YAML list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	for _, k := range []string{"identifier", "url", "path", "title", "status"} {
		if _, ok := entries[0][k]; !ok {
			t.Errorf("rendered entry missing key %q", k)
		}
	}
}
