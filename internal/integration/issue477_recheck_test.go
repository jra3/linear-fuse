package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/fs"
)

// #477 through the mount, which is the only place the bug is a bug. The unit
// tests pin the predicate (serverSaysGone) and the two mutation tails, and the
// repo tests pin the prune; none of them can show the thing the ticket is
// actually about — that the mount KEEPS LISTING an entity Linear has deleted,
// keeps accepting writes into it, and keeps failing identically. That is a
// property of a directory listing, so it needs a directory.
//
// The scenario is the ticket's:
//
//	OPS-4 was deleted in Linear. The local row survives, and it looks fresh
//	forever by every staleness measure the cache has — a deleted issue's
//	updated_at stops moving, and its detail_synced_at was stamped by the last
//	browse that succeeded. So no read rediscovers the truth, and the only
//	event that learns anything new is the write Linear rejects.
//
// Both arms run against a fake Linear whose answers are the whole variable: one
// says "Entity not found", the other says its own side broke. The create fails
// either way; only the first may prune.

const (
	goneTeamID     = "e2e477-team"
	goneTeamKey    = "OPS"
	goneIssueID    = "e2e477-issue"
	goneIssueIdent = "OPS-4"
	goneDocID      = "e2e477-doc"
	goneDocSlug    = "pager-runbook"
)

// goneT0 anchors the cache's clock: the row was synced, and nothing has moved
// since, because there is nothing left upstream to move it.
var goneT0 = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// goneLinear is the Linear this mount talks to. It answers exactly the two
// operations the scenario turns on — the comment create the user attempts, and
// the issue-details fetch a recheck makes — and returns empty data for
// everything else.
//
// The counters are the load-bearing part: detailQueries distinguishes "the
// recheck fired and Linear said no" from "nothing ever asked", which is the
// difference between the two arms.
type goneLinear struct {
	mutationError string
	detailError   string

	// releaseDetail, when set, holds the details fetch until the test closes
	// it. The hint is a background trigger, so without this the prune races
	// the caller reading .error — and the transcript is meant to show both.
	releaseDetail chan struct{}

	detailQueries atomic.Int32
	mutations     atomic.Int32
}

func (s *goneLinear) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	switch {
	case strings.Contains(req.Query, "mutation CreateComment("),
		strings.Contains(req.Query, "mutation UpdateDocument("):
		s.mutations.Add(1)
		goneWriteError(w, s.mutationError)
	case strings.Contains(req.Query, "query IssueDetails("):
		s.detailQueries.Add(1)
		if s.releaseDetail != nil {
			<-s.releaseDetail
		}
		goneWriteError(w, s.detailError)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}
}

// goneWriteError renders a GraphQL error envelope the way Linear does.
func goneWriteError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]any{{"message": message}},
	})
}

// goneMessage is Linear's own wording for an entity that is gone.
const goneMessage = "Entity not found: Issue - Could not find referenced Issue."

// goneMount is one private mount over a purpose-seeded store. It is NOT the
// suite's shared fixture mount: this scenario needs a repository with a live
// API client (the shared fixture mount deliberately has none, so every SWR
// surface there — including the recheck — is inert by construction).
type goneMount struct {
	root  string
	store *db.Store
}

// run executes one shell command with the mount as the working directory and
// records it as a transcript line, exactly as a user would have typed and seen
// it. The `cd` is inside the script rather than cmd.Dir for the reason
// issue427_rekey_test.go's twin documents: cmd.Dir chdirs in the vforked child,
// which can deadlock against our own FUSE server.
func (m *goneMount) run(t *testing.T, cmdline string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", "cd '"+m.root+"' && "+cmdline)
	out, _ := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\n")
	text = strings.ReplaceAll(text, m.root, "~/linear")
	if text == "" {
		t.Logf("$ %s", cmdline)
	} else {
		t.Logf("$ %s\n%s", cmdline, goneIndent(text))
	}
	return text
}

func goneIndent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func (m *goneMount) path(parts ...string) string {
	return filepath.Join(append([]string{m.root}, parts...)...)
}

// writeCreate is `echo "..." > <dir>/_create` with the errno kept. The shell
// redirect drops the close(2) error, and the errno IS the user-visible verdict
// here, so the transcript line is logged and the error returned.
func (m *goneMount) writeCreate(t *testing.T, dir, content string) error {
	t.Helper()
	rel := strings.TrimPrefix(dir, m.root+"/")
	f, err := os.OpenFile(filepath.Join(dir, "_create"), os.O_WRONLY, 0200)
	if err != nil {
		t.Logf("$ echo %q > %s/_create\n  open: %v", content, rel, err)
		return err
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		t.Logf("$ echo %q > %s/_create\n  write: %v", content, rel, err)
		return err
	}
	// Flush runs at close; the create's errno arrives here.
	err = f.Close()
	if err == nil {
		t.Logf("$ echo %q > %s/_create", content, rel)
	} else {
		t.Logf("$ echo %q > %s/_create\n  sh: %s/_create: %v", content, rel, rel, err)
	}
	return err
}

