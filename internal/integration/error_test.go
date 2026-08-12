package integration

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jra3/linear-fuse/internal/marshal"
)

// =============================================================================
// Error Handling Tests
// =============================================================================

// TestInvalidStatusReturnsError is one of the #420 census one-liners. It used to
// write an unresolvable status and then throw the verdict away (`_ = err`) under
// a comment saying either behaviour was acceptable — so a test whose NAME states
// a contract asserted nothing at all, and paid for it with a live issue create.
//
// The mount does keep that contract: an unknown state name fails in
// resolveIssueUpdate (internal/fs/resolve.go) with a FieldError, so the save is
// EINVAL and .error carries the Field/Value/Error detail plus the pointer to
// states.md.
//
// No mode guard, and no created issue: the rejection happens during resolution,
// BEFORE the mutator is called, so the write is inert under a live key too, and
// the subject comes from someIssueDir rather than a seeded row. Same argument as
// TestUnknownFrontmatterKeyIsRejected below, which this now mirrors — including
// the atomic save, the only save form whose verdict is synchronous.
func TestInvalidStatusReturnsError(t *testing.T) {
	dir := someIssueDir(t)
	path := filepath.Join(dir, "issue.md")
	errPath := filepath.Join(dir, ".error")

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	// Restoring the original bytes is also what retires the .error the rejection
	// leaves standing (#400): an unchanged save is a successful one.
	defer func() {
		if werr := claudeToolAtomicSave(t, path, orig); werr != nil {
			t.Errorf("restoring the original bytes of %s failed (%v): the issue is left "+
				"with a stale .error accusing a document that is valid", path, werr)
		}
	}()

	const bogus = "InvalidStatusThatDoesNotExist"
	modified, err := modifyFrontmatter(orig, "status", bogus)
	if err != nil {
		t.Fatalf("modify frontmatter: %v", err)
	}

	werr := claudeToolAtomicSave(t, path, modified)
	if !errors.Is(werr, syscall.EINVAL) {
		t.Fatalf("saving issue.md with status %q returned %v, want EINVAL — an unresolvable "+
			"workflow state must not read as a successful edit", bogus, werr)
	}

	data := readFileUntilContains(t, errPath, bogus, errorVisibilityWait)
	if !strings.Contains(string(data), bogus) {
		t.Fatalf(".error must name the rejected status %q, got: %q", bogus, data)
	}
	// The remedy travels with the complaint: an agent that reads only .error has
	// to learn where the valid states are listed.
	if !strings.Contains(string(data), "states.md") {
		t.Errorf(".error names the bad status but not where valid ones are listed: %q", data)
	}

	// Nothing partially applied.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read issue.md after the rejected save: %v", err)
	}
	if !bytes.Equal(after, orig) {
		t.Errorf("issue.md changed after a rejected save:\nbefore:\n%s\nafter:\n%s", orig, after)
	}
}

func TestWriteToReadOnlyFileReturnsError(t *testing.T) {
	// Try to write to team.md (read-only metadata file)
	path := teamInfoPath(testTeamKey)
	err := os.WriteFile(path, []byte("test"), 0644)
	if err == nil {
		t.Error("Expected error when writing to read-only team.md")
	}
}

func TestWriteToStatesFileReturnsError(t *testing.T) {
	// Try to write to states.md (read-only metadata file)
	path := teamStatesPath(testTeamKey)
	err := os.WriteFile(path, []byte("test"), 0644)
	if err == nil {
		t.Error("Expected error when writing to read-only states.md")
	}
}

func TestWriteToLabelsFileReturnsError(t *testing.T) {
	// Try to write to labels.md (read-only metadata file)
	path := teamLabelsPath(testTeamKey)
	err := os.WriteFile(path, []byte("test"), 0644)
	if err == nil {
		t.Error("Expected error when writing to read-only labels.md")
	}
}

func TestWriteToReadmeReturnsError(t *testing.T) {
	// Try to write to README.md (read-only)
	path := rootPath() + "/README.md"
	err := os.WriteFile(path, []byte("test"), 0644)
	if err == nil {
		t.Error("Expected error when writing to read-only README.md")
	}
}

func TestDeleteNewMdReturnsError(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Delete _create Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Try to delete _create
	path := newCommentPath(testTeamKey, issue.Identifier)
	err = os.Remove(path)
	if err == nil {
		t.Error("Expected error when deleting _create")
	}
}

func TestMkdirInRootReturnsError(t *testing.T) {
	// Try to create directory in root
	path := rootPath() + "/invalid_dir"
	err := os.Mkdir(path, 0755)
	if err == nil {
		os.Remove(path) // cleanup if it somehow succeeded
		t.Error("Expected error when creating directory in root")
	}
}

func TestMkdirInTeamReturnsError(t *testing.T) {
	// Try to create arbitrary directory in team (only issues/ supports mkdir)
	path := teamPath(testTeamKey) + "/invalid_dir"
	err := os.Mkdir(path, 0755)
	if err == nil {
		os.Remove(path) // cleanup if it somehow succeeded
		t.Error("Expected error when creating directory directly in team")
	}
}

