package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/marshal"
	"github.com/jra3/linear-fuse/internal/testutil/fixtures"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// enableMockMutations swaps in the in-memory mutation fake (T0/#155) for the
// duration of the test, so create/edit paths run to success offline (reaching
// ClearWriteError/AppendWriteSuccess and upserting to the store) instead of
// failing at the network with the fixture-mode dummy key. The real client is
// restored on cleanup so loud-failure tests that intend mutations to fail are
// unaffected. Tests using this must not t.Parallel() (the fake is process-global
// on the shared mount). Extra options (e.g. WithBodyReformat) tailor the fake to
// one test's scenario; the defaults are what every other test gets.
//
// It returns the fake so a test can audit what mutations actually went out
// (Client.Updates, #415) — nil in live mode, where nothing is injected and every
// caller is behind a skipIfLiveAPI guard anyway. Most callers ignore it.
func enableMockMutations(t *testing.T, opts ...mockmutation.Option) *mockmutation.Client {
	t.Helper()
	if liveAPIMode {
		return nil // live mode uses the real API; the fake would mask it
	}
	mock := mockmutation.New(append([]mockmutation.Option{
		mockmutation.WithTeamKey(testTeamKey),
		mockmutation.WithStore(lfs.GetStore()),
	}, opts...)...)
	lfs.InjectTestMutationClient(mock)
	t.Cleanup(func() { lfs.InjectTestMutationClient(nil) })
	return mock
}

// updatesFor filters the fake mutator's audit log down to one entity, which is
// how a write test asks "what actually went out for MY row" on a mount every
// other test is also writing to. A rejected write shows up as an EMPTY slice
// (#415): the stored value alone cannot tell a refused save from one that
// happened to persist the same bytes.
func updatesFor(mock *mockmutation.Client, id string) []mockmutation.UpdateCall {
	var out []mockmutation.UpdateCall
	for _, u := range mock.Updates() {
		if u.ID == id {
			out = append(out, u)
		}
	}
	return out
}

// issueProbe is a throwaway issue seeded straight into the fixture store for a
// single test: the ID the mutation audit log keys on, the identifier it takes
// in the mount, the two paths that follow from it, and the body it started with.
type issueProbe struct {
	ID         string // opaque issue ID — what mockmutation.UpdateCall.ID carries
	Identifier string // TST-NNNNN, the name the issue takes in the mount
	Dir        string // <mount>/teams/<KEY>/issues/<Identifier>
	Path       string // …/issue.md
	Body       string // the description it was seeded with
}

// seedIssueProbe upserts one throwaway issue and returns the handles a write
// test needs. tag names the probe in its issue ID, so a leaked row says which
// test left it; title and body are what the seeded issue starts with. Cleanup
// deletes the row, so a probe never colours the next test — which is why a
// test driving several sequences seeds one probe per subtest rather than
// sharing a row, where a buffer one rejection restored carries into the next.
// The sharper reason is updatesFor above: the audit log keys on the issue ID,
// so a shared row hands one subtest's update calls to the other's assertions.
//
// The identifier comes from ONE monotone allocator, not a per-file range: the
// hand-rolled `fmt.Sprintf("TST-%d", 30000+time.Now().UnixNano()%10000)` idiom
// this replaces made every new write test pick an unused decade by hand and
// still risk a birthday collision inside it.
//
// Spelling "TST-" here rather than in each caller is safe under the #395 rule
// only because TestFixtureLiteralsCarryTheGuard follows calls into helpers:
// a test reaching a fixture literal through this function is held to the same
// mode-guard requirement as one that spells it inline.
func seedIssueProbe(t *testing.T, tag, title, body string) issueProbe {
	t.Helper()
	if testStore == nil {
		t.Fatal("fixture mode left no test store; seedIssueProbe has nothing to seed into")
	}

	ctx := context.Background()
	team := fixtures.FixtureAPITeam()
	n := probeSeq.Add(1)
	id := fmt.Sprintf("%s-probe-%d", tag, n)
	identifier := fmt.Sprintf("TST-%d", probeIdentifierBase+n)

	row, err := db.APIIssueToDBIssue(fixtures.FixtureAPIIssue(
		fixtures.WithIssueID(id, identifier),
		fixtures.WithTitle(title),
		fixtures.WithDescription(body),
		fixtures.WithTeam(&team),
	))
	if err != nil {
		t.Fatalf("convert %s seed: %v", tag, err)
	}
	if err := testStore.Queries().UpsertIssue(ctx, row.ToUpsertParams()); err != nil {
		t.Fatalf("seed %s upsert: %v", tag, err)
	}
	t.Cleanup(func() { _ = testStore.Queries().DeleteIssue(context.Background(), id) })

	return issueProbe{
		ID:         id,
		Identifier: identifier,
		Dir:        issueDirPath(testTeamKey, identifier),
		Path:       issueFilePath(testTeamKey, identifier),
		Body:       body,
	}
}

