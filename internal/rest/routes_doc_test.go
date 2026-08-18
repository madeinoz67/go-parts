package rest

// routes_doc_test.go — the docs-vs-code drift gate (REST half).
//
// docs/reference/api.md's Routes table is the contract for the HTTP surface;
// this test parses the HandleFunc registrations out of this package's
// non-test sources and diffs them against the documented rows in BOTH
// directions: an undocumented route is a finding, and so is a documented route
// the server no longer registers (the POST /parts/{id}/stock class of drift
// that survived the flat-model pivot until a manual sweep caught it).
//
// Parse contracts: only rows in the "## Routes" section count (other
// route-shaped tables elsewhere in api.md are ignored); query strings
// ("?q=…") are stripped; {param} placeholders must match the registration's
// verbatim. The registration side requires a STRING LITERAL pattern —
// const-indirected routes fail loud via assertRoutePatternsLiteral (adversary
// F4: the regex cannot see them, and the gate would shrink silently after the
// doc row was "fixed" to match). A bare mux.Handle (none exists today) would
// need this parser AND the api.md table widened together.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go/ast"
	"go/parser"
	"go/token"
)

var (
	codeRouteRe = regexp.MustCompile(`HandleFunc\("([A-Z]+)\s+([^"]+)"`)
	// The path class excludes backticks and whitespace so a match can never
	// run past its own table cell (a bare [^?] crosses newlines greedily).
	docRouteRe = regexp.MustCompile(`(?m)^\|\s*` + "`" + `([A-Z]+)` + "`" + `\s*\|\s*` + "`" + `(/[^\s` + "`" + `]+)` + "`")
)

// packageGoFiles returns this package's non-test .go files. All of them, not
// just server.go: if routes ever split across files, a single-file read would
// keep passing while missing the new ones (partial-parse rot).
func packageGoFiles(t *testing.T) []string {
	t.Helper()
	des, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, de := range des {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".go") || strings.HasSuffix(de.Name(), "_test.go") {
			continue
		}
		out = append(out, de.Name())
	}
	return out
}

// assertRoutePatternsLiteral fails loud on any HandleFunc whose pattern
// argument is not a string literal (adversary F4).
func assertRoutePatternsLiteral(t *testing.T) {
	t.Helper()
	fset := token.NewFileSet()
	for _, name := range packageGoFiles(t) {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) == 0 {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); !ok || lit.Kind != token.STRING {
				t.Fatalf("%s: HandleFunc pattern is not a string literal — the drift test cannot see it; inline the literal", fset.Position(call.Args[0].Pos()))
			}
			return true
		})
	}
}

// codeRoutes extracts the registered "METHOD /path" set from this package's
// sources.
func codeRoutes(t *testing.T) map[string]bool {
	t.Helper()
	var src strings.Builder
	for _, name := range packageGoFiles(t) {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
		src.WriteByte('\n')
	}
	out := map[string]bool{}
	for _, m := range codeRouteRe.FindAllStringSubmatch(src.String(), -1) {
		out[m[1]+" "+m[2]] = true
	}
	if len(out) == 0 {
		t.Fatal("no HandleFunc registrations parsed from this package's sources — parser rotted?")
	}
	return out
}

// docRoutes reads ../../docs/reference/api.md and extracts the Routes
// table's "METHOD /path" set (query strings stripped). Only the "## Routes"
// section is parsed — route-shaped tables elsewhere in the file (planned
// sections, examples) don't count (adversary F9).
func docRoutes(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../docs/reference/api.md")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	inRoutes := false
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(ln, "## ") {
			inRoutes = strings.TrimSpace(ln[3:]) == "Routes"
			continue
		}
		if inRoutes {
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	out := map[string]bool{}
	for _, m := range docRouteRe.FindAllStringSubmatch(b.String(), -1) {
		path := strings.SplitN(m[2], "?", 2)[0] // "/parts?q=…" documents "/parts"
		out[m[1]+" "+path] = true
	}
	if len(out) == 0 {
		t.Fatal("no route rows parsed from api.md's Routes section — parser rotted, or the section header moved?")
	}
	return out
}

func TestRESTDocMatchesRoutes(t *testing.T) {
	assertRoutePatternsLiteral(t)
	code, doc := codeRoutes(t), docRoutes(t)
	var undocumented, ghost []string
	for r := range code {
		if !doc[r] {
			undocumented = append(undocumented, r)
		}
	}
	for r := range doc {
		if !code[r] {
			ghost = append(ghost, r)
		}
	}
	if len(undocumented) > 0 || len(ghost) > 0 {
		sort.Strings(undocumented)
		sort.Strings(ghost)
		t.Errorf("REST docs-vs-code drift:\n  UNDOCUMENTED (registered in internal/rest sources, not in api.md's Routes table):\n    %s\n  GHOST (in api.md's Routes table, not registered):\n    %s",
			strings.Join(undocumented, "\n    "), strings.Join(ghost, "\n    "))
	}
}
