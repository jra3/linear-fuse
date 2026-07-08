package fs

import (
	"fmt"
	"strings"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// Project-label selection (see CONTEXT.md): the pure half of the workspace
// project-label surface — catalog rendering here, name→ID resolution and the
// selection policy in this file's siblings (write path). Everything in this
// file is unit-testable with a literal catalog slice: no mount, no interface.

// projectLabelsMarkdown renders the root project-labels.md catalog. The
// assignment rules live IN the file — it is what an agent reads after a
// validation .error — and the render is stable for an empty catalog (never
// ENOENT; the README promises the file exists).
func projectLabelsMarkdown(labels []api.ProjectLabel) []byte {
	var yaml, table strings.Builder
	for _, l := range labels {
		yaml.WriteString(fmt.Sprintf("  - id: %s\n    name: %s\n", l.ID, l.Name))
		if l.Color != "" {
			yaml.WriteString(fmt.Sprintf("    color: %q\n", l.Color))
		}
		if l.Description != "" {
			yaml.WriteString(fmt.Sprintf("    description: %q\n", l.Description))
		}
		if l.IsGroup {
			yaml.WriteString("    group: true\n")
		}
		if l.Parent != nil {
			parent := l.Parent.Name
			if parent == "" {
				parent = l.Parent.ID
			}
			yaml.WriteString(fmt.Sprintf("    parent: %s\n", parent))
		}
		if l.RetiredAt != nil {
			yaml.WriteString("    retired: true\n")
		}

		group := "—"
		if l.Parent != nil {
			group = l.Parent.Name
			if group == "" {
				group = l.Parent.ID
			}
		}
		color := "—"
		if l.Color != "" {
			color = l.Color
		}
		var flags []string
		if l.IsGroup {
			flags = append(flags, "group (assign a child)")
		}
		if l.RetiredAt != nil {
			flags = append(flags, "retired")
		}
		table.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			l.Name, group, color, strings.Join(flags, ", "), l.ID))
	}

	body := table.String()
	if len(labels) == 0 {
		body = "No project labels defined.\n"
	} else {
		body = `| Name | Group | Color | Flags | ID |
|------|-------|-------|-------|-----|
` + body
	}

	return []byte(fmt.Sprintf(`---
labels:
%s---

# Project Labels (workspace-wide)

Assign in any project.md frontmatter: `+"`labels: [Platform, Q3-Bet]`"+`
(Names are resolved case-insensitively; a raw label ID is also accepted.)

Rules:
- Labels marked `+"`group: true`"+` are containers and CANNOT be assigned; assign one
  of their children instead.
- At most ONE child from each group may be on a project at a time.
- Labels marked `+"`retired: true`"+` cannot be newly assigned; existing assignments remain.

%s`, yaml.String(), body))
}

// projectLabelCatalogTimes derives the catalog file's times from the entities
// themselves — mtime = newest UpdatedAt, ctime = oldest CreatedAt; zero when
// the catalog is empty (renderFile's never-fabricate-now() contract).
func projectLabelCatalogTimes(labels []api.ProjectLabel) (mtime, ctime time.Time) {
	for _, l := range labels {
		if l.UpdatedAt.After(mtime) {
			mtime = l.UpdatedAt
		}
		if !l.CreatedAt.IsZero() && (ctime.IsZero() || l.CreatedAt.Before(ctime)) {
			ctime = l.CreatedAt
		}
	}
	return mtime, ctime
}
