package fs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// #414 and #449 are the same defect found twice: a node build site wrote its
// kernel timeout as an inline duration (`30*time.Second`) instead of reading the
// mount's configured policy, so `WithKernelCacheTimeouts` silently governed
// nothing beneath it — a short-timeout fixture looked like a broken refresh path
// rather than a timeout that never applied. #414 fixed the six directory sites;
// #449 fixed the three render-file ones. Nothing stood between the two but grep,
// and `grep '30 * time.Second'` also matches request timeouts and retry ladders.
//
// This rule pins the wiring the way TestEverySpecSetsPinIno does: the timeout
// argument at every build site must be one of the NAMED policies in
// timeoutPolicies below, or the mount's own `lfs.entryTimeout()` — never a
// duration spelled inline, in any form. A fifth policy, should one ever be
// justified, is a new named constant with a comment saying why, added to that
// map — which is the whole ask: deliberate, and greppable by name.

// timeoutPolicies is the closed set of names a build site may hand the kernel as
// a caching bound. The set is CLOSED on purpose. An earlier version of this rule
// accepted any identifier, selector or call — which meant it rejected only
// `30*time.Second`, the one spelling already removed from the tree, while
// `time.Second` (the spelling this package uses elsewhere, and so the likeliest
// next mistake), `time.Minute` and `time.Duration(30e9)` all passed as "named
// policies".
var timeoutPolicies = map[string]bool{
	"inheritTimeout":       true,
	"mountDefaultTimeout":  true,
	"editableFileTimeout":  true,
	"transientFileTimeout": true,
}

// entryTimeoutAccessor is the one call form a build site may pass instead of a
// policy constant: the mount's configured bound, read through LinearFS.
const entryTimeoutAccessor = "entryTimeout"

// guardedHelpers names every function that takes a kernel caching bound and
// hands it to the FUSE reply. The argument index is NOT listed here — it is
// derived from each function's own declaration (see timeoutParamIndex), because
// a hand-maintained index misaims silently: shift `mountRenderFile` by one and
// the rule starts checking `out`, an identifier it accepts, while the
// call-sites-found counter keeps the "rule stopped checking" alarm quiet.
//
// A build helper missing from this list is the next place the defect can hide,
// so the test asserts every name still resolves to a declaration AND still has
// call sites.
var guardedHelpers = []string{
	// The three inode builders, and the manifest that feeds two of them for the
	// entity directories.
	"newDirInode",
	"newFileInode",
	"newRenderInode",
	"lookupRenderFile",
	"mountRenderFile",
	"newDirManifest",
	// The single choke point that actually writes the bound onto the reply.
	// Guarding it closes the escape hatch the builder-only rule left open: a
	// handler that filled an EntryOut by hand (`_create` in collection.go, the
	// atomic-save scratch inode in atomicwrite.go) bypassed every builder and
	// was invisible to this test, which is exactly the defect class #449 asked
	// it to cover.
	"applyNodeTimeout",
}

func TestNoHardcodedKernelTimeouts(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	files := parsePackageSource(t, fset)

	// Derive each helper's timeout argument index from its declaration, and
	// verify the parameter it lands on is really named `timeout` and typed
	// time.Duration.
	index := map[string]int{}
	for _, fn := range guardedHelpers {
		idx, err := timeoutParamIndex(files, fn)
		if err != nil {
			t.Errorf("%s: %v (renamed, removed, or its timeout parameter changed — this rule "+
				"has stopped checking it)", fn, err)
			continue
		}
		index[fn] = idx
	}

	seen := map[string]int{}
	var offenders []string

	forEachCall(files, func(enclosing *ast.FuncDecl, call *ast.CallExpr) {
		fn := calleeName(call.Fun)
		idx, guarded := index[fn]
		if !guarded || idx >= len(call.Args) {
			return
		}
		// A helper forwarding its own parameter to the next helper down is not a
		// call site; the value was checked where it entered.
		seen[fn]++
		arg := call.Args[idx]
		if !isNamedTimeout(arg, enclosing) {
			offenders = append(offenders, fmt.Sprintf("%s (%s: %s)",
				fset.Position(arg.Pos()), fn, exprText(fset, arg)))
		}
	})

	for fn := range index {
		if seen[fn] == 0 {
			t.Errorf("no call sites found for %s; it was renamed or removed and this rule "+
				"has stopped checking it", fn)
		}
	}

	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("node build sites passing a kernel timeout that is not a named policy "+
			"(WithKernelCacheTimeouts cannot govern an inline duration — see #414/#449; use "+
			"lfs.entryTimeout(), one of %s, or add a named constant to timeoutPolicies with a "+
			"comment saying why):\n  %s",
			strings.Join(policyNames(), "/"), strings.Join(offenders, "\n  "))
	}
}

