package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkFileURL returns the `url:` field of a *.link file read through the mount,
// i.e. the entity a given on-disk name actually resolves to.
func linkFileURL(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "url: "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("%s has no url: field:\n%s", path, data)
	return ""
}

// recordedPathsByURL maps a collection's .last entries from the created entity's
// URL to the on-disk name the create recorded for it.
func recordedPathsByURL(t *testing.T, dir string) map[string]string {
	t.Helper()
	byURL := map[string]string{}
	for _, e := range parseLastSidecar(t, filepath.Join(dir, ".last")) {
		byURL[e["url"]] = e["path"]
	}
	return byURL
}

// removeAllWithPrefix deletes every entry in dir whose name starts with prefix,
// re-listing between removals because a deduplicated name is positional.
func removeAllWithPrefix(dir, prefix string) {
	for {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		victim := ""
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), prefix) {
				victim = e.Name()
				break
			}
		}
		if victim == "" {
			return
		}
		if err := os.Remove(filepath.Join(dir, victim)); err != nil {
			return
		}
	}
}

// assertRecordedNamesRoundTrip is the shared #333 Gap-2 assertion: for every
// created URL, the name the create recorded in .last must open THAT entity
// through the mount — not a same-named sibling. It also insists a "(2)" name is
// among them, so the check can't pass vacuously on a run where no collision
// actually happened.
func assertRecordedNamesRoundTrip(t *testing.T, dir string, urls []string) {
	t.Helper()
	recorded := recordedPathsByURL(t, dir)
	deduped := false
	for _, url := range urls {
		name, ok := recorded[url]
		if !ok {
			t.Fatalf(".last has no entry for created URL %q (entries: %v)", url, recorded)
		}
		if strings.Contains(name, " (2)") {
			deduped = true
		}
		if got := linkFileURL(t, filepath.Join(dir, name)); got != url {
			t.Errorf("created %s but .last recorded %q, which opens %s (#333: the create landed at a name the reader can't open)",
				url, name, got)
		}
	}
	if !deduped {
		t.Errorf("no deduplicated name among %v — the two creates did not collide, so this proves nothing", recorded)
	}
}

// TestIssue333_CreateTailRecordsNameLookupServes is the mount-level regression
// for #333 Gap 2, exercised the way a user/agent actually experiences it: write
// two same-titled entries to _create, read the collection's .last to learn where
// they landed, then open those paths. Because the listing deduplicates
// ("Docs (2).link") while the create tail used to record the pre-dedup base
// ("Docs.link"), one of the two creates was recorded at a name that opens the
// OTHER entity — visible in readdir, unopenable by identity. Both surfaces that
// deduplicate (issue attachments, project/initiative links) are covered.
func TestIssue333_CreateTailRecordsNameLookupServes(t *testing.T) {
	skipIfLiveAPI(t, "fixture-mode offline write-path check; uses the mock mutator")
	enableMockMutations(t)

	t.Run("attachments", func(t *testing.T) {
		// A throwaway issue (unique per run), never shared TST-1, so repeated runs
		// don't accumulate into the fixture attachment counts.
		issue := createRefreshTestIssue(t, "Issue333 Attachment Collision Probe")
		dir := attachmentsPath(testTeamKey, issue)

		urls := []string{
			"https://example.com/issue333/attachment/first",
			"https://example.com/issue333/attachment/second",
		}
		for _, url := range urls {
			// Same title, distinct URLs: a real second create (the URL-match
			// idempotency skip would otherwise swallow it) that collides on name.
			if err := writeToWriteOnly(t, filepath.Join(dir, "_create"), url+" Docs"); err != nil {
				t.Fatalf("create attachment %s: %v", url, err)
			}
		}
		assertRecordedNamesRoundTrip(t, dir, urls)
	})

	t.Run("project links", func(t *testing.T) {
		dir := filepath.Join(projectsPath(testTeamKey), "test-project", "links")

		urls := []string{
			"https://example.com/issue333/link/first",
			"https://example.com/issue333/link/second",
		}
		for _, url := range urls {
			if err := writeToWriteOnly(t, filepath.Join(dir, "_create"), url+" Issue333 Link Probe"); err != nil {
				t.Fatalf("create link %s: %v", url, err)
			}
		}
		// The shared fixture project outlives this test; drop both probes so a
		// -count rerun starts clean. Re-list after every removal: deleting one of a
		// deduplicated pair renames the survivor back to the base name, so a
		// snapshot of both names would leave one behind.
		t.Cleanup(func() { removeAllWithPrefix(dir, "Issue333 Link Probe") })
		assertRecordedNamesRoundTrip(t, dir, urls)
	})
}
