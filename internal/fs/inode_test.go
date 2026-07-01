package fs

import "testing"

// TestInoStableAndKeyed checks the primitive: same inputs → same inode, and the
// namespace actually participates (different namespace → different inode).
func TestInoStableAndKeyed(t *testing.T) {
	t.Parallel()
	if ino("a", "x") != ino("a", "x") {
		t.Error("ino is not stable for equal inputs")
	}
	if ino("a", "x") == ino("b", "x") {
		t.Error("namespace does not affect the inode")
	}
	if ino("a", "x") == ino("a", "y") {
		t.Error("id does not affect the inode")
	}
}

// TestInoNamespacesDistinct is the collision guard: every per-kind inode helper
// must map a shared id to a distinct inode. If two helpers ever pick the same
// namespace string, two different entities would share an inode and the kernel
// would confuse them — this test fails the moment that happens.
func TestInoNamespacesDistinct(t *testing.T) {
	t.Parallel()
	const id = "shared-id"
	inodes := map[string]uint64{
		"issueIno":                issueIno(id),
		"issuesDirIno":            issuesDirIno(id),
		"issueDirIno":             issueDirIno(id),
		"childrenDirIno":          childrenDirIno(id),
		"historyIno":              historyIno(id),
		"errorIno":                errorIno(id),
		"commentsDirIno":          commentsDirIno(id),
		"commentIno":              commentIno(id),
		"docsDirIno":              docsDirIno(id),
		"documentIno":             documentIno(id),
		"labelsDirIno":            labelsDirIno(id),
		"labelIno":                labelIno(id),
		"milestonesDirIno":        milestonesDirIno(id),
		"milestoneIno":            milestoneIno(id),
		"milestonesCreateIno":     milestonesCreateIno(id),
		"projectsDirIno":          projectsDirIno(id),
		"projectInfoIno":          projectInfoIno(id),
		"updatesDirIno":           updatesDirIno(id),
		"attachmentsDirIno":       attachmentsDirIno(id),
		"embeddedFileIno":         embeddedFileIno(id),
		"externalAttachmentIno":   externalAttachmentIno(id),
		"attachmentsCreateIno":    attachmentsCreateIno(id),
		"relationsDirIno":         relationsDirIno(id),
		"relationIno":             relationIno(id),
		"relationsCreateIno":      relationsCreateIno(id),
		"initiativeInfoIno":       initiativeInfoIno(id),
		"initiativeProjectsIno":   initiativeProjectsIno(id),
		"initiativeUpdatesDirIno": initiativeUpdatesDirIno(id),
	}

	seen := make(map[uint64]string, len(inodes))
	for name, v := range inodes {
		if other, dup := seen[v]; dup {
			t.Errorf("inode collision: %s and %s both hash id %q to %d", name, other, id, v)
		}
		seen[v] = name
	}
}
