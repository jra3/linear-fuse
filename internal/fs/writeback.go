package fs

import (
	"fmt"
	"strings"
)

// Read-your-writes verification.
//
// After a write is accepted by the Linear API and the entity is re-fetched, we
// compare the free-text values we *sent* against what actually *persisted*. If
// a value did not stick — a silent revert (#136) or a server-side truncation —
// the write must not look successful: we surface the divergence via .error so an
// LLM/editor that verifies by re-reading sees a loud failure instead of a file
// that quietly reverted.
//
// Structured fields (state, assignee, labels, …) are not checked here: the API
// rejects bad values outright (handled by the resolve+EINVAL paths), so they
// cannot silently revert. Free-text fields (title, description/body) are the
// ones prone to silent loss, and they are what an agent most needs verified.

// textEquivalent reports whether two free-text values are the same modulo
// leading/trailing whitespace. Linear trims trailing whitespace on store, so a
// whitespace-only difference is not a divergence.
func textEquivalent(a, b string) bool {
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

// writeBackDivergence reports a read-your-writes violation for a single free-text
// field, or "" when the value persisted faithfully. want is what we sent, got is
// what the fresh fetch returned, prev is the value before the write.
//
// It distinguishes the two failure modes so the .error message is actionable:
//   - silent revert: got matches prev — the change was dropped entirely.
//   - lossy persist: got matches neither want nor prev — e.g. truncation or
//     server-side rewriting.
func writeBackDivergence(field, want, got, prev string) string {
	if textEquivalent(want, got) {
		return "" // persisted faithfully
	}

	wantLen := len(strings.TrimSpace(want))
	gotLen := len(strings.TrimSpace(got))

	if textEquivalent(got, prev) {
		return fmt.Sprintf("Field: %s\nError: the write was accepted but the value reverted to its previous content (you wrote %d chars, %d persisted). This is a silent revert — re-read the file to see the stored value.", field, wantLen, gotLen)
	}

	return fmt.Sprintf("Field: %s\nError: the persisted value differs from what you wrote (you wrote %d chars, %d persisted). The change may have been truncated or rewritten server-side — re-read the file to see the stored value.", field, wantLen, gotLen)
}
