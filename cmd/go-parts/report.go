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
		Short: "List duplicate MPN / local-number values (read-only)",
		Long: `List duplicate MPN / local-number values (read-only).

Scans every part record and groups collisions by value, printing the value
and the ids of every part carrying it. Never mutates anything; fix a
collision by editing the losing part's MPN or local number (the store
re-reserves identities on Update).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ps, _, _, _, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			groups := [2]map[string][]string{{}, {}}
			kinds := [2]string{"MPN", "local number"}
			for _, p := range ps.List() {
				if p.MPN != "" {
					groups[0][p.MPN] = append(groups[0][p.MPN], p.ID)
				}
				if p.LocalNumber != "" {
					groups[1][p.LocalNumber] = append(groups[1][p.LocalNumber], p.ID)
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
