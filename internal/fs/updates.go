package fs

import (
	"bytes"
	"fmt"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// updateMarkdown renders a status update (project or initiative) as
// YAML-frontmatter markdown. The two update collections share this exact format
// — they differ only in the api type they carry — so both pass their fields in
// here rather than each hand-rolling the identical writer. The read-only update
// files are served through renderFile with a closure over this. Its naming
// sibling is updateEntryName (indexedlisting.go).
func updateMarkdown(id, health string, created, updated time.Time, user *api.User, body string) []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.WriteString(fmt.Sprintf("id: %s\n", id))
	buf.WriteString(fmt.Sprintf("health: %s\n", health))
	buf.WriteString(fmt.Sprintf("created: %q\n", created.Format(time.RFC3339)))
	buf.WriteString(fmt.Sprintf("updated: %q\n", updated.Format(time.RFC3339)))
	if user != nil {
		buf.WriteString(fmt.Sprintf("author: %s\n", user.Email))
		buf.WriteString(fmt.Sprintf("authorName: %s\n", user.Name))
	}
	buf.WriteString("---\n\n")
	buf.WriteString(body)
	buf.WriteString("\n")
	return buf.Bytes()
}
