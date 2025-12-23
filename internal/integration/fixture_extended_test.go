package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Cycles Directory Tests
// =============================================================================

func TestFixtureCyclesDirectoryExists(t *testing.T) {
	cyclesPath := filepath.Join(teamPath(testTeamKey), "cycles")
	info, err := os.Stat(cyclesPath)
	if err != nil {
		t.Fatalf("Failed to stat cycles directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("cycles should be a directory")
	}
}

func TestFixtureCyclesDirectoryListing(t *testing.T) {
	cyclesPath := filepath.Join(teamPath(testTeamKey), "cycles")
	entries, err := os.ReadDir(cyclesPath)
	if err != nil {
		t.Fatalf("Failed to read cycles directory: %v", err)
	}

	// Should have at least one cycle (Sprint 42)
	if len(entries) == 0 {
		t.Error("Expected at least one cycle in the cycles directory")
	}
}

func TestFixtureCycleDirectoryContents(t *testing.T) {
	cyclesPath := filepath.Join(teamPath(testTeamKey), "cycles")
	entries, err := os.ReadDir(cyclesPath)
	if err != nil {
		t.Fatalf("Failed to read cycles directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No cycles to test")
	}

	// Check first cycle directory
	cycleDir := filepath.Join(cyclesPath, entries[0].Name())
	cycleEntries, err := os.ReadDir(cycleDir)
	if err != nil {
		t.Fatalf("Failed to read cycle directory: %v", err)
	}

	// Should have cycle.md info file
	hasCycleMd := false
	for _, e := range cycleEntries {
		if e.Name() == "cycle.md" {
			hasCycleMd = true
			break
		}
	}
	if !hasCycleMd {
		t.Error("Cycle directory should contain cycle.md")
	}
}

// =============================================================================
// Initiatives Directory Tests
// =============================================================================

func TestFixtureInitiativesDirectoryExists(t *testing.T) {
	initiativesPath := filepath.Join(mountPoint, "initiatives")
	info, err := os.Stat(initiativesPath)
	if err != nil {
		t.Fatalf("Failed to stat initiatives directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("initiatives should be a directory")
	}
}

func TestFixtureInitiativesDirectoryListing(t *testing.T) {
	initiativesPath := filepath.Join(mountPoint, "initiatives")
	entries, err := os.ReadDir(initiativesPath)
	if err != nil {
		t.Fatalf("Failed to read initiatives directory: %v", err)
	}

	// Should have at least one initiative (test-initiative)
	if len(entries) == 0 {
		t.Error("Expected at least one initiative in the initiatives directory")
	}
}

func TestFixtureInitiativeDirectoryContents(t *testing.T) {
	initiativesPath := filepath.Join(mountPoint, "initiatives")
	entries, err := os.ReadDir(initiativesPath)
	if err != nil {
		t.Fatalf("Failed to read initiatives directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No initiatives to test")
	}

	// Check first initiative directory
	initDir := filepath.Join(initiativesPath, entries[0].Name())
	initEntries, err := os.ReadDir(initDir)
	if err != nil {
		t.Fatalf("Failed to read initiative directory: %v", err)
	}

	// Should have initiative.md info file
	hasInitMd := false
	hasProjects := false
	for _, e := range initEntries {
		if e.Name() == "initiative.md" {
			hasInitMd = true
		}
		if e.Name() == "projects" {
			hasProjects = true
		}
	}
	if !hasInitMd {
		t.Error("Initiative directory should contain initiative.md")
	}
	if !hasProjects {
		t.Error("Initiative directory should contain projects subdirectory")
	}
}

func TestFixtureInitiativeInfoFile(t *testing.T) {
	initiativesPath := filepath.Join(mountPoint, "initiatives")
	entries, err := os.ReadDir(initiativesPath)
	if err != nil {
		t.Fatalf("Failed to read initiatives directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No initiatives to test")
	}

	// Read initiative.md
	initInfoPath := filepath.Join(initiativesPath, entries[0].Name(), "initiative.md")
	content, err := os.ReadFile(initInfoPath)
	if err != nil {
		t.Fatalf("Failed to read initiative.md: %v", err)
	}

	// Should have frontmatter with key fields
	contentStr := string(content)
	if !strings.Contains(contentStr, "id:") {
		t.Error("initiative.md should contain id field")
	}
	if !strings.Contains(contentStr, "name:") {
		t.Error("initiative.md should contain name field")
	}
}

// =============================================================================
// Labels Directory Tests
// =============================================================================

func TestFixtureLabelsDirectoryExists(t *testing.T) {
	labelsPath := filepath.Join(teamPath(testTeamKey), "labels")
	info, err := os.Stat(labelsPath)
	if err != nil {
		t.Fatalf("Failed to stat labels directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("labels should be a directory")
	}
}

func TestFixtureLabelsDirectoryListing(t *testing.T) {
	labelsPath := filepath.Join(teamPath(testTeamKey), "labels")
	entries, err := os.ReadDir(labelsPath)
	if err != nil {
		t.Fatalf("Failed to read labels directory: %v", err)
	}

	// Should have labels (Bug, Feature, Documentation) + new.md
	if len(entries) < 3 {
		t.Errorf("Expected at least 3 labels, got %d", len(entries))
	}
}

func TestFixtureLabelFileReadable(t *testing.T) {
	labelsPath := filepath.Join(teamPath(testTeamKey), "labels")
	entries, err := os.ReadDir(labelsPath)
	if err != nil {
		t.Fatalf("Failed to read labels directory: %v", err)
	}

	// Find a label file (not new.md)
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		labelPath := filepath.Join(labelsPath, entry.Name())
		content, err := os.ReadFile(labelPath)
		if err != nil {
			t.Fatalf("Failed to read label file %s: %v", entry.Name(), err)
		}

		// Should have frontmatter with color
		if !strings.Contains(string(content), "color:") {
			t.Errorf("Label file %s should contain color field", entry.Name())
		}
		return
	}
	t.Skip("No label files found to test")
}

// =============================================================================
// Filter Views Tests (by/status, by/assignee, by/label)
// =============================================================================

func TestFixtureByAssigneeDirectoryExists(t *testing.T) {
	byPath := filepath.Join(teamPath(testTeamKey), "by", "assignee")
	info, err := os.Stat(byPath)
	if err != nil {
		t.Fatalf("Failed to stat by/assignee directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("by/assignee should be a directory")
	}
}

func TestFixtureByAssigneeListing(t *testing.T) {
	byPath := filepath.Join(teamPath(testTeamKey), "by", "assignee")
	entries, err := os.ReadDir(byPath)
	if err != nil {
		t.Fatalf("Failed to read by/assignee directory: %v", err)
	}

	// Should have assignee directories
	if len(entries) == 0 {
		t.Error("Expected assignee directories in by/assignee")
	}
}

func TestFixtureByLabelDirectoryExists(t *testing.T) {
	byPath := filepath.Join(teamPath(testTeamKey), "by", "label")
	info, err := os.Stat(byPath)
	if err != nil {
		t.Fatalf("Failed to stat by/label directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("by/label should be a directory")
	}
}

func TestFixtureByLabelListing(t *testing.T) {
	byPath := filepath.Join(teamPath(testTeamKey), "by", "label")
	entries, err := os.ReadDir(byPath)
	if err != nil {
		t.Fatalf("Failed to read by/label directory: %v", err)
	}

	// Should have label directories (Bug, Feature, Documentation)
	if len(entries) < 3 {
		t.Errorf("Expected at least 3 label directories, got %d", len(entries))
	}
}

func TestFixtureByLabelContainsIssues(t *testing.T) {
	// TST-4 has Bug label
	bugPath := filepath.Join(teamPath(testTeamKey), "by", "label", "Bug")
	entries, err := os.ReadDir(bugPath)
	if err != nil {
		t.Fatalf("Failed to read by/label/Bug directory: %v", err)
	}

	hasIssue := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "TST-") {
			hasIssue = true
			break
		}
	}
	if !hasIssue {
		t.Error("Expected at least one issue symlink in Bug label directory")
	}
}

func TestFixtureUnassignedDirectoryExists(t *testing.T) {
	unassignedPath := filepath.Join(teamPath(testTeamKey), "by", "assignee", "unassigned")
	info, err := os.Stat(unassignedPath)
	if err != nil {
		t.Fatalf("Failed to stat by/assignee/unassigned directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("by/assignee/unassigned should be a directory")
	}
}

func TestFixtureUnassignedContainsIssues(t *testing.T) {
	// TST-7 is unassigned
	unassignedPath := filepath.Join(teamPath(testTeamKey), "by", "assignee", "unassigned")
	entries, err := os.ReadDir(unassignedPath)
	if err != nil {
		t.Fatalf("Failed to read by/assignee/unassigned directory: %v", err)
	}

	hasTST7 := false
	for _, entry := range entries {
		if entry.Name() == "TST-7" {
			hasTST7 = true
			break
		}
	}
	if !hasTST7 {
		t.Error("Expected TST-7 in unassigned directory")
	}
}

// =============================================================================
// Issue Children Directory Tests
// =============================================================================

func TestFixtureIssueChildrenDirectoryExists(t *testing.T) {
	// TST-1 is parent of TST-2
	childrenPath := filepath.Join(issueDirPath(testTeamKey, "TST-1"), "children")
	info, err := os.Stat(childrenPath)
	if err != nil {
		t.Fatalf("Failed to stat children directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("children should be a directory")
	}
}

func TestFixtureIssueChildrenContainsChild(t *testing.T) {
	// TST-1 is parent of TST-2
	childrenPath := filepath.Join(issueDirPath(testTeamKey, "TST-1"), "children")
	entries, err := os.ReadDir(childrenPath)
	if err != nil {
		t.Fatalf("Failed to read children directory: %v", err)
	}

	hasTST2 := false
	for _, entry := range entries {
		if entry.Name() == "TST-2" {
			hasTST2 = true
			// Should be a symlink
			if entry.Type()&os.ModeSymlink == 0 {
				t.Error("TST-2 in children should be a symlink")
			}
			break
		}
	}
	if !hasTST2 {
		t.Error("Expected TST-2 as child of TST-1")
	}
}

func TestFixtureChildSymlinkTarget(t *testing.T) {
	// TST-1 is parent of TST-2
	childLink := filepath.Join(issueDirPath(testTeamKey, "TST-1"), "children", "TST-2")

	// Read the symlink target
	target, err := os.Readlink(childLink)
	if err != nil {
		t.Fatalf("Failed to read symlink: %v", err)
	}

	// Should point to ../TST-2
	if target != "../TST-2" {
		t.Errorf("Child symlink should point to ../TST-2, got %s", target)
	}
}

// =============================================================================
// Search Directory Tests
// =============================================================================

func TestFixtureSearchDirectoryExists(t *testing.T) {
	searchPath := filepath.Join(teamPath(testTeamKey), "search")
	info, err := os.Stat(searchPath)
	if err != nil {
		t.Fatalf("Failed to stat search directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("search should be a directory")
	}
}


// =============================================================================
// Additional Filter Tests
// =============================================================================

func TestFixtureByStatusInProgressContainsIssues(t *testing.T) {
	// Multiple issues have "started" state
	inProgressPath := filepath.Join(teamPath(testTeamKey), "by", "status", "In Progress")
	entries, err := os.ReadDir(inProgressPath)
	if err != nil {
		t.Fatalf("Failed to read In Progress directory: %v", err)
	}

	// TST-1, TST-4, TST-6, TST-8 are in started state
	issueCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "TST-") {
			issueCount++
		}
	}
	if issueCount < 3 {
		t.Errorf("Expected at least 3 issues in In Progress, got %d", issueCount)
	}
}

func TestFixtureByStatusBacklogContainsIssues(t *testing.T) {
	backlogPath := filepath.Join(teamPath(testTeamKey), "by", "status", "Backlog")
	entries, err := os.ReadDir(backlogPath)
	if err != nil {
		t.Fatalf("Failed to read Backlog directory: %v", err)
	}

	// TST-3 is in backlog state
	hasTST3 := false
	for _, entry := range entries {
		if entry.Name() == "TST-3" {
			hasTST3 = true
			break
		}
	}
	if !hasTST3 {
		t.Error("Expected TST-3 in Backlog")
	}
}

func TestFixtureFilterSymlinksResolve(t *testing.T) {
	// Pick a symlink from by/status and verify it resolves
	inProgressPath := filepath.Join(teamPath(testTeamKey), "by", "status", "In Progress")
	entries, err := os.ReadDir(inProgressPath)
	if err != nil {
		t.Fatalf("Failed to read In Progress directory: %v", err)
	}

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "TST-") {
			continue
		}

		linkPath := filepath.Join(inProgressPath, entry.Name())

		// Verify it's a symlink
		if entry.Type()&os.ModeSymlink == 0 {
			t.Errorf("%s should be a symlink", entry.Name())
			continue
		}

		// Verify it resolves
		info, err := os.Stat(linkPath)
		if err != nil {
			t.Errorf("Failed to resolve symlink %s: %v", entry.Name(), err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("Symlink %s should resolve to a directory", entry.Name())
		}
		return // Just test one
	}
}
