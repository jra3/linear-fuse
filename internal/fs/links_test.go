package fs

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/repo"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// linkTestLFS builds a LinearFS with a real store and a succeeding mock mutator,
// the harness the links create-path tests drive.
func linkTestLFS(t *testing.T) (*LinearFS, *db.Store) {
	t.Helper()
	cfg := &config.Config{APIKey: "test-key", Cache: config.CacheConfig{TTL: 100 * time.Millisecond, MaxEntries: 100}}
	lfs, err := NewLinearFS(cfg, true)
	if err != nil {
		t.Fatalf("NewLinearFS: %v", err)
	}
	t.Cleanup(func() { lfs.Close() })

	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	lfs.store = store
	lfs.repo = repo.NewSQLiteRepository(store, nil)
	// WithStore lets the mock overlay untouched fields from current state (like the
	// real whole-entity mutation responses); reads are guarded, so a closed store
	// in the persist-failure tests just skips the overlay.
	lfs.InjectTestMutationClient(mockmutation.New(mockmutation.WithStore(store)))
	return lfs, store
}

// seedProjectLink writes a well-formed external-link row into the store for
// projectID, so createLink's cache pre-check matches url. It mints the entity via
// the mock (real identity/timestamps) then upserts it directly — the store is the
// cache, and these tests drive what the authoritative live list disagrees with.
func seedProjectLink(t *testing.T, store *db.Store, projectID, url, label string) {
	t.Helper()
	seed, err := mockmutation.New(mockmutation.WithStore(store)).CreateEntityExternalLink(
		context.Background(), map[string]any{"url": url, "label": label, "projectId": projectID})
	if err != nil {
		t.Fatalf("seed CreateEntityExternalLink: %v", err)
	}
	params, err := db.APIEntityExternalLinkToDB(*seed, projectID, "")
	if err != nil {
		t.Fatalf("APIEntityExternalLinkToDB: %v", err)
	}
	if err := store.Queries().UpsertEntityExternalLink(context.Background(), params); err != nil {
		t.Fatalf("seed UpsertEntityExternalLink: %v", err)
	}
}

// erroringLiveReader is a MutationClient whose authoritative live reads all fail,
// used to drive linkStillLive's verify-error branch: when a cache row cannot be
// confirmed against Linear, the safe default is to keep the idempotent skip rather
// than risk minting a duplicate Linear would not dedup. It embeds a nil
// MutationClient (any mutation call panics — the skip path never mutates).
type erroringLiveReader struct {
	MutationClient
	err error
}

func (e erroringLiveReader) GetProjectLinks(ctx context.Context, projectID string) ([]api.EntityExternalLink, error) {
	return nil, e.err
}

func (e erroringLiveReader) GetInitiativeLinks(ctx context.Context, initiativeID string) ([]api.EntityExternalLink, error) {
	return nil, e.err
}

func (e erroringLiveReader) GetIssueAttachments(ctx context.Context, issueID string) ([]api.Attachment, error) {
	return nil, e.err
}

// recheckMutator drives the create tails' post-mutation re-check branch: the
// mutation FAILS, but the authoritative live list already holds the URL (the
// mutation committed before its response was lost, or Linear auto-linked it), so
// the tail must adopt the live entity as the idempotent success it is rather than
// surface the raw mutation error. It embeds a nil MutationClient (only the two
// create mutations under test are overridden) and implements the liveReader seam
// with configurable live lists. Shared by the link and attachment re-check tests.
type recheckMutator struct {
	MutationClient
	err   error
	links []api.EntityExternalLink
	atts  []api.Attachment
}

func (m recheckMutator) CreateEntityExternalLink(ctx context.Context, input map[string]any) (*api.EntityExternalLink, error) {
	return nil, m.err
}

func (m recheckMutator) LinkURL(ctx context.Context, issueID, url, title string) (*api.Attachment, error) {
	return nil, m.err
}

func (m recheckMutator) GetProjectLinks(ctx context.Context, projectID string) ([]api.EntityExternalLink, error) {
	return m.links, nil
}

func (m recheckMutator) GetInitiativeLinks(ctx context.Context, initiativeID string) ([]api.EntityExternalLink, error) {
	return nil, nil
}

func (m recheckMutator) GetIssueAttachments(ctx context.Context, issueID string) ([]api.Attachment, error) {
	return m.atts, nil
}

