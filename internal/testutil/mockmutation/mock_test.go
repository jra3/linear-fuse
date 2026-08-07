package mockmutation

import (
	"context"
	"testing"

	"github.com/jra3/linear-fuse/internal/marshal"
)

// TestClearedEstimateRoundTripsAsNil pins the one thing a fake owes the code it
// stands in for: a clear must read back as the ABSENCE of a value.
//
// MarkdownToIssueUpdate spells a removed field as a present key with a nil
// value. A fake that coerced that nil to a zero — `iss.Estimate = &0` — would
// hand the next render a real zero-point estimate, the next diff would read that
// as an estimate worth clearing, and the clear would re-send itself on every
// later save. That non-convergence would be entirely the double's, which is the
// trap: it looks like a production bug and invites a fix in production code.
//
// Written against the marshal output rather than a hand-built map so the two
// stay coupled — if the clear's wire spelling ever changes, this test moves with
// it instead of quietly asserting a shape nothing sends.
func TestClearedEstimateRoundTripsAsNil(t *testing.T) {
	ctx := context.Background()
	c := New()
	const id = "issue-1"

	if err := c.UpdateIssue(ctx, id, map[string]any{
		"title": "T", "description": "body", "estimate": 5, "dueDate": "2026-01-31",
	}); err != nil {
		t.Fatalf("set estimate: %v", err)
	}
	set, err := c.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("read back the set estimate: %v", err)
	}
	if set.Estimate == nil || *set.Estimate != 5 {
		t.Fatalf("estimate did not round-trip as 5, got %v", set.Estimate)
	}
	if set.DueDate == nil || *set.DueDate != "2026-01-31" {
		t.Fatalf("dueDate did not round-trip, got %v", set.DueDate)
	}

	// The clear exactly as the write path spells it: no estimate/due key in the
	// document, against an issue that has both.
	update, err := marshal.MarkdownToIssueUpdate([]byte("---\ntitle: T\n---\nbody\n"), set)
	if err != nil {
		t.Fatalf("diff a document with no estimate: %v", err)
	}
	if got, present := update["estimate"]; !present || got != nil {
		t.Fatalf("the diff did not spell the estimate clear as a nil value, got %#v", update)
	}
	if got, present := update["dueDate"]; !present || got != nil {
		t.Fatalf("the diff did not spell the due-date clear as a nil value, got %#v", update)
	}
	if err := c.UpdateIssue(ctx, id, update); err != nil {
		t.Fatalf("apply the clear: %v", err)
	}

	cleared, err := c.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("read back the cleared estimate: %v", err)
	}
	if cleared.Estimate != nil {
		t.Errorf("a cleared estimate read back as %v, want nil — a fake that answers a clear "+
			"with a value makes the clear re-send itself forever", *cleared.Estimate)
	}
	if cleared.DueDate != nil {
		t.Errorf("a cleared due date read back as %q, want nil", *cleared.DueDate)
	}

	// Convergence is the point: rendering what the clear left and diffing it back
	// must find nothing to send.
	doc, err := marshal.IssueToMarkdown(cleared)
	if err != nil {
		t.Fatalf("render the cleared issue: %v", err)
	}
	again, err := marshal.MarkdownToIssueUpdate(doc, cleared)
	if err != nil {
		t.Fatalf("re-diff the cleared issue: %v", err)
	}
	if _, present := again["estimate"]; present {
		t.Errorf("a no-op re-save of the cleared issue re-sent %#v", again)
	}
}