func TestCreateFileInRootReturnsError(t *testing.T) {
	// Try to create a file in root
	path := rootPath() + "/invalid.txt"
	err := os.WriteFile(path, []byte("test"), 0644)
	if err == nil {
		os.Remove(path) // cleanup if it somehow succeeded
		t.Error("Expected error when creating file in root")
	}
}

func TestNonexistentPathReturnsENOENT(t *testing.T) {
	testCases := []struct {
		name string
		path string
	}{
		{"nonexistent team", teamPath("NONEXISTENT_TEAM_KEY")},
		{"nonexistent issue", issueDirPath(testTeamKey, testTeamKey+"-999999")},
		{"nonexistent user", userPath("nonexistent_user_email@example.com")},
		{"nonexistent file in team", teamPath(testTeamKey) + "/nonexistent.md"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := os.Stat(tc.path)
			if err == nil {
				t.Errorf("Expected error for %s", tc.name)
			}
			if !os.IsNotExist(err) {
				t.Errorf("Expected ENOENT for %s, got: %v", tc.name, err)
			}
		})
	}
}

// TestMalformedYAMLIsRejectedLegibly is the second #420 census one-liner. As
// TestMalformedYAMLDoesNotCrash it discarded the write verdict (`_ = err`,
// "either error or no error is acceptable") and asserted only that the mount
// still answered a ReadDir — a bar the mount clears whether or not it handled
// the bad document. Since #370 the verdict is specified: a frontmatter parse
// failure is EINVAL with "Parse error" in .error, and an indicator-triggered
// failure carries a quoting hint (marshal/frontmatter.go quotingHint).
//
// Unguarded for the same reason as TestInvalidStatusReturnsError: the parse
// fails before any resolution or mutation, so the write is inert in both modes.
// The mount-is-still-alive check the old name promised is kept — it is the one
// thing the original did assert.
func TestMalformedYAMLIsRejectedLegibly(t *testing.T) {
	dir := someIssueDir(t)
	path := filepath.Join(dir, "issue.md")
	errPath := filepath.Join(dir, ".error")

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	defer func() {
		if werr := claudeToolAtomicSave(t, path, orig); werr != nil {
			t.Errorf("restoring the original bytes of %s failed (%v): the issue is left "+
				"with a stale .error accusing a document that is valid", path, werr)
		}
	}()

	malformed := []byte("---\ntitle: [unclosed bracket\n---\nbody")
	werr := claudeToolAtomicSave(t, path, malformed)
	if !errors.Is(werr, syscall.EINVAL) {
		t.Fatalf("saving malformed frontmatter returned %v, want EINVAL", werr)
	}

	data := readFileUntilContains(t, errPath, "Parse error", errorVisibilityWait)
	if !strings.Contains(string(data), "Parse error") {
		t.Errorf(".error must say the document failed to parse, got: %q", data)
	}

	// The document was rejected whole: no field of it landed.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read issue.md after the rejected save: %v", err)
	}
	if !bytes.Equal(after, orig) {
		t.Errorf("issue.md changed after a rejected save:\nbefore:\n%s\nafter:\n%s", orig, after)
	}

	// And the mount is still serving — the claim the old name made.
	if _, err := os.ReadDir(teamsPath()); err != nil {
		t.Errorf("filesystem became unresponsive after malformed YAML: %v", err)
	}
}

