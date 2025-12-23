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
	info, err := os.Stat(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to stat initiatives directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("initiatives should be a directory")
	}
}

func TestFixtureInitiativesDirectoryListing(t *testing.T) {
	entries, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives directory: %v", err)
	}

	// Should have at least one initiative (test-initiative)
	if len(entries) == 0 {
		t.Error("Expected at least one initiative in the initiatives directory")
	}
}

func TestFixtureInitiativeDirectoryContents(t *testing.T) {
	entries, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No initiatives to test")
	}

	// Check first initiative directory
	initDir := initiativePath(entries[0].Name())
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
	entries, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No initiatives to test")
	}

	// Read initiative.md
	initInfoPath := filepath.Join(initiativePath(entries[0].Name()), "initiative.md")
	content, err := os.ReadFile(initInfoPath)
	if err != nil {
		t.Fatalf("Failed to read initiative.md: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Check required fields
	requiredFields := []string{"id", "name", "slug", "status"}
	for _, field := range requiredFields {
		if _, ok := doc.Frontmatter[field]; !ok {
			t.Errorf("Missing required field %q in initiative.md", field)
		}
	}
}

// =============================================================================
// Labels Directory Tests
// =============================================================================

func TestFixtureLabelsDirectoryExists(t *testing.T) {
	info, err := os.Stat(labelsPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to stat labels directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("labels should be a directory")
	}
}

func TestFixtureLabelsDirectoryListing(t *testing.T) {
	entries, err := os.ReadDir(labelsPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read labels directory: %v", err)
	}

	// Should have labels (Bug, Feature, Documentation) + new.md
	if len(entries) < 3 {
		t.Errorf("Expected at least 3 labels, got %d", len(entries))
	}
}

func TestFixtureLabelFileContents(t *testing.T) {
	entries, err := os.ReadDir(labelsPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read labels directory: %v", err)
	}

	testedCount := 0
	for _, entry := range entries {
		if entry.Name() == "new.md" {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		content, err := os.ReadFile(labelFilePath(testTeamKey, entry.Name()))
		if err != nil {
			t.Fatalf("Failed to read label file %s: %v", entry.Name(), err)
		}

		doc, err := parseFrontmatter(content)
		if err != nil {
			t.Fatalf("Failed to parse label frontmatter: %v", err)
		}

		// Check required fields
		if _, ok := doc.Frontmatter["id"]; !ok {
			t.Errorf("Label %s missing id field", entry.Name())
		}
		if _, ok := doc.Frontmatter["name"]; !ok {
			t.Errorf("Label %s missing name field", entry.Name())
		}
		if _, ok := doc.Frontmatter["color"]; !ok {
			t.Errorf("Label %s missing color field", entry.Name())
		}

		testedCount++
	}

	if testedCount == 0 {
		t.Skip("No label files found to test")
	}
}

// =============================================================================
// Filter Views Tests (by/status, by/assignee, by/label)
// =============================================================================

func TestFixtureByAssigneeDirectoryExists(t *testing.T) {
	info, err := os.Stat(byAssigneePath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to stat by/assignee directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("by/assignee should be a directory")
	}
}

func TestFixtureByAssigneeListing(t *testing.T) {
	entries, err := os.ReadDir(byAssigneePath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read by/assignee directory: %v", err)
	}

	// Should have assignee directories
	if len(entries) == 0 {
		t.Error("Expected assignee directories in by/assignee")
	}
}

func TestFixtureByLabelDirectoryExists(t *testing.T) {
	info, err := os.Stat(byLabelPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to stat by/label directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("by/label should be a directory")
	}
}

func TestFixtureByLabelListing(t *testing.T) {
	entries, err := os.ReadDir(byLabelPath(testTeamKey))
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
	bugPath := filepath.Join(byLabelPath(testTeamKey), "Bug")
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
	unassignedPath := filepath.Join(byAssigneePath(testTeamKey), "unassigned")
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
	unassignedPath := filepath.Join(byAssigneePath(testTeamKey), "unassigned")
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
	info, err := os.Stat(searchPath(testTeamKey))
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

func TestFixtureByStatusBacklogContainsIssues(t *testing.T) {
	backlogPath := byStatusPath(testTeamKey, "Backlog")
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
	inProgressPath := byStatusPath(testTeamKey, "In Progress")
	entries, err := os.ReadDir(inProgressPath)
	if err != nil {
		t.Fatalf("Failed to read In Progress directory: %v", err)
	}

	testedCount := 0
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
		testedCount++
	}

	if testedCount == 0 {
		t.Error("No symlinks found to test")
	}
}

// =============================================================================
// My/ Personal Views Tests
// =============================================================================

func TestFixtureMyDirectoryExists(t *testing.T) {
	info, err := os.Stat(myPath())
	if err != nil {
		t.Fatalf("Failed to stat my directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("my should be a directory")
	}
}

func TestFixtureMyDirectoryContents(t *testing.T) {
	entries, err := os.ReadDir(myPath())
	if err != nil {
		t.Fatalf("Failed to read my directory: %v", err)
	}

	expectedDirs := map[string]bool{
		"assigned": false,
		"created":  false,
		"active":   false,
	}

	for _, entry := range entries {
		if _, ok := expectedDirs[entry.Name()]; ok {
			expectedDirs[entry.Name()] = true
		}
	}

	for dir, found := range expectedDirs {
		if !found {
			t.Errorf("Expected my/%s directory", dir)
		}
	}
}

func TestFixtureMyCreatedDirectoryExists(t *testing.T) {
	info, err := os.Stat(myCreatedPath())
	if err != nil {
		t.Fatalf("Failed to stat my/created directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("my/created should be a directory")
	}
}

func TestFixtureMyActiveDirectoryExists(t *testing.T) {
	info, err := os.Stat(myActivePath())
	if err != nil {
		t.Fatalf("Failed to stat my/active directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("my/active should be a directory")
	}
}

func TestFixtureMyViewsContainSearchDirectory(t *testing.T) {
	// Each my/ subview should have a search directory
	paths := []string{myAssignedPath(), myCreatedPath(), myActivePath()}

	for _, path := range paths {
		searchDir := filepath.Join(path, "search")
		info, err := os.Stat(searchDir)
		if err != nil {
			t.Errorf("my/ view should have search directory: %v", err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("search in %s should be a directory", path)
		}
	}
}

// =============================================================================
// Users Directory Tests
// =============================================================================

func TestFixtureUsersDirectoryExists(t *testing.T) {
	info, err := os.Stat(usersPath())
	if err != nil {
		t.Fatalf("Failed to stat users directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("users should be a directory")
	}
}

func TestFixtureUsersDirectoryListing(t *testing.T) {
	entries, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Failed to read users directory: %v", err)
	}

	// Should have at least one user (test-user from fixtures)
	if len(entries) == 0 {
		t.Error("Expected at least one user in users directory")
	}
}

func TestFixtureUserDirectoryContents(t *testing.T) {
	entries, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Failed to read users directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No users to test")
	}

	// Check first user directory
	userDir := userPath(entries[0].Name())
	userEntries, err := os.ReadDir(userDir)
	if err != nil {
		t.Fatalf("Failed to read user directory: %v", err)
	}

	// Should have user.md
	hasUserMd := false
	for _, e := range userEntries {
		if e.Name() == "user.md" {
			hasUserMd = true
			break
		}
	}
	if !hasUserMd {
		t.Error("User directory should contain user.md")
	}
}

func TestFixtureUserInfoFile(t *testing.T) {
	entries, err := os.ReadDir(usersPath())
	if err != nil {
		t.Fatalf("Failed to read users directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No users to test")
	}

	// Read user.md
	userInfoPath := filepath.Join(userPath(entries[0].Name()), "user.md")
	content, err := os.ReadFile(userInfoPath)
	if err != nil {
		t.Fatalf("Failed to read user.md: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Check required fields
	requiredFields := []string{"id", "name", "email", "status"}
	for _, field := range requiredFields {
		if _, ok := doc.Frontmatter[field]; !ok {
			t.Errorf("Missing required field %q in user.md", field)
		}
	}
}

// =============================================================================
// Search Directory Tests
// =============================================================================

func TestFixtureSearchDirectoryIsEmpty(t *testing.T) {
	entries, err := os.ReadDir(searchPath(testTeamKey))
	if err != nil {
		t.Fatalf("Failed to read search directory: %v", err)
	}

	// Search directory should be empty - queries are created on-demand via lookup
	if len(entries) != 0 {
		t.Error("Search directory should be empty")
	}
}

func TestFixtureSearchQueryLookup(t *testing.T) {
	// Looking up a query directory should create it
	queryPath := filepath.Join(searchPath(testTeamKey), "test")
	info, err := os.Stat(queryPath)
	if err != nil {
		t.Fatalf("Failed to stat search query directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("Search query lookup should return a directory")
	}
}

func TestFixtureSearchQueryContainsMatchingIssues(t *testing.T) {
	// Search for "issue" which should match TST-1 ("This is test issue 1")
	queryPath := filepath.Join(searchPath(testTeamKey), "issue")
	entries, err := os.ReadDir(queryPath)
	if err != nil {
		t.Fatalf("Failed to read search results: %v", err)
	}

	hasMatch := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "TST-") {
			hasMatch = true
			break
		}
	}
	if !hasMatch {
		t.Error("Search for 'issue' should find at least one issue")
	}
}

func TestFixtureSearchMultiWordQuery(t *testing.T) {
	// Multi-word queries use + as separator
	queryPath := filepath.Join(searchPath(testTeamKey), "test+issue")
	info, err := os.Stat(queryPath)
	if err != nil {
		t.Fatalf("Failed to stat multi-word search query: %v", err)
	}
	if !info.IsDir() {
		t.Error("Multi-word search query should return a directory")
	}
}

// =============================================================================
// Cycle File Content Tests
// =============================================================================

func TestFixtureCycleInfoFile(t *testing.T) {
	cyclesPath := filepath.Join(teamPath(testTeamKey), "cycles")
	entries, err := os.ReadDir(cyclesPath)
	if err != nil {
		t.Fatalf("Failed to read cycles directory: %v", err)
	}

	// Find a cycle directory (not symlinks like "current")
	var cycleDirName string
	for _, entry := range entries {
		if entry.IsDir() {
			cycleDirName = entry.Name()
			break
		}
	}

	if cycleDirName == "" {
		t.Skip("No cycle directory found")
	}

	// Read cycle.md
	cycleInfoPath := filepath.Join(cyclesPath, cycleDirName, "cycle.md")
	content, err := os.ReadFile(cycleInfoPath)
	if err != nil {
		t.Fatalf("Failed to read cycle.md: %v", err)
	}

	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("Failed to parse frontmatter: %v", err)
	}

	// Check required fields
	requiredFields := []string{"id", "number", "name", "team", "startsAt", "endsAt", "status"}
	for _, field := range requiredFields {
		if _, ok := doc.Frontmatter[field]; !ok {
			t.Errorf("Missing required field %q in cycle.md", field)
		}
	}

	// Check progress section
	if _, ok := doc.Frontmatter["progress"]; !ok {
		t.Error("cycle.md should have progress section")
	}
}

// =============================================================================
// Project Updates Directory Tests
// =============================================================================

func TestFixtureProjectUpdatesDirectoryExists(t *testing.T) {
	projectPath := filepath.Join(projectsPath(testTeamKey), "test-project")
	updatesPath := filepath.Join(projectPath, "updates")
	info, err := os.Stat(updatesPath)
	if err != nil {
		t.Fatalf("Failed to stat updates directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("updates should be a directory")
	}
}

func TestFixtureProjectUpdatesHasNewMd(t *testing.T) {
	projectPath := filepath.Join(projectsPath(testTeamKey), "test-project")
	newMdPath := filepath.Join(projectPath, "updates", "new.md")
	_, err := os.Stat(newMdPath)
	if err != nil {
		t.Errorf("updates/new.md should exist: %v", err)
	}
}

// =============================================================================
// Initiative Updates Directory Tests
// =============================================================================

func TestFixtureInitiativeUpdatesDirectoryExists(t *testing.T) {
	entries, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No initiatives to test")
	}

	updatesPath := filepath.Join(initiativePath(entries[0].Name()), "updates")
	info, err := os.Stat(updatesPath)
	if err != nil {
		t.Fatalf("Failed to stat updates directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("updates should be a directory")
	}
}

func TestFixtureInitiativeUpdatesHasNewMd(t *testing.T) {
	entries, err := os.ReadDir(initiativesPath())
	if err != nil {
		t.Fatalf("Failed to read initiatives directory: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No initiatives to test")
	}

	newMdPath := filepath.Join(initiativePath(entries[0].Name()), "updates", "new.md")
	_, err = os.Stat(newMdPath)
	if err != nil {
		t.Errorf("updates/new.md should exist: %v", err)
	}
}

// =============================================================================
// Filter Search Directory Tests
// =============================================================================

func TestFixtureByStatusSearchExists(t *testing.T) {
	inProgressPath := byStatusPath(testTeamKey, "In Progress")
	searchDir := filepath.Join(inProgressPath, "search")
	info, err := os.Stat(searchDir)
	if err != nil {
		t.Fatalf("Failed to stat by/status/In Progress/search: %v", err)
	}
	if !info.IsDir() {
		t.Error("search should be a directory")
	}
}

func TestFixtureSearchResultSymlinksAreValid(t *testing.T) {
	// Search for "issue" which should match our test issues
	queryPath := filepath.Join(searchPath(testTeamKey), "issue")
	entries, err := os.ReadDir(queryPath)
	if err != nil {
		t.Fatalf("Failed to read search results: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No search results found")
	}

	// Each entry should be a symlink
	for _, entry := range entries {
		symlinkPath := filepath.Join(queryPath, entry.Name())
		info, err := os.Lstat(symlinkPath)
		if err != nil {
			t.Errorf("Failed to lstat %s: %v", entry.Name(), err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Search result %s should be a symlink", entry.Name())
		}
	}
}

func TestFixtureSearchResultSymlinkTarget(t *testing.T) {
	// Search for "issue" and check symlink target
	queryPath := filepath.Join(searchPath(testTeamKey), "issue")
	entries, err := os.ReadDir(queryPath)
	if err != nil {
		t.Fatalf("Failed to read search results: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No search results found")
	}

	// Read the symlink target
	symlinkPath := filepath.Join(queryPath, entries[0].Name())
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("Failed to read symlink: %v", err)
	}

	// Target should point to ../../issues/{identifier}
	if !strings.HasPrefix(target, "../../issues/") {
		t.Errorf("Symlink target = %q, should start with '../../issues/'", target)
	}
}

func TestFixtureSearchResultSymlinkResolves(t *testing.T) {
	// Search for "issue" and follow the symlink
	queryPath := filepath.Join(searchPath(testTeamKey), "issue")
	entries, err := os.ReadDir(queryPath)
	if err != nil {
		t.Fatalf("Failed to read search results: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No search results found")
	}

	// Follow the symlink - os.Stat follows symlinks
	symlinkPath := filepath.Join(queryPath, entries[0].Name())
	info, err := os.Stat(symlinkPath)
	if err != nil {
		t.Fatalf("Failed to follow symlink: %v", err)
	}

	// The resolved path should be a directory (issue directory)
	if !info.IsDir() {
		t.Error("Resolved symlink should point to a directory")
	}
}

func TestFixtureSearchResultIssueContent(t *testing.T) {
	// Search, follow symlink, and read issue.md
	queryPath := filepath.Join(searchPath(testTeamKey), "issue")
	entries, err := os.ReadDir(queryPath)
	if err != nil {
		t.Fatalf("Failed to read search results: %v", err)
	}

	if len(entries) == 0 {
		t.Skip("No search results found")
	}

	// Follow the symlink and read issue.md
	symlinkPath := filepath.Join(queryPath, entries[0].Name())
	issueMdPath := filepath.Join(symlinkPath, "issue.md")
	content, err := os.ReadFile(issueMdPath)
	if err != nil {
		t.Fatalf("Failed to read issue.md via search result: %v", err)
	}

	if len(content) == 0 {
		t.Error("Issue content should not be empty")
	}
}

func TestFixtureScopedSearchQueryLookup(t *testing.T) {
	// Test scoped search within a filtered view
	inProgressPath := byStatusPath(testTeamKey, "In Progress")
	searchDir := filepath.Join(inProgressPath, "search", "test")
	info, err := os.Stat(searchDir)
	if err != nil {
		t.Fatalf("Failed to stat scoped search query: %v", err)
	}
	if !info.IsDir() {
		t.Error("Scoped search query should return a directory")
	}
}

func TestFixtureScopedSearchResults(t *testing.T) {
	// Test scoped search results within a filtered view
	inProgressPath := byStatusPath(testTeamKey, "In Progress")
	searchDir := filepath.Join(inProgressPath, "search", "test")
	entries, err := os.ReadDir(searchDir)
	if err != nil {
		t.Fatalf("Failed to read scoped search results: %v", err)
	}

	// Results should be symlinks
	for _, entry := range entries {
		symlinkPath := filepath.Join(searchDir, entry.Name())
		info, err := os.Lstat(symlinkPath)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Scoped search result %s should be a symlink", entry.Name())
		}
	}
}

func TestFixtureMyAssignedSearchExists(t *testing.T) {
	// Test search in my/assigned view
	searchDir := filepath.Join(mountPoint, "my", "assigned", "search")
	info, err := os.Stat(searchDir)
	if err != nil {
		t.Fatalf("Failed to stat my/assigned/search: %v", err)
	}
	if !info.IsDir() {
		t.Error("my/assigned/search should be a directory")
	}
}

func TestFixtureMyAssignedSearchQuery(t *testing.T) {
	// Test search query in my/assigned view
	queryPath := filepath.Join(mountPoint, "my", "assigned", "search", "test")
	info, err := os.Stat(queryPath)
	if err != nil {
		t.Fatalf("Failed to stat my/assigned/search query: %v", err)
	}
	if !info.IsDir() {
		t.Error("my/assigned/search query should return a directory")
	}
}
