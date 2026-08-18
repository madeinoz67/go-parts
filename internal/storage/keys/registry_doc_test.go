package keys

// registry_doc_test.go — the docs-vs-code drift gate (keyspace half).
//
// docs/internals/keyspace-registry.md is the single source of truth for the
// Pebble prefix bytes ("every new assignment must be registered here as it
// lands"). The registry's own disjointness test catches collisions; THIS test
// catches the other half of the contract — a prefix allocated in this package
// but missing from the registry's Allocated line, or vice versa. Both are
// blocking review findings per CLAUDE.md §2; this makes them red tests
// instead of hoping the reviewer remembers.
//
// Parse contracts: prefix consts are hex OR decimal literals (a decimal const
// is converted before comparison — adversary F3: `byte = 32` was invisible to
// a hex-only regex), every byte-typed const in this package must be named
// *Prefix and be compared (the loud shape guard fails on anything else rather
// than shrinking silently), and the registry's Allocated line must enumerate
// every byte individually — no ranges (`0x05–0x07` reads as just its
// endpoints; adversary F10).

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	codePrefixRe  = regexp.MustCompile(`(\w*Prefix)\s+byte\s*=\s*(0x[0-9A-Fa-f]+|\d+)`)
	byteConstRe   = regexp.MustCompile(`(?m)^\s*(\w+)\s+byte\s*=`)
	hexTokenRe    = regexp.MustCompile(`0x[0-9A-Fa-f]+`)
	allocatedLine = regexp.MustCompile(`(?m)^Allocated:.*$`)
)

// normalize folds a literal to canonical form for comparison: lowercase 0x
// prefix, uppercase digits; decimals are converted (32 → 0x20).
func normalize(lit string) string {
	if !strings.HasPrefix(lit, "0x") {
		if n, err := strconv.Atoi(lit); err == nil {
			return fmt.Sprintf("0x%02X", n)
		}
		return lit
	}
	return "0x" + strings.ToUpper(strings.TrimPrefix(lit, "0x"))
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
	src := buf.String()

	code := map[string]string{} // byte -> const name
	byName := map[string]bool{} // const names the comparison saw
	for _, m := range codePrefixRe.FindAllStringSubmatch(src, -1) {
		if prev, dup := code[normalize(m[2])]; dup {
			t.Fatalf("%s and %s both claim %s — the disjointness test owns this, but doc-drift cannot proceed", prev, m[1], m[2])
		}
		code[normalize(m[2])] = m[1]
		byName[m[1]] = true
	}
	if len(code) == 0 {
		t.Fatal("no prefix consts parsed from this package's sources — parser rotted?")
	}

	// Loud shape guard (adversary F3): every byte-typed const here is a
	// keyspace prefix by convention. One named outside the *Prefix shape, or
	// in the shape but missed by the comparison regex, fails loud instead of
	// silently shrinking what the gate sees.
	for _, m := range byteConstRe.FindAllStringSubmatch(src, -1) {
		if !strings.HasSuffix(m[1], "Prefix") {
			t.Fatalf("byte const %s does not end in 'Prefix' — it is either an unregistered keyspace (register it in the registry and Allocated line) or misnamed so the drift comparison cannot see it", m[1])
		}
		if !byName[m[1]] {
			t.Fatalf("byte const %s was not captured by the prefix comparison — parser rot; fix the regex in this test (a grouped one-line decl (a, b byte = 0x20, 0x21) or a computed literal (base+2) also lands here: split the declaration or use a plain literal)", m[1])
		}
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
		t.Errorf("keyspace registry drift:\n  UNREGISTERED (in this package's prefix consts, missing from the Allocated line):\n    %s\n  GHOST (in the Allocated line, no const claims it):\n    %s",
			strings.Join(undocumented, "\n    "), strings.Join(ghost, "\n    "))
	}
}
