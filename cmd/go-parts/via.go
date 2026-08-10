package main

import (
	"encoding/json"
	"os"

	"github.com/madeinoz67/go-parts/internal/link"
	"github.com/spf13/cobra"
)

// newViaCmd builds `go-parts via <code>` (§5.17) — the generic Via resolver at
// the CLI. Resolves a P- or L- code to its entity (Scan-to-Find): a location
// resolves WITH its embedded contents, a part to itself. Output is JSON (the
// resolver is machine-oriented — same shape as GET /via/{code}); pipe through
// jq for a human view. Uses openStores so the same stores + injected policy the
// other cross-entity CLI ops use are in play.
func newViaCmd(dataDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "via <code>",
		Short: "Resolve a Via code (P-/L-) to its entity — scan-to-find (§5.17)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ps, ls, vs, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			res, err := link.Resolve(vs, ps, ls, args[0])
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(res)
		},
	}
}
