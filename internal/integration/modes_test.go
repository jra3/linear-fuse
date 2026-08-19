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
//
// The scan FOLLOWS CALLS into the suite's own helpers, not just Test bodies: a
// shared seeder like seedIssueProbe spells "TST-" once on behalf of every test
// that uses it, and a rule that only read Test bodies would let the next caller
// arrive unguarded — the precise failure #395 exists to prevent, reintroduced
// by the refactor that made the seeding shared. A test is held to the rule if
// it reaches a fixture literal through ANY chain of package-local calls, and
// the failure names the chain so the fix is still a one-line decision.
func TestFixtureLiteralsCarryTheGuard(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test sources: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no test sources found; the check would pass vacuously")
	}

	// Every package-level func in the suite, by name — Go guarantees the names
	// are unique within the package, so the name is the whole identity. Closures
	// bound to locals (`seed := func(...)`) are not declarations and need no
	// entry: their source sits inside the body that declares them, so the body
	// scan already sees whatever they spell.
	type suiteFunc struct {
		file  string
		body  string   // source text of the body, for the literal and guard scans
		calls []string // package-local functions it calls, in source order
	}
	funcs := map[string]*suiteFunc{}
	var declared []string // declaration order, so failures are deterministic

	fset := token.NewFileSet()
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
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			// Positions are FileSet-global; convert to this file's byte offsets.
			body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			var calls []string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if ident, ok := call.Fun.(*ast.Ident); ok {
						calls = append(calls, ident.Name)
					}
				}
				return true
			})
			funcs[fn.Name.Name] = &suiteFunc{file: file, body: body, calls: calls}
			declared = append(declared, fn.Name.Name)
		}
	}

	// reachesLiteral walks the call graph breadth-first from start and returns
	// the shortest chain to a body that spells a fixture literal, plus the
	// literal. Breadth-first so the reported chain is the shortest explanation,
	// and `seen` keeps recursion (and mutual recursion) from looping.
	reachesLiteral := func(start string) (chain []string, lit string) {
		type step struct {
			name string
			via  []string
		}
		seen := map[string]bool{start: true}
		for queue := []step{{start, []string{start}}}; len(queue) > 0; queue = queue[1:] {
			cur := queue[0]
			fn := funcs[cur.name]
			if fn == nil {
				continue // a closure local, a stdlib call, or a builtin
			}
			// This file spells the literals to define them; its own bodies are
			// the definition, not a use.
			if fn.file != "modes_test.go" {
				for _, l := range fixtureLiterals {
					if strings.Contains(fn.body, l) {
						return cur.via, strings.Trim(l, `"`)
					}
				}
			}
			for _, callee := range fn.calls {
				if seen[callee] {
					continue
				}
				seen[callee] = true
				queue = append(queue, step{callee, append(append([]string(nil), cur.via...), callee)})
			}
		}
		return nil, ""
	}

	checked := 0
	for _, name := range declared {
		fn := funcs[name]
		// TestMain is the suite's entry point, not a test: it seeds the fixture
		// population itself, and already branches on liveAPIMode to decide
		// whether to.
		if !strings.HasPrefix(name, "Test") || name == "TestMain" || fn.file == "modes_test.go" {
			continue
		}
		checked++
		chain, lit := reachesLiteral(name)
		if lit == "" {
			continue
		}
		// The guard must be in the TEST's own body: that is where a reader
		// looks, and it is what `grep skipIf` over the file answers.
		if strings.Contains(fn.body, "skipIfLiveAPI") || strings.Contains(fn.body, "skipIfNoWriteTests") {
			continue
		}
		where := "names"
		if len(chain) > 1 {
			where = "reaches, via " + strings.Join(chain[1:], " → ") + ","
		}
		t.Errorf("%s: %s %s the fixture-seeded %s but carries no mode guard.\n"+
			"  Either add skipIfLiveAPI(t, fixtureSeededData) — a live workspace has no such row —\n"+
			"  or take the identifier from someIssueID(t)/someProjectSlug(t) if the contract holds live too.",
			fn.file, name, where, lit)
	}
	if checked == 0 {
		t.Fatal("no test functions found outside modes_test.go; the check would pass vacuously")
	}
	t.Logf("checked %d test functions across %d files", checked, len(files))
}
