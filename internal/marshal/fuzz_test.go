package marshal

import (
	"bytes"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// seedCorpus is shared by the fuzz targets: realistic documents plus the
// adversarial inputs past bugs came in through (unquoted hex colors that YAML
// reads as comments, colons and delimiters inside values, unclosed frontmatter,
// bodies that themselves start with the delimiter, nested/typed YAML).
var seedCorpus = []string{
	"",
	"just a body, no frontmatter",
	"---\ntitle: Test\nstatus: In Progress\n---\nBody content",
	"---\ntitle: Fix bug\npriority: high\nlabels: [Bug, Backend]\nestimate: 3\n---\nDescription.",
	"---\nname: Critical\ncolor: '#FF0000'\n---\n",
	"---\ncolor: #FF0000\n---\n",                              // unquoted hex → YAML comment
	"---\ntitle: has: a colon in it\n---\nbody",               // colon inside a value
	"---\ntitle: value with --- inside\n---\nb",               // delimiter inside a value
	"---\nunclosed frontmatter\nno closing",                   // unclosed
	"---\n---\n---\nbody starting with delimiter",             // body starts with ---
	"---\nlabels:\n  - a\n  - b\n  - 2026\n---\n",             // numeric list element
	"---\nnested:\n  a: 1\n  b: {c: 2}\n---\nx",               // nested maps
	"---\ndue: 2026-02-01\nestimate: \"3\"\ncycle: 42\n---\n", // typed scalars
	"---\nhealth: atRisk\n---\nWe are blocked.",
	"---\ntitle: \"\"\nassignee:\n---\n", // empty / null values
	"---\n\n---\nbody",                   // empty frontmatter block
}

// FuzzParse asserts the frontmatter parser never panics and that its normalized
// form is stable: when a document carries frontmatter, Render(Parse(x)) must
// re-parse to the same body and render byte-identically a second time. A
// fixpoint violation is exactly the YAML round-trip corruption class (a value
// that renders one way and parses back another).
func FuzzParse(f *testing.F) {
	for _, s := range seedCorpus {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, content []byte) {
		doc, err := Parse(content)
		if err != nil {
			return // a clean rejection is a valid outcome; the contract is "no panic"
		}
		if doc == nil {
			t.Fatal("Parse returned nil doc with nil error")
		}

		rendered, err := Render(doc)
		if err != nil {
			t.Fatalf("Render of a successfully-parsed doc failed: %v", err)
		}

		// The meaningful round trip lives on documents that actually carry
		// frontmatter. With no frontmatter Render is the identity over the body,
		// so a body that happens to start with the delimiter is a separate
		// concern (the entity render paths always emit frontmatter).
		if len(doc.Frontmatter) == 0 {
			return
		}

		// Scope the round trip to the real frontmatter contract: string keys with
		// string or flat string-list values, which is exactly what every
		// marshal.Render caller emits (title/status/assignee/labels/id/color/…).
		// This targets the reachable corruption class — a *string* value that
		// renders one way and parses back another (a label named "Q3: Bets", a
		// value with colons, delimiters, quotes, newlines, or unicode; the kind
		// of naive-render bug CONTEXT.md calls out). It deliberately excludes
		// YAML's numeric-formatting corners that fuzzing surfaced but no path
		// reaches: a nested map with numeric keys collapses to a duplicate key
		// (int 0 and float 0.0 render identically), and float negative zero
		// (`-0`) re-parses as int 0 — neither shape is ever rendered from a
		// typed entity, whose numeric fields are small ints coerced before write.
		if !stringValuedFrontmatter(doc.Frontmatter) {
			return
		}

		doc2, err := Parse(rendered)
		if err != nil {
			t.Fatalf("Render output did not re-parse: %v\ninput=%q\nrendered=%q", err, content, rendered)
		}
		if doc2.Body != doc.Body {
			t.Fatalf("round-trip changed body:\n before=%q\n after =%q", doc.Body, doc2.Body)
		}
		if len(doc2.Frontmatter) != len(doc.Frontmatter) {
			t.Fatalf("round-trip changed frontmatter key count: %d -> %d\nrendered=%q",
				len(doc.Frontmatter), len(doc2.Frontmatter), rendered)
		}

		// Render must be a fixpoint: normalizing an already-normalized doc is a
		// no-op. Divergence means a value serializes unstably.
		rendered2, err := Render(doc2)
		if err != nil {
			t.Fatalf("second Render failed: %v", err)
		}
		if !bytes.Equal(rendered, rendered2) {
			t.Fatalf("Render is not a fixpoint:\n first =%q\n second=%q", rendered, rendered2)
		}
	})
}

// stringValuedFrontmatter reports whether every value is a string or a flat
// list of strings — the value shapes entity renders actually emit, and the
// shapes for which Parse/Render is a true fixpoint. Non-string scalars (numbers,
// bools, null) and nested containers are out of contract (see FuzzParse).
func stringValuedFrontmatter(fm map[string]any) bool {
	isStringy := func(v any) bool {
		switch t := v.(type) {
		case string:
			return true
		case []any:
			for _, e := range t {
				if _, ok := e.(string); !ok {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	for _, v := range fm {
		if !isStringy(v) {
			return false
		}
	}
	return true
}

// FuzzEntityParsers drives every markdown->entity parse entry point with the
// same hostile bytes. These parse untrusted file contents a user writes into
// the mount, so the contract is: return a value or a clean error, never crash.
// (Fuzz fails the input on any panic or hang.)
func FuzzEntityParsers(f *testing.F) {
	for _, s := range seedCorpus {
		f.Add([]byte(s))
	}
	// Non-nil zero-value originals for the diffing parsers.
	issue := &api.Issue{}
	label := &api.Label{}
	doc := &api.Document{}
	milestone := &api.ProjectMilestone{}

	f.Fuzz(func(t *testing.T, content []byte) {
		_, _ = MarkdownToIssueUpdate(content, issue)
		_, _ = MarkdownToIssueCreate(content)
		_, _ = MarkdownToLabelUpdate(content, label)
		_, _, _, _ = ParseNewLabel(content)
		_, _ = MarkdownToProjectEdit(content)
		_, _ = MarkdownToInitiativeEdit(content)
		_, _ = MarkdownToDocumentUpdate(content, doc)
		_, _, _ = ParseNewDocument(content)
		_, _ = MarkdownToMilestoneUpdate(content, milestone)
		_, _ = ParseNewMilestone(content)
		_, _, _ = MarkdownToStatusUpdate(content)
	})
}