// TestTimeoutSettersGoThroughOneChokePoint keeps the rule above complete.
// It checks the arguments handed to a known set of helpers, so a handler that
// writes SetAttrTimeout/SetEntryTimeout onto an EntryOut directly would be
// invisible to it — and two live sites did exactly that until #449's rework.
// Routing every write through applyNodeTimeout is what makes "check the
// arguments" equal "check every timeout the kernel is ever told".
func TestTimeoutSettersGoThroughOneChokePoint(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	files := parsePackageSource(t, fset)

	setters := map[string]bool{"SetAttrTimeout": true, "SetEntryTimeout": true}
	var offenders []string
	calls := 0

	forEachCall(files, func(enclosing *ast.FuncDecl, call *ast.CallExpr) {
		if !setters[calleeName(call.Fun)] {
			return
		}
		calls++
		if enclosing != nil && enclosing.Name.Name == "applyNodeTimeout" {
			return
		}
		offenders = append(offenders, fset.Position(call.Pos()).String())
	})

	if calls == 0 {
		t.Fatal("no SetAttrTimeout/SetEntryTimeout calls found at all; this rule has stopped " +
			"checking anything")
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("kernel timeout written outside applyNodeTimeout — TestNoHardcodedKernelTimeouts "+
			"checks arguments, so a direct setter call is a site it cannot see (#449). Call "+
			"applyNodeTimeout with a named policy instead:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// TestNamedTimeoutPolicies pins what the mount-independent names mean — in terms
// of the reply bytes go-fuse actually sends, not an intention.
func TestNamedTimeoutPolicies(t *testing.T) {
	t.Parallel()

	if inheritTimeout >= 0 {
		t.Errorf("inheritTimeout = %v; must be negative — applyNodeTimeout treats any value >= 0 "+
			"as a bound to write", inheritTimeout)
	}
	for _, tc := range []struct {
		name  string
		bound time.Duration
	}{
		{"editableFileTimeout", editableFileTimeout},
		{"transientFileTimeout", transientFileTimeout},
	} {
		if tc.bound <= 0 || tc.bound >= DefaultEntryTimeout {
			t.Errorf("%s = %v; must be a positive bound shorter than the %v default",
				tc.name, tc.bound, DefaultEntryTimeout)
		}
		var out fuse.EntryOut
		applyNodeTimeout(&out, tc.bound)
		if out.EntryTimeout() != tc.bound || out.AttrTimeout() != tc.bound {
			t.Errorf("%s: reply carries entry=%v attr=%v, want %v on both",
				tc.name, out.EntryTimeout(), out.AttrTimeout(), tc.bound)
		}
	}

	// Every name the rule accepts must actually be declared, so a constant
	// renamed without updating timeoutPolicies leaves a stale entry behind that
	// this catches rather than the rule silently accepting a name nothing uses.
	fset := token.NewFileSet()
	files := parsePackageSource(t, fset)
	declared := packageLevelNames(files)
	for name := range timeoutPolicies {
		if !declared[name] {
			t.Errorf("timeoutPolicies accepts %q but the package declares no such name; it was "+
				"renamed and the allowlist is stale", name)
		}
	}
}

// TestMountDefaultTimeoutEqualsInherit pins the equivalence the constants'
// documentation claims, and that an earlier name for mountDefaultTimeout denied.
//
// It was called noKernelCache and documented as "the kernel may not cache this
// at all". go-fuse does not implement that policy. rawBridge.setEntryOutTimeout
// substitutes the mount's configured defaults into any reply whose timeouts read
// back as zero, and SetEntryTimeout(0) leaves them reading back as zero — so an
// explicit zero and an untouched reply are the same bytes, and both mean "use
// the mount default" (30s entry / 60s attr in production). Naming that "no
// caching" made every reader of these sites wrong about what they do.
func TestMountDefaultTimeoutEqualsInherit(t *testing.T) {
	t.Parallel()

	var explicitZero, untouched fuse.EntryOut
	applyNodeTimeout(&explicitZero, mountDefaultTimeout)
	applyNodeTimeout(&untouched, inheritTimeout)

	if explicitZero != untouched {
		t.Errorf("mountDefaultTimeout and inheritTimeout produce different replies:\n  %+v\n  %+v",
			explicitZero, untouched)
	}
	// The predicate go-fuse's bridge tests before substituting the mount default.
	if explicitZero.EntryTimeout() != 0 || explicitZero.AttrTimeout() != 0 {
		t.Errorf("mountDefaultTimeout reply reads back entry=%v attr=%v; the bridge substitutes "+
			"the mount default only while both read 0, so a non-zero here means these surfaces "+
			"stopped inheriting", explicitZero.EntryTimeout(), explicitZero.AttrTimeout())
	}
}

// TestApplyNodeTimeoutRejectsNegative pins the guard that keeps a negative bound
// from becoming an immortal cache entry. fuse.EntryOut.SetAttrTimeout computes
// `AttrValid = uint64(ns / 1e9)` with no sign check, so -1s lands as
// 18446744073709551615 seconds. newDirInode and fillRenderEntry had the check;
// newFileInode did not, and the guard rule's own failure message offers
// inheritTimeout as a remedy at all of them.
func TestApplyNodeTimeoutRejectsNegative(t *testing.T) {
	t.Parallel()

	for _, bound := range []time.Duration{inheritTimeout, -time.Second, -time.Hour} {
		var out fuse.EntryOut
		applyNodeTimeout(&out, bound)
		if out.AttrValid != 0 || out.EntryValid != 0 || out.AttrValidNsec != 0 || out.EntryValidNsec != 0 {
			t.Errorf("applyNodeTimeout(%v) wrote AttrValid=%d EntryValid=%d; a negative bound must "+
				"leave the reply untouched, not overflow into an effectively infinite TTL",
				bound, out.AttrValid, out.EntryValid)
		}
	}
}

// TestEntryTimeoutHonorsConfiguredZero pins the other half of #449: a mount that
// configured 0 asked for "no kernel entry caching", and until the field became a
// pointer the <= 0 guard read that as "never mounted" and handed back 30s — so
// WithKernelCacheTimeouts(0, 0) disabled caching at inheritTimeout sites and
// silently did not at every site routed through this helper.
//
// The negative case is the guard that swap dropped. `<= 0` was total over
// negative input; `== nil` is not, and a build site reads this as a concrete
// bound rather than the inheritTimeout sentinel, so a negative returned verbatim
// would reach the FUSE setters.
func TestEntryTimeoutHonorsConfiguredZero(t *testing.T) {
	t.Parallel()

	unmounted := &LinearFS{}
	if got := unmounted.entryTimeout(); got != DefaultEntryTimeout {
		t.Errorf("unmounted entryTimeout() = %v, want the %v default", got, DefaultEntryTimeout)
	}

	for _, want := range []time.Duration{0, 100 * time.Millisecond, 90 * time.Second} {
		configured := want
		lfs := &LinearFS{kernelEntryTimeout: &configured}
		if got := lfs.entryTimeout(); got != want {
			t.Errorf("entryTimeout() with %v configured = %v, want %v", want, got, want)
		}
	}

	for _, configured := range []time.Duration{-time.Nanosecond, -time.Second} {
		bound := configured
		lfs := &LinearFS{kernelEntryTimeout: &bound}
		if got := lfs.entryTimeout(); got != DefaultEntryTimeout {
			t.Errorf("entryTimeout() with %v configured = %v, want the %v default clamp — a "+
				"negative reaching a build site is an effectively infinite kernel cache",
				configured, got, DefaultEntryTimeout)
		}
	}
}

// isNamedTimeout reports whether a timeout argument names a policy rather than
// spelling a duration inline.
//
// Accepted: one of the timeoutPolicies constants; a call to entryTimeout(); the
// bare parameter `timeout` inside a helper that declares one (a value already
// checked where it entered); and the manifest's `.timeout` field, which only
// newDirManifest fills and which is itself a guarded call site.
//
// Everything else is rejected, INCLUDING anything rooted at package time:
// `time.Second` is a selector and `time.Duration(x)` is a call, so an
// accept-any-selector-or-call rule waved both through.
func isNamedTimeout(arg ast.Expr, enclosing *ast.FuncDecl) bool {
	switch e := arg.(type) {
	case *ast.ParenExpr:
		return isNamedTimeout(e.X, enclosing)
	case *ast.Ident:
		if timeoutPolicies[e.Name] {
			return true
		}
		return e.Name == "timeout" && declaresTimeoutParam(enclosing)
	case *ast.SelectorExpr:
		// m.timeout — never time.Anything, which this rule exists to reject.
		return !rootedAtTime(e) && e.Sel.Name == "timeout"
	case *ast.CallExpr:
		return calleeName(e.Fun) == entryTimeoutAccessor
	default:
		return false
	}
}

// rootedAtTime reports whether a selector's base is the identifier `time`.
func rootedAtTime(sel *ast.SelectorExpr) bool {
	base, ok := sel.X.(*ast.Ident)
	return ok && base.Name == "time"
}

// declaresTimeoutParam reports whether fn has a `timeout` parameter, so a bare
// `timeout` identifier is a forwarded (already-checked) value rather than a
// same-named local standing in for a literal.
func declaresTimeoutParam(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if name.Name == "timeout" {
				return true
			}
		}
	}
	return false
}

// timeoutParamIndex resolves fn's declaration in the parsed package and returns
// the ARGUMENT index of its `timeout time.Duration` parameter — derived, not
// hand-maintained, so inserting or reordering a parameter cannot leave the rule
// silently inspecting the wrong argument. Receivers are excluded, which is what
// makes the parameter position and the argument position the same number.
func timeoutParamIndex(files map[string]*ast.File, fn string) (int, error) {
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != fn || fd.Type.Params == nil {
				continue
			}
			idx := 0
			for _, field := range fd.Type.Params.List {
				if len(field.Names) == 0 { // an unnamed parameter still occupies a position
					idx++
					continue
				}
				for _, name := range field.Names {
					if name.Name != "timeout" {
						idx++
						continue
					}
					sel, ok := field.Type.(*ast.SelectorExpr)
					if !ok || !rootedAtTime(sel) || sel.Sel.Name != "Duration" {
						return 0, fmt.Errorf("parameter %d is named timeout but is not a time.Duration", idx)
					}
					return idx, nil
				}
			}
			return 0, fmt.Errorf("declared with no timeout parameter")
		}
	}
	return 0, fmt.Errorf("no declaration found")
}

