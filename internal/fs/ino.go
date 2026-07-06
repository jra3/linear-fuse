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
func issueDirIno(issueID string) uint64    { return ino("dir", issueID) }
func issuesDirIno(teamID string) uint64    { return ino("issues", teamID) }
func childrenDirIno(issueID string) uint64 { return ino("children", issueID) }
func historyIno(issueID string) uint64     { return ino("history", issueID) }
func errorIno(issueID string) uint64       { return ino("error", issueID) }

// Comments -----------------------------------------------------------------

func commentsDirIno(issueID string) uint64 { return ino("comments", issueID) }
func commentIno(commentID string) uint64   { return ino("comment", commentID) }

// Documents ----------------------------------------------------------------

func docsDirIno(parentID string) uint64 { return ino("docs", parentID) }
func documentIno(docID string) uint64   { return ino("doc", docID) }

// Attachments --------------------------------------------------------------

func attachmentsDirIno(issueID string) uint64          { return ino("attachments", issueID) }
func embeddedFileIno(fileID string) uint64             { return ino("file", fileID) }
func externalAttachmentIno(attachmentID string) uint64 { return ino("extatt", attachmentID) }

// Relations ----------------------------------------------------------------

func relationsDirIno(issueID string) uint64 { return ino("relations", issueID) }
func relationIno(relationID string) uint64  { return ino("relation", relationID) }

// Labels -------------------------------------------------------------------

func labelsDirIno(teamID string) uint64 { return ino("labels", teamID) }
func labelIno(labelID string) uint64    { return ino("label", labelID) }

// Projects -----------------------------------------------------------------

func projectsDirIno(teamID string) uint64    { return ino("projects", teamID) }
func projectInfoIno(projectID string) uint64 { return ino("project-info", projectID) }
func updatesDirIno(projectID string) uint64  { return ino("updates", projectID) }

// Milestones ---------------------------------------------------------------

func milestonesDirIno(projectID string) uint64 { return ino("milestones", projectID) }
func milestoneIno(milestoneID string) uint64   { return ino("milestone", milestoneID) }

// Initiatives --------------------------------------------------------------

func initiativeInfoIno(initiativeID string) uint64 { return ino("initiative-info", initiativeID) }
func initiativeProjectsIno(initiativeID string) uint64 {
	return ino("initiative-projects", initiativeID)
}
func initiativeUpdatesDirIno(initiativeID string) uint64 {
	return ino("initiative-updates", initiativeID)
}

// Team views ---------------------------------------------------------------

func recentDirIno(teamID string) uint64 { return ino("recentdir", teamID) }

// Sidecars -----------------------------------------------------------------

func metaIno(key string) uint64    { return ino("meta", key) }
func successIno(key string) uint64 { return ino("last", key) }
