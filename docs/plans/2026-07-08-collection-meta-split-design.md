# Collection .meta split — round-18 candidate 2 (grilling-locked)

Candidate 2 from the 2026-07-08 round-18 review. Grilling-locked with John 2026-07-08.

## The hazard

The four small editable entities render server-managed fields into their
EDITABLE files, and their parses silently ignore edits to them — a silent
no-op with no `.error`, violating the documented failure model ("an edit that
fails or appears to no-op is explained at the sibling `.error`") and the
editable-only split issue/project/initiative follow:

| entity | read-only fields leaked into editable frontmatter | render seam today |
|---|---|---|
| documents | id, url, created, updated, creator, slug | marshal |
| comments | ALL frontmatter (id, created, updated, edited, author, authorName) | hand-rolled in fs/comments.go (bypasses the marshal seam) |
| milestones | id | marshal |
| labels | id (plus a generated BODY that re-prints name/color/ID) | fs, via renderWithFrontmatter |

Comment flush (`extractCommentBody`) discards frontmatter wholesale — any
frontmatter edit is silently dropped.

## Locked decisions

- **Full .meta split, all four entities** (user's call: extend the established
  issue/project/initiative pattern; a changed-value guard and a drop-the-fields
  option were considered and rejected). Editable `.md` carries editable fields
  only; a read-only `.meta` sidecar (0444, renderFile machinery, same as
  issue.meta) carries the server-managed fields. The mistake becomes
  unrepresentable instead of punished.
- Comment `.md` becomes **pure body** (no frontmatter at all). The lenient
  strip-leading-frontmatter-on-write behavior stays (an agent pasting
  old-format content must not break).
- Accepted costs, eyes open: collection listings double (a `.meta` per item);
  comment authorship moves out of the read path into the sidecar (file
  mtime/ctime still carry the times).
- Every listed `.md` has a listed, openable `.meta` and vice versa — the
  listed⇔openable guarantee extends to the sidecars, pinned by round-trip
  tests.
- Renders join the **marshal seam** (CONTEXT "Entity render"): comments' and
  labels' renders move from fs into marshal, with Meta variants for all four.
  Exact frontmatter key-set tests pin editable vs meta key sets (the
  project/initiative precedent).
- Labels: frontmatter {name, color, description} (all editable); the
  generated decorative body (which re-printed ID) moves its facts to
  label `.meta`; the builder inspects `parseLabelMarkdown` and preserves the
  parse contract for the body.
- README (`generateReadme`) + `TestGeneratedReadmeMatchesBehavior` updated in
  the same change — the split alters documented file shapes.
- New ino wrappers per sidecar kind, registered in
  `TestInodeNamespaceDistinct`.
