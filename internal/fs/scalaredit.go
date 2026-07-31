package fs

import (
	"strings"
	"syscall"
)

// scalarEdit is the diff of the two scalar fields both project.md and
// initiative.md expose for editing — a name (frontmatter) and the body, which
// maps to Linear's long `content` field (see #5). It owns the change decision
// (what counts as "changed") and the read-your-writes divergence
// classification, so the two handlers no longer each hand-roll a `fieldChanged`
// flag and a byte-identical commitWriteBack compare closure. See CONTEXT.md
// "Scalar edit (scalarEdit)".
//
// It stays neutral to the entity type: the caller maps name/desc onto its own
// typed update input (api.ProjectUpdateInput / api.InitiativeUpdateInput) and
// pulls the fresh values back out for divergences — nothing Project- or
// Initiative-shaped crosses this interface. (The `desc`/`origDesc` field names
// are historical; the value they carry is the body-mapped content.)
type scalarEdit struct {
	name, desc         *string // new value, non-nil iff that field changed
	origName, origDesc string  // pre-write values, for the divergence "original"
}

// newScalarEdit diffs an already-parsed name/body against the current
// name/content (marshal.MarkdownToProjectEdit/MarkdownToInitiativeEdit own the
// extraction and the name coercion). The body maps to the content field; both
// sides are trimmed for the change test so a render/parse trailing-newline
// delta doesn't read as an edit, and the trimmed body is what we send. An empty
// or unchanged name is left alone.
func newScalarEdit(name, body string, curName, curDesc string) scalarEdit {
	e := scalarEdit{origName: curName, origDesc: curDesc}
	if newDesc := strings.TrimSpace(body); newDesc != strings.TrimSpace(curDesc) {
		e.desc = &newDesc
	}
	if name != "" && name != curName {
		e.name = &name
	}
	return e
}

// changed reports whether either scalar field needs an API update.
func (e scalarEdit) changed() bool { return e.name != nil || e.desc != nil }

// clearsBody reports whether this edit empties a body that currently has
// content. Linear accepts an empty `content` and then ignores it, so this
// particular edit does not take effect — observed live as "you wrote 0 chars,
// 34 persisted" (#398).
//
// It is deliberately NOT a pre-flight refusal. The mutation still goes out, and
// this only changes how the write-back classifies the result: if the body really
// did survive, the caller gets EINVAL and clearBodyMessage instead of the generic
// silent-revert EIO. EIO means "the write didn't stick, try again", which is the
// wrong instruction for a write that can never stick; EINVAL plus an explanation
// is one the caller can act on. And because the verdict comes from what actually
// persisted rather than from a hardcoded belief about Linear, a backend that DOES
// apply an empty content — a future Linear, or the mock mutator offline — simply
// succeeds, and nobody has to remember to delete a stale refusal.
func (e scalarEdit) clearsBody() bool {
	return e.desc != nil && *e.desc == "" && strings.TrimSpace(e.origDesc) != ""
}

// clearBodyMessage is the .error for a clear that the server declined to apply.
// entity is "project" or "initiative".
func clearBodyMessage(entity string) string {
	return "Field: content (body)\n" +
		"Error: Linear accepted the update and kept the previous body — it ignores an empty content value, " +
		"so a " + entity + " body cannot be emptied this way. Re-saving the same empty body will do the same thing.\n" +
		"Fix: replace the body with the text you want rather than emptying it (a single \"-\" if you need it " +
		"visually empty). Frontmatter fields still clear the normal way, by removing the key."
}

// divergences classifies the read-your-writes result for each field that was
// sent, comparing what we sent against what persisted (relative to the pre-write
// value). Only fields that actually changed are checked — an untouched field
// can't diverge. name is checked before description, one canonical order.
//
// entity ("project"/"initiative") only names the surface in the one message that
// needs it: a body-clear the server declined (#398).
func (e scalarEdit) divergences(entity, freshName, freshDesc string) []writeBackResult {
	var results []writeBackResult
	if e.name != nil {
		results = append(results, writeBackDivergence("name", *e.name, freshName, e.origName))
	}
	if e.desc != nil {
		// A declined clear is a silent revert by the generic rule, but a
		// permanently unfixable one, so it gets its own verdict: EINVAL with an
		// explanation, not a retryable-looking EIO. If the clear DID take, this
		// falls through and reports success like any other faithful write.
		if e.clearsBody() && strings.TrimSpace(freshDesc) != "" {
			results = append(results, writeBackResult{
				message: clearBodyMessage(entity),
				fatal:   true,
				errno:   syscall.EINVAL,
			})
		} else {
			results = append(results, writeBackDivergence("content (body)", *e.desc, freshDesc, e.origDesc))
		}
	}
	return results
}
