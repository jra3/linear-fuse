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
	"syscall"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/fs"
	linearsync "github.com/jra3/linear-fuse/internal/sync"
)

// #427 through the mount, which is the only place the bug is a bug. The unit
// tests around it drive the drift check (internal/db), the rebuild
// (internal/sync) and the resolution policy (internal/fs) directly; what none
// of them can show is the thing a user does — open a path under teams/ and
// save it — because IssuesNode.Lookup's inode construction needs a live
// go-fuse bridge and cannot run unmounted. This test mounts one.
//
// The scenario is the ticket's, with the two halves that make a stale
// identifier dangerous rather than merely wrong:
//
//	SPY was the Agents team's key. Linear renamed it to AGT, which re-keys
//	every one of its issues server-side WITHOUT bumping any issue's updatedAt,
//	so the incremental cursor structurally cannot see it — the cached
//	identifiers stay SPY-*, forever. Then a new Spooks team takes the freed
//	SPY key, and its own SPY-7 is a genuinely different issue.
//
// From there, "SPY-7" names one issue in the cache and a different one in
// Linear, and GetIssueByIdentifier is workspace-wide, so any path spelling it
// resolves to the Agents team's issue — which is the issue a save at that
// path would mutate.

const (
	rekeyAgentsTeamID = "e2e427-team-agents"
	rekeyAgentsOldKey = "SPY" // the key Linear renamed away
	rekeyAgentsNewKey = "AGT"
	rekeySpooksTeamID = "e2e427-team-spooks"
	rekeySpooksKey    = "SPY" // the freed key, taken by a genuinely new team
)

// rekeyT0 anchors the scenario's clock. Every cached row and every watermark
// sits at T0: the rename bumped nothing, which is the whole reason the
// incremental cursor cannot see it.
var rekeyT0 = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// rekeyIssue builds an issue as the server returns it — the nested team
// carries its key, because IssueFields selects team { id key name } and that
// nested key is what lands in the blob the mount renders.
func rekeyIssue(id, identifier, title, teamID, teamKey, body string, updatedAt time.Time) api.Issue {
	return api.Issue{
		ID:          id,
		Identifier:  identifier,
		Title:       title,
		Description: body,
		State:       api.State{ID: "state-unstarted", Name: "Todo", Type: "unstarted"},
		Team:        &api.Team{ID: teamID, Key: teamKey, Name: teamKey + " team"},
		URL:         "https://linear.app/e2e427/issue/" + identifier,
		CreatedAt:   updatedAt,
		UpdatedAt:   updatedAt,
	}
}

// rekeyAPI is the Linear the sync worker talks to: a GraphQL endpoint that
// answers the two operations this scenario turns on (the teams list and a
// team's issues page) and returns empty data for everything else, which the
// worker log-and-continues past. It dispatches issues on the teamId variable,
// because the whole point is that two teams answer differently.
type rekeyAPI struct {
	teams  []api.Team
	issues map[string][]api.Issue

	// Every request, verbatim. A mutation recorded here is a mutation that
	// reached Linear, which is the only unambiguous way to state what a save
	// at a stale path would have done to a real workspace.
	mu       sync.Mutex
	requests []string
}

