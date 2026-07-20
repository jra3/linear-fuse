package fs

import (
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// reservedLiterals is the set of control names a rendered fs name must never
// collide with (the collectionTrio triggers, the sidecar suffixes, and the two
// view aliases). safeName's exact-match escape guarantees a sanitized name that
// equals one of these gets an -<id> suffix.
var reservedLiterals = []string{
	"_create", ".error", ".last", ".meta", "current", "unassigned",
}

// hostileNames is the corpus of pathological / malicious raw name inputs fed
// through every builder. Each must survive sanitization without producing a
// path-escaping, control-char-carrying, empty, dot, or reserved-literal name.
var hostileNames = []string{
	"..",
	".",
	"",
	"/",
	"\\",
	"../../etc/passwd",
	"a/b/c",
	"foo\x00bar",
	"\x00",
	"\x01\x02\x1f",
	"tab\there",
	"new\nline",
	"trailing   ",
	"trailing...",
	"  ...  ",
	"-leadingdash",
	"_create",
	".error",
	".last",
	".meta",
	"current",
	"unassigned",
	"café",           // unicode should be preserved
	"日本語",            // unicode should be preserved
	"normal-name",    // benign control
	"Normal Name 42", // benign control
}

// assertSafe checks the universal safety invariant every builder output must
// satisfy: no path separators, no NUL, no C0 controls, never "", ".", "..",
// and never exactly equal to a reserved control literal.
func assertSafe(t *testing.T, builder, raw, got string) {
	t.Helper()
	if strings.ContainsAny(got, "/\\\x00") {
		t.Errorf("%s(%q) = %q: contains path separator or NUL", builder, raw, got)
	}
	for _, r := range got {
		if r < 0x20 {
			t.Errorf("%s(%q) = %q: contains C0 control char %#x", builder, raw, got, r)
		}
	}
	if got == "" || got == "." || got == ".." {
		t.Errorf("%s(%q) = %q: is empty/./.. (invalid path component)", builder, raw, got)
	}
	// A rendered name must never EXACTLY equal a reserved control file. The
	// builders that append a suffix (labelFilename etc.) produce e.g.
	// "_create.md", which is fine — only an exact match is a shadow.
	for _, res := range reservedLiterals {
		if got == res {
			t.Errorf("%s(%q) = %q: shadows reserved literal %q", builder, raw, got, res)
		}
	}
}

func TestSafeName_HostileCorpus(t *testing.T) {
	const id = "ID-STABLE-123"
	for _, raw := range hostileNames {
		got := safeName(raw, id)
		assertSafe(t, "safeName", raw, got)
	}
}

func TestSafeName_ReservedEscapeAppendsID(t *testing.T) {
	const id = "abc123"
	for _, res := range reservedLiterals {
		got := safeName(res, id)
		if got == res {
			t.Errorf("safeName(%q, %q) = %q: reserved literal not escaped", res, id, got)
		}
		if !strings.HasSuffix(got, "-"+id) {
			t.Errorf("safeName(%q, %q) = %q: expected -<id> suffix", res, id, got)
		}
	}
}

func TestSafeName_EmptyFallsBackToID(t *testing.T) {
	const id = "fallback-id"
	// Only inputs that sanitize to "", ".", or ".." fall back to the id. A raw
	// "/" becomes "-" (a valid, if ugly, single component) — not a fallback case
	// per the spec, but still path-safe (asserted by the corpus test).
	for _, raw := range []string{"", ".", "..", "   ", "...", "  ...  "} {
		if got := safeName(raw, id); got != id {
			t.Errorf("safeName(%q, %q) = %q: expected id fallback", raw, id, got)
		}
	}
}

func TestSafeName_ContainingDotNotEscaped(t *testing.T) {
	// Only EXACT matches escape; a name merely containing a reserved substring
	// is left alone (no false-positive churn).
	for _, raw := range []string{"my.error.log", "not_create", ".errorlog", "currentish"} {
		got := safeName(raw, "id")
		if strings.HasSuffix(got, "-id") {
			t.Errorf("safeName(%q) = %q: should not have been reserved-escaped", raw, got)
		}
	}
}

// --- Builder corpus: every name/target builder must produce a safe output for
// every hostile input. ---

func TestBuilders_HostileCorpus(t *testing.T) {
	for _, raw := range hostileNames {
		// cycleDirName
		assertSafe(t, "cycleDirName", raw, cycleDirName(api.Cycle{ID: "cyc-1", Name: raw}))

		// userDirName (via DisplayName)
		assertSafe(t, "userDirName", raw, userDirName(api.User{ID: "usr-1", DisplayName: raw}))

		// sanitizeFilename (attachment title component)
		assertSafe(t, "sanitizeFilename", raw, sanitizeFilename(raw, "att-1"))

		// linkName (external attachment .link name)
		assertSafe(t, "linkName", raw, linkName(api.Attachment{ID: "att-1", Title: raw}))

		// labelFilename
		assertSafe(t, "labelFilename", raw, labelFilename(api.Label{ID: "lbl-1", Name: raw}))

		// documentFilename (via title; empty SlugID)
		assertSafe(t, "documentFilename", raw, documentFilename(api.Document{ID: "doc-1", Title: raw}))

		// milestoneFilename
		assertSafe(t, "milestoneFilename", raw, milestoneFilename(api.ProjectMilestone{ID: "ms-1", Name: raw}))

		// projectDirName
		assertSafe(t, "projectDirName", raw, projectDirName(api.Project{ID: "prj-1", Slug: "prj-slug", Name: raw}))

		// initiativeDirName
		assertSafe(t, "initiativeDirName", raw, initiativeDirName(api.Initiative{ID: "ini-1", Name: raw}))

		// initiativeProjectDirName
		assertSafe(t, "initiativeProjectDirName", raw, initiativeProjectDirName(api.InitiativeProject{ID: "ip-1", Slug: "ip-slug", Name: raw}))

		// assigneeHandle (by/assignee value)
		assertSafe(t, "assigneeHandle", raw, assigneeHandle(&api.User{ID: "usr-2", DisplayName: raw}))

		// teamIssueTarget (symlink target): both remote-derived components — the
		// team key and the issue identifier — must be safeName'd, so a hostile
		// value can never inject a path segment into .../teams/<k>/issues/<i>.
		// Checked prefix-agnostically via the suffix; reverting either component
		// to a raw field breaks it (a raw '/' would inject extra segments).
		// An empty team key legitimately ENOENTs (degenerate); only assert the
		// safe-component invariant when a target is actually produced.
		if gotTarget, errno := teamIssueTarget(api.Issue{ID: "iss-1", Identifier: raw, Team: &api.Team{ID: "team-1", Key: raw}}); errno == 0 {
			wantSuffix := "/teams/" + safeName(raw, "team-1") + "/issues/" + safeName(raw, "iss-1")
			if !strings.HasSuffix(gotTarget, wantSuffix) {
				t.Errorf("teamIssueTarget(%q) = %q: components must be safeName'd (want suffix %q)", raw, gotTarget, wantSuffix)
			}
		}
	}
}

// TestSafeName_NonBreaking pins that benign, already-safe names render
// identically after the safety pass — the change must not churn the live
// mount's directory names.
func TestSafeName_NonBreaking(t *testing.T) {
	cases := []struct{ builder, raw, want string }{
		{"cycleDirName", "Sprint 42", "Sprint-42"},
		{"userDirName", "alice", "alice"},
		{"labelFilename", "Bug", "Bug.md"},
		{"labelFilename", "Backend Work", "Backend-Work.md"},
		{"milestoneFilename", "Phase 1", "Phase 1.md"},
		{"projectDirName", "API Gateway", "api-gateway"},
		{"initiativeDirName", "Platform Modernization", "platform-modernization"},
		{"documentFilename", "Design Notes", "design-notes.md"},
	}
	for _, tc := range cases {
		var got string
		switch tc.builder {
		case "cycleDirName":
			got = cycleDirName(api.Cycle{ID: "c", Name: tc.raw})
		case "userDirName":
			got = userDirName(api.User{ID: "u", DisplayName: tc.raw})
		case "labelFilename":
			got = labelFilename(api.Label{ID: "l", Name: tc.raw})
		case "milestoneFilename":
			got = milestoneFilename(api.ProjectMilestone{ID: "m", Name: tc.raw})
		case "projectDirName":
			got = projectDirName(api.Project{ID: "p", Slug: "s", Name: tc.raw})
		case "initiativeDirName":
			got = initiativeDirName(api.Initiative{ID: "i", Name: tc.raw})
		case "documentFilename":
			got = documentFilename(api.Document{ID: "d", Title: tc.raw})
		}
		if got != tc.want {
			t.Errorf("%s(%q) = %q, want %q (non-breaking regression)", tc.builder, tc.raw, got, tc.want)
		}
	}
}
