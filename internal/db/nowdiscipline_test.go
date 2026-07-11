package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncedAtStampsUseDBNow enforces the write-side time invariant: every
// SyncedAt / DetailSyncedAt stamp persisted to SQLite must come from db.Now()
// (UTC), never a bare time.Now(). SQLite orders timestamp TEXT
// lexicographically and the driver binds a time.Time with its zone offset, so
// a local-zone stamp misorders against UTC cutoff strings — reconcile's
// cutoff-before-fetch prune pattern silently deletes fresh rows east of UTC.
//
// This is the persisted-stamp sibling of the sync worker's clock-seam grep
// rule (internal/sync/clock.go). Two shapes are flagged across all non-test
// sources under internal/:
//
//  1. direct:   SyncedAt: time.Now()
//  2. indirect: a function that both calls bare time.Now() and builds a
//     composite literal with a SyncedAt/DetailSyncedAt field (the
//     now := time.Now(); ...; SyncedAt: now pattern)
//
// If shape 2 ever false-positives (a function legitimately using time.Now()
// for display while also stamping via db.Now()), hoist the display time into
// a helper rather than weakening this test.
func TestSyncedAtStampsUseDBNow(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	var violations []string
	fset := token.NewFileSet()

	err = filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var timeNowPos []token.Pos
			var stampPos []token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
						if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && sel.Sel.Name == "Now" {
							timeNowPos = append(timeNowPos, node.Pos())
						}
					}
				case *ast.KeyValueExpr:
					if key, ok := node.Key.(*ast.Ident); ok &&
						(key.Name == "SyncedAt" || key.Name == "DetailSyncedAt") {
						stampPos = append(stampPos, node.Pos())
					}
				}
				return true
			})
			if len(timeNowPos) > 0 && len(stampPos) > 0 {
				violations = append(violations,
					fset.Position(stampPos[0]).String()+
						" — function "+fn.Name.Name+" stamps SyncedAt and calls bare time.Now() (at "+
						fset.Position(timeNowPos[0]).String()+"); stamp via db.Now() instead")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, v := range violations {
		t.Error(v)
	}
}
