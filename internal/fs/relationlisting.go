package fs

import (
	"fmt"
	"strings"

	"github.com/jra3/linear-fuse/internal/api"
	"gopkg.in/yaml.v3"
)

// relationListing owns the `{type}-{ID}.rel` filenames of the relations
// directory — the direction-aware sibling of namedListing (labels/docs/
// milestones), indexedListing (comments/updates), and attachmentListing.
// The directory projects one relation table in two directions: outgoing
// relations name themselves from the relation type and the related issue
// ("blocks-ENG-123.rel"), inverse relations from the inverted type and the
// source issue ("blocked-by-ENG-456.rel"). Both Readdir and Lookup derive
// names through this one module over one canonical order (outgoing first,
// then inverse — the repo's order within each), so a file you can `ls` you
// can also open and `rm` — before this, each surface restated the derivation
// and the direction split independently (four sites), and the file had no
// test twin.
//
// Collisions are first-match, emit-once — namedListing's recorded policy, for
// its recorded reason: the .rel name is a resolution key (`rm` must delete
// exactly what find matched), so a disambiguated "blocks-ENG-123 (2).rel"
// would mint a name that resolves nowhere. entries() implements the emit-once
// (first wins) and find() replays entries(), so readdir and find agree by
// construction. Entries with a nil issue or empty identifier are skipped —
// they would derive a broken name.
//
// Ordering is the repo's job, never this module's: the caller fetches both
// direction slices and passes them; the module is pure and unit-tested on
// literals.
type relationListing struct {
	outgoing []api.IssueRelation
	inverse  []api.IssueRelation
}

// relationEntry is one derived directory entry: the final name, the relation
// it resolves to, and which direction derived it (the render and Unlink
// policies differ by direction).
type relationEntry struct {
	relation  api.IssueRelation
	name      string
	isInverse bool
}

// relationFileName derives a relation file's name. The create surface reuses
// it for its .last path and kernel-entry name, so the format string exists
// exactly once.
func relationFileName(relType, identifier string) string {
	// relType is a controlled enum (blocks/related/…) and identifier is a
	// server-assigned issue identifier (TEAM-NNN) — neither can carry a path
	// separator or control char, so this .rel name (which also feeds rm) needs no
	// safeName pass. safename:ok structured id
	return fmt.Sprintf("%s-%s.rel", relType, identifier)
}

// inverseRelationType returns the inverse relation type name
func inverseRelationType(relType string) string {
	switch relType {
	case "blocks":
		return "blocked-by"
	case "duplicate":
		return "duplicated-by"
	case "related":
		return "related-to"
	case "similar":
		return "similar-to"
	default:
		return relType + "-inverse"
	}
}

// entries is the Readdir projection: outgoing relations first, then inverse,
// each name emitted once (first wins), skipping entries whose issue is nil or
// unidentified.
func (l relationListing) entries() []relationEntry {
	result := make([]relationEntry, 0, len(l.outgoing)+len(l.inverse))
	seen := make(map[string]struct{}, len(l.outgoing)+len(l.inverse))
	emit := func(rel api.IssueRelation, name string, isInverse bool) {
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		result = append(result, relationEntry{relation: rel, name: name, isInverse: isInverse})
	}
	for _, rel := range l.outgoing {
		if rel.RelatedIssue == nil || rel.RelatedIssue.Identifier == "" {
			continue
		}
		emit(rel, relationFileName(rel.Type, rel.RelatedIssue.Identifier), false)
	}
	for _, rel := range l.inverse {
		if rel.Issue == nil || rel.Issue.Identifier == "" {
			continue
		}
		emit(rel, relationFileName(inverseRelationType(rel.Type), rel.Issue.Identifier), true)
	}
	return result
}

// find replays the same derivation and returns the entry whose name matches —
// the Lookup projection. Every name entries() emits resolves here by
// construction.
func (l relationListing) find(name string) (relationEntry, bool) {
	for _, e := range l.entries() {
		if e.name == name {
			return e, true
		}
	}
	return relationEntry{}, false
}

// relationContent renders a relation file's YAML body — a plain YAML document
// with no frontmatter delimiters (the .rel format has never had them). The
// encoder marshals struct fields in declaration order, so output is
// byte-identical to the old hand-Sprintf for plain values, and quotes where
// YAML demands — a title containing a colon used to render INVALID YAML (the
// .rel twin of the catalog-render fix).
func relationContent(rel api.IssueRelation, isInverse bool) string {
	var v any
	switch {
	case isInverse && rel.Issue != nil:
		v = struct {
			Type  string `yaml:"type"`
			From  string `yaml:"from"`
			Title string `yaml:"title,omitempty"`
		}{rel.Type, rel.Issue.Identifier, rel.Issue.Title}
	case rel.RelatedIssue != nil:
		v = struct {
			Type  string `yaml:"type"`
			To    string `yaml:"to"`
			Title string `yaml:"title,omitempty"`
		}{rel.Type, rel.RelatedIssue.Identifier, rel.RelatedIssue.Title}
	default:
		v = struct {
			Type string `yaml:"type"`
		}{rel.Type}
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		// Structs of plain strings cannot fail to marshal; degrade to the
		// bare type line rather than an empty file.
		return "type: " + rel.Type + "\n"
	}
	return string(out)
}

// parseRelationInput parses the relations _create command syntax:
// "<type> <ISSUE-ID>", or a bare "<ISSUE-ID>" defaulting the type to
// "related". Pure syntax only — the identifier→issue resolution stays with
// the caller, which owns the repo. (This lives in fs, not marshal: marshal's
// seam is markdown↔entity, and a one-line command for a write-only trigger
// is not a markdown document.)
func parseRelationInput(content string) (relType, identifier string, err error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", "", &FieldError{Field: "content", Message: `empty content. Write "<type> <ISSUE-ID>", e.g. "blocks ENG-123".`}
	}
	parts := strings.Fields(content)
	if len(parts) == 1 {
		// Just identifier, default to "related"
		relType, identifier = "related", parts[0]
	} else {
		relType, identifier = parts[0], parts[1]
	}

	validTypes := map[string]bool{"blocks": true, "duplicate": true, "related": true, "similar": true}
	if !validTypes[relType] {
		return "", "", &FieldError{Field: "type", Value: relType, Message: "invalid relation type. Use one of: blocks, duplicate, related, similar."}
	}
	return relType, identifier, nil
}
