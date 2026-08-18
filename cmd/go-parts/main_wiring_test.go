package main

// main_wiring_test.go — the main() seam guard (adversary F1, 2026-08-18).
//
// docs_cli_test.go walks newRootCmd()'s tree, so its "the REAL cobra tree"
// claim holds only while main() wires nothing itself: a command added directly
// in main() ships undocumented with every gate green. This test fails loud on
// that seam — main() must contain no AddCommand/Flags/PersistentFlags selector
// calls; wiring belongs in newRootCmd().
//
// Residual, disclosed: an aliased indirect (add := root.AddCommand; add(...))
// evades the selector-name check; there is no package-level root to guard, so
// the pathological path is unguardable at this cost.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestMainWiresNothing(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "AddCommand", "Flags", "PersistentFlags":
					t.Errorf("%s: main() wires the cobra surface directly (%s) — move it to newRootCmd() or the drift gate goes blind to it", fset.Position(sel.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}
}
