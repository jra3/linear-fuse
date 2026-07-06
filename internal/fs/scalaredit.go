package fs

import (
	"strings"

	"github.com/jra3/linear-fuse/internal/marshal"
)

// scalarEdit is the diff of the two scalar fields both project.md and
// initiative.md expose for editing — a name (frontmatter) and a description
// (body). It owns the change decision (what counts as "changed"), the name
// coercion, and the read-your-writes divergence classification, so the two
// handlers no longer each hand-roll a `fieldChanged` flag and a byte-identical
// commitWriteBack compare closure. See CONTEXT.md "Scalar edit (scalarEdit)".
//
// It stays neutral to the entity type: the caller maps name/desc onto its own
// typed update input (api.ProjectUpdateInput / api.InitiativeUpdateInput) and
// pulls the fresh values back out for divergences — nothing Project- or
// Initiative-shaped crosses this interface.
type scalarEdit struct {
	name, desc         *string // new value, non-nil iff that field changed
	origName, origDesc string  // pre-write values, for the divergence "original"
}

// newScalarEdit diffs a parsed document against the current name/description.
// The body maps to the description; both sides are trimmed for the change test
// so a render/parse trailing-newline delta doesn't read as an edit, and the
// trimmed body is what we send. The frontmatter name is coerced the way the
// issue handler coerces its scalars (a numeric or bare-scalar name updates
// rather than being silently dropped); an empty or unchanged name is left alone.
func newScalarEdit(doc *marshal.Document, curName, curDesc string) scalarEdit {
	e := scalarEdit{origName: curName, origDesc: curDesc}
	if newDesc := strings.TrimSpace(doc.Body); newDesc != strings.TrimSpace(curDesc) {
		e.desc = &newDesc
	}
	if name := marshal.ScalarToString(doc.Frontmatter["name"]); name != "" && name != curName {
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
		results = append(results, writeBackDivergence("description (body)", *e.desc, freshDesc, e.origDesc))
	}
	return results
}
