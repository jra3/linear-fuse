package fs

import "hash/fnv"

// ino is the one hash behind every virtual inode number in the filesystem: a
// stable 64-bit value derived from an entity kind and its id. Every inode is
// namespaced by its kind prefix — there are no bare hashes — so two entities
// that happen to share an id (an issue and its comments directory, say) never
// collide. The wrapper list below IS the inode namespace: adding a virtual file
// means adding a wrapper here, never hashing inline. TestInodeNamespaceDistinct
// guards that every kind stays distinct. See CONTEXT.md "Inode namespace".
//
// scratchIno (atomicwrite.go) is deliberately not a wrapper: its key mixes the
// parent directory inode with the name, so it hashes differently.
func ino(kind, id string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(kind + ":" + id))
	return h.Sum64()
}

// Issue tree ---------------------------------------------------------------

func issueIno(issueID string) uint64       { return ino("issue", issueID) }
func issueDirIno(issueID string) uint64    { return ino("issuedir", issueID) }
func issuesDirIno(teamID string) uint64    { return ino("issues", teamID) }
func childrenDirIno(issueID string) uint64 { return ino("children", issueID) }
func historyIno(issueID string) uint64     { return ino("history", issueID) }
func errorIno(issueID string) uint64       { return ino("error", issueID) }

// Comments -----------------------------------------------------------------

func commentsDirIno(issueID string) uint64 { return ino("comments", issueID) }
func commentIno(commentID string) uint64   { return ino("comment", commentID) }
func commentMetaIno(commentID string) uint64 {
	return ino("comment-meta", commentID)
}

// Documents ----------------------------------------------------------------

func docsDirIno(parentID string) uint64   { return ino("docs", parentID) }
func documentIno(docID string) uint64     { return ino("doc", docID) }
func documentMetaIno(docID string) uint64 { return ino("doc-meta", docID) }

// Attachments --------------------------------------------------------------

func attachmentsDirIno(issueID string) uint64          { return ino("attachments", issueID) }
func embeddedFileIno(fileID string) uint64             { return ino("file", fileID) }
func externalAttachmentIno(attachmentID string) uint64 { return ino("extatt", attachmentID) }

// External links (project/initiative "Links / Resources") ------------------

func linksDirIno(parentID string) uint64   { return ino("links", parentID) }
func externalLinkIno(linkID string) uint64 { return ino("extlink", linkID) }

// Relations ----------------------------------------------------------------

func relationsDirIno(issueID string) uint64 { return ino("relations", issueID) }
func relationIno(relationID string) uint64  { return ino("relation", relationID) }

// Labels -------------------------------------------------------------------

func labelsDirIno(teamID string) uint64  { return ino("labels", teamID) }
func labelIno(labelID string) uint64     { return ino("label", labelID) }
func labelMetaIno(labelID string) uint64 { return ino("label-meta", labelID) }

// projectLabelsCatalogIno is the root project-labels.md catalog file — a
// workspace singleton, so the id is a constant.
func projectLabelsCatalogIno() uint64 { return ino("project-labels-catalog", "workspace") }

// Projects -----------------------------------------------------------------

func projectsDirIno(teamID string) uint64     { return ino("projects", teamID) }
func projectDirIno(projectID string) uint64   { return ino("projectdir", projectID) }
func projectInfoIno(projectID string) uint64  { return ino("project-info", projectID) }
func updatesDirIno(projectID string) uint64   { return ino("updates", projectID) }
func projectUpdateIno(updateID string) uint64 { return ino("project-update", updateID) }

// Milestones ---------------------------------------------------------------

func milestonesDirIno(projectID string) uint64 { return ino("milestones", projectID) }
func milestoneIno(milestoneID string) uint64   { return ino("milestone", milestoneID) }
func milestoneMetaIno(milestoneID string) uint64 {
	return ino("milestone-meta", milestoneID)
}

// Initiatives --------------------------------------------------------------

func initiativeDirIno(initiativeID string) uint64  { return ino("initiativedir", initiativeID) }
func initiativeInfoIno(initiativeID string) uint64 { return ino("initiative-info", initiativeID) }
func initiativeProjectsIno(initiativeID string) uint64 {
	return ino("initiative-projects", initiativeID)
}
func initiativeUpdatesDirIno(initiativeID string) uint64 {
	return ino("initiative-updates", initiativeID)
}
func initiativeUpdateIno(updateID string) uint64 { return ino("initiative-update", updateID) }

// Root views ----------------------------------------------------------------
// The stateless top-level containers (teams/, users/, my/, initiatives/) and
// the my/ subdirs are keyed by their fixed directory name — there is exactly
// one of each per mount.

func viewDirIno(name string) uint64 { return ino("viewdir", name) }
func myDirIno(name string) uint64   { return ino("mydir", name) }

// Team tree -----------------------------------------------------------------

func teamDirIno(teamID string) uint64   { return ino("teamdir", teamID) }
func cyclesDirIno(teamID string) uint64 { return ino("cyclesdir", teamID) }
func cycleDirIno(cycleID string) uint64 { return ino("cycledir", cycleID) }

// Filter views (by/) ----------------------------------------------------------
// Composite keys: a category dir is per team+category, a value dir per
// team+category+value. FUSE names never contain "/", so "/" is a safe joiner.

func byDirIno(teamID string) uint64 { return ino("bydir", teamID) }
func byCategoryIno(teamID, category string) uint64 {
	return ino("bycat", teamID+"/"+category)
}
func byValueIno(teamID, category, value string) uint64 {
	return ino("byval", teamID+"/"+category+"/"+value)
}

// Users ----------------------------------------------------------------------

func userDirIno(userID string) uint64 { return ino("userdir", userID) }

// Team views ---------------------------------------------------------------

func recentDirIno(teamID string) uint64 { return ino("recentdir", teamID) }

// Sidecars -----------------------------------------------------------------

func metaIno(key string) uint64    { return ino("meta", key) }
func successIno(key string) uint64 { return ino("last", key) }