// seedGoneCache writes the cache as it stands after the issue was deleted in
// Linear: the row is present, and it is DETAIL-FRESH. That stamp is the whole
// point — it is what any prior successful browse of comments/, docs/ or
// attachments/ leaves behind, and it is why no read will ever rediscover the
// deletion on its own.
func seedGoneCache(t *testing.T, store *db.Store) {
	t.Helper()
	ctx := context.Background()
	q := store.Queries()

	team := api.Team{ID: goneTeamID, Key: goneTeamKey, Name: "Operations", CreatedAt: goneT0, UpdatedAt: goneT0}
	if err := q.UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	issue := api.Issue{
		ID:          goneIssueID,
		Identifier:  goneIssueIdent,
		Title:       "Rotate the on-call pager",
		Description: "Deleted in Linear after this cache row was written.",
		State:       api.State{ID: "state-todo", Name: "Todo", Type: "unstarted"},
		Team:        &api.Team{ID: goneTeamID, Key: goneTeamKey, Name: "Operations"},
		URL:         "https://linear.app/e2e477/issue/" + goneIssueIdent,
		CreatedAt:   goneT0,
		UpdatedAt:   goneT0,
	}
	row, err := db.APIIssueToDBIssue(issue)
	if err != nil {
		t.Fatalf("convert issue: %v", err)
	}
	if err := q.UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	// Two sub-resources, so the listing has something to lose besides the row —
	// and so both untouched tails have a surface here: a create into comments/,
	// and a retitle (rename) in docs/.
	comment, err := db.APICommentToDBComment(api.Comment{
		ID: "e2e477-comment", Body: "Paged twice last night.",
		CreatedAt: goneT0, UpdatedAt: goneT0,
	}, goneIssueID)
	if err != nil {
		t.Fatalf("convert comment: %v", err)
	}
	if err := q.UpsertComment(ctx, comment); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	doc, err := db.APIDocumentToDBDocument(api.Document{
		ID: goneDocID, SlugID: goneDocSlug, Title: "Pager runbook",
		Content:   "Who to wake, and when.",
		Issue:     &api.Issue{ID: goneIssueID},
		CreatedAt: goneT0, UpdatedAt: goneT0,
	})
	if err != nil {
		t.Fatalf("convert document: %v", err)
	}
	if err := q.UpsertDocument(ctx, doc); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	// Detail-fresh: the stamp lands after updated_at, which is what the
	// event-driven staleness flavor reads as "nothing to revalidate".
	if err := q.StampIssueDetailSynced(ctx, db.StampIssueDetailSyncedParams{
		DetailSyncedAt: db.ToNullTime(time.Now().Add(time.Minute)), ID: goneIssueID,
	}); err != nil {
		t.Fatalf("stamp detail synced: %v", err)
	}
}

// goneCacheTTL is the kernel's attr/entry timeout, so a row the repository
// prunes behind the mount becomes visible as an absent directory after a short
// settle rather than an unbounded wait.
const goneCacheTTL = 100 * time.Millisecond

// newGoneMount stands up the mount: its own store, its own mountpoint, and a
// repository whose API client points at the scenario's endpoint so nothing
// reaches the network. EnableSQLiteCache (not InjectTestStore) is what wires
// that client into the repository — the recheck is a repository fetch, and a
// nil-client repository cannot make one.
func newGoneMount(t *testing.T, endpoint string) *goneMount {
	t.Helper()

	root, err := os.MkdirTemp("", "linearfs-test-477-mnt-*")
	if err != nil {
		t.Fatalf("create mountpoint: %v", err)
	}
	stateDir, err := os.MkdirTemp("", "linearfs-test-477-state-*")
	if err != nil {
		t.Fatalf("create state dir: %v", err)
	}

	lfs, err := fs.NewLinearFS(&config.Config{APIKey: "e2e477-key"}, false)
	if err != nil {
		t.Fatalf("create linearfs: %v", err)
	}
	lfs.SetTestAPIURL(endpoint)
	if err := lfs.EnableSQLiteCache(filepath.Join(stateDir, "cache.db")); err != nil {
		t.Fatalf("enable sqlite cache: %v", err)
	}
	store := lfs.GetStore()
	seedGoneCache(t, store)

	server, err := fs.MountFS(root, lfs, false,
		fs.WithKernelCacheTimeouts(goneCacheTTL, goneCacheTTL))
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	if err := server.WaitMount(); err != nil {
		_ = server.Unmount()
		t.Fatalf("wait mount: %v", err)
	}

	t.Cleanup(func() {
		if err := server.Unmount(); err != nil {
			t.Logf("unmount %s: %v", root, err)
		}
		lfs.Close()
		os.RemoveAll(root)
		os.RemoveAll(stateDir)
	})

	return &goneMount{root: root, store: store}
}

