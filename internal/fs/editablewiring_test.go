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
)

// The two halves of serve-your-own-writes are wired per editable file, and #387
// is what happens when one half is forgotten: a comment/doc/label/milestone .md
// armed no pin and consulted none, so a write survived only as long as its node
// did — an agent that wrote a comment and re-read it after a dentry forget saw
// Linear's render and reported a byte-count mismatch on a write that succeeded.
//
// This rule pins the wiring itself rather than the behavior, because the
// behavior needs a dentry forget inside a 10s window, which a test cannot force
// through the kernel reliably (the same constraint that keeps #388 a known
// bound). What it can guarantee is that the arming half cannot be omitted
// quietly; the consuming half is a compile-time `var _ editableFile` assertion
// carried next to each node type (see nodeattr.go's editableFile).

// TestEverySpecSetsPinIno is the arming half. A pin is armed only when a spec
// carries a nonzero pinIno, and the field is easy to leave out — it was left out
// of all four collection files for exactly that reason (#387). Rather than trust
// review to catch the fifth, read the package's own source and require every
// editFlushSpec literal to set it.
//
// A future editable file that genuinely must not pin (one not built through
// newFileInode, whose pin nothing would ever read) should set `pinIno: 0`
// explicitly with a comment saying why: that keeps the omission deliberate and
// visible, which is all this rule asks for.
func TestEverySpecSetsPinIno(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	var missing []string
	found := 0

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			// editFlushSpec[api.Comment]{...} — a generic type instantiation.
			idx, ok := lit.Type.(*ast.IndexExpr)
			if !ok {
				return true
			}
			ident, ok := idx.X.(*ast.Ident)
			if !ok || ident.Name != "editFlushSpec" {
				return true
			}
			found++
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "pinIno" {
					return true
				}
			}
			missing = append(missing, fset.Position(lit.Pos()).String())
			return true
		})
	}

	if found == 0 {
		t.Fatal("no editFlushSpec literals found; this rule has stopped checking anything")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("editFlushSpec literals with no pinIno (their writes would not survive the node — see #387):\n  %s",
			strings.Join(missing, "\n  "))
	}
}