// TestCreateLinkCollisionRecordsDedupedName is the #333 Gap-2 regression: when a
// new external link's label collides with an existing one, the create tail must
// record (in .last) and invalidate the DEDUPLICATED name Readdir/Lookup resolve
// (`Docs (2).link`), not the pre-dedup base (`Docs.link`). The base name resolves
// — by the listing's first match — to the OTHER link, so recording it strands the
// new link at a name the reader can't open (and invalidates the wrong kernel
// entry). .last's path and the kernel-notify name share one derivation, so the
// user-visible .last path stands in for both.
func TestCreateLinkCollisionRecordsDedupedName(t *testing.T) {
	lfs, store := linkTestLFS(t)

	const projectID = "proj-collide"
	dir := &LinksNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: projectID}
	key := collectionErrorKey("links", dir.parentID())

	// Seed an existing link labelled "Docs" that sorts FIRST — sort_order -1 is
	// below the mock create's default 0, so ORDER BY sort_order,id forces the new
	// colliding link into the "(2)" slot deterministically (ID string-sort aside).
	seed := api.EntityExternalLink{
		ID: "aaa-seed-docs", Label: "Docs", URL: "https://example.com/seed",
		SortOrder: -1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	params, err := db.APIEntityExternalLinkToDB(seed, projectID, "")
	if err != nil {
		t.Fatalf("APIEntityExternalLinkToDB: %v", err)
	}
	if err := store.Queries().UpsertEntityExternalLink(context.Background(), params); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	// Create a second "Docs" link with a distinct URL, so it is a real create and
	// not an idempotent URL-match skip.
	const newURL = "https://example.com/new-docs"
	if errno := dir.createLink(context.Background(), []byte(newURL+" Docs")); errno != 0 {
		t.Fatalf("createLink: errno = %v, want 0", errno)
	}

	got := lfs.GetWriteSuccess(key)
	if len(got) != 1 {
		t.Fatalf("want 1 .last entry, got %d: %+v", len(got), got)
	}
	recorded := got[0].Path

	// The recorded name must resolve — through the shared listing derivation — back
	// to the link that was actually created (matched by its unique URL), not the seed.
	listing := dir.listing(context.Background(), nil)
	entry, ok := listing.find(recorded)
	if !ok {
		t.Fatalf(".last recorded name %q is not resolvable by the listing", recorded)
	}
	if entry.link.URL != newURL {
		t.Errorf(".last recorded %q, which resolves to link URL %q; want it to resolve to the created link %q (#333 strand)",
			recorded, entry.link.URL, newURL)
	}
}

// TestCreateLinkMutateFailureRechecksLive covers links.go's post-mutation
// re-check: CreateEntityExternalLink fails, yet the authoritative live list
// already has the URL, so createLink adopts the live link as an idempotent success
// (a .last entry, errno 0) instead of surfacing the raw error. Only expressible
// through the injectable liveReader seam.
func TestCreateLinkMutateFailureRechecksLive(t *testing.T) {
	lfs, _ := linkTestLFS(t)

	const projectID = "proj-recheck"
	const url = "https://example.com/recheck-link"
	live := api.EntityExternalLink{ID: "extlink-recheck", URL: url, Label: "Recheck Link", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	lfs.InjectTestMutationClient(recheckMutator{err: errors.New("mutation response lost"), links: []api.EntityExternalLink{live}})

	dir := &LinksNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: projectID}
	key := collectionErrorKey("links", dir.parentID())

	errno := dir.createLink(context.Background(), []byte(url+" Recheck Link"))
	if errno != 0 {
		t.Fatalf("createLink after a lost mutation response: errno = %v, want 0 (live re-check confirms)", errno)
	}
	if got := lfs.GetWriteSuccess(key); len(got) != 1 || got[0].URL != url {
		t.Fatalf("re-check must adopt the live link as success (.last), got: %+v", got)
	}
}

