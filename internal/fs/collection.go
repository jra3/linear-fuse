package fs

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// The writable-collection trio.
//
// Every writable collection directory serves the same three virtual files:
// the `_create` trigger, the `.error` feedback file, and the `.last` sidecar
// of recent creations. Which files a directory serves is a contract the
// generated README documents globally ("every writable directory has a .error
// feedback file") — but restating it in every Readdir and Lookup let surfaces
// drift: the updates directories shipped without .error/.last entirely until
// the contract was re-audited. collectionTrio owns the trio once, so a new
// collection can't forget it, the same way InvalidateCreated is guaranteed by
// the create tail.
//
// A directory declares its trio with a spec and delegates:
//
//	func (n *CommentsNode) trio() collectionTrio {
//		return collectionTrio{kind: "comments", parentID: n.issueID, onFlush: n.createComment}
//	}
//	// Readdir: entries := n.trio().entries(); append per-entity items…
//	// Lookup:  if inode, ok := n.lfs.lookupCollectionTrio(ctx, n, n.trio(), name, out); ok { return inode, 0 }
//
// Collections created by mkdir instead of _create (projects) set onFlush nil
// and serve only .error/.last.
type collectionTrio struct {
	// kind namespaces the .error/.last keys (collectionErrorKey/SuccessKey).
	kind string
	// parentID scopes the collection (issue, team, project, initiative ID).
	parentID string
	// onFlush is the create surface behind _create (see createFileNode).
	// nil means the collection has no _create trigger.
	onFlush func(ctx context.Context, content []byte) syscall.Errno
}

// entries returns the virtual-file header every writable collection's Readdir
// starts with.
func (t collectionTrio) entries() []fuse.DirEntry {
	entries := make([]fuse.DirEntry, 0, 3)
	if t.onFlush != nil {
		entries = append(entries, fuse.DirEntry{Name: "_create", Mode: syscall.S_IFREG})
	}
	return append(entries,
		fuse.DirEntry{Name: ".error", Mode: syscall.S_IFREG},
		fuse.DirEntry{Name: ".last", Mode: syscall.S_IFREG},
	)
}

// lookupCollectionTrio serves the trio names for one collection. It returns
// (inode, true) when name was one of them; (nil, false) otherwise, so the
// caller falls through to its per-entity lookup.
func (lfs *LinearFS) lookupCollectionTrio(ctx context.Context, parent fs.InodeEmbedder, t collectionTrio, name string, out *fuse.EntryOut) (*fs.Inode, bool) {
	switch name {
	case "_create":
		if t.onFlush == nil {
			return nil, false
		}
		now := time.Now()
		node := newCreateFile(lfs, t.onFlush)
		out.Attr.Mode = 0200 | syscall.S_IFREG
		out.Attr.Uid = lfs.uid
		out.Attr.Gid = lfs.gid
		out.Attr.Size = 0
		out.Attr.SetTimes(&now, &now, &now)
		out.SetAttrTimeout(1 * time.Second)
		out.SetEntryTimeout(1 * time.Second)
		return parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG}), true
	case ".error":
		return lfs.lookupErrorFile(ctx, parent, collectionErrorKey(t.kind, t.parentID), out), true
	case ".last":
		return lfs.lookupSuccessFile(ctx, parent, collectionSuccessKey(t.kind, t.parentID), out), true
	}
	return nil, false
}
