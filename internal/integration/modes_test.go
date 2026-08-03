package integration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// =============================================================================
// Which mode does this test run in?
//
// The suite runs in three modes:
//
//	mode                          LINEARFS_LIVE_API   LINEARFS_WRITE_TESTS
//	fixture (make test)           unset               —
//	live read-only (…-ro)         1                   unset
//	live + writes  (…-rw)         1                   1
//
// Every test belongs to some subset of them, and this file is the single place
// that membership is expressed. There are exactly three ways to say it:
//
//   - skipIfNoWriteTests(t)   — live + writes only: the tests that mutate a real
//     workspace.
//   - skipIfLiveAPI(t, why)   — fixture only, with `why` naming the kind of
//     fixture dependence (the constants below, or the mock-modelled backend
//     behaviour described under them). Its condition is the exact inverse of
//     skipIfNoWriteTests; the two must never agree to run one test.
//   - no guard                — runs in every mode. A test in this group may not
//     name a seeded row: derive identifiers with someIssueID/someProjectSlug.
//
// So `grep skipIf` over a file answers "does this run live?" per test. #395 was
// filed because it did not: four files asserted seeded fixture data with no
// guard at all, and the first live dispatch of the write suite failed ~48 tests
// by construction — every one of them a hardcoded TST-1 path, not a product bug.
// =============================================================================

// Reasons a test is fixture-mode-only. skipIfLiveAPI takes one; they are shared
// constants because the same claim recurs across dozens of tests and the
// difference between them is what you need when triaging a live run.
const (
	// fixtureSeededData: the assertions name rows only the fixture population
	// creates — TST-1's comments/docs/attachments/child/relation, the
	// test-project slug and its milestones/updates, the synthetic label
	// catalog. A live workspace has none of them, so the test fails by
	// construction instead of finding anything.
	fixtureSeededData = "fixture-mode: asserts seeded fixture data (TST-1, test-project, …)"

	// fixtureWriteContract: the test proves a structural write contract by
	// writing through the mount. The write is inert offline (no API, no auth)
	// but would mutate a real workspace under a live key — and gating it with
	// skipIfNoWriteTests instead would delete it from the default offline
	// suite, which is the only place it ever runs.
	fixtureWriteContract = "fixture-mode write-contract guard; the same write would mutate a real workspace under a live key"
)

// The third kind of fixture dependence has no shared constant, because the
// useful part of the sentence differs per test: the assertion needs the mock
// mutator to MODEL A BACKEND BEHAVIOUR that the real one need not have —
// markdown reformat-on-store (mockmutation.WithBodyReformat), or a backend that
// ignores an empty content (mockmutation.WithEmptyContentIgnored). These take a
// `why` naming the option they depend on.
//
// Guarded the other way, such a test asserts a belief about Linear that nothing
// upholds: #411 is TestClearProjectBodyIsRejectedLegibly under
// skipIfNoWriteTests, asserting the declined-clear verdict against whatever the
// server happened to do — so it failed the first live write run for a non-bug,
// having created a real project to get there.

// skipIfNoWriteTests skips the test unless it is running in live + writes mode.
// It guards the tests that mutate a real workspace. liveAPIMode is checked
// FIRST and reported first: LINEARFS_WRITE_TESTS=1 alone means the offline
// fixture suite, not a write run (#386).
func skipIfNoWriteTests(t interface{ Skip(...any) }) {
	if !liveAPIMode {
		t.Skip("Skipped: requires live API (set LINEARFS_LIVE_API=1 and LINEAR_API_KEY)")
	}
	if os.Getenv("LINEARFS_WRITE_TESTS") != "1" {
		t.Skip("Skipped: write tests disabled (set LINEARFS_WRITE_TESTS=1 to enable)")
	}
}

// skipIfLiveAPI skips the test under a live API key. It is the inverse interlock
// to skipIfNoWriteTests and the ONE way this suite marks a test fixture-only;
// `why` is one of the constants above, or a sentence specific to the test, and
// is what a live run prints in place of the test.
func skipIfLiveAPI(t interface{ Skip(...any) }, why string) {
	if liveAPIMode {
		t.Skip("Skipped: " + why)
	}
}

// The seeded entities the fixture population guarantees. Tests that assert
// against their *contents* are fixture-only (skipIfLiveAPI + fixtureSeededData);
// tests that merely need *an* issue or project reach them through someIssueID /
// someProjectSlug, which fall back to the live workspace.
const (
	fixtureIssueID     = "TST-1"
	fixtureProjectSlug = "test-project"
)