// TestEmptyWriteDoesNotCorrupt is the live half of #397. An emptied issue.md is
// a truncation accident — a crashed editor, a `> file`, a botched Write tool
// call — and it must be REJECTED, not applied: an empty document has no fields,
// so applying it clears every removable field the issue had (measured: assignee,
// due, estimate, labels, and the body, in one mutation).
//
// This test used to observe that and t.Logf about it, so a test named
// "DoesNotCorrupt" reported PASS while watching the corruption. The assertions
// below are the ones its name always claimed: the write fails with EINVAL, the
// .error explains it, and the issue's fields are untouched.
func TestEmptyWriteDoesNotCorrupt(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Empty Write Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Read original content
	path := issueFilePath(testTeamKey, issue.Identifier)
	original, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}
	doc, err := parseFrontmatter(original)
	if err != nil {
		t.Fatalf("parse original issue.md: %v", err)
	}

	// Give the issue a real body FIRST. createTestIssue mkdirs the issue with
	// nothing but a title, so without this the description is "" and the
	// body-was-not-cleared assertion below compares "" to "" — it would pass just
	// as happily if the empty write had wiped the body, which is the whole point
	// of #397. The write goes through the mount so it needs no option the helper
	// honours.
	withBody, err := marshal.Render(&marshal.Document{
		Frontmatter: doc.Frontmatter,
		Body:        "a body that an empty write must not clear",
	})
	if err != nil {
		t.Fatalf("render issue.md with a body: %v", err)
	}
	claudeToolWrite(t, path, withBody)
	waitForCacheExpiry()

	original, err = readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("re-read issue.md after seeding the body: %v", err)
	}
	doc, err = parseFrontmatter(original)
	if err != nil {
		t.Fatalf("parse seeded issue.md: %v", err)
	}
	originalTitle, _ := doc.Frontmatter["title"].(string)
	originalBody := doc.Body
	if strings.TrimSpace(originalBody) == "" {
		t.Fatal("issue.md still has no body after seeding one; the body-wipe assertion below would be vacuous")
	}

	// Empty the file the way a truncating save does. The rename form is used
	// deliberately: an O_TRUNC+write can have its verdict masked when the kernel
	// serves the write from a primed page cache, whereas a rename runs Flush
	// inline and hands back the errno (the same reason claudeToolAtomicSave
	// exists).
	werr := claudeToolAtomicSave(t, path, []byte{})
	if !errors.Is(werr, syscall.EINVAL) {
		t.Errorf("emptying issue.md returned %v, want EINVAL — an empty document has no fields, "+
			"so applying it clears every removable field the issue had", werr)
	}

	// The rejection is legible: .error says what happened and how to recover.
	errPath := filepath.Join(issueDirPath(testTeamKey, issue.Identifier), ".error")
	data := readFileUntilContains(t, errPath, "Empty write rejected", errorVisibilityWait)
	for _, want := range []string{"Empty write rejected", "Nothing was written"} {
		if !strings.Contains(string(data), want) {
			t.Errorf(".error does not mention %q after an empty write, got:\n%s", want, data)
		}
	}

	// And nothing was applied. The check reads the stored row rather than the file
	// so it sees what LINEAR holds: the atomic-save path this test uses lands the
	// empty bytes on a transient node, so issue.md still serves its own content
	// either way and would not distinguish "rejected" from "applied".
	fresh, err := getIssueFromSQLite(issue.ID)
	if err != nil {
		t.Fatalf("issue not readable after the rejected write: %v", err)
	}
	if fresh.Title != originalTitle {
		t.Errorf("title = %q after a rejected empty write, want the original %q", fresh.Title, originalTitle)
	}
	if strings.TrimSpace(fresh.Description) != strings.TrimSpace(originalBody) {
		t.Errorf("description = %q after a rejected empty write, want the original %q", fresh.Description, originalBody)
	}
}

// TestUnknownFrontmatterKeyIsRejected is the #426 guard at the mount: a key
// issue.md does not accept must fail the save with EINVAL and name itself in
// .error. It ran the other way before — exit 0, empty .error, no mutation, and
// the key gone on the next fresh render — so a typo'd `teem:`/`assigne:` read as
// a successful edit, which is the one outcome the failure model has no room for.
//
// No mode guard: the rejection happens in marshal, before any resolution or
// mutation, so the write is inert under a live key too — nothing is created,
// nothing is changed, and the issue comes from someIssueDir rather than a seeded
// row. The save goes through a temp-file rename because that is the path editors
// and the Claude Code Write tool take, and the only one whose verdict is
// synchronous (a raw O_TRUNC write can be served from a primed page cache and
// never reach Flush at all).
func TestUnknownFrontmatterKeyIsRejected(t *testing.T) {
	dir := someIssueDir(t)
	path := filepath.Join(dir, "issue.md")
	errPath := filepath.Join(dir, ".error")

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	// Best-effort restore for the early-exit paths; the success path restores
	// explicitly below and asserts what the restore does to .error.
	defer func() { _ = claudeToolAtomicSave(t, path, orig) }()

	const typo = "teem"
	bad := append([]byte("---\n"+typo+": SPY\n"), bytes.TrimPrefix(orig, []byte("---\n"))...)

	werr := claudeToolAtomicSave(t, path, bad)
	if !errors.Is(werr, syscall.EINVAL) {
		t.Fatalf("saving issue.md with an unknown %q key returned %v, want EINVAL — an unrecognized key must not read as a successful edit", typo, werr)
	}

	data := readFileUntilContains(t, errPath, typo, errorVisibilityWait)
	if !strings.Contains(string(data), typo) {
		t.Fatalf(".error must name the rejected key %q, got: %q", typo, data)
	}
	// The remedy has to travel with the complaint: an agent that only reads
	// .error should learn which keys the file does accept.
	if !strings.Contains(string(data), "issue.md accepts:") {
		t.Errorf(".error names the bad key but not the accepted fields, so the writer has to guess: %q", data)
	}

	// The rejected document must not have partially applied: the file still
	// reads as it did before the save.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read issue.md after the rejected save: %v", err)
	}
	if !bytes.Equal(after, orig) {
		t.Errorf("issue.md changed after a rejected save:\nbefore:\n%s\nafter:\n%s", orig, after)
	}

	// Saving the file back unmodified is the recovery an agent performs after
	// reading .error, and it must retire the complaint (#400). The write changes
	// nothing, so it sends no mutation — but it is still a success, and a
	// success clears .error. Until this landed, the accusation outlived the
	// document it was about: the next reader saw a valid file with a populated
	// .error and no way to tell it was stale.
	if werr := claudeToolAtomicSave(t, path, orig); werr != nil {
		t.Fatalf("restoring the original bytes failed: %v", werr)
	}
	if data, _ := os.ReadFile(errPath); strings.TrimSpace(string(data)) != "" {
		t.Errorf(".error still holds the rejection after a faithful no-op re-save: %q", data)
	}
}
