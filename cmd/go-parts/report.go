package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// newDedupeReportCmd builds `go-parts dedupe-report` — a READ-ONLY scan of the
// parts corpus listing MPN / LocalNumber collisions (schema v5 identity
// uniqueness). A pre-v5 store may legitimately contain duplicates; the v5
// backfill indexes first-writer-wins, so duplicate losers stay unindexed.
// This report reads the RECORDS (not the index), names every colliding group
// with its part ids, and never mutates — repair is the operator's edit
// (Update re-reserves identities correctly once the collision is resolved).
func newDedupeReportCmd(dataDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "dedupe-report",
		Short: "List duplicate MPN / local-number values",
		Long: `List duplicate MPN / local-number values.

Scans every part record and groups collisions by (whitespace-trimmed)
value, printing the value and the ids of every part carrying it. The
RECORDS are never modified, but opening the store runs the normal open
path (schema migration + the idempotent identity-index backfill) — this is
not a read-only open. Fix a collision by editing the losing part's MPN or
local number (the store re-reserves identities on Update).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ps, _, _, _, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			groups := [2]map[string][]string{{}, {}}
			kinds := [2]string{"MPN", "local number"}
			for _, p := range ps.List() {
				// Group on TRIMMED values, matching the write path's
				// normalization — a legacy " PAD-1 " and a new "PAD-1" are the
				// same identity (adversary finding 6).
				if mpn := strings.TrimSpace(p.MPN); mpn != "" {
					groups[0][mpn] = append(groups[0][mpn], p.ID)
				}
				if ln := strings.TrimSpace(p.LocalNumber); ln != "" {
					groups[1][ln] = append(groups[1][ln], p.ID)
				}
			}
			dupes := 0
			for kind, m := range groups {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					if len(m[k]) > 1 {
						dupes++
						fmt.Printf("DUPLICATE %s %q: %s\n", kinds[kind], k, strings.Join(m[k], ", "))
					}
				}
			}
			if dupes == 0 {
				fmt.Println("no duplicate MPN or local-number values")
			}
			return nil
		},
	}
}
