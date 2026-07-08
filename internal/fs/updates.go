package fs

import (
	"context"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// lookupUpdateFile serves a read-only status-update file (project or initiative)
// through renderFile — rendered fresh on each read. Both update collections
// share this; they differ only in the ino they key on and the api type they
// carry, so the fields are passed positionally (the two update structs share no
// interface). Collapses the render-closure + lookupRenderFile pairing the two
// Lookups hand-rolled identically.
func (b *BaseNode) lookupUpdateFile(ctx context.Context, out *fuse.EntryOut, name, id, health string, created, updated time.Time, user *api.User, body string, ino uint64) *fs.Inode {
	render := func(context.Context) ([]byte, time.Time, time.Time) {
		return updateMarkdown(id, health, created, updated, user, body), updated, created
	}
	return b.lookupRenderFile(ctx, out, name, render, ino, 30*time.Second)
}

// updateMarkdown renders a status update (project or initiative) as
// YAML-frontmatter markdown. The two update collections share this exact format
// — they differ only in the api type they carry — so both pass their fields in
// here rather than each hand-rolling the identical writer. The read-only update
// files are served through renderFile with a closure over this. Its naming
// sibling is updateEntryName (indexedlisting.go). Frontmatter goes through
// renderWithFrontmatter so a hostile author name stays valid YAML.
func updateMarkdown(id, health string, created, updated time.Time, user *api.User, body string) []byte {
	fm := map[string]any{
		"id":      id,
		"health":  health,
		"created": created.Format(time.RFC3339),
		"updated": updated.Format(time.RFC3339),
	}
	if user != nil {
		fm["author"] = user.Email
		fm["authorName"] = user.Name
	}
	return renderWithFrontmatter(fm, "\n"+body+"\n")
}
