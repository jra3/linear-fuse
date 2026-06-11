package fs

import (
	"strings"
	"testing"
)

func TestTextEquivalent(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"hello", "hello", true},
		{"hello", "hello\n", true},          // trailing newline trimmed
		{"  hello  ", "hello", true},        // surrounding whitespace trimmed
		{"hello world", "hello  world", false}, // internal whitespace is significant
		{"hello", "goodbye", false},
		{"", "", true},
		{"", "  \n ", true}, // both blank
	}
	for _, c := range cases {
		if got := textEquivalent(c.a, c.b); got != c.want {
			t.Errorf("textEquivalent(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestWriteBackDivergence_Faithful(t *testing.T) {
	// Persisted exactly what we wrote (modulo trailing newline) → no divergence.
	if d := writeBackDivergence("description (body)", "new body", "new body\n", "old body"); d != "" {
		t.Errorf("expected no divergence on faithful persist, got: %q", d)
	}
}

func TestWriteBackDivergence_SilentRevert(t *testing.T) {
	// We asked to change the body, the API accepted, but the stored value is
	// still the previous content — the #136 silent revert.
	d := writeBackDivergence("description (body)", "a much larger new body", "old body", "old body")
	if d == "" {
		t.Fatal("expected a divergence for a silent revert, got none")
	}
	if !strings.Contains(d, "reverted to its previous content") {
		t.Errorf("expected silent-revert wording, got: %q", d)
	}
	if !strings.Contains(d, "Field: description (body)") {
		t.Errorf("expected field name in message, got: %q", d)
	}
}

func TestWriteBackDivergence_LossyPersist(t *testing.T) {
	// Persisted value matches neither what we wrote nor the previous value
	// (e.g. truncation) → reported as a lossy persist, not a revert.
	d := writeBackDivergence("description (body)", "the full long body text here", "the full long", "totally different previous")
	if d == "" {
		t.Fatal("expected a divergence for a truncated persist, got none")
	}
	if strings.Contains(d, "reverted to its previous content") {
		t.Errorf("truncation should not be reported as a revert, got: %q", d)
	}
	if !strings.Contains(d, "truncated or rewritten") {
		t.Errorf("expected truncation wording, got: %q", d)
	}
}
