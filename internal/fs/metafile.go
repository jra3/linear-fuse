package fs

import (
	"context"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// The read-only `<entity>.meta` sidecar.
//
// Under the "editable in, server-managed out" write contract (#150), each
// editable file (issue.md, project.md, initiative.md) carries only the fields a
// writer may set. The server-managed, write-volatile fields (id, url, updated,
// slug, branch, timestamps, links, relations, …) live here instead, so a
// successful write to the editable file never rewrites the bytes the writer
// wrote — the staleness/"modified since read" churn goes away.
//
// `.meta` is a plain renderFile: it holds a render closure (not baked bytes) and
// renders current content on demand. This is load-bearing: go-fuse dedups inodes
// by StableAttr.Ino, so a second Lookup for the same entity returns the
// *original* node and discards any freshly-constructed one — baking bytes or
// times at Lookup would serve stale content for the life of the mount. Rendering
// on demand from a live source (the repo) means the served node always reflects
// current state. Editors of the sibling editable file call
// InvalidateUpdated(metaIno(id)) so the kernel drops its cached size/attrs and
// re-reads. Times are rendered through for the same reason the bytes are: a baked
// mtime would freeze at first-Lookup forever — breaking the `mtime=updatedAt`
// contract for the very file that exposes `updated:`.

// lookupMetaFile mounts a read-only `.meta` virtual file backed by a render
// closure as a child of parent. render is called on demand (Lookup/Read/Getattr)
// and must return the current meta bytes and times from a live source. Timeouts
// are zero (like .error/.last) so the file never serves a stale cached size.
func (lfs *LinearFS) lookupMetaFile(ctx context.Context, parent fs.InodeEmbedder, name, key string, render renderFunc, out *fuse.EntryOut) *fs.Inode {
	return lfs.mountRenderFile(ctx, parent, name, render, metaIno(key), 0, out)
}
