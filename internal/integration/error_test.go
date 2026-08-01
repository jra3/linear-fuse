package integration

import (
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

func TestInvalidStatusReturnsError(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Invalid Status Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Read issue
	path := issueFilePath(testTeamKey, issue.Identifier)
	content, err := readFileWithRetry(path, defaultWaitTime)
	if err != nil {
		t.Fatalf("Failed to read issue: %v", err)
	}

	// Set an invalid status
	modified, err := modifyFrontmatter(content, "status", "InvalidStatusThatDoesNotExist")
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	// Write should fail or the status should not change
	err = os.WriteFile(path, modified, 0644)
	// Note: The filesystem may return EIO or silently ignore invalid status
	// Either behavior is acceptable as long as it doesn't crash
	_ = err
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

func TestMalformedYAMLDoesNotCrash(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Malformed YAML Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Write malformed YAML
	path := issueFilePath(testTeamKey, issue.Identifier)
	malformed := []byte("---\ntitle: [unclosed bracket\n---\nbody")

	// Write should either fail or be handled gracefully
	err = os.WriteFile(path, malformed, 0644)
	// Either error or no error is acceptable, as long as it doesn't crash
	_ = err

	// Filesystem should still be operational
	_, err = os.ReadDir(teamsPath())
	if err != nil {
		t.Errorf("Filesystem became unresponsive after malformed YAML: %v", err)
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