// TestCreateLinkPersistFailureFailsLoud is the #283 regression: an external-link
// create whose SQLite reflection fails must fail loud (EIO) with a de-dupe .error,
// not report success — commitCreate's #276 persist gate has to fire. The bug was
// that the persist closure called the void upsertLink and returned nil regardless,
// so a wedged upsert returned 0 with a clean .error and a .last advertising a link
// the store never got. That is a duplicate-mint window, since Linear does not dedup
// external links server-side and the cache pre-check is the only guard.
func TestCreateLinkPersistFailureFailsLoud(t *testing.T) {
	lfs, store := linkTestLFS(t)

	const projectID = "proj-1"
	dir := &LinksNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: projectID}

	// Close the store so the persist (UpsertEntityExternalLink) fails while the
	// mock mutation still succeeds — the #276 confirmed-reflection wedge condition.
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	errno := dir.createLink(context.Background(), []byte("https://example.com/x Probe Link"))
	if errno != syscall.EIO {
		t.Fatalf("createLink on a failed persist: errno = %v, want EIO", errno)
	}

	key := collectionErrorKey("links", dir.parentID())
	if e := lfs.GetWriteError(key); e == nil {
		t.Errorf(".error must be set on an unconfirmed reflection")
	}
	// A create the local cache can't serve must never be advertised via .last.
	if got := lfs.GetWriteSuccess(key); len(got) != 0 {
		t.Errorf(".last advertised a link the cache can't serve: %+v", got)
	}
}

// TestCreateLinkPhantomProceedsToRealCreate is the #288 phantom red-green: the
// links cache holds a row (never pruned by a worker) whose URL Linear no longer
// has — a link deleted out of band. A re-create of that URL must NOT trust the
// stale cache row and skip; it must verify against the authoritative live list,
// see the row is a phantom, and fall through to a real create (a .last entry).
// This is the inverse of the idempotent skip and is only expressible once the
// live read goes through the injectable liveReader seam: the mock serves a live
// list that deliberately omits the URL the store still holds.
func TestCreateLinkPhantomProceedsToRealCreate(t *testing.T) {
	lfs, store := linkTestLFS(t)

	const projectID = "proj-phantom"
	const url = "https://example.com/phantom"

	// Seed the store with the phantom row so createLink's cache pre-check matches.
	seedProjectLink(t, store, projectID, url, "Phantom Link")

	// Inject a mutator whose authoritative live list for this project is EMPTY —
	// the store row is a phantom (deleted on Linear, still cached locally).
	mock := mockmutation.New(mockmutation.WithStore(store))
	mock.SetLiveLinks(projectID, nil)
	lfs.InjectTestMutationClient(mock)

	dir := &LinksNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: projectID}
	key := collectionErrorKey("links", dir.parentID())

	errno := dir.createLink(context.Background(), []byte(url+" Phantom Link"))
	if errno != 0 {
		t.Fatalf("createLink on a phantom row: errno = %v, want 0 (real create)", errno)
	}
	// Proceeding to a real create records a .last entry; an idempotent skip does not.
	if got := lfs.GetWriteSuccess(key); len(got) != 1 {
		t.Fatalf("phantom re-create must mint a real link (.last), got %d entries: %+v", len(got), got)
	}
}

// TestCreateLinkVerifyErrorKeepsSkip pins linkStillLive's third branch (#288):
// when the authoritative live read FAILS, a cache match cannot be confirmed as a
// phantom, so createLink keeps the idempotent cache-trust skip — protecting
// against a duplicate Linear would not dedup is the safer default when we cannot
// confirm. It must NOT fall through to a real create (no .last), the inverse of
// the phantom case above where the live list is authoritative and empty.
func TestCreateLinkVerifyErrorKeepsSkip(t *testing.T) {
	lfs, store := linkTestLFS(t)

	const projectID = "proj-verifyerr"
	const url = "https://example.com/verifyerr"

	// Seed the store so the cache pre-check matches; the live read then errors.
	seedProjectLink(t, store, projectID, url, "Verify Error Link")
	lfs.InjectTestMutationClient(erroringLiveReader{err: errors.New("live read failed")})

	dir := &LinksNode{attrNode: attrNode{BaseNode: BaseNode{lfs: lfs}}, projectID: projectID}
	key := collectionErrorKey("links", dir.parentID())

	errno := dir.createLink(context.Background(), []byte(url+" Verify Error Link"))
	if errno != 0 {
		t.Fatalf("createLink on an unconfirmable cache row: errno = %v, want 0 (cache-trust skip)", errno)
	}
	// A skip mutates nothing, so nothing is advertised via .last.
	if got := lfs.GetWriteSuccess(key); len(got) != 0 {
		t.Fatalf("verify-error path must keep the idempotent skip, but minted .last: %+v", got)
	}
}
