package fs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// removalRejected is the verdict for a directory node whose entries must NOT be
// removable through the filesystem. go-fuse dispatches Unlink/Rmdir to the PARENT
// directory's ops and REPORTS SUCCESS when the parent implements neither
// NodeUnlinker nor NodeRmdirer — so a missing handler silently no-ops the rm/rmdir
// (errno 0, empty .error) while the row/file resurrects on the next readdir
// (#286/#287, the twin of the #282 listingDir fix).
//
// These surfaces are uniformly non-removable through the filesystem: status
// updates and symlink views (whose deletion has a documented owner — editing the
// parent's markdown), the _create/.error/.last control files, read-only metadata
// (team.md, README.md, states.md), and an entity's structural sub-directories
// (comments/, docs/, milestones/, updates/, …). The honest answer is a loud
// refusal, not a fabricated success — so every such node returns EPERM. The name
// argument is unused: the whole surface is uniformly non-removable.
func removalRejected() syscall.Errno { return syscall.EPERM }

// Unlink guards — rm of an entry these directory nodes list must fail loud, not
// silently succeed (#286/#287).
var (
	_ fs.NodeUnlinker = (*ChildrenNode)(nil)
	_ fs.NodeUnlinker = (*IssuesNode)(nil)
	_ fs.NodeUnlinker = (*UpdatesNode)(nil)
	_ fs.NodeUnlinker = (*InitiativeUpdatesNode)(nil)
	_ fs.NodeUnlinker = (*InitiativeProjectsNode)(nil)
	_ fs.NodeUnlinker = (*ProjectsNode)(nil)
	_ fs.NodeUnlinker = (*TeamNode)(nil)
	_ fs.NodeUnlinker = (*RootNode)(nil)
)

func (*ChildrenNode) Unlink(context.Context, string) syscall.Errno          { return removalRejected() }
func (*IssuesNode) Unlink(context.Context, string) syscall.Errno            { return removalRejected() }
func (*UpdatesNode) Unlink(context.Context, string) syscall.Errno           { return removalRejected() }
func (*InitiativeUpdatesNode) Unlink(context.Context, string) syscall.Errno { return removalRejected() }
func (*InitiativeProjectsNode) Unlink(context.Context, string) syscall.Errno {
	return removalRejected()
}
func (*ProjectsNode) Unlink(context.Context, string) syscall.Errno { return removalRejected() }
func (*TeamNode) Unlink(context.Context, string) syscall.Errno     { return removalRejected() }
func (*RootNode) Unlink(context.Context, string) syscall.Errno     { return removalRejected() }

// Rmdir guards — rmdir of an entity's structural sub-directory, or of an
// initiative, must fail loud, not silently succeed (#287).
var (
	_ fs.NodeRmdirer = (*IssueDirectoryNode)(nil)
	_ fs.NodeRmdirer = (*ProjectNode)(nil)
	_ fs.NodeRmdirer = (*InitiativeNode)(nil)
	_ fs.NodeRmdirer = (*InitiativesNode)(nil)
)

func (*IssueDirectoryNode) Rmdir(context.Context, string) syscall.Errno { return removalRejected() }
func (*ProjectNode) Rmdir(context.Context, string) syscall.Errno        { return removalRejected() }
func (*InitiativeNode) Rmdir(context.Context, string) syscall.Errno     { return removalRejected() }
func (*InitiativesNode) Rmdir(context.Context, string) syscall.Errno    { return removalRejected() }
