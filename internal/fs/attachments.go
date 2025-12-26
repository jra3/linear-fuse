package fs

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// attachmentsDirIno generates a stable inode for an issue's attachments directory
func attachmentsDirIno(issueID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("attachments:" + issueID))
	return h.Sum64()
}

// embeddedFileIno generates a stable inode for an embedded file
func embeddedFileIno(fileID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte("file:" + fileID))
	return h.Sum64()
}

// AttachmentsNode represents the /teams/{KEY}/issues/{ID}/attachments directory
type AttachmentsNode struct {
	BaseNode
	issueID string
}

var _ fs.NodeReaddirer = (*AttachmentsNode)(nil)
var _ fs.NodeLookuper = (*AttachmentsNode)(nil)
var _ fs.NodeGetattrer = (*AttachmentsNode)(nil)

func (n *AttachmentsNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0755 | syscall.S_IFDIR
	n.SetOwner(out)
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *AttachmentsNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	files, err := n.lfs.GetIssueEmbeddedFiles(ctx, n.issueID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Deduplicate filenames by appending (2), (3), etc. for duplicates
	nameCount := make(map[string]int)
	entries := make([]fuse.DirEntry, len(files))
	for i, file := range files {
		name := deduplicateFilename(file.Filename, nameCount)
		entries[i] = fuse.DirEntry{
			Name: name,
			Mode: syscall.S_IFREG,
		}
	}

	return fs.NewListDirStream(entries), 0
}

func (n *AttachmentsNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	files, err := n.lfs.GetIssueEmbeddedFiles(ctx, n.issueID)
	if err != nil {
		return nil, syscall.EIO
	}

	// Build deduplicated name mapping (same logic as Readdir)
	nameCount := make(map[string]int)
	fileByDeduplicatedName := make(map[string]api.EmbeddedFile)
	for _, file := range files {
		deduped := deduplicateFilename(file.Filename, nameCount)
		fileByDeduplicatedName[deduped] = file
	}

	file, ok := fileByDeduplicatedName[name]
	if !ok {
		return nil, syscall.ENOENT
	}

	node := &EmbeddedFileNode{
		BaseNode: BaseNode{lfs: n.lfs},
		file:     file,
	}

	// Set initial attributes
	out.Attr.Mode = 0444 | syscall.S_IFREG
	out.Attr.Uid = n.lfs.uid
	out.Attr.Gid = n.lfs.gid
	if file.FileSize > 0 {
		out.Attr.Size = uint64(file.FileSize)
	} else {
		out.Attr.Size = 1024 * 1024 // Placeholder for lazy-fetch
	}
	out.SetAttrTimeout(30 * time.Second)
	out.SetEntryTimeout(30 * time.Second)

	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  embeddedFileIno(file.ID),
	}), 0
}

// EmbeddedFileNode represents a file in the /attachments/ directory
// Files are lazily fetched from Linear's CDN on first read
type EmbeddedFileNode struct {
	BaseNode
	file api.EmbeddedFile
}

var _ fs.NodeGetattrer = (*EmbeddedFileNode)(nil)
var _ fs.NodeOpener = (*EmbeddedFileNode)(nil)
var _ fs.NodeReader = (*EmbeddedFileNode)(nil)

func (n *EmbeddedFileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	now := time.Now()
	out.Mode = 0444 // Read-only
	n.SetOwner(out)
	if n.file.FileSize > 0 {
		out.Size = uint64(n.file.FileSize)
	} else {
		// Report a placeholder size so tools will attempt to read the file.
		// Lazy-fetch happens during Read(), which will return actual content.
		// Use 1MB as a reasonable placeholder for images.
		out.Size = 1024 * 1024
	}
	out.SetTimes(&now, &now, &now)
	return 0
}

func (n *EmbeddedFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Don't use kernel caching since file might be lazily downloaded
	return nil, 0, 0
}

func (n *EmbeddedFileNode) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// Lazy fetch: download file from Linear CDN if not cached
	content, err := n.lfs.FetchEmbeddedFile(ctx, n.file)
	if err != nil {
		return nil, syscall.EIO
	}

	if off >= int64(len(content)) {
		return fuse.ReadResultData(nil), 0
	}

	end := off + int64(len(dest))
	if end > int64(len(content)) {
		end = int64(len(content))
	}

	return fuse.ReadResultData(content[off:end]), 0
}

// deduplicateFilename returns a unique filename by appending (2), (3), etc. for duplicates.
// The nameCount map tracks how many times each base name has been seen.
func deduplicateFilename(name string, nameCount map[string]int) string {
	nameCount[name]++
	count := nameCount[name]
	if count == 1 {
		return name
	}

	// Insert counter before extension: image.png -> image (2).png
	ext := ""
	base := name
	if dot := strings.LastIndex(name, "."); dot > 0 {
		ext = name[dot:]
		base = name[:dot]
	}
	return fmt.Sprintf("%s (%d)%s", base, count, ext)
}
