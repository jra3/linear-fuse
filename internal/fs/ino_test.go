package fs

import "testing"

// TestInoStableAndKeyed pins the three properties every inode number relies on:
// it is stable for a given (kind, id), it varies with the id, and it varies with
// the kind — so an issue and its comments directory (same id, different kind)
// never share an inode.
func TestInoStableAndKeyed(t *testing.T) {
	t.Parallel()
	if ino("k", "a") != ino("k", "a") {
		t.Error("ino not stable for the same (kind, id)")
	}
	if ino("k", "a") == ino("k", "b") {
		t.Error("ino collides across ids")
	}
	if ino("k1", "x") == ino("k2", "x") {
		t.Error("ino collides across kinds for the same id")
	}
}

// TestInodeNamespaceDistinct is the namespace guard: every wrapper, given one
// shared id, must produce a distinct inode. It catches a duplicated or
// mistyped kind prefix (e.g. the one-character gap between "comment" and
// "comments"), confirms issueIno's prefix no longer collides with a bare hash,
// and is the checklist a newly added kind must join. scratchIno is excluded
// deliberately — its key is parent-scoped, not a kind:id pair.
func TestInodeNamespaceDistinct(t *testing.T) {
	t.Parallel()
	const id = "shared-id"
	namespace := map[string]uint64{
		"issueIno":                issueIno(id),
		"issueDirIno":             issueDirIno(id),
		"issuesDirIno":            issuesDirIno(id),
		"childrenDirIno":          childrenDirIno(id),
		"historyIno":              historyIno(id),
		"errorIno":                errorIno(id),
		"commentsDirIno":          commentsDirIno(id),
		"commentIno":              commentIno(id),
		"docsDirIno":              docsDirIno(id),
		"documentIno":             documentIno(id),
		"attachmentsDirIno":       attachmentsDirIno(id),
		"embeddedFileIno":         embeddedFileIno(id),
		"externalAttachmentIno":   externalAttachmentIno(id),
		"relationsDirIno":         relationsDirIno(id),
		"relationIno":             relationIno(id),
		"labelsDirIno":            labelsDirIno(id),
		"labelIno":                labelIno(id),
		"projectsDirIno":          projectsDirIno(id),
		"projectDirIno":           projectDirIno(id),
		"projectInfoIno":          projectInfoIno(id),
		"updatesDirIno":           updatesDirIno(id),
		"milestonesDirIno":        milestonesDirIno(id),
		"milestoneIno":            milestoneIno(id),
		"initiativeDirIno":        initiativeDirIno(id),
		"initiativeInfoIno":       initiativeInfoIno(id),
		"initiativeProjectsIno":   initiativeProjectsIno(id),
		"initiativeUpdatesDirIno": initiativeUpdatesDirIno(id),
		"recentDirIno":            recentDirIno(id),
		"metaIno":                 metaIno(id),
		"successIno":              successIno(id),
	}

	seen := make(map[uint64]string, len(namespace))
	for name, got := range namespace {
		if other, dup := seen[got]; dup {
			t.Errorf("inode collision: %s and %s both hash to %d for id %q", name, other, got, id)
		}
		seen[got] = name
	}
}