// probeIdentifierBase sits above every identifier the fixture population and
// the static seeds use (TST-1…TST-n, TST-90xx), so a probe can never shadow a
// row a read test asserts against; probeSeq makes each probe's number unique
// within the run, which is all the per-run temp store needs.
const probeIdentifierBase = 50000

var probeSeq atomic.Int64

// isControlFile reports whether a directory entry is a virtual control/feedback
// file (the _create trigger or the .error feedback file) rather than a real
// entity file. Listing-assertion loops skip these.
func isControlFile(name string) bool {
	return name == "_create" || name == ".error" || name == ".last"
}

// firstRealEntry returns the name of the first directory entry that is not a
// control file (.error / _create), or "" if there is none. Use this instead of
// entries[0] when a dir may contain control files, since os.ReadDir sorts
// ".error" ahead of real entries.
func firstRealEntry(entries []os.DirEntry) string {
	for _, e := range entries {
		if !isControlFile(e.Name()) {
			return e.Name()
		}
	}
	return ""
}

// countItemFiles counts the item .md files in a collection directory, which is
// almost never the same number as len(os.ReadDir(dir)).
//
// A collection listing is three things concatenated (collectionDir.entries):
// the trio (_create, .error, .last), one .md per item, and one .meta sidecar
// per .md. So a directory holding N items has N*2+3 entries, and adding one
// item moves the raw count by two, not one. Asserting on entry counts couples a
// test to the trio and sidecar layout; asserting on .md files says what the
// test actually means.
func countItemFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			n++
		}
	}
	return n
}

// Path builders

func rootPath() string {
	return mountPoint
}

func teamsPath() string {
	return filepath.Join(mountPoint, "teams")
}

func teamPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey)
}

func teamInfoPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "team.md")
}

func teamStatesPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "states.md")
}

func teamLabelsPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "labels.md")
}

func issuesPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues")
}

func issueDirPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID)
}

func recentPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "recent")
}

func issuesErrorPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", ".error")
}

func issuesLastPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", ".last")
}

func issueFilePath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "issue.md")
}

func issueMetaPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "issue.meta")
}

func commentsPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "comments")
}

func commentFilePath(teamKey, issueID, filename string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "comments", filename)
}

func newCommentPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "comments", "_create")
}

func docsPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "docs")
}

func docFilePath(teamKey, issueID, filename string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "docs", filename)
}

func newDocPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "docs", "_create")
}

func cyclesPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "cycles")
}

func byStatusPath(teamKey, status string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "by", "status", status)
}

func projectsPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "projects")
}

func projectDirPath(teamKey, slug string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "projects", slug)
}

func projectFilePath(teamKey, slug string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "projects", slug, "project.md")
}

func projectMetaPath(teamKey, slug string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "projects", slug, "project.meta")
}

func initiativeMetaPath(slug string) string {
	return filepath.Join(mountPoint, "initiatives", slug, "initiative.meta")
}

// assertMetaHasFields reads a .meta sidecar and fails if any field is missing.
func assertMetaHasFields(t *testing.T, metaPath string, fields ...string) {
	t.Helper()
	content, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read %s: %v", metaPath, err)
	}
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parse %s frontmatter: %v", metaPath, err)
	}
	for _, f := range fields {
		if _, ok := doc.Frontmatter[f]; !ok {
			t.Errorf("%s missing server field %q", filepath.Base(metaPath), f)
		}
	}
}

func myPath() string {
	return filepath.Join(mountPoint, "my")
}

func myAssignedPath() string {
	return filepath.Join(mountPoint, "my", "assigned")
}

func myCreatedPath() string {
	return filepath.Join(mountPoint, "my", "created")
}

func myActivePath() string {
	return filepath.Join(mountPoint, "my", "active")
}

func usersPath() string {
	return filepath.Join(mountPoint, "users")
}

func userPath(username string) string {
	return filepath.Join(mountPoint, "users", username)
}

func initiativesPath() string {
	return filepath.Join(mountPoint, "initiatives")
}

func initiativePath(slug string) string {
	return filepath.Join(mountPoint, "initiatives", slug)
}

func labelsPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "labels")
}

func labelFilePath(teamKey, filename string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "labels", filename)
}

func byPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "by")
}

func attachmentsPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "attachments")
}

func attachmentFilePath(teamKey, issueID, filename string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "attachments", filename)
}

func byAssigneePath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "by", "assignee")
}

func byLabelPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "by", "label")
}

// Retry helpers

func readFileWithRetry(path string, maxWait time.Duration) ([]byte, error) {
	deadline := time.Now().Add(maxWait)
	var lastErr error

	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			return content, nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}

	return nil, fmt.Errorf("failed to read %s after %v: %w", path, maxWait, lastErr)
}

func waitForDirEntry(dir, name string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if e.Name() == name {
					return nil
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	return fmt.Errorf("entry %s not found in %s after %v", name, dir, maxWait)
}

// waitForNoDirEntry is the negative twin of waitForDirEntry: it polls until name
// is ABSENT from dir (a delete/rename-away invalidates the kernel cache
// asynchronously, so the removed entry can linger for a beat under load).
func waitForNoDirEntry(dir, name string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err == nil {
			present := false
			for _, e := range entries {
				if e.Name() == name {
					present = true
					break
				}
			}
			if !present {
				return nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	return fmt.Errorf("entry %s still present in %s after %v", name, dir, maxWait)
}

const defaultWaitTime = 500 * time.Millisecond

// dirHas / dirLacks are polling predicates for post-mutation listing checks. A
// write invalidates the kernel directory cache asynchronously (server.Inode/
// EntryNotify), so a one-shot os.ReadDir immediately after the mutation races
// that invalidation — the flake behind #296's "created/renamed X not in
// listing". These poll up to defaultWaitTime, which a real `ls` a moment later
// never needs. Use dirHas where the entry SHOULD now appear and dirLacks where
// it SHOULD now be gone; the one-shot dirContains stays for pre-existing
// fixture entries that never raced a mutation.
func dirHas(path, name string) bool   { return waitForDirEntry(path, name, defaultWaitTime) == nil }
func dirLacks(path, name string) bool { return waitForNoDirEntry(path, name, defaultWaitTime) == nil }

// waitForCacheExpiry waits for the internal cache to expire.
// Only needed after API-direct operations (createTestIssue, etc.) where
// the filesystem wasn't notified of the change. After filesystem writes,
// cache invalidation is immediate - no wait needed.
func waitForCacheExpiry() {
	// The wait is for the mount's kernel attr/entry timeout (100ms in tests via
	// fs.WithKernelCacheTimeouts) — not a repository TTL, which does not exist
	// (#482). Sleep a little past it.
	time.Sleep(150 * time.Millisecond)
}

// Frontmatter helpers — thin delegations to the marshal seam. The tests must
// parse and re-render frontmatter with the SAME implementation the product
// uses (marshal.Parse/marshal.Render), never a private fork: a fork validates
// the mount against a stale copy of its own contract, and Sprintf-built YAML
// is the exact idiom the catalog-render fix killed (a `Q3: Bets` name emitted
// invalid YAML).

func parseFrontmatter(content []byte) (*marshal.Document, error) {
	return marshal.Parse(content)
}

func modifyFrontmatter(content []byte, field string, value any) ([]byte, error) {
	doc, err := marshal.Parse(content)
	if err != nil {
		return nil, err
	}
	doc.Frontmatter[field] = value
	return marshal.Render(doc)
}

func removeFrontmatterField(content []byte, field string) ([]byte, error) {
	doc, err := marshal.Parse(content)
	if err != nil {
		return nil, err
	}
	delete(doc.Frontmatter, field)
	return marshal.Render(doc)
}

// Directory listing helpers

// dirNames reads path and returns the set of entry names, failing the test
// loudly (with the ReadDir error) if the directory can't be read. Use it for
// one-shot listing assertions on fixture/pre-existing entries: it collapses the
// hand-rolled `names := make(map[string]bool)` + range loop, and the returned
// set gives callers a `got %v` diagnostic for free. For post-mutation listings
// that race async kernel-cache invalidation, use the polling dirHas/dirLacks.
func dirNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read dir %s: %v", path, err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	return names
}

// dirContains reports whether path has an entry named name. It routes through
// dirNames, so a ReadDir failure is a loud t.Fatalf rather than a silent false
// (the old error-swallowing form was strictly worse than a hand-rolled loop
// when the assertion failed — it hid the failure). For entries that may race a
// just-issued mutation, use the polling dirHas/dirLacks instead.
func dirContains(t *testing.T, path, name string) bool {
	t.Helper()
	return dirNames(t, path)[name]
}
