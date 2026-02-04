package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Initiative Reading Tests
// =============================================================================

func TestReadInitiativeFile(t *testing.T) {
	// Find first initiative
	initiatives, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives dir: %v", err)
	}
	if len(initiatives) == 0 {
		t.Skip("No initiatives in test data")
	}

	slug := initiatives[0].Name()
	initiativeFile := filepath.Join(initiativesPath(), slug, "initiative.md")

	content, err := os.ReadFile(initiativeFile)
	if err != nil {
		t.Fatalf("Failed to read initiative.md: %v", err)
	}

	// Parse frontmatter
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Verify required fields
	requiredFields := []string{"id", "name", "slug", "status"}
	for _, field := range requiredFields {
		if _, ok := doc.Frontmatter[field]; !ok {
			t.Errorf("Missing required field: %s", field)
		}
	}

	// Verify projects format (should be simple list of slugs)
	if projects, ok := doc.Frontmatter["projects"]; ok {
		projectList, ok := projects.([]any)
		if !ok {
			t.Fatalf("Expected projects to be a list, got %T", projects)
		}

		// Verify each project is a simple string (slug), not a map
		for i, proj := range projectList {
			if _, ok := proj.(string); !ok {
				t.Errorf("Expected project[%d] to be string slug, got %T", i, proj)
			}
		}
	}
}

func TestReadInitiativeProjectsDir(t *testing.T) {
	// Find first initiative
	initiatives, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives dir: %v", err)
	}
	if len(initiatives) == 0 {
		t.Skip("No initiatives in test data")
	}

	slug := initiatives[0].Name()
	projectsDir := filepath.Join(initiativesPath(), slug, "projects")

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		t.Fatalf("Failed to read projects dir: %v", err)
	}

	// Verify each entry is a symlink
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			t.Errorf("Expected %s to be a symlink", entry.Name())
		}

		// Verify symlink points to valid team project
		symlinkPath := filepath.Join(projectsDir, entry.Name())
		target, err := os.Readlink(symlinkPath)
		if err != nil {
			t.Errorf("Failed to read symlink %s: %v", entry.Name(), err)
			continue
		}

		if !strings.Contains(target, "../../teams/") {
			t.Errorf("Expected symlink to point to teams/, got: %s", target)
		}
	}
}

// =============================================================================
// Initiative Editing Tests
// =============================================================================

