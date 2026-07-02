package fs

import (
	"strings"
	"testing"
)

func TestNormalizeMarkdown(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b string
		want bool // whether normalized forms are equal
	}{
		{"trailing newline", "hello", "hello\n", true},
		{"surrounding whitespace", "  hello  ", "hello", true},
		{"bullet marker flip", "* one\n* two", "- one\n- two", true},
		{"mixed bullet markers", "+ one\n* two", "- one\n- two", true},
		{"trailing per-line whitespace", "line one   \nline two\t", "line one\nline two", true},
		{"blank-line run collapse", "a\n\n\n\nb", "a\n\nb", true},
		{"CRLF vs LF", "a\r\nb", "a\nb", true},
		{"internal whitespace is significant", "hello world", "hello  world", false},
		{"different text", "hello", "goodbye", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeMarkdown(c.a) == normalizeMarkdown(c.b)
			if got != c.want {
				t.Errorf("normalizeMarkdown(%q)==normalizeMarkdown(%q) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestWriteBackDivergence_Faithful(t *testing.T) {
	t.Parallel()
	// Persisted exactly what we wrote (modulo trailing newline) → no divergence.
	if r := writeBackDivergence("description (body)", "new body", "new body\n", "old body"); r.message != "" {
		t.Errorf("expected no divergence on faithful persist, got: %q", r.message)
	}
	// Linear flipping bullet markers is a benign reformat, not a divergence.
	if r := writeBackDivergence("description (body)", "* a\n* b", "- a\n- b", "old"); r.message != "" {
		t.Errorf("expected bullet reflow to be faithful, got: %q", r.message)
	}
}

func TestWriteBackDivergence_SilentRevert(t *testing.T) {
	t.Parallel()
	// We asked to change the body, the API accepted, but the stored value is
	// still the previous content — the #136 silent revert. Always fatal.
	r := writeBackDivergence("description (body)", "a much larger new body", "old body", "old body")
	if r.message == "" {
		t.Fatal("expected a divergence for a silent revert, got none")
	}
	if !r.fatal {
		t.Error("a silent revert must be fatal")
	}
	if !strings.Contains(r.message, "reverted to its previous content") {
		t.Errorf("expected silent-revert wording, got: %q", r.message)
	}
	if !strings.Contains(r.message, "Field: description (body)") {
		t.Errorf("expected field name in message, got: %q", r.message)
	}
}

func TestWriteBackDivergence_Truncation(t *testing.T) {
	t.Parallel()
	// Persisted value is substantially shorter than what we wrote, and matches
	// neither the written nor the previous value → real truncation, fatal.
	want := strings.Repeat("content line\n", 20) // ~260 chars
	got := "content line\n"                      // almost everything dropped
	r := writeBackDivergence("description (body)", want, got, "totally different previous")
	if r.message == "" {
		t.Fatal("expected a divergence for a truncated persist, got none")
	}
	if !r.fatal {
		t.Error("substantial truncation must be fatal")
	}
	if strings.Contains(r.message, "reverted to its previous content") {
		t.Errorf("truncation should not be reported as a revert, got: %q", r.message)
	}
	if !strings.Contains(r.message, "truncated server-side") {
		t.Errorf("expected truncation wording, got: %q", r.message)
	}
}

func TestWriteBackDivergence_BenignReformat(t *testing.T) {
	t.Parallel()
	// A small markup rewrite that survives normalization (e.g. Linear mangling
	// bold across a newline) is reported as a non-fatal note, not an EIO: the
	// text is intact and the close must succeed (#146).
	want := "intro text **without checking out\nthat branch** more text"
	got := "intro text **without checking out****\n****that branch** more text"
	r := writeBackDivergence("description (body)", want, got, "old body")
	if r.message == "" {
		t.Fatal("expected a note for a server-side markup rewrite, got none")
	}
	if r.fatal {
		t.Error("a benign markup reformat must not be fatal")
	}
	if !strings.Contains(r.message, "reformatted the markdown server-side") {
		t.Errorf("expected reformat wording, got: %q", r.message)
	}
}

func TestWriteBackError_Aggregation(t *testing.T) {
	t.Parallel()
	// All faithful → empty, non-fatal.
	if msg, fatal := writeBackError(writeBackResult{}, writeBackResult{}); msg != "" || fatal {
		t.Errorf("all-faithful should be empty/non-fatal, got msg=%q fatal=%v", msg, fatal)
	}

	// A note alone → non-fatal, "note" header.
	note := writeBackResult{message: "Field: x\nNote: reformatted", fatal: false}
	msg, fatal := writeBackError(note)
	if fatal {
		t.Error("a note alone must not be fatal")
	}
	if !strings.Contains(msg, "Read-your-writes note") {
		t.Errorf("expected note header, got: %q", msg)
	}

	// Any fatal field makes the whole outcome fatal with the violation header.
	bad := writeBackResult{message: "Field: y\nError: reverted", fatal: true}
	msg, fatal = writeBackError(note, bad)
	if !fatal {
		t.Error("any fatal field must make the outcome fatal")
	}
	if !strings.Contains(msg, "Read-your-writes violation") {
		t.Errorf("expected violation header, got: %q", msg)
	}
	if !strings.Contains(msg, "reformatted") || !strings.Contains(msg, "reverted") {
		t.Errorf("expected both field messages joined, got: %q", msg)
	}
}
