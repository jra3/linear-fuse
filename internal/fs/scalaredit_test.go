package fs

import (
	"testing"

	"github.com/jra3/linear-fuse/internal/marshal"
)

// scalarEdit is pure — it works on a parsed marshal.Document plus the current
// name/description, with no FUSE mount, SQLite, or API. These tests pin the
// change decision, the name coercion (F2), and the divergence classification.

func doc(name string, body string) *marshal.Document {
	fm := map[string]any{}
	if name != "" {
		fm["name"] = name
	}
	return &marshal.Document{Frontmatter: fm, Body: body}
}

func TestScalarEditDetectsBothFields(t *testing.T) {
	e := newScalarEdit(doc("New Name", "New body"), "Old Name", "Old body")
	if !e.changed() {
		t.Fatal("changed() = false, want true")
	}
	if e.name == nil || *e.name != "New Name" {
		t.Errorf("name = %v, want New Name", e.name)
	}
	if e.desc == nil || *e.desc != "New body" {
		t.Errorf("desc = %v, want New body", e.desc)
	}
}

func TestScalarEditNoChange(t *testing.T) {
	e := newScalarEdit(doc("Same", "Same body"), "Same", "Same body")
	if e.changed() {
		t.Errorf("changed() = true, want false (name=%v desc=%v)", e.name, e.desc)
	}
}

func TestScalarEditTrailingNewlineIsNoOp(t *testing.T) {
	// The load-bearing trim: a render/parse trailing-newline delta must not read
	// as an edit, or every no-op save would rewrite the description.
	e := newScalarEdit(doc("Same", "Body text\n"), "Same", "Body text")
	if e.desc != nil {
		t.Errorf("desc = %q, want nil (trailing newline should be a no-op)", *e.desc)
	}
}

func TestScalarEditCoercesNumericName(t *testing.T) {
	// F2: a YAML numeric name coerces and updates, rather than being silently
	// dropped by a direct string type assertion.
	e := newScalarEdit(&marshal.Document{Frontmatter: map[string]any{"name": 123}, Body: "b"}, "Old", "b")
	if e.name == nil || *e.name != "123" {
		t.Errorf("name = %v, want coerced \"123\"", e.name)
	}
}

func TestScalarEditMissingNameLeavesItAlone(t *testing.T) {
	// No name key in frontmatter: name stays unset, no panic on the nil value.
	e := newScalarEdit(&marshal.Document{Frontmatter: map[string]any{}, Body: "new body"}, "Keep", "old body")
	if e.name != nil {
		t.Errorf("name = %v, want nil when frontmatter has no name", e.name)
	}
	if e.desc == nil {
		t.Error("desc should still update from the body")
	}
}

func TestScalarEditClearsDescription(t *testing.T) {
	// Emptying the body clears the description (sends an empty string).
	e := newScalarEdit(doc("Same", "   "), "Same", "had content")
	if e.desc == nil || *e.desc != "" {
		t.Errorf("desc = %v, want pointer to empty string (cleared)", e.desc)
	}
}

func TestScalarEditDivergencesOnlyChangedFields(t *testing.T) {
	// Only the fields that were sent are checked — an untouched field can't
	// diverge. Here only the name changed.
	e := newScalarEdit(doc("Sent Name", "Same body"), "Old Name", "Same body")
	results := e.divergences("Sent Name", "Same body")
	if len(results) != 1 {
		t.Fatalf("divergences = %d results, want 1 (only name changed)", len(results))
	}
	if results[0].message != "" || results[0].fatal {
		t.Errorf("faithful name write should be a clean result, got %+v", results[0])
	}
}

func TestScalarEditDivergencesFlagsSilentRevert(t *testing.T) {
	// Sent a new name, but the fresh value reverted to the original: fatal.
	e := newScalarEdit(doc("Sent Name", "b"), "Old Name", "b")
	results := e.divergences("Old Name", "b") // fresh reverted to original
	if len(results) != 1 || !results[0].fatal {
		t.Fatalf("expected 1 fatal divergence for a silent revert, got %+v", results)
	}
}