// goneWaitGone polls until the issue directory stops resolving, which is the
// user-visible statement that the cache healed.
func goneWaitGone(m *goneMount) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(m.path("teams", goneTeamKey, "issues", goneIssueIdent)); errors.Is(err, syscall.ENOENT) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err := os.Stat(m.path("teams", goneTeamKey, "issues", goneIssueIdent))
	return errors.Is(err, syscall.ENOENT)
}

// goneLogBuffer captures the repository's own announcement of the prune, which
// is what an operator reads in the service log.
type goneLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *goneLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *goneLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestIssue477FailedCreateHealsTheStaleListingThroughTheMount is the ticket's
// scenario end to end: a create into a directory whose owner Linear has deleted
// fails legibly AND stops the mount from serving the dead entity.
func TestIssue477FailedCreateHealsTheStaleListingThroughTheMount(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: mounts its own store, seeded to an entity a live workspace cannot be asked to have deleted behind us")

	linear := &goneLinear{
		mutationError: goneMessage,
		detailError:   goneMessage,
		releaseDetail: make(chan struct{}),
	}
	srv := httptest.NewServer(http.HandlerFunc(linear.serve))
	defer srv.Close()

	var logs goneLogBuffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()

	m := newGoneMount(t, srv.URL)

	// -----------------------------------------------------------------
	t.Log("\n### 1. The stale row, as a user finds it\n" +
		"OPS-4 was deleted in Linear. The mount still lists it, still renders\n" +
		"it, and nothing about reading it asks Linear anything: a deleted\n" +
		"entity is fresh forever by every staleness measure the cache has.")
	// -----------------------------------------------------------------
	listing := m.run(t, "ls teams/OPS/issues/")
	if !strings.Contains(listing, goneIssueIdent) {
		t.Fatalf("teams/OPS/issues/ does not list the stale %s:\n%s", goneIssueIdent, listing)
	}
	m.run(t, "cat teams/OPS/issues/OPS-4/issue.md")
	m.run(t, "ls teams/OPS/issues/OPS-4/comments/")
	if q := linear.detailQueries.Load(); q != 0 {
		t.Fatalf("a read reached Linear (%d issue-details queries); the scenario's premise is that none does", q)
	}
	t.Log("  (0 issue-details fetches: no read rediscovers the deletion)")

	// -----------------------------------------------------------------
	t.Log("\n### 2. The write Linear rejects\n" +
		"This is the one event that learns something new. The create fails\n" +
		"ENOENT and .error says why — and both sidecars are written BEFORE the\n" +
		"hint reaches the cache, so the caller never waits on a background\n" +
		"trigger.")
	// -----------------------------------------------------------------
	err := m.writeCreate(t, m.path("teams", goneTeamKey, "issues", goneIssueIdent, "comments"),
		"Any progress on this?")
	if !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("create into a gone issue's comments/: want ENOENT, got %v", err)
	}
	errText := m.run(t, "cat teams/OPS/issues/OPS-4/comments/.error")
	for _, want := range []string{"Entity not found", "retrying will NOT help"} {
		if !strings.Contains(errText, want) {
			t.Errorf(".error does not carry %q:\n%s", want, errText)
		}
	}
	lastText := m.run(t, "cat teams/OPS/issues/OPS-4/comments/.last")
	if !strings.Contains(lastText, "failed") {
		t.Errorf(".last does not record the failed create:\n%s", lastText)
	}

	// -----------------------------------------------------------------
	t.Log("\n### 3. The hint reaches the cache, and the mount heals\n" +
		"The tail does not delete anything itself: it triggers the issue's own\n" +
		"SWR spec, which RE-ASKS Linear and prunes only on Linear's answer.")
	// -----------------------------------------------------------------
	close(linear.releaseDetail) // let the recheck's fetch answer
	if !goneWaitGone(m) {
		t.Fatalf("teams/OPS/issues/%s still resolves after the recheck (issue-details queries=%d)",
			goneIssueIdent, linear.detailQueries.Load())
	}
	if q := linear.detailQueries.Load(); q == 0 {
		t.Fatal("the row vanished without any fetch: the prune must go through a re-ask, not the fs layer's verdict")
	}
	after := m.run(t, "ls teams/OPS/issues/")
	if strings.Contains(after, goneIssueIdent) {
		t.Errorf("teams/OPS/issues/ still lists %s:\n%s", goneIssueIdent, after)
	}
	m.run(t, "cat teams/OPS/issues/OPS-4/issue.md")
	if _, err := os.Stat(m.path("teams", goneTeamKey, "issues", goneIssueIdent, "issue.md")); !errors.Is(err, syscall.ENOENT) {
		t.Errorf("issue.md of a pruned issue: want ENOENT, got %v", err)
	}
	// The sub-resource went with it: the prune is a cascade, not one row.
	if rows, err := m.store.Queries().ListIssueComments(context.Background(), goneIssueID); err != nil {
		t.Fatalf("list comments: %v", err)
	} else if len(rows) != 0 {
		t.Errorf("the pruned issue kept %d comment row(s)", len(rows))
	}

	log.SetOutput(prevOut)
	log.SetFlags(prevFlags)
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "orphan issue") {
			t.Logf("service log: %s", line)
		}
	}
}

