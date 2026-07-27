package integration

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAssigneeUnassignedNotShadowed is the #363 receipt (TB1 residual, pairs
// with #332/#333): the assembled, mount-level proof that a team member whose
// DisplayName is literally "unassigned" cannot hijack the synthetic
// by/assignee/unassigned bucket.
//
// The unit layer already covers the escape itself (safeName maps the exact
// collision to "unassigned-<id>", and internal/fs/safename_test.go drives the
// assigneeHandle builder through the hostile corpus). What only the mount
// exercises is that the two buckets stay distinct end to end: the assignee-less
// issues route through GetUnassignedIssues into unassigned/, while the hostile
// member's issues route through resolveAssigneeID into the escaped
// unassigned-user-shadow/.
//
// Fixture data lives in integration_test.go's populateTestFixtures: user-shadow
// (DisplayName "unassigned"), TST-90 assigned to it, TST-91 genuinely
// unassigned.
func TestAssigneeUnassignedNotShadowed(t *testing.T) {
	if liveAPIMode {
		t.Skip("fixture-mode: asserts the seeded hostile 'unassigned' member")
	}

	const (
		escapedBucket   = "unassigned-user-shadow" // safeName("unassigned", "user-shadow")
		hostileIssue    = "TST-90"                 // assigned to the hostile member
		unassignedIssue = "TST-91"                 // genuinely assignee-less
	)

	// Both buckets exist side by side: the escape produced a distinct directory
	// rather than collapsing onto the reserved "unassigned" literal.
	buckets := dirNames(t, byAssigneePath(testTeamKey))
	if !buckets["unassigned"] {
		t.Errorf("by/assignee missing the synthetic unassigned bucket (got %v)", buckets)
	}
	if !buckets[escapedBucket] {
		t.Errorf("by/assignee missing the escaped %q bucket for the hostile member (got %v)", escapedBucket, buckets)
	}

	// The synthetic unassigned/ bucket lists the assignee-less issue and is NOT
	// shadowed by the hostile member's issue.
	unassigned := dirNames(t, filepath.Join(byAssigneePath(testTeamKey), "unassigned"))
	if !unassigned[unassignedIssue] {
		t.Errorf("by/assignee/unassigned missing genuinely-unassigned %s (got %v)", unassignedIssue, unassigned)
	}
	if unassigned[hostileIssue] {
		t.Errorf("by/assignee/unassigned leaked hostile-assigned %s — the synthetic bucket IS shadowed (got %v)", hostileIssue, unassigned)
	}

	// The escaped bucket lists the hostile member's issue and not the
	// assignee-less one — the two do not bleed into each other.
	escaped := dirNames(t, filepath.Join(byAssigneePath(testTeamKey), escapedBucket))
	if !escaped[hostileIssue] {
		t.Errorf("by/assignee/%s missing hostile-assigned %s (got %v)", escapedBucket, hostileIssue, escaped)
	}
	if escaped[unassignedIssue] {
		t.Errorf("by/assignee/%s leaked unassigned %s (got %v)", escapedBucket, unassignedIssue, escaped)
	}

	// The hostile member's issue is reachable through the escaped bucket symlink
	// and correctly attributed (issue.md's assignee renders the member's email).
	content, err := os.ReadFile(filepath.Join(byAssigneePath(testTeamKey), escapedBucket, hostileIssue, "issue.md"))
	if err != nil {
		t.Fatalf("read %s via escaped bucket: %v", hostileIssue, err)
	}
	doc, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parse %s frontmatter: %v", hostileIssue, err)
	}
	if got, _ := doc.Frontmatter["assignee"].(string); got != "shadow@example.com" {
		t.Errorf("%s assignee = %q, want the hostile member's email shadow@example.com", hostileIssue, got)
	}
}