func (s *rekeyAPI) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)
	s.mu.Lock()
	s.requests = append(s.requests, string(body))
	s.mu.Unlock()

	data := map[string]any{}
	switch {
	case strings.Contains(req.Query, "query Teams("):
		data = map[string]any{"teams": map[string]any{
			"nodes":    s.teams,
			"pageInfo": api.PageInfo{},
		}}
	case strings.Contains(req.Query, "query TeamIssuesByUpdatedAt("):
		teamID, _ := req.Variables["teamId"].(string)
		data = map[string]any{"team": map[string]any{"issues": map[string]any{
			"nodes":    s.issues[teamID],
			"pageInfo": api.PageInfo{},
		}}}
	case strings.Contains(req.Query, "mutation UpdateIssue("):
		// Accept the write. A server that refused would leave the transcript
		// ambiguous about whether a wrong-issue save is actually harmful.
		id, _ := req.Variables["id"].(string)
		data = map[string]any{"issueUpdate": map[string]any{
			"success": true,
			"issue":   map[string]any{"id": id, "updatedAt": rekeyT0},
		}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// mutationsNaming returns the recorded mutations whose body mentions needle —
// an issue UUID, say. Empty means nothing was ever sent about it.
func (s *rekeyAPI) mutationsNaming(needle string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, req := range s.requests {
		if strings.Contains(req, "mutation ") && strings.Contains(req, needle) {
			out = append(out, req)
		}
	}
	return out
}

// rekeyMount is one private mount over a purpose-seeded store, plus the shell
// transcript helper the test reports through. It is deliberately NOT the
// suite's shared fixture mount: this scenario needs two teams whose keys
// collide across time, which is not a state the fixture population can hold.
type rekeyMount struct {
	root  string
	store *db.Store
}

// run executes one shell command with the mount as the working directory and
// records it in the test log as a transcript line, exactly as a user would
// have typed and seen it. The output is returned for assertions.
//
// The `cd` is inside the script rather than cmd.Dir, and that is load-bearing:
// cmd.Dir makes the chdir happen in the pre-exec child, and Go's os/exec forks
// with CLONE_VFORK|CLONE_VM, which freezes the forking thread inside a
// RawSyscall until the child execs. A RawSyscall never enters syscall state, so
// the scheduler cannot reclaim that thread's P — and the chdir the child is
// waiting on is a FUSE request only THIS process can answer. With few enough
// Ps (GOMAXPROCS=4 on a CI runner; deterministically at GOMAXPROCS=1) the
// server goroutine never gets scheduled and the two sides deadlock forever.
// Chdir-ing after exec costs nothing and cannot deadlock: by then the parent
// thread is running again and the mount answers normally.
func (m *rekeyMount) run(t *testing.T, cmdline string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", "cd '"+m.root+"' && "+cmdline)
	out, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\n")
	// The mount lives in a temp dir; show it as the path a user has.
	text = strings.ReplaceAll(text, m.root, "~/linear")
	status := "exit 0"
	if err != nil {
		status = err.Error()
	}
	if text == "" {
		t.Logf("$ %s\n  [%s]", cmdline, status)
	} else {
		t.Logf("$ %s\n%s\n  [%s]", cmdline, rekeyIndent(text), status)
	}
	return text
}

func rekeyIndent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func (m *rekeyMount) path(parts ...string) string {
	return filepath.Join(append([]string{m.root}, parts...)...)
}

func (m *rekeyMount) issueFile(teamKey, identifier string) string {
	return m.path("teams", teamKey, "issues", identifier, "issue.md")
}

// seedRekeyCache writes the cache exactly as it stands AFTER the rename and
// after the next cycle took the new team key: the teams rows are current, and
// every one of the Agents team's issue rows still spells the old key in both
// its identifier column and its data blob. sync_meta is at T0 for both teams,
// so the incremental cursor believes both are up to date.
func seedRekeyCache(t *testing.T, store *db.Store) {
	t.Helper()
	ctx := context.Background()
	q := store.Queries()

	teams := []api.Team{
		{ID: rekeyAgentsTeamID, Key: rekeyAgentsNewKey, Name: "Agents", CreatedAt: rekeyT0, UpdatedAt: rekeyT0},
		{ID: rekeySpooksTeamID, Key: rekeySpooksKey, Name: "Spooks", CreatedAt: rekeyT0, UpdatedAt: rekeyT0},
	}
	for _, team := range teams {
		if err := q.UpsertTeam(ctx, db.APITeamToDBTeam(team)); err != nil {
			t.Fatalf("seed team %s: %v", team.Key, err)
		}
	}

	cached := []api.Issue{
		// The Agents team's issues, as the last pre-rename cycle left them.
		rekeyIssue("e2e427-agents-7", "SPY-7", "Rotate the signing key", rekeyAgentsTeamID, rekeyAgentsOldKey,
			"Agents-team work. Nobody outside the team should be writing here.", rekeyT0),
		rekeyIssue("e2e427-agents-8", "SPY-8", "Retire the old bastion", rekeyAgentsTeamID, rekeyAgentsOldKey,
			"Agents-team work.", rekeyT0),
		// The Spooks team's own SPY-3: cached, and consistent with its team's
		// current key. It is the control — a legitimately resolvable issue
		// that the guard must keep resolving from any team's issues/ dir,
		// because ProjectNode.Lookup and ChildrenNode.Lookup put genuinely
		// cross-team issues under a containing team's directory.
		rekeyIssue("e2e427-spooks-3", "SPY-3", "Vet the new listening post", rekeySpooksTeamID, rekeySpooksKey,
			"Spooks-team work.", rekeyT0),
	}
	for _, issue := range cached {
		row, err := db.APIIssueToDBIssue(issue)
		if err != nil {
			t.Fatalf("convert %s: %v", issue.Identifier, err)
		}
		if err := q.UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
			t.Fatalf("seed %s: %v", issue.Identifier, err)
		}
	}

	for _, teamID := range []string{rekeyAgentsTeamID, rekeySpooksTeamID} {
		if err := q.UpsertSyncMeta(ctx, db.UpsertSyncMetaParams{
			TeamID:             teamID,
			LastSyncedAt:       rekeyT0,
			LastIssueUpdatedAt: db.ToNullTime(rekeyT0),
			IssueCount:         db.ToNullInt64(0),
		}); err != nil {
			t.Fatalf("seed watermark for %s: %v", teamID, err)
		}
	}
}

// rekeyServerTruth is what Linear holds now: the Agents team re-keyed (same
// UUIDs, same updatedAt — the rename bumped nothing), and the Spooks team with
// its own SPY-7, plus a newer SPY-9 sibling. That sibling is what makes the
// withheld watermark load-bearing: MAX(updated_at) is taken over rows that
// LAND, so if SPY-7's upsert collides and the cursor still advances past it,
// the next cycle calls SPY-7 unchanged and it never appears again.
func rekeyServerTruth() *rekeyAPI {
	return &rekeyAPI{
		teams: []api.Team{
			// Spooks first, so the cycle's team loop reaches the identifier
			// collision BEFORE the rebuild that frees the key.
			{ID: rekeySpooksTeamID, Key: rekeySpooksKey, Name: "Spooks", CreatedAt: rekeyT0, UpdatedAt: rekeyT0},
			{ID: rekeyAgentsTeamID, Key: rekeyAgentsNewKey, Name: "Agents", CreatedAt: rekeyT0, UpdatedAt: rekeyT0},
		},
		issues: map[string][]api.Issue{
			rekeyAgentsTeamID: {
				rekeyIssue("e2e427-agents-7", "AGT-7", "Rotate the signing key", rekeyAgentsTeamID, rekeyAgentsNewKey,
					"Agents-team work. Nobody outside the team should be writing here.", rekeyT0),
				rekeyIssue("e2e427-agents-8", "AGT-8", "Retire the old bastion", rekeyAgentsTeamID, rekeyAgentsNewKey,
					"Agents-team work.", rekeyT0),
			},
			rekeySpooksTeamID: {
				rekeyIssue("e2e427-spooks-9", "SPY-9", "Recruit two analysts", rekeySpooksTeamID, rekeySpooksKey,
					"Spooks-team work.", rekeyT0.Add(2*time.Hour)),
				rekeyIssue("e2e427-spooks-7", "SPY-7", "Surveillance budget review", rekeySpooksTeamID, rekeySpooksKey,
					"Spooks-team work. A DIFFERENT issue from the Agents team's cached SPY-7.", rekeyT0.Add(time.Hour)),
				rekeyIssue("e2e427-spooks-3", "SPY-3", "Vet the new listening post", rekeySpooksTeamID, rekeySpooksKey,
					"Spooks-team work.", rekeyT0),
			},
		},
	}
}

// newRekeyMount stands up the mount: its own store, its own mountpoint, and a
// client pointed at the scenario's GraphQL endpoint so nothing reaches the
// network. The dirs are named linearfs-test-* so TestMain's preflight and
// sweep collect them if this test ever dies before its cleanup.
func newRekeyMount(t *testing.T, endpoint string) *rekeyMount {
	t.Helper()

	root, err := os.MkdirTemp("", "linearfs-test-427-mnt-*")
	if err != nil {
		t.Fatalf("create mountpoint: %v", err)
	}
	stateDir, err := os.MkdirTemp("", "linearfs-test-427-state-*")
	if err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	store, err := db.Open(filepath.Join(stateDir, "cache.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	seedRekeyCache(t, store)

	lfs, err := fs.NewLinearFS(&config.Config{
		APIKey: "e2e427-key",
		Cache:  config.CacheConfig{TTL: rekeyCacheTTL},
	}, false)
	if err != nil {
		t.Fatalf("create linearfs: %v", err)
	}
	lfs.SetTestAPIURL(endpoint)
	if err := lfs.InjectTestStore(store); err != nil {
		t.Fatalf("inject store: %v", err)
	}

	server, err := fs.MountFS(root, lfs, false,
		fs.WithKernelCacheTimeouts(rekeyCacheTTL, rekeyCacheTTL))
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

	return &rekeyMount{root: root, store: store}
}

// rekeyCacheTTL is both the repository TTL and the kernel's attr/entry
// timeout, so a sync that changes rows behind the mount becomes visible after
// one settle of this length rather than an unbounded wait.
const rekeyCacheTTL = 100 * time.Millisecond

// settle waits out the caches above before reading a surface a sync just
// changed.
func rekeySettle() { time.Sleep(4 * rekeyCacheTTL) }

// captureSyncLog runs fn with the standard logger redirected, and returns the
// worker's [sync] lines. The repair announces itself there, and that
// announcement is what an operator actually sees when a mount heals itself.
func rekeyCaptureSyncLog(fn func()) []string {
	var buf rekeyLogBuffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	fn()
	log.SetOutput(prevOut)
	log.SetFlags(prevFlags)

	var lines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "[sync]") {
			lines = append(lines, line)
		}
	}
	return lines
}

// rekeyLogBuffer is a mutex-guarded bytes.Buffer: log output can arrive from the
// worker's goroutines as well as the caller's.
type rekeyLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *rekeyLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *rekeyLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestIssue427StaleIdentifierIsRefusedAndRepairedThroughTheMount(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode: mounts its own store, seeded to a team-key rename a live workspace cannot be asked to perform")

	truth := rekeyServerTruth()
	linear := httptest.NewServer(http.HandlerFunc(truth.serve))
	defer linear.Close()

	m := newRekeyMount(t, linear.URL)

	// -----------------------------------------------------------------
	t.Log("\n### 1. The damaged cache, as a user finds it\n" +
		"The Agents team was renamed SPY→AGT. Its issue directories still\n" +
		"carry the old key, because no issue's updatedAt moved.")
	// -----------------------------------------------------------------
	listing := m.run(t, "ls teams/")
	for _, want := range []string{rekeyAgentsNewKey, rekeySpooksKey} {
		if !strings.Contains(listing, want) {
			t.Fatalf("teams/ does not list %s:\n%s", want, listing)
		}
	}
	agents := m.run(t, "ls teams/AGT/issues/")
	if !strings.Contains(agents, "SPY-7") {
		t.Fatalf("expected the stale SPY-7 name in the renamed team's listing:\n%s", agents)
	}

	// -----------------------------------------------------------------
	t.Log("\n### 2. The wrong-issue READ is refused\n" +
		"SPY-7 in Linear is now the Spooks team's issue. In the cache it is\n" +
		"still the Agents team's. Resolution is workspace-wide, so before the\n" +
		"guard this path served — and a save would have mutated — the Agents\n" +
		"team's issue from under the Spooks team's directory.")
	// -----------------------------------------------------------------
	m.run(t, "cat teams/SPY/issues/SPY-7/issue.md")
	if _, err := os.ReadFile(m.issueFile(rekeySpooksKey, "SPY-7")); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("teams/SPY/issues/SPY-7/issue.md: want ENOENT, got %v", err)
	}
	// The same refusal from the renamed team's own directory, where the
	// stale name is still listed.
	m.run(t, "cat teams/AGT/issues/SPY-7/issue.md")
	if _, err := os.ReadFile(m.issueFile(rekeyAgentsNewKey, "SPY-7")); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("teams/AGT/issues/SPY-7/issue.md: want ENOENT, got %v", err)
	}
	// And nothing lands in .error: a stale identifier is a resolution miss,
	// not a write failure, so there is no failure for a writer to read back.
	m.run(t, "cat teams/AGT/issues/SPY-7/.error")

	// -----------------------------------------------------------------
	t.Log("\n### 3. The wrong-issue WRITE is refused\n" +
		"This is the sharp end of #427: the entity Lookup returns is the one\n" +
		"IssueFileNode.Flush writes back to Linear.")
	// -----------------------------------------------------------------
	m.run(t, `printf -- '---\ntitle: Rotate the signing key\n---\nEDITED BY SOMEONE IN THE SPOOKS TEAM.\n' > teams/SPY/issues/SPY-7/issue.md`)
	if _, err := os.Stat(m.path("teams", rekeySpooksKey, "issues", "SPY-7")); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("the stale issue directory should not exist: %v", err)
	}
	// The direct statement of the hazard: nothing about the Agents team's
	// issue was ever sent to Linear. Without the guard the save above
	// resolves to it and issues `mutation UpdateIssue` against its UUID.
	if sent := truth.mutationsNaming("e2e427-agents-7"); len(sent) > 0 {
		t.Fatalf("a save at the stale path mutated the Agents team's issue:\n%s", strings.Join(sent, "\n"))
	}
	t.Log("  (no mutation reached Linear for the Agents team's issue e2e427-agents-7)")

	// -----------------------------------------------------------------
	t.Log("\n### 4. Cross-team resolution still works (the guard is not team scoping)\n" +
		"SPY-3 belongs to Spooks. Opening it from the Agents team's issues/\n" +
		"directory must still resolve, or every cross-team project member and\n" +
		"sub-issue symlink would dangle.")
	// -----------------------------------------------------------------
	crossTeam := m.run(t, "cat teams/AGT/issues/SPY-3/issue.md")
	if !strings.Contains(crossTeam, "Vet the new listening post") {
		t.Fatalf("cross-team lookup of SPY-3 from teams/AGT failed:\n%s", crossTeam)
	}

	// -----------------------------------------------------------------
	t.Log("\n### 5. The `parent:` write path refuses the same identifier\n" +
		"ResolveIssueID feeds parentId into IssueUpdateInput, so a stale\n" +
		"identifier there re-parents someone else's issue. It is rejected as\n" +
		"an unknown issue, and the reason is readable from .error.")
	// -----------------------------------------------------------------
	control := m.issueFile(rekeySpooksKey, "SPY-3")
	original, err := os.ReadFile(control)
	if err != nil {
		t.Fatalf("read the control issue: %v", err)
	}
	edited := strings.Replace(string(original), "\nstatus:", "\nparent: SPY-7\nstatus:", 1)
	if edited == string(original) {
		edited = strings.Replace(string(original), "---\n", "---\nparent: SPY-7\n", 1)
	}
	if err := os.WriteFile(control, []byte(edited), 0o644); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("writing parent: SPY-7 should fail with EINVAL, got %v", err)
	}
	errText := m.run(t, "cat teams/SPY/issues/SPY-3/.error")
	if !strings.Contains(errText, "SPY-7") || !strings.Contains(errText, "unknown issue") {
		t.Fatalf(".error does not explain the refusal:\n%s", errText)
	}

	// -----------------------------------------------------------------
	t.Log("\n### 6. One sync cycle repairs the renamed team\n" +
		"The drift check reads the invariant (every cached identifier's prefix\n" +
		"is its team's current key), so it fires without any rename event.")
	// -----------------------------------------------------------------
	client := api.NewClient("e2e427-key")
	client.SetAPIURL(linear.URL)
	worker := linearsync.NewWorker(client, m.store, linearsync.Config{Interval: time.Hour})

	ctx := context.Background()
	for _, line := range rekeyCaptureSyncLog(func() {
		if err := worker.SyncNow(ctx); err != nil {
			t.Errorf("first sync cycle: %v", err)
		}
	}) {
		t.Logf("  %s", line)
	}
	rekeySettle()

	agents = m.run(t, "ls teams/AGT/issues/")
	for _, want := range []string{"AGT-7", "AGT-8"} {
		if !strings.Contains(agents, want) {
			t.Fatalf("renamed team's listing is missing %s:\n%s", want, agents)
		}
	}
	if strings.Contains(agents, "SPY-") {
		t.Fatalf("stale identifiers survived the rebuild:\n%s", agents)
	}
	healed := m.run(t, "cat teams/AGT/issues/AGT-7/issue.md")
	if !strings.Contains(healed, "Rotate the signing key") {
		t.Fatalf("AGT-7 does not render the Agents team's issue:\n%s", healed)
	}

	// -----------------------------------------------------------------
	t.Log("\n### 7. The freed key converges on the next cycle\n" +
		"The Spooks team's own SPY-7 collided with the stale row in cycle 1,\n" +
		"so its watermark advance was withheld — without that, MAX(updated_at)\n" +
		"over the rows that landed (SPY-9, two hours newer) would have stepped\n" +
		"the cursor past SPY-7 and the issue would never sync again.")
	// -----------------------------------------------------------------
	for _, line := range rekeyCaptureSyncLog(func() {
		if err := worker.SyncNow(ctx); err != nil {
			t.Errorf("second sync cycle: %v", err)
		}
	}) {
		t.Logf("  %s", line)
	}
	rekeySettle()

	spooks := m.run(t, "ls teams/SPY/issues/")
	for _, want := range []string{"SPY-3", "SPY-7", "SPY-9"} {
		if !strings.Contains(spooks, want) {
			t.Fatalf("Spooks team's listing is missing %s:\n%s", want, spooks)
		}
	}
	resolved := m.run(t, "cat teams/SPY/issues/SPY-7/issue.md")
	if !strings.Contains(resolved, "Surveillance budget review") {
		t.Fatalf("SPY-7 should now resolve to the Spooks team's issue:\n%s", resolved)
	}
	if strings.Contains(resolved, "Rotate the signing key") {
		t.Fatalf("SPY-7 still resolves to the Agents team's issue:\n%s", resolved)
	}

	t.Log("\n### Result\n" +
		"Before the repair the stale path was refused rather than served or\n" +
		"written; after it, the same path names the issue Linear says it names.")
}