// someIssueID returns an issue identifier that exists in the mounted workspace:
// the fixture's fully-populated TST-1 offline, the first issue the live
// workspace lists otherwise. Tests asserting a contract that must hold in BOTH
// modes — stat determinism, the .meta split, fsync support — use this instead of
// hardcoding "TST-1", which is what made them fixture-only by accident (#395).
//
// The live pick is an arbitrary issue, so it may have no comments or docs; a
// caller that needs a populated collection must skip on the empty case rather
// than fail, and say the workspace lacks it.
//
// The live pick is made once per run and memoized. A live run creates issues as
// it goes, and identifiers sort lexically ("LIG-100" < "LIG-99"), so re-listing
// per call would let the answer move mid-run: two tests would reason about
// different issues, and one of them about an issue another test is mutating.
func someIssueID(t *testing.T) string {
	t.Helper()
	if !liveAPIMode {
		return fixtureIssueID
	}
	pickedIssueOnce.Do(func() {
		pickedIssue = firstRealEntry(mustReadDir(t, issuesPath(testTeamKey)))
	})
	if pickedIssue == "" {
		t.Skipf("workspace team %s lists no issues", testTeamKey)
	}
	return pickedIssue
}

// someProjectSlug is someIssueID's twin for projects: the fixture's
// test-project offline, the first project the live workspace lists otherwise,
// memoized the same way and for the same reason.
func someProjectSlug(t *testing.T) string {
	t.Helper()
	if !liveAPIMode {
		return fixtureProjectSlug
	}
	pickedProjectOnce.Do(func() {
		entries, err := os.ReadDir(projectsPath(testTeamKey))
		if err != nil {
			return
		}
		pickedProject = firstRealEntry(entries)
	})
	if pickedProject == "" {
		t.Skipf("workspace team %s lists no projects", testTeamKey)
	}
	return pickedProject
}

var (
	pickedIssueOnce   sync.Once
	pickedIssue       string
	pickedProjectOnce sync.Once
	pickedProject     string
)

// someProjectDir / someIssueDir are the path forms of the two pickers, for the
// many callers that want the directory rather than the bare identifier.
func someIssueDir(t *testing.T) string {
	t.Helper()
	return issueDirPath(testTeamKey, someIssueID(t))
}

func someProjectDir(t *testing.T) string {
	t.Helper()
	return projectDirPath(testTeamKey, someProjectSlug(t))
}

// fixtureLiterals are the seeded names that only exist in the fixture store.
// A test that spells one is asserting against the fixture seed by definition.
// SUB is the fixture's sub-team of TST — the only team hierarchy the offline
// set has, and absent from any live workspace, so naming it is the same claim
// as naming TST-1.
var fixtureLiterals = []string{`"TST-`, `"test-project"`, `"SUB"`, `"SUB-`}

// TestFixtureLiteralsCarryTheGuard is the standing check for #395: a test that
// names a seeded row must say so with skipIfLiveAPI, because a live workspace
// has no such row and the test can only fail there — 48 of them did, on the
// first live dispatch of the write suite, every one a stat of a path that does
// not exist. The fix is one of two lines, and the failure message says which:
// add the guard, or take the identifier from someIssueID/someProjectSlug.
//
// It reads the suite's own source rather than its behavior, because the thing
// being guarded is unobservable from inside a fixture-mode run: that is exactly
// why it went unnoticed until someone spent a live workspace to find out.
func TestFixtureLiteralsCarryTheGuard(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test sources: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no test sources found; the check would pass vacuously")
	}

	fset := token.NewFileSet()
	checked := 0
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		parsed, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Body == nil {
				continue
			}
			checked++
			// Positions are FileSet-global; convert to this file's byte offsets.
			body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			// This file spells the literals to define them; skip its own bodies.
			if file == "modes_test.go" {
				continue
			}
			for _, lit := range fixtureLiterals {
				if !strings.Contains(body, lit) {
					continue
				}
				if strings.Contains(body, "skipIfLiveAPI") || strings.Contains(body, "skipIfNoWriteTests") {
					continue
				}
				t.Errorf("%s: %s names the fixture-seeded %s but carries no mode guard.\n"+
					"  Either add skipIfLiveAPI(t, fixtureSeededData) — a live workspace has no such row —\n"+
					"  or take the identifier from someIssueID(t)/someProjectSlug(t) if the contract holds live too.",
					file, fn.Name.Name, strings.Trim(lit, `"`))
			}
		}
	}
	t.Logf("checked %d test functions across %d files", checked, len(files))
}