// TestIssue477BackendFaultLeavesTheCacheAlone is the control, and the reason
// the hint is a hint. The create fails exactly as loudly, but Linear said its
// own side broke rather than "this entity is gone" — so nothing may be pruned,
// and nothing may even be ASKED: a recheck there would fetch during a window
// the caller is being told to wait out.
func TestIssue477BackendFaultLeavesTheCacheAlone(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: mounts its own store, seeded to an entity a live workspace cannot be asked to have deleted behind us")

	linear := &goneLinear{
		mutationError: "Internal server error",
		detailError:   goneMessage,
	}
	srv := httptest.NewServer(http.HandlerFunc(linear.serve))
	defer srv.Close()

	m := newGoneMount(t, srv.URL)

	m.run(t, "ls teams/OPS/issues/")
	err := m.writeCreate(t, m.path("teams", goneTeamKey, "issues", goneIssueIdent, "comments"),
		"Any progress on this?")
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("create against a backend fault: want EIO, got %v", err)
	}
	m.run(t, "cat teams/OPS/issues/OPS-4/comments/.error")

	// Give a recheck every chance to have fired before declaring it did not.
	time.Sleep(10 * goneCacheTTL)
	if q := linear.detailQueries.Load(); q != 0 {
		t.Errorf("a backend fault triggered %d recheck fetch(es); only Linear's not-found may", q)
	}
	after := m.run(t, "ls teams/OPS/issues/")
	if !strings.Contains(after, goneIssueIdent) {
		t.Errorf("teams/OPS/issues/ lost %s to a rejection that says nothing about whether it exists:\n%s",
			goneIssueIdent, after)
	}
	if _, err := os.Stat(m.path("teams", goneTeamKey, "issues", goneIssueIdent, "issue.md")); err != nil {
		t.Errorf("issue.md should still resolve after a backend fault: %v", err)
	}
}

// TestIssue477FailedRetitleHealsTheStaleListingThroughTheMount is the same
// scenario through the OTHER untouched tail: a rename. Retitling a document
// under an issue Linear has deleted is a `mv` in docs/, and before #477 it
// failed forever against a directory the mount kept serving.
func TestIssue477FailedRetitleHealsTheStaleListingThroughTheMount(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: mounts its own store, seeded to an entity a live workspace cannot be asked to have deleted behind us")

	linear := &goneLinear{
		mutationError: goneMessage,
		detailError:   goneMessage,
		releaseDetail: make(chan struct{}),
	}
	srv := httptest.NewServer(http.HandlerFunc(linear.serve))
	defer srv.Close()

	m := newGoneMount(t, srv.URL)

	docs := m.path("teams", goneTeamKey, "issues", goneIssueIdent, "docs")
	m.run(t, "ls teams/OPS/issues/OPS-4/docs/")

	// The retitle a user performs: mv the document to its new name. Run through
	// os.Rename as well as the transcript, because the errno is the verdict.
	m.run(t, `mv teams/OPS/issues/OPS-4/docs/pager-runbook.md "teams/OPS/issues/OPS-4/docs/Pager runbook v2.md"`)
	err := os.Rename(filepath.Join(docs, "pager-runbook.md"), filepath.Join(docs, "Pager runbook v2.md"))
	if !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("retitle under a gone issue: want ENOENT, got %v", err)
	}
	errText := m.run(t, "cat teams/OPS/issues/OPS-4/docs/.error")
	if !strings.Contains(errText, "Entity not found") {
		t.Errorf("docs/.error does not carry Linear's rejection:\n%s", errText)
	}

	close(linear.releaseDetail)
	if !goneWaitGone(m) {
		t.Fatalf("teams/OPS/issues/%s still resolves after the retitle's recheck (issue-details queries=%d)",
			goneIssueIdent, linear.detailQueries.Load())
	}
	after := m.run(t, "ls teams/OPS/issues/")
	if strings.Contains(after, goneIssueIdent) {
		t.Errorf("teams/OPS/issues/ still lists %s:\n%s", goneIssueIdent, after)
	}
}
