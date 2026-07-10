package fs

import (
	"strings"
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

// divergences classifies the read-your-writes result for each field that was
// sent, comparing what we sent against what persisted (relative to the pre-write
// value). Only fields that actually changed are checked — an untouched field
// can't diverge. name is checked before description, one canonical order.
func (e scalarEdit) divergences(freshName, freshDesc string) []writeBackResult {
	var results []writeBackResult
	if e.name != nil {
		results = append(results, writeBackDivergence("name", *e.name, freshName, e.origName))
	}
	if e.desc != nil {
		results = append(results, writeBackDivergence("content (body)", *e.desc, freshDesc, e.origDesc))
	}
	return results
}
