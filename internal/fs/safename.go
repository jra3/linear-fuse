package fs

import "strings"

// reservedNames is the exact set of control literals a rendered fs name must
// never collide with. They are the collectionTrio triggers (_create), the
// feedback sidecars (.error, .last), the read-through sidecar suffix (.meta),
// and the two view aliases (current in cycles/, unassigned in by/assignee/).
// safeName escapes a sanitized name that lands exactly on one of these by
// appending -<id>. Exact-match only: a name that merely CONTAINS a dot (e.g.
// "my.error.log") is left alone — only a shadow that would hijack a control
// file is escaped.
var reservedNames = map[string]struct{}{
	"_create":    {},
	".error":     {},
	".last":      {},
	".meta":      {},
	"current":    {},
	"unassigned": {},
}

// safeName is the single safety chokepoint every fs name/target builder routes
// its output through. It is a lenient strip-and-replace pass layered over each
// builder's own cosmetic transform (projectDirName stays slug-cased,
// cycleDirName stays space-hyphen) — it unifies the safety INVARIANT, not the
// cosmetic style. A remote Linear string can be arbitrary, so this is the one
// place that guarantees a rendered name can never escape its directory, carry a
// control character into a path, collapse to an invalid "" / "." / ".."
// component, or shadow a reserved control file.
//
// raw is the builder's cosmetically-transformed candidate name; id is the
// entity's stable identifier (or slug) used as the fallback when raw sanitizes
// to nothing usable, and as the disambiguating suffix when raw would shadow a
// reserved literal. The fallback/suffix keeps the escape deterministic and
// unique: two distinct remote names can never both escape into the same slot.
//
// The pass:
//   - replaces /, \, NUL, and every C0 control char (< 0x20) with '-';
//   - trims trailing spaces and dots (a name ending in '.' or ' ' is a
//     Windows/path footgun and "foo." collapses to "foo" on some layers);
//   - if the result is "", ".", or ".." → returns id;
//   - if the result exactly equals a reserved literal → appends "-" + id.
func safeName(raw, id string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if r == '/' || r == '\\' || r < 0x20 {
			b.WriteByte('-')
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimRight(b.String(), " .")

	if s == "" || s == "." || s == ".." {
		return id
	}
	if _, reserved := reservedNames[s]; reserved {
		return s + "-" + id
	}
	return s
}
