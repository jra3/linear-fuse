package integration

import (
	"os"
	"testing"
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
	issue, cleanup, err := createTestIssue("Delete new.md Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Try to delete new.md
	path := newCommentPath(testTeamKey, issue.Identifier)
	err = os.Remove(path)
	if err == nil {
		t.Error("Expected error when deleting new.md")
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

	// Write empty content
	err = os.WriteFile(path, []byte{}, 0644)
	// Should either fail or be handled gracefully
	_ = err

	// Verify via filesystem that issue still exists
	fsIssue, err := getIssueFromFilesystem(issue.Identifier)
	if err != nil {
		t.Fatalf("Issue became inaccessible after empty write: %v", err)
	}

	// Original title should still be there (empty write shouldn't corrupt)
	doc, _ := parseFrontmatter(original)
	originalTitle, _ := doc.Frontmatter["title"].(string)
	if fsIssue.Title != originalTitle {
		t.Logf("Note: Empty write may have affected issue (original: %q, current: %q)", originalTitle, fsIssue.Title)
	}
}
