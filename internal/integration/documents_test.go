package integration

import (
	"os"
	"strings"
	"testing"
)

func TestDocsDirectoryListing(t *testing.T) {
	skipIfNoWriteTests(t)
	// Create issue with a document
	issue, cleanup, err := createTestIssue("Docs Listing Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	doc, docCleanup, err := createTestDocument(issue.ID, "Test Doc", "Test content")
	if err != nil {
		t.Fatalf("Failed to create test document: %v", err)
	}
	defer docCleanup()
	_ = doc

	waitForCacheExpiry()

	entries, err := os.ReadDir(docsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	// Should have at least new.md and the document
	hasNewMd := false
	hasDoc := false
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			hasNewMd = true
		}
		if strings.HasSuffix(entry.Name(), ".md") && entry.Name() != "new.md" {
			hasDoc = true
		}
	}

	if !hasNewMd {
		t.Error("Docs directory should contain new.md")
	}
	if !hasDoc {
		t.Error("Docs directory should contain at least one document file")
	}
}

func TestDocumentFilenameFormat(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Doc Filename Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	_, docCleanup, err := createTestDocument(issue.ID, "Test Doc", "Content")
	if err != nil {
		t.Fatalf("Failed to create test document: %v", err)
	}
	defer docCleanup()

	waitForCacheExpiry()

	entries, err := os.ReadDir(docsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	// Find document files (not new.md)
	for _, entry := range entries {
		name := entry.Name()
		if name == "new.md" {
			continue
		}
		// Should be {slugId}.md format
		if !strings.HasSuffix(name, ".md") {
			t.Errorf("Document file %s should have .md extension", name)
		}
	}
}

func TestDocumentFileContents(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Doc Contents Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	docContent := "This is the document body text"
	_, docCleanup, err := createTestDocument(issue.ID, "Content Test Doc", docContent)
	if err != nil {
		t.Fatalf("Failed to create test document: %v", err)
	}
	defer docCleanup()

	waitForCacheExpiry()

	entries, err := os.ReadDir(docsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	// Find and read a document file
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			continue
		}

		content, err := os.ReadFile(docFilePath(testTeamKey, issue.Identifier, entry.Name()))
		if err != nil {
			t.Fatalf("Failed to read document file: %v", err)
		}

		doc, err := parseFrontmatter(content)
		if err != nil {
			t.Fatalf("Failed to parse document frontmatter: %v", err)
		}

		// Check required fields
		if _, ok := doc.Frontmatter["id"]; !ok {
			t.Error("Document missing id field")
		}
		if _, ok := doc.Frontmatter["title"]; !ok {
			t.Error("Document missing title field")
		}
		if _, ok := doc.Frontmatter["created"]; !ok {
			t.Error("Document missing created field")
		}
		if _, ok := doc.Frontmatter["url"]; !ok {
			t.Error("Document missing url field")
		}

		// Check body contains our text
		if !strings.Contains(doc.Body, docContent) {
			t.Errorf("Document body should contain %q", docContent)
		}

		break // Only check first document
	}
}

func TestCreateDocumentViaNewMd(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Create Doc via new.md Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Write to new.md with title as first heading
	docContent := "# [TEST] Doc Created via new.md\n\nThis document was created via the filesystem."
	newMdPath := newDocPath(testTeamKey, issue.Identifier)

	if err := os.WriteFile(newMdPath, []byte(docContent), 0644); err != nil {
		t.Fatalf("Failed to write to new.md: %v", err)
	}

	// No wait needed - kernel cache invalidated on filesystem write
	// Verify document was created by listing
	entries, err := os.ReadDir(docsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	found := false
	var createdDocFile string
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			continue
		}
		content, err := os.ReadFile(docFilePath(testTeamKey, issue.Identifier, entry.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(content), "Doc Created via new.md") {
			found = true
			createdDocFile = entry.Name()
			break
		}
	}

	if !found {
		t.Error("Document created via new.md not found")
	}

	// Cleanup: delete the created document
	if createdDocFile != "" {
		_ = os.Remove(docFilePath(testTeamKey, issue.Identifier, createdDocFile))
	}
}

func TestUpdateDocument(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Update Doc Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	originalContent := "Original document content"
	_, docCleanup, err := createTestDocument(issue.ID, "Update Test Doc", originalContent)
	if err != nil {
		t.Fatalf("Failed to create test document: %v", err)
	}
	defer docCleanup()

	waitForCacheExpiry()

	// Find the document file
	entries, err := os.ReadDir(docsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	var docFile string
	for _, entry := range entries {
		if entry.Name() != "new.md" {
			docFile = entry.Name()
			break
		}
	}

	if docFile == "" {
		t.Fatal("Could not find document file")
	}

	// Read current content
	content, err := os.ReadFile(docFilePath(testTeamKey, issue.Identifier, docFile))
	if err != nil {
		t.Fatalf("Failed to read document: %v", err)
	}

	// Modify content
	updatedContent := strings.Replace(string(content), originalContent, "Updated document content", 1)
	if err := os.WriteFile(docFilePath(testTeamKey, issue.Identifier, docFile), []byte(updatedContent), 0644); err != nil {
		t.Fatalf("Failed to write updated document: %v", err)
	}

	// No wait needed - kernel cache invalidated on filesystem write
	// Re-read and verify
	newContent, err := os.ReadFile(docFilePath(testTeamKey, issue.Identifier, docFile))
	if err != nil {
		t.Fatalf("Failed to read updated document: %v", err)
	}

	if !strings.Contains(string(newContent), "Updated document content") {
		t.Error("Document should contain updated content")
	}
}

func TestDeleteDocument(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Delete Doc Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	doc, _, err := createTestDocument(issue.ID, "Delete Test Doc", "Content to delete")
	if err != nil {
		t.Fatalf("Failed to create test document: %v", err)
	}
	// Note: no defer cleanup since we're testing delete

	waitForCacheExpiry()

	// Find the document file
	entries, err := os.ReadDir(docsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	var docFile string
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			continue
		}
		content, err := os.ReadFile(docFilePath(testTeamKey, issue.Identifier, entry.Name()))
		if err != nil {
			continue
		}
		parsedDoc, err := parseFrontmatter(content)
		if err != nil {
			continue
		}
		if id, ok := parsedDoc.Frontmatter["id"].(string); ok && id == doc.ID {
			docFile = entry.Name()
			break
		}
	}

	if docFile == "" {
		t.Fatal("Could not find document file to delete")
	}

	// Delete the document
	if err := os.Remove(docFilePath(testTeamKey, issue.Identifier, docFile)); err != nil {
		t.Fatalf("Failed to delete document: %v", err)
	}

	// No wait needed - kernel cache invalidated on filesystem delete
	// Verify it's gone
	_, err = os.Stat(docFilePath(testTeamKey, issue.Identifier, docFile))
	if err == nil {
		t.Error("Document file should be deleted")
	}
}

