package rest

// routes_doc_test.go — the docs-vs-code drift gate (REST half).
//
// docs/reference/api.md's Routes table is the contract for the HTTP surface;
// this test parses the HandleFunc registrations out of server.go (the file
// this package lives in — no reflection needed, ServeMux exposes no pattern
// list) and diffs them against the documented rows in BOTH directions: an
// undocumented route is a finding, and so is a documented route the server no
// longer registers (the POST /parts/{id}/stock class of drift that survived
// the flat-model pivot until a manual sweep caught it).
//
// The doc's parse contract: rows look like "| `GET` | `/parts/{id}` | …" in
// the Routes table; query strings ("?q=…") are stripped; {param} placeholders
// must match the registration's placeholders verbatim ({id}, {partId},
// {code}).

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	codeRouteRe = regexp.MustCompile(`HandleFunc\("([A-Z]+)\s+([^"]+)"`)
	// The path class excludes backticks and whitespace so a match can never
	// run past its own table cell (a bare [^?] crosses newlines greedily).
	docRouteRe = regexp.MustCompile(`(?m)^\|\s*` + "`" + `([A-Z]+)` + "`" + `\s*\|\s*` + "`" + `(/[^\s` + "`" + `]+)` + "`")
)

// codeRoutes reads server.go next to this test and extracts the registered
// "METHOD /path" set.
func codeRoutes(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range codeRouteRe.FindAllStringSubmatch(string(src), -1) {
		out[m[1]+" "+m[2]] = true
	}
	if len(out) == 0 {
		t.Fatal("no HandleFunc registrations parsed from server.go — parser rotted?")
	}
	return out
}

// docRoutes reads ../../docs/reference/api.md and extracts the Routes table's
// "METHOD /path" set (query strings stripped).
func docRoutes(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("../../docs/reference/api.md")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range docRouteRe.FindAllStringSubmatch(string(src), -1) {
		path := strings.SplitN(m[2], "?", 2)[0] // "/parts?q=…" documents "/parts"
		out[m[1]+" "+path] = true
	}
	if len(out) == 0 {
		t.Fatal("no route rows parsed from api.md — parser rotted?")
	}
	return out
}

func TestRESTDocMatchesRoutes(t *testing.T) {
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
		t.Errorf("REST docs-vs-code drift:\n  UNDOCUMENTED (registered in server.go, not in api.md):\n    %s\n  GHOST (in api.md, not registered):\n    %s",
			strings.Join(undocumented, "\n    "), strings.Join(ghost, "\n    "))
	}
}
