package fs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// #414 and #449 are the same defect found twice: a node build site wrote its
// kernel timeout as an inline duration (`30*time.Second`) instead of reading the
// mount's configured policy, so `WithKernelCacheTimeouts` silently governed
// nothing beneath it — a short-timeout fixture looked like a broken refresh path
// rather than a timeout that never applied. #414 fixed the six directory sites;
// #449 fixed the four render-file ones. Nothing stood between the two but grep,
// and `grep '30 * time.Second'` also matches request timeouts and retry ladders.
//
// This rule pins the wiring the way TestEverySpecSetsPinIno does: the timeout
// argument at every build site must be a NAMED policy, never a literal, and each
// name says what it means at the call site: `lfs.entryTimeout()` (the mount's
// configured bound), `inheritTimeout` (leave the mount default untouched),
// `noKernelCache` (0: the kernel may not cache this at all), and
// `editableFileTimeout` (the short editable-`.md` tier). A fifth class, should
// one ever be justified, is a new named constant with a comment saying why —
// which is the whole ask: deliberate, and greppable by name.

// timeoutArg names each node build helper and the index of its timeout
// parameter. Every helper that hands the kernel a caching bound belongs here;
// a new one that does not is the next place this defect can hide, so the test
// also asserts each name below still exists in the package.
var timeoutArg = map[string]int{
	// newDirInode(ctx, out, name, child, na, ino, timeout)
	"newDirInode": 6,
	// newFileInode(ctx, out, name, child, na, ino, timeout)
	"newFileInode": 6,
	// newRenderInode(ctx, out, name, child, ino, timeout)
	"newRenderInode": 5,
	// lookupRenderFile(ctx, out, name, render, ino, timeout)
	"lookupRenderFile": 5,
	// mountRenderFile(ctx, parent, name, render, ino, timeout, out)
	"mountRenderFile": 5,
	// newDirManifest(parent, id, created, updated, timeout) — feeds all of the
	// above for the entity directories that build through a manifest.
	"newDirManifest": 4,
}

func TestNoHardcodedKernelTimeouts(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	files := parsePackageSource(t, fset)

	seen := map[string]int{}
	var offenders []string

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn := calleeName(call.Fun)
			idx, guarded := timeoutArg[fn]
			if !guarded || idx >= len(call.Args) {
				return true
			}
			seen[fn]++
			if !isNamedTimeout(call.Args[idx]) {
				offenders = append(offenders, fset.Position(call.Args[idx].Pos()).String()+
					" ("+fn+")")
			}
			return true
		})
	}

	for fn := range timeoutArg {
		if seen[fn] == 0 {
			t.Errorf("no call sites found for %s; it was renamed or removed and this rule "+
				"has stopped checking it", fn)
		}
	}

	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("node build sites passing a literal kernel timeout instead of a named policy "+
			"(WithKernelCacheTimeouts cannot govern these — see #414/#449; use lfs.entryTimeout(), "+
			"inheritTimeout, noKernelCache, editableFileTimeout, or add a named constant):\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestNamedTimeoutPolicies pins what the three mount-independent names mean, so
// a future edit cannot quietly redefine "no caching" as something that caches.
func TestNamedTimeoutPolicies(t *testing.T) {
	t.Parallel()

	if inheritTimeout >= 0 {
		t.Errorf("inheritTimeout = %v; must be negative — the build helpers apply any value >= 0",
			inheritTimeout)
	}
	if noKernelCache != 0 {
		t.Errorf("noKernelCache = %v; must be 0", noKernelCache)
	}
	if editableFileTimeout <= 0 || editableFileTimeout >= DefaultEntryTimeout {
		t.Errorf("editableFileTimeout = %v; must be a positive bound shorter than the %v default",
			editableFileTimeout, DefaultEntryTimeout)
	}
}

// TestEntryTimeoutHonorsConfiguredZero pins the other half of #449: a mount that
// configured 0 asked for "no kernel entry caching", and until the field became a
// pointer the <= 0 guard read that as "never mounted" and handed back 30s — so
// WithKernelCacheTimeouts(0, 0) disabled caching at inheritTimeout sites and
// silently did not at every site routed through this helper.
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
}

// isNamedTimeout reports whether a timeout argument names a policy rather than
// spelling a duration inline. Accepted: a bare identifier (inheritTimeout,
// noKernelCache, editableFileTimeout), a selector (m.timeout — a field already
// filled from a named policy), or a call (lfs.entryTimeout()). Rejected: a basic
// literal and any arithmetic over one, which is every spelling the defect took.
func isNamedTimeout(arg ast.Expr) bool {
	switch e := arg.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.CallExpr:
		return true
	case *ast.ParenExpr:
		return isNamedTimeout(e.X)
	default:
		return false
	}
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

// parsePackageSource parses the package's own non-test sources, keyed by file
// name. Shared shape with specsMissingField (editablewiring_test.go): these
// rules read the source because they pin wiring, not behavior.
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
