package fs

import (
	"strings"
	"syscall"
	"testing"
)

// scalarEdit is pure — it works on an already-parsed name/body plus the
// current name/description, with no FUSE mount, SQLite, or API (the extraction
// and name coercion live in marshal.MarkdownToProjectEdit/InitiativeEdit).
// These tests pin the change decision and the divergence classification.

func TestScalarEditDetectsBothFields(t *testing.T) {
	e := newScalarEdit("New Name", "New body", "Old Name", "Old body")
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
	e := newScalarEdit("Same", "Same body", "Same", "Same body")
	if e.changed() {
		t.Errorf("changed() = true, want false (name=%v desc=%v)", e.name, e.desc)
	}
}

func TestScalarEditTrailingNewlineIsNoOp(t *testing.T) {
	// The load-bearing trim: a render/parse trailing-newline delta must not read
	// as an edit, or every no-op save would rewrite the description.
	e := newScalarEdit("Same", "Body text\n", "Same", "Body text")
	if e.desc != nil {
		t.Errorf("desc = %q, want nil (trailing newline should be a no-op)", *e.desc)
	}
}

func TestScalarEditEmptyNameLeavesItAlone(t *testing.T) {
	// An empty name (no name key, or one that coerced to ""): name stays unset.
	e := newScalarEdit("", "new body", "Keep", "old body")
	if e.name != nil {
		t.Errorf("name = %v, want nil for an empty name", e.name)
	}
	if e.desc == nil {
		t.Error("desc should still update from the body")
	}
}

func TestScalarEditClearsDescription(t *testing.T) {
	// Emptying the body diffs as an empty-string send — the diff is honest about
	// what the writer asked for, and the mutation goes out. Whether it took is
	// decided afterwards, from what persisted (see the declined-clear test).
	e := newScalarEdit("Same", "   ", "Same", "had content")
	if e.desc == nil || *e.desc != "" {
		t.Errorf("desc = %v, want pointer to empty string (cleared)", e.desc)
	}
}

// TestScalarEditClearsBody pins the shape #398 turns on: which edits are a
// "clear the body" at all. Only those get the special verdict when the server
// declines them; everything else keeps the generic divergence classification.
func TestScalarEditClearsBody(t *testing.T) {
	cases := []struct {
		name             string
		body, currentDoc string
		want             bool
	}{
		{"emptying a body with content", "", "had content", true},
		{"whitespace-only over content", "   \n", "had content", true},
		{"already empty stays a no-op", "", "", false},
		{"already whitespace stays a no-op", "", "  \n", false},
		{"replacing content is fine", "new text", "had content", false},
		{"unchanged body is fine", "same", "same", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newScalarEdit("Same", tc.body, "Same", tc.currentDoc)
			if got := e.clearsBody(); got != tc.want {
				t.Errorf("clearsBody() = %v, want %v (body=%q current=%q, desc=%v)",
					got, tc.want, tc.body, tc.currentDoc, e.desc)
			}
		})
	}
}

// TestClearBodyMessageIsActionable: the verdict is only worth having if it tells
// the caller the thing they cannot discover themselves — that no re-save of the
// same bytes will ever work, and what to write instead.
func TestClearBodyMessageIsActionable(t *testing.T) {
	msg := clearBodyMessage("project")
	for _, want := range []string{"content (body)", "kept the previous body", "Re-saving the same empty body", "replace the body"} {
		if !strings.Contains(msg, want) {
			t.Errorf("clear-body .error does not mention %q:\n%s", want, msg)
		}
	}
}

// TestScalarEditDeclinedClearIsEINVALNotEIO is the classification #398 turns on:
// the SAME edit gets two different verdicts depending on what the server did with
// it. Declined (the body survived) → EINVAL, because retrying is futile. Applied
// (the body really is gone) → clean success, no divergence at all. Deciding from
// the persisted value rather than from a belief about Linear is what makes the
// second case work without anyone editing this code.
func TestScalarEditDeclinedClearIsEINVALNotEIO(t *testing.T) {
	e := newScalarEdit("Same", "", "Same", "the previous body")

	declined := e.divergences("project", "Same", "the previous body")
	if len(declined) != 1 || !declined[0].fatal {
		t.Fatalf("declined clear = %+v, want one fatal result", declined)
	}
	if declined[0].errno != syscall.EINVAL {
		t.Errorf("declined clear errno = %v, want EINVAL (EIO would tell the caller to retry a write that can never stick)", declined[0].errno)
	}
	if !strings.Contains(declined[0].message, "kept the previous body") {
		t.Errorf("declined clear message is the generic silent-revert text, not the #398 explanation:\n%s", declined[0].message)
	}

	applied := e.divergences("project", "Same", "")
	if len(applied) != 1 || applied[0].message != "" || applied[0].fatal {
		t.Errorf("applied clear = %+v, want a clean result — the body really was emptied", applied)
	}
}

// TestWriteBackErrorCarriesFatalErrno: the errno override has to survive the
// combine step, and a plain fatal result must still leave it zero so
// commitWriteBack falls back to EIO.
func TestWriteBackErrorCarriesFatalErrno(t *testing.T) {
	_, fatal, errno := writeBackError(writeBackResult{message: "m", fatal: true, errno: syscall.EINVAL})
	if !fatal || errno != syscall.EINVAL {
		t.Errorf("fatal=%v errno=%v, want true/EINVAL", fatal, errno)
	}
	_, fatal, errno = writeBackError(writeBackResult{message: "m", fatal: true})
	if !fatal || errno != 0 {
		t.Errorf("plain fatal: fatal=%v errno=%v, want true/0 (caller falls back to EIO)", fatal, errno)
	}
	_, fatal, errno = writeBackError(writeBackResult{message: "note", fatal: false, errno: syscall.EINVAL})
	if fatal || errno != 0 {
		t.Errorf("non-fatal note: fatal=%v errno=%v, want false/0 — errno is ignored unless fatal", fatal, errno)
	}
}

func TestScalarEditDivergencesOnlyChangedFields(t *testing.T) {
	// Only the fields that were sent are checked — an untouched field can't
	// diverge. Here only the name changed.
	e := newScalarEdit("Sent Name", "Same body", "Old Name", "Same body")
	results := e.divergences("project", "Sent Name", "Same body")
	if len(results) != 1 {
		t.Fatalf("divergences = %d results, want 1 (only name changed)", len(results))
	}
	if results[0].message != "" || results[0].fatal {
		t.Errorf("faithful name write should be a clean result, got %+v", results[0])
	}
}

func TestScalarEditDivergencesFlagsSilentRevert(t *testing.T) {
	// Sent a new name, but the fresh value reverted to the original: fatal.
	e := newScalarEdit("Sent Name", "b", "Old Name", "b")
	results := e.divergences("project", "Old Name", "b") // fresh reverted to original
	if len(results) != 1 || !results[0].fatal {
		t.Fatalf("expected 1 fatal divergence for a silent revert, got %+v", results)
	}
}