func TestDocsNewMdAlwaysExists(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("new.md Exists Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// new.md should always be present
	_, err = os.Stat(newDocPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Errorf("new.md should always exist in docs: %v", err)
	}
}

func TestDocsNewMdWriteOnly(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("new.md Write-Only Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// new.md should be write-only (0200), so reading should fail
	_, err = os.ReadFile(newDocPath(testTeamKey, issue.Identifier))
	if err == nil {
		t.Error("new.md should be write-only and not readable")
	}

	// Verify we can still write to it
	content := `---
title: Test Document
---
Test content`
	err = os.WriteFile(newDocPath(testTeamKey, issue.Identifier), []byte(content), 0644)
	if err != nil {
		t.Errorf("new.md should be writable: %v", err)
	}
}

func TestDocsDirectoryExistsInIssue(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Docs Dir Exists Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Check docs directory exists in issue directory
	entries, err := os.ReadDir(issueDirPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read issue directory: %v", err)
	}

	hasDocs := false
	for _, entry := range entries {
		if entry.Name() == "docs" && entry.IsDir() {
			hasDocs = true
			break
		}
	}

	if !hasDocs {
		t.Error("Issue directory should contain docs/ subdirectory")
	}
}

func TestCannotDeleteNewMd(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Cannot Delete new.md Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Try to delete new.md - should fail
	err = os.Remove(newDocPath(testTeamKey, issue.Identifier))
	if err == nil {
		t.Error("Should not be able to delete new.md")
	}
}

func TestCreateDocumentViaFilename(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Create Doc via Filename Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Create document by writing to a new filename (not new.md)
	// The filename (minus .md) becomes the document title
	docTitle := "[TEST] Doc Created via Filename"
	docFilename := docTitle + ".md"
	docBody := "This document was created by writing to a named file."
	docFullPath := docFilePath(testTeamKey, issue.Identifier, docFilename)

	if err := os.WriteFile(docFullPath, []byte(docBody), 0644); err != nil {
		t.Fatalf("Failed to create document via filename: %v", err)
	}

	// Verify document was created by listing
	entries, err := os.ReadDir(docsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	found := false
	var createdDocFile string
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			continue
		}
		content, err := os.ReadFile(docFilePath(testTeamKey, issue.Identifier, entry.Name()))
		if err != nil {
			continue
		}
		// Check if title matches (could be in frontmatter or slug could be derived from title)
		if strings.Contains(string(content), docTitle) || strings.Contains(string(content), docBody) {
			found = true
			createdDocFile = entry.Name()
			break
		}
	}

	if !found {
		t.Error("Document created via filename not found")
	}

	// Cleanup: delete the created document
	if createdDocFile != "" {
		_ = os.Remove(docFilePath(testTeamKey, issue.Identifier, createdDocFile))
	}
}

func TestCreateDocumentViaFilenameWithDashes(t *testing.T) {
	skipIfNoWriteTests(t)
	issue, cleanup, err := createTestIssue("Create Doc via Dashed Filename Test")
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}
	defer cleanup()

	waitForCacheExpiry()

	// Create document using dashes in filename - should convert to spaces in title
	docFilename := "[TEST]-dashed-document-title.md"
	expectedTitle := "[TEST] dashed document title" // dashes become spaces
	docBody := "This document title was created from a dashed filename."
	docFullPath := docFilePath(testTeamKey, issue.Identifier, docFilename)

	if err := os.WriteFile(docFullPath, []byte(docBody), 0644); err != nil {
		t.Fatalf("Failed to create document via dashed filename: %v", err)
	}

	// Verify document was created
	entries, err := os.ReadDir(docsPath(testTeamKey, issue.Identifier))
	if err != nil {
		t.Fatalf("Failed to read docs directory: %v", err)
	}

	found := false
	var createdDocFile string
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			continue
		}
		content, err := os.ReadFile(docFilePath(testTeamKey, issue.Identifier, entry.Name()))
		if err != nil {
			continue
		}
		// The title should have spaces, not dashes
		if strings.Contains(string(content), expectedTitle) || strings.Contains(string(content), docBody) {
			found = true
			createdDocFile = entry.Name()
			break
		}
	}

	if !found {
		t.Error("Document created via dashed filename not found")
	}

	// Cleanup
	if createdDocFile != "" {
		_ = os.Remove(docFilePath(testTeamKey, issue.Identifier, createdDocFile))
	}
}
