package fs

import (
	"strings"
	"testing"
)

// createParseSeedCorpus feeds the two write-only _create command parsers
// (parseLinkInput, parseRelationInput). These parse arbitrary bytes a user
// writes into relations/_create, links/_create, or attachments/_create through
// the mount, so the contract is "structured result or clean error, never a
// crash." The corpus mixes realistic commands with adversarial whitespace,
// bare tokens, unicode, NUL, and an oversized input.
var createParseSeedCorpus = []string{
	"",
	" ",
	"   ",
	"\t\n ",
	"https://example.com",
	"https://example.com Label here",
	"https://example.com [Bracketed Label]",
	"blocks ENG-123",
	"related",
	"duplicate TEAM-1",
	"similar\tPROJ-9", // tab: Fields splits it (relations), SplitN(" ") does not (links)
	"blocks",          // a bare type name — becomes the identifier for relations
	"  leading spaces then url",
	"url\nwith\nnewlines",
	"a b c d e",
	"nospaceurl",
	"café ☃ 日本語",
	"has\x00nul url",
	strings.Repeat("x", 4096),
}

// FuzzParseLinkInput asserts parseLinkInput never panics and honors its
// contract: whitespace-only content yields empty url and label; any other
// content yields a non-empty url that is a single space-free token (the first
// field) and a non-empty label (the rest, or the url itself).
func FuzzParseLinkInput(f *testing.F) {
	for _, s := range createParseSeedCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, content string) {
		url, label := parseLinkInput(content)
		if strings.TrimSpace(content) == "" {
			if url != "" || label != "" {
				t.Fatalf("whitespace content %q must yield empty url/label, got url=%q label=%q", content, url, label)
			}
			return
		}
		if url == "" {
			t.Fatalf("non-empty content %q yielded an empty url", content)
		}
		if strings.Contains(url, " ") {
			t.Fatalf("url %q contains a space (must be the first space-delimited token)", url)
		}
		if label == "" {
			t.Fatalf("content %q yielded an empty label (must default to the url)", content)
		}
	})
}

// FuzzParseRelationInput asserts parseRelationInput never panics and honors its
// contract: it returns either a FieldError, or a valid relation type
// (blocks/duplicate/related/similar) with a non-empty identifier. A nil error
// paired with an invalid type or empty identifier is a broken contract.
func FuzzParseRelationInput(f *testing.F) {
	for _, s := range createParseSeedCorpus {
		f.Add(s)
	}
	validTypes := map[string]bool{"blocks": true, "duplicate": true, "related": true, "similar": true}
	f.Fuzz(func(t *testing.T, content string) {
		relType, identifier, err := parseRelationInput(content)
		if err != nil {
			return // a clean FieldError is a valid outcome
		}
		if !validTypes[relType] {
			t.Fatalf("content %q: nil error but invalid relation type %q", content, relType)
		}
		if identifier == "" {
			t.Fatalf("content %q: nil error but empty identifier", content)
		}
	})
}