func TestEditInitiativeProjects(t *testing.T) {
	skipIfNoWriteTests(t)

	// Find first initiative with projects
	initiatives, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives dir: %v", err)
	}
	if len(initiatives) == 0 {
		t.Skip("No initiatives in test data")
	}

	var initiativeSlug string
	var initiativeFile string
	var originalProjects []string

	// Find an initiative that has at least one project
	for _, init := range initiatives {
		testFile := filepath.Join(initiativesPath(), init.Name(), "initiative.md")
		content, err := os.ReadFile(testFile)
		if err != nil {
			continue
		}

		doc, err := parseFrontmatter(content)
		if err != nil {
			continue
		}

		if projects, ok := doc.Frontmatter["projects"]; ok {
			if projList, ok := projects.([]any); ok && len(projList) > 0 {
				initiativeSlug = init.Name()
				initiativeFile = testFile
				for _, p := range projList {
					if slug, ok := p.(string); ok {
						originalProjects = append(originalProjects, slug)
					}
				}
				break
			}
		}
	}

	if initiativeSlug == "" {
		t.Skip("No initiatives with projects found")
	}

	// Store original content for cleanup
	originalContent, err := os.ReadFile(initiativeFile)
	if err != nil {
		t.Fatalf("Failed to read original content: %v", err)
	}
	defer func() {
		// Restore original state
		if err := os.WriteFile(initiativeFile, originalContent, 0644); err != nil {
			t.Errorf("Failed to restore initiative: %v", err)
		}
		waitForCacheExpiry()
	}()

	// Test 1: Remove a project
	t.Run("RemoveProject", func(t *testing.T) {
		if len(originalProjects) < 2 {
			t.Skip("Need at least 2 projects to test removal")
		}

		// Remove the last project
		modifiedProjects := originalProjects[:len(originalProjects)-1]
		removedProject := originalProjects[len(originalProjects)-1]

		modified, err := modifyFrontmatter(originalContent, "projects", modifiedProjects)
		if err != nil {
			t.Fatalf("Failed to modify frontmatter: %v", err)
		}

		if err := os.WriteFile(initiativeFile, modified, 0644); err != nil {
			t.Fatalf("Failed to write initiative: %v", err)
		}

		// Verify via filesystem (no wait needed - cache invalidated immediately)
		updated, err := os.ReadFile(initiativeFile)
		if err != nil {
			t.Fatalf("Failed to read updated initiative: %v", err)
		}

		doc, err := parseFrontmatter(updated)
		if err != nil {
			t.Fatalf("Failed to parse updated frontmatter: %v", err)
		}

		updatedProjects := []string{}
		if projects, ok := doc.Frontmatter["projects"]; ok {
			if projList, ok := projects.([]any); ok {
				for _, p := range projList {
					if slug, ok := p.(string); ok {
						updatedProjects = append(updatedProjects, slug)
					}
				}
			}
		}

		if len(updatedProjects) != len(modifiedProjects) {
			t.Errorf("Expected %d projects, got %d", len(modifiedProjects), len(updatedProjects))
		}

		// Verify removed project is not in list
		for _, p := range updatedProjects {
			if p == removedProject {
				t.Errorf("Removed project %s still in list", removedProject)
			}
		}

		// Verify projects directory was updated (symlink removed)
		projectsDir := filepath.Join(initiativesPath(), initiativeSlug, "projects")
		entries, err := os.ReadDir(projectsDir)
		if err != nil {
			t.Fatalf("Failed to read projects dir: %v", err)
		}

		foundRemoved := false
		for _, entry := range entries {
			if strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(removedProject)) {
				foundRemoved = true
				break
			}
		}

		if foundRemoved {
			t.Errorf("Removed project symlink still exists in projects/")
		}
	})

	// Restore for next test
	if err := os.WriteFile(initiativeFile, originalContent, 0644); err != nil {
		t.Fatalf("Failed to restore for next test: %v", err)
	}
	waitForCacheExpiry()

	// Test 2: Add a project back
	t.Run("AddProject", func(t *testing.T) {
		// We need to find an available project slug to add
		// For now, just verify we can re-add if we removed one
		if len(originalProjects) < 1 {
			t.Skip("Need at least 1 project to test adding")
		}

		// Remove first, then add back
		modifiedProjects := originalProjects[:len(originalProjects)-1]
		addedProject := originalProjects[len(originalProjects)-1]

		// First remove
		modified, err := modifyFrontmatter(originalContent, "projects", modifiedProjects)
		if err != nil {
			t.Fatalf("Failed to modify frontmatter: %v", err)
		}
		if err := os.WriteFile(initiativeFile, modified, 0644); err != nil {
			t.Fatalf("Failed to write initiative: %v", err)
		}
		waitForCacheExpiry()

		// Then add back
		finalProjects := append(modifiedProjects, addedProject)
		modified, err = modifyFrontmatter(modified, "projects", finalProjects)
		if err != nil {
			t.Fatalf("Failed to modify frontmatter: %v", err)
		}
		if err := os.WriteFile(initiativeFile, modified, 0644); err != nil {
			t.Fatalf("Failed to write initiative: %v", err)
		}

		// Verify
		updated, err := os.ReadFile(initiativeFile)
		if err != nil {
			t.Fatalf("Failed to read updated initiative: %v", err)
		}

		doc, err := parseFrontmatter(updated)
		if err != nil {
			t.Fatalf("Failed to parse updated frontmatter: %v", err)
		}

		updatedProjects := []string{}
		if projects, ok := doc.Frontmatter["projects"]; ok {
			if projList, ok := projects.([]any); ok {
				for _, p := range projList {
					if slug, ok := p.(string); ok {
						updatedProjects = append(updatedProjects, slug)
					}
				}
			}
		}

		if len(updatedProjects) != len(finalProjects) {
			t.Errorf("Expected %d projects, got %d", len(finalProjects), len(updatedProjects))
		}

		// Verify added project is in list
		foundAdded := false
		for _, p := range updatedProjects {
			if p == addedProject {
				foundAdded = true
				break
			}
		}

		if !foundAdded {
			t.Errorf("Added project %s not found in list", addedProject)
		}
	})
}

