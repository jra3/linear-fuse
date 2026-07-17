package fs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/repo"
)

func TestAttachmentURLsEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "https://example.com/pr/1", "https://example.com/pr/1", true},
		{"trailing slash on one", "https://example.com/pr/1/", "https://example.com/pr/1", true},
		{"surrounding whitespace", "  https://example.com/pr/1 ", "https://example.com/pr/1", true},
		{"different path", "https://example.com/pr/1", "https://example.com/pr/2", false},
		{"different host", "https://a.com/pr/1", "https://b.com/pr/1", false},
		{"empty both", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attachmentURLsEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("attachmentURLsEqual(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestCreateAttachmentIdempotentOnDuplicate covers #146: writing a URL that is
// already attached to the issue must be an idempotent no-op success (errno 0,
// no .error set), not an opaque API failure. The duplicate is caught by the
// local pre-check, which returns before ever touching the API client — so this
// exercises the fix end-to-end without a live client.
func TestCreateAttachmentIdempotentOnDuplicate(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{APIKey: "test-key", Cache: config.CacheConfig{TTL: 100 * time.Millisecond, MaxEntries: 100}}
	lfs, err := NewLinearFS(cfg, true)
	if err != nil {
		t.Fatalf("NewLinearFS failed: %v", err)
	}
	defer lfs.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	lfs.store = store
	lfs.repo = repo.NewSQLiteRepository(store, nil)

	ctx := context.Background()
	const issueID = "issue-1"
	const url = "https://github.com/antimetal/overlook/pull/4125"

	att := api.Attachment{ID: "att-1", Title: "PR 4125", URL: url}
	data, _ := json.Marshal(att)
	if err := store.Queries().UpsertAttachment(ctx, db.UpsertAttachmentParams{
		ID: att.ID, IssueID: issueID, Title: att.Title, Url: att.URL, Metadata: json.RawMessage("{}"), SyncedAt: time.Now(), Data: data,
	}); err != nil {
		t.Fatalf("UpsertAttachment failed: %v", err)
	}

	// Pre-seed a stale error so we can confirm a successful no-op clears it.
	attErrKey := collectionErrorKey("attachments", issueID)
	lfs.SetWriteError(attErrKey, "stale error from a prior failure")

	dir := &AttachmentsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, issueID: issueID}
	// A trailing slash must still be recognized as the same URL.
	if errno := dir.createAttachment(ctx, []byte(url+"/\n")); errno != 0 {
		t.Fatalf("createAttachment() on duplicate URL errno = %d, want 0 (idempotent no-op)", errno)
	}
	if e := lfs.GetWriteError(attErrKey); e != nil {
		t.Errorf("expected .error cleared after idempotent no-op, got %q", e.Message)
	}
}

// TestCreateAttachmentPersistFailureFailsLoud is the #284 regression (twin of
// #283): an attachment link whose SQLite reflection fails must fail loud (EIO)
// with a de-dupe .error, not report success. The bug was that the persist closure
// called the void upsertAttachment and returned nil regardless, so a wedged upsert
// returned 0 with a clean .error and a .last advertising a link the store never
// got — bypassing commitCreate's #276 persist gate.
func TestCreateAttachmentPersistFailureFailsLoud(t *testing.T) {
	lfs, store := linkTestLFS(t)

	const issueID = "issue-1"
	dir := &AttachmentsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, issueID: issueID}

	// Close the store so the persist (UpsertAttachment) fails while the mock
	// mutation still succeeds — the #276 confirmed-reflection wedge condition.
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	errno := dir.createAttachment(context.Background(), []byte("https://example.com/pr/9 Probe PR"))
	if errno != syscall.EIO {
		t.Fatalf("createAttachment on a failed persist: errno = %v, want EIO", errno)
	}

	key := collectionErrorKey("attachments", issueID)
	if e := lfs.GetWriteError(key); e == nil {
		t.Errorf(".error must be set on an unconfirmed reflection")
	}
	if got := lfs.GetWriteSuccess(key); len(got) != 0 {
		t.Errorf(".last advertised an attachment the cache can't serve: %+v", got)
	}
}
