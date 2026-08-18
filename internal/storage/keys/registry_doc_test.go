package keys

// registry_doc_test.go — the docs-vs-code drift gate (keyspace half).
//
// docs/internals/keyspace-registry.md is the single source of truth for the
// Pebble prefix bytes ("every new assignment must be registered here as it
// lands"). The registry's own disjointness test catches collisions; THIS test
// catches the other half of the contract — a prefix allocated in keys.go but
// missing from the registry's Allocated line, or vice versa (a registry entry
// for a byte no const claims). Both are blocking review findings per
// CLAUDE.md §2; this makes them red tests instead of hoping the reviewer
// remembers.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	codePrefixRe  = regexp.MustCompile(`(\w*Prefix)\s+byte\s*=\s*(0x[0-9A-Fa-f]+)`)
	hexTokenRe    = regexp.MustCompile(`0x[0-9A-Fa-f]+`)
	allocatedLine = regexp.MustCompile(`(?m)^Allocated:.*$`)
)

// normalize folds a hex literal to canonical form for comparison: lowercase
// 0x prefix, uppercase digits (0xf0 == 0xF0 == 0xF0).
func normalize(h string) string {
	return "0x" + strings.ToUpper(strings.TrimPrefix(h, "0x"))
}

func TestRegistryDocMatchesPrefixes(t *testing.T) {
	// All non-test .go files in the package, not just keys.go: if prefixes
	// ever split across files, a single-file read would keep passing while
	// missing the new ones (partial-parse rot).
	des, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	for _, de := range des {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".go") || strings.HasSuffix(de.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(de.Name())
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	code := map[string]string{} // byte -> const name
	for _, m := range codePrefixRe.FindAllStringSubmatch(buf.String(), -1) {
		if prev, dup := code[normalize(m[2])]; dup {
			t.Fatalf("keys.go: %s and %s both claim %s — the disjointness test owns this, but doc-drift cannot proceed", prev, m[1], m[2])
		}
		code[normalize(m[2])] = m[1]
	}
	if len(code) == 0 {
		t.Fatal("no prefix consts parsed from keys.go — parser rotted?")
	}

	reg, err := os.ReadFile("../../../docs/internals/keyspace-registry.md")
	if err != nil {
		t.Fatal(err)
	}
	alloc := allocatedLine.FindString(string(reg))
	if alloc == "" {
		t.Fatal("no 'Allocated:' line in keyspace-registry.md — the registry's summary is the parse contract")
	}
	doc := map[string]bool{}
	for _, tok := range hexTokenRe.FindAllString(alloc, -1) {
		doc[normalize(tok)] = true
	}

	var undocumented, ghost []string
	for b, name := range code {
		if !doc[b] {
			undocumented = append(undocumented, b+" ("+name+")")
		}
	}
	for b := range doc {
		if _, ok := code[b]; !ok {
			ghost = append(ghost, b)
		}
	}
	if len(undocumented) > 0 || len(ghost) > 0 {
		sort.Strings(undocumented)
		sort.Strings(ghost)
		t.Errorf("keyspace registry drift:\n  UNREGISTERED (in keys.go, missing from the Allocated line):\n    %s\n  GHOST (in the Allocated line, no keys.go const):\n    %s",
			strings.Join(undocumented, "\n    "), strings.Join(ghost, "\n    "))
	}
}
