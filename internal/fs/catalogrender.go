package fs

import "github.com/jra3/linear-fuse/internal/marshal"

// renderWithFrontmatter renders a generated file through the marshal seam:
// frontmatter built as Go data (yaml.v3 encodes it), hand-built body appended
// verbatim. This replaces the fmt.Sprintf-concatenated YAML the catalog
// renderers used to emit, where a value like `Q3: Bets` (colon), a
// double-quoted name, or a `#hex` color landed unquoted/mis-quoted and the
// frontmatter stopped parsing as YAML — exactly the files agents machine-parse
// after a validation .error. The encoder quotes whatever the value requires;
// the prose/table BODY stays hand-built and is passed through untouched.
//
// Ordering: yaml.v3 sorts map keys deterministically (the same canonical order
// issue.md and the .meta sidecars already carry), so output is byte-stable
// across renders. Key ORDER inside the block is not part of any file's
// contract — top-level key NAMES are (`team:`, `states:`, `labels:`, …).
func renderWithFrontmatter(fm map[string]any, body string) []byte {
	out, err := marshal.Render(&marshal.Document{Frontmatter: fm, Body: body})
	if err != nil {
		// Maps of scalars/lists cannot fail to marshal; if one somehow does,
		// degrade to the body (these files promise to exist, never error).
		return []byte(body)
	}
	return out
}
