package integration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The #476 guard through the mount. Every test here writes to a label it created
// itself, so nothing names a seeded row — but a write through the mount is a real
// mutation under a live key, so they carry the write-contract guard (see
// modes_test.go) and run against the mock mutator only.
//
// They assert against the STORE-facing readback of a fresh label, never the
// mount's readback of the file they wrote. Since #494 the file readback looks
// like it would do: all three rejections here fail the parse, so the flush never
// reaches Linear, restores the label's pre-write render, and the file reads back
// unchanged. It is still not evidence. That render is rebuilt from the node's own
// cached label, which no failing write updates, so the file reads unchanged
// whether or not a mutation went out — only the catalog, which renders from the
// store, distinguishes the two.

// seedGuardLabel creates a label through labels/_create and returns its filename
// (discovered from .last) and its generated name. The name carries a nanosecond
// suffix so no test names a seeded fixture row, none collides with another in
// the shared mount, and a repeat run (-count=N) does not meet its own leftovers;
// the label is removed again afterwards so the team's label catalog — which
// other tests in this package assert against — is left as it was found.
func seedGuardLabel(t *testing.T, prefix, description string) (filename, name string) {
	t.Helper()
	dir := labelsPath(testTeamKey)
	name = fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, "_create"),
		[]byte("---\nname: "+name+"\ncolor: \"#ff0000\"\ndescription: "+description+"\n---\n"), 0200); err != nil {
		t.Fatalf("create label %s: %v", name, err)
	}
	for _, e := range parseLastSidecar(t, filepath.Join(dir, ".last")) {
		if e["title"] == name {
			t.Cleanup(func() {
				if err := os.Remove(filepath.Join(dir, e["path"])); err != nil {
					t.Logf("cleanup label %s: %v", e["path"], err)
				}
			})
			return e["path"], name
		}
	}
	t.Fatalf("labels/.last has no entry for %s", name)
	return "", ""
}

// assertLabelUnchanged proves the rejected document applied NOTHING, reading the
// team's labels.md catalog — which renders from the store — rather than the file
// that was written. Each rejected document below also carries a name change, so
// a guard that ran after the diff instead of before it would show up here as the
// renamed label.
func assertLabelUnchanged(t *testing.T, filename, seededName, attemptedName string) {
	t.Helper()
	entries, err := os.ReadDir(labelsPath(testTeamKey))
	if err != nil {
		t.Fatalf("readdir labels/: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Name() == filename {
			found = true
		}
	}
	if !found {
		t.Errorf("label %s vanished from the listing under a rejected write", filename)
	}
	catalog, err := os.ReadFile(teamLabelsPath(testTeamKey))
	if err != nil {
		t.Fatalf("read labels.md: %v", err)
	}
	if !strings.Contains(string(catalog), seededName) {
		t.Errorf("labels.md no longer lists %q after a rejected write", seededName)
	}
	if strings.Contains(string(catalog), attemptedName) {
		t.Errorf("labels.md lists %q — the rejected document applied its name change", attemptedName)
	}
}

// TestUnknownLabelKeyIsRejected: the reported write. `parent:` is a field this
// surface cannot express (label groups), and before the guard it was accepted at
// exit 0 with no mutation sent and no trace anywhere.
func TestUnknownLabelKeyIsRejected(t *testing.T) {
	skipIfLiveAPI(t, fixtureWriteContract)
	enableMockMutations(t)

	filename, name := seedGuardLabel(t, "guard-unknown-key", "seeded")
	dir := labelsPath(testTeamKey)

	err := os.WriteFile(filepath.Join(dir, filename),
		[]byte("---\nname: "+name+"-RENAMED\nparent: Context\ndescription: changed\n---\n"), 0644)
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("write with an unknown key = %v, want EINVAL at close(2)", err)
	}
	assertLabelUnchanged(t, filename, name, name+"-RENAMED")

	errText := readErrorFile(t, filepath.Join(dir, ".error"))
	for _, want := range []string{"Field: parent", "unknown field", "name, color, description"} {
		if !strings.Contains(errText, want) {
			t.Errorf("labels/.error does not carry %q:\n%s", want, errText)
		}
	}
}

// TestLabelBodyIsRejectedNotDropped: a label has no content field, so prose
// below the closing --- is text the mount would accept and send nowhere.
func TestLabelBodyIsRejectedNotDropped(t *testing.T) {
	skipIfLiveAPI(t, fixtureWriteContract)
	enableMockMutations(t)

	filename, name := seedGuardLabel(t, "guard-body", "seeded")
	dir := labelsPath(testTeamKey)

	err := os.WriteFile(filepath.Join(dir, filename),
		[]byte("---\nname: "+name+"-RENAMED\n---\nThis prose has nowhere to go.\n"), 0644)
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("write with a body = %v, want EINVAL at close(2)", err)
	}
	assertLabelUnchanged(t, filename, name, name+"-RENAMED")

	errText := readErrorFile(t, filepath.Join(dir, ".error"))
	for _, want := range []string{"Field: body", "frontmatter-only"} {
		if !strings.Contains(errText, want) {
			t.Errorf("labels/.error does not carry %q:\n%s", want, errText)
		}
	}
}

// TestLabelErrorFileCarriesTime: a collection .error is retired only by the next
// successful write to that directory, so a reader needs the content itself to
// say when it was recorded — agents cat, they do not stat.
func TestLabelErrorFileCarriesTime(t *testing.T) {
	skipIfLiveAPI(t, fixtureWriteContract)
	enableMockMutations(t)

	filename, name := seedGuardLabel(t, "guard-error-time", "seeded")
	dir := labelsPath(testTeamKey)

	before := time.Now().Add(-2 * time.Minute)
	if err := os.WriteFile(filepath.Join(dir, filename),
		[]byte("---\nname: "+name+"\ndescriptoin: typo\n---\n"), 0644); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("write with a misspelled key = %v, want EINVAL", err)
	}

	errText := readErrorFile(t, filepath.Join(dir, ".error"))
	var stamp string
	for _, line := range strings.Split(errText, "\n") {
		if rest, ok := strings.CutPrefix(line, "Time: "); ok {
			stamp = rest
		}
	}
	if stamp == "" {
		t.Fatalf("labels/.error carries no Time: line:\n%s", errText)
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("Time: %q is not RFC3339: %v", stamp, err)
	}
	if at.Before(before) || at.After(time.Now().Add(2*time.Minute)) {
		t.Errorf("Time: %v is not near the write", at)
	}
}

// readErrorFile reads a .error whole. It exists so the guard tests read the
// rendered bytes (message + Time:) rather than a stat.
func readErrorFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