// forEachCall visits every call expression in the package, passing the enclosing
// function declaration (nil outside one).
func forEachCall(files map[string]*ast.File, visit func(enclosing *ast.FuncDecl, call *ast.CallExpr)) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, decl := range files[name].Decls {
			fd, _ := decl.(*ast.FuncDecl)
			ast.Inspect(decl, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					visit(fd, call)
				}
				return true
			})
		}
	}
}

// packageLevelNames collects the names the package declares at top level.
func packageLevelNames(files map[string]*ast.File) map[string]bool {
	names := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					names[name.Name] = true
				}
			}
		}
	}
	return names
}

// policyNames returns the accepted policy constants, sorted, for a failure
// message that lists the real remedies rather than a stale hand-typed set.
func policyNames() []string {
	out := make([]string, 0, len(timeoutPolicies))
	for name := range timeoutPolicies {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// calleeName returns the bare function name of a call target: `f`, `x.f`, or
// `x.y.f` all yield `f`.
func calleeName(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	default:
		return ""
	}
}

// exprText renders an expression back to source for a failure message, so the
// report names the offending spelling and not just a position.
func exprText(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "<unprintable>"
	}
	return b.String()
}

// parsePackageSource parses the package's own non-test sources, keyed by file
// name. Shared with specsMissingField (editablewiring_test.go): these rules read
// the source because they pin wiring, not behavior.
func parsePackageSource(t *testing.T, fset *token.FileSet) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	files := map[string]*ast.File{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = file
	}
	if len(files) == 0 {
		t.Fatal("no package sources found; this rule has stopped checking anything")
	}
	return files
}
