package fs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	cfg := &config.Config{APIKey: "test-key"}
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

// TestCreateAttachmentCollisionRecordsDedupedName is the #333 Gap-2 regression for
// the attachments surface (twin of the links test): a new external attachment whose
// title collides with an existing one must record (in .last) and invalidate the
// DEDUPLICATED name Readdir/Lookup resolve (`Docs (2).link`), not the pre-dedup base
// (`Docs.link`) that first-matches the other attachment. .last's path and the
// kernel-notify name share one derivation, so .last stands in for both.
func TestCreateAttachmentCollisionRecordsDedupedName(t *testing.T) {
	lfs, store := linkTestLFS(t)
	ctx := context.Background()

	const issueID = "issue-collide"
	dir := &AttachmentsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, issueID: issueID}
	key := collectionErrorKey("attachments", issueID)

	// Seed an existing "Docs" attachment that sorts FIRST the way production data
	// does: an already-synced sibling carries a real, EARLIER created_at (the mock
	// mutator stamps its creates at a fixed 2026-01-01), so ORDER BY created_at,id
	// puts the seed first and the new colliding attachment in the "(2)" slot.
	seedCreated := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	seed := api.Attachment{ID: "seed-docs", Title: "Docs", URL: "https://example.com/seed", CreatedAt: seedCreated, UpdatedAt: seedCreated}
	data, _ := json.Marshal(seed)
	if err := store.Queries().UpsertAttachment(ctx, db.UpsertAttachmentParams{
		ID: seed.ID, IssueID: issueID, Title: seed.Title, Url: seed.URL,
		Metadata:  json.RawMessage("{}"),
		CreatedAt: sql.NullTime{Time: seedCreated, Valid: true},
		UpdatedAt: sql.NullTime{Time: seedCreated, Valid: true},
		SyncedAt:  time.Now(), Data: data,
	}); err != nil {
		t.Fatalf("seed UpsertAttachment: %v", err)
	}

	// Create a second "Docs" attachment with a distinct URL, so it is a real create
	// and not an idempotent URL-match skip.
	const newURL = "https://example.com/new-docs"
	if errno := dir.createAttachment(ctx, []byte(newURL+" Docs")); errno != 0 {
		t.Fatalf("createAttachment: errno = %v, want 0", errno)
	}

	got := lfs.GetWriteSuccess(key)
	if len(got) != 1 {
		t.Fatalf("want 1 .last entry, got %d: %+v", len(got), got)
	}
	recorded := got[0].Path

	// The recorded name must resolve — through the shared listing derivation — back
	// to the attachment that was actually created (matched by its unique URL).
	entry, ok := dir.listing(ctx, nil).find(recorded)
	if !ok {
		t.Fatalf(".last recorded name %q is not resolvable by the listing", recorded)
	}
	if entry.external == nil || entry.external.URL != newURL {
		t.Errorf(".last recorded %q, which does not resolve to the created attachment %q (#333 strand)", recorded, newURL)
	}
	// The created attachment sorts second only because the create path persisted its
	// real created_at; a NULL one would sort first and silently flip the suffix on
	// the next sync.
	if recorded != "Docs (2).link" {
		t.Errorf(".last recorded %q, want %q — the newer attachment must take the deduped slot", recorded, "Docs (2).link")
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

// TestCreateAttachmentMutateFailureRechecksLive covers attachments.go's
// post-mutation re-check (#284's idempotency half): LinkURL fails, but the URL is
// in fact already attached (Linear auto-linked a branch-named PR, or the mutation
// committed before its response was lost), so createAttachment adopts the live
// attachment as an idempotent success (a .last entry, errno 0) rather than
// surfacing the raw rejection. Only expressible through the injectable liveReader
// seam — recheckMutator's LinkURL fails while its GetIssueAttachments serves the URL.
func TestCreateAttachmentMutateFailureRechecksLive(t *testing.T) {
	lfs, _ := linkTestLFS(t)

	const issueID = "issue-recheck"
	const url = "https://github.com/antimetal/overlook/pull/9999"
	live := api.Attachment{ID: "att-recheck", Title: "Recheck PR", URL: url}
	lfs.InjectTestMutationClient(recheckMutator{err: errors.New("Unable to create issue attachment"), atts: []api.Attachment{live}})

	dir := &AttachmentsNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, issueID: issueID}
	key := collectionErrorKey("attachments", issueID)

	errno := dir.createAttachment(context.Background(), []byte(url+" Recheck PR"))
	if errno != 0 {
		t.Fatalf("createAttachment after a failed mutation with the URL live: errno = %v, want 0 (live re-check confirms)", errno)
	}
	if got := lfs.GetWriteSuccess(key); len(got) != 1 || got[0].URL != url {
		t.Fatalf("re-check must adopt the live attachment as success (.last), got: %+v", got)
	}
}