func TestEditInitiativeProjectsInvalidSlug(t *testing.T) {
	skipIfNoWriteTests(t)

	// Find first initiative
	initiatives, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives dir: %v", err)
	}
	if len(initiatives) == 0 {
		t.Skip("No initiatives in test data")
	}

	initiativeSlug := initiatives[0].Name()
	initiativeFile := filepath.Join(initiativesPath(), initiativeSlug, "initiative.md")

	originalContent, err := os.ReadFile(initiativeFile)
	if err != nil {
		t.Fatalf("Failed to read initiative: %v", err)
	}

	defer func() {
		// Restore original state
		if err := os.WriteFile(initiativeFile, originalContent, 0644); err != nil {
			t.Errorf("Failed to restore initiative: %v", err)
		}
		waitForCacheExpiry()
	}()

	// Try to add an invalid project slug
	invalidProjects := []string{"nonexistent-project-slug-12345"}
	modified, err := modifyFrontmatter(originalContent, "projects", invalidProjects)
	if err != nil {
		t.Fatalf("Failed to modify frontmatter: %v", err)
	}

	// Write should fail (EINVAL) but we can't easily check errno in Go
	// The write will succeed but flush will fail
	err = os.WriteFile(initiativeFile, modified, 0644)

	// Writing the file doesn't fail (it's buffered), but the change won't be persisted
	// After a short wait, re-reading should show original content
	waitForCacheExpiry()

	updated, err := os.ReadFile(initiativeFile)
	if err != nil {
		t.Fatalf("Failed to read initiative after invalid write: %v", err)
	}

	// Should either show original content OR the invalid slug should not be synced
	// For now we just verify the file is still readable
	_, err = parseFrontmatter(updated)
	if err != nil {
		t.Errorf("Initiative file became unparseable after invalid write: %v", err)
	}
}

// =============================================================================
// Cache Coherency Tests
// =============================================================================

func TestInitiativeCacheCoherency(t *testing.T) {
	skipIfNoWriteTests(t)

	// Find first initiative with projects
	initiatives, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives dir: %v", err)
	}
	if len(initiatives) == 0 {
		t.Skip("No initiatives in test data")
	}

	initiativeSlug := initiatives[0].Name()
	initiativeFile := filepath.Join(initiativesPath(), initiativeSlug, "initiative.md")

	originalContent, err := os.ReadFile(initiativeFile)
	if err != nil {
		t.Fatalf("Failed to read initiative: %v", err)
	}

	defer func() {
		if err := os.WriteFile(initiativeFile, originalContent, 0644); err != nil {
			t.Errorf("Failed to restore initiative: %v", err)
		}
		waitForCacheExpiry()
	}()

	// Get original projects
	doc, err := parseFrontmatter(originalContent)
	if err != nil {
		t.Fatalf("Failed to parse original: %v", err)
	}

	var originalProjects []string
	if projects, ok := doc.Frontmatter["projects"]; ok {
		if projList, ok := projects.([]any); ok {
			for _, p := range projList {
				if slug, ok := p.(string); ok {
					originalProjects = append(originalProjects, slug)
				}
			}
		}
	}

	if len(originalProjects) == 0 {
		t.Skip("Initiative has no projects")
	}

	// Modify projects list
	newProjects := originalProjects[:max(1, len(originalProjects)-1)]
	modified, err := modifyFrontmatter(originalContent, "projects", newProjects)
	if err != nil {
		t.Fatalf("Failed to modify: %v", err)
	}

	if err := os.WriteFile(initiativeFile, modified, 0644); err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Immediately read back (no wait needed - cache invalidated)
	updated, err := os.ReadFile(initiativeFile)
	if err != nil {
		t.Fatalf("Failed to read updated: %v", err)
	}

	updatedDoc, err := parseFrontmatter(updated)
	if err != nil {
		t.Fatalf("Failed to parse updated: %v", err)
	}

	var updatedProjects []string
	if projects, ok := updatedDoc.Frontmatter["projects"]; ok {
		if projList, ok := projects.([]any); ok {
			for _, p := range projList {
				if slug, ok := p.(string); ok {
					updatedProjects = append(updatedProjects, slug)
				}
			}
		}
	}

	// Verify immediate visibility of changes
	if len(updatedProjects) != len(newProjects) {
		t.Errorf("Cache coherency issue: expected %d projects, got %d", len(newProjects), len(updatedProjects))
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
