package fs

import (
	"fmt"
	"regexp"
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
//
// Linear also reformats markdown server-side on store (#146): it flips unordered
// list markers (`*`/`+` ↔ `-`), trims trailing per-line whitespace, collapses
// blank-line runs, and occasionally mangles markup like bold spanning a newline.
// These are not data loss, so they must not surface as a hard EIO — otherwise a
// clean save reads as a failure and triggers retries/escalation. We therefore
// (1) normalize the known-benign transforms before comparing, and (2) classify
// any residual difference: a silent revert or substantial shrinkage is fatal
// (real loss), while a minor markup rewrite is reported as a non-fatal note.

var (
	// bulletRe matches an unordered-list bullet (`*`, `+`, or `-`) and the space
	// after it, capturing the leading indent. Linear flips between markers.
	bulletRe = regexp.MustCompile(`(?m)^(\s*)[*+-][ \t]+`)
	// blankRunRe matches a run of 3+ newlines (2+ blank lines), which markdown
	// renders identically to a single blank line.
	blankRunRe = regexp.MustCompile(`\n{3,}`)
)

// normalizeMarkdown canonicalizes the server-side markdown transforms Linear
// applies on store so a faithful save doesn't read as a divergence. It is
// deliberately conservative: every transform here preserves the text, so it can
// never hide truncation or a revert.
func normalizeMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, " \t")     // Linear strips trailing whitespace
		line = bulletRe.ReplaceAllString(line, "$1- ") // canonicalize bullet markers
		lines[i] = line
	}
	s = strings.Join(lines, "\n")
	s = blankRunRe.ReplaceAllString(s, "\n\n") // collapse blank-line runs
	return strings.TrimSpace(s)
}

// writeBackResult is the outcome of a read-your-writes check on one field.
// message is empty when the value persisted faithfully. When non-empty, fatal
// reports whether the divergence is real data loss (surface as EIO) or a benign
// server-side reformat (record an informational note but let the write succeed).
type writeBackResult struct {
	message string
	fatal   bool
}

// writeBackDivergence classifies how a single free-text field persisted. want is
// what we sent, got is what the fresh fetch returned, prev is the value before
// the write.
func writeBackDivergence(field, want, got, prev string) writeBackResult {
	// Equal modulo Linear's known-benign markdown reformatting => faithful.
	if normalizeMarkdown(want) == normalizeMarkdown(got) {
		return writeBackResult{}
	}

	wantLen := len(strings.TrimSpace(want))
	gotLen := len(strings.TrimSpace(got))

	// Silent revert (#136): the persisted value matches the pre-write content —
	// the change was dropped entirely. Always fatal.
	if normalizeMarkdown(got) == normalizeMarkdown(prev) {
		return writeBackResult{
			message: fmt.Sprintf("Field: %s\nError: the write was accepted but the value reverted to its previous content (you wrote %d chars, %d persisted). This is a silent revert — re-read the file to see the stored value.", field, wantLen, gotLen),
			fatal:   true,
		}
	}

	// Substantial shrinkage signals truncation rather than reflow. Linear's
	// normalization nudges length by a handful of chars; a real truncation drops
	// a large fraction. Fatal when the persisted value lost more than ~10% of the
	// written length (and more than a few chars).
	if lost := wantLen - gotLen; lost > 16 && lost*10 > wantLen {
		return writeBackResult{
			message: fmt.Sprintf("Field: %s\nError: the persisted value is substantially shorter than what you wrote (you wrote %d chars, %d persisted — %d lost). The change may have been truncated server-side — re-read the file to see the stored value.", field, wantLen, gotLen, lost),
			fatal:   true,
		}
	}

	// Otherwise the text survived but Linear reformatted markup we don't
	// normalize (e.g. bold spanning a newline). The write succeeded; report a
	// non-fatal note rather than failing the close.
	return writeBackResult{
		message: fmt.Sprintf("Field: %s\nNote: saved, but Linear reformatted the markdown server-side (you wrote %d chars, %d persisted). The text is intact; re-read the file to see the stored formatting.", field, wantLen, gotLen),
		fatal:   false,
	}
}

// writeBackError combines per-field results into the .error payload and reports
// whether the overall outcome is fatal. Returns ("", false) when every field
// persisted faithfully.
func writeBackError(results ...writeBackResult) (string, bool) {
	var msgs []string
	fatal := false
	for _, r := range results {
		if r.message == "" {
			continue
		}
		msgs = append(msgs, r.message)
		if r.fatal {
			fatal = true
		}
	}
	if len(msgs) == 0 {
		return "", false
	}
	header := "Read-your-writes note: your write was accepted; Linear reformatted the markdown server-side (no content lost).\n"
	if fatal {
		header = "Read-your-writes violation: your write was accepted by Linear but did not persist as written.\n"
	}
	return header + strings.Join(msgs, "\n"), fatal
}

// writeBackKind returns a word for log lines describing a divergence outcome.
func writeBackKind(fatal bool) string {
	if fatal {
		return "violation"
	}
	return "note"
}
