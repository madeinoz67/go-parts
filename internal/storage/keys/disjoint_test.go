package keys

import "testing"

// Single-table disjointness guard (registry charter). Add every public prefix
// here. A collision is a blocking review finding.
func TestPrefixesDisjointFromTable(t *testing.T) {
	prefixes := []byte{
		partsPrefix, metaPrefix, viaPrefix,
		ftsPostingPrefix, ftsIndexedPrefix, ftsGlobalStatsPrefix,
	}
	seen := map[byte]bool{}
	for _, p := range prefixes {
		if seen[p] {
			t.Fatalf("prefix byte 0x%02x registered twice", p)
		}
		seen[p] = true
	}
}
