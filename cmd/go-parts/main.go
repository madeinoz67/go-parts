// Package main is the go-parts entry point. The cobra root command holds the
// version flag and the persistent --data-dir; the start/stop/status
// subcommands drive the daemon lifecycle (Task 11).
//
// Lifecycle wiring (§5.12 CLI conventions, mirrors go-rag/MuninnDB):
//
//   - start: load config (Default + overlay), apply --bind if given, call
//     daemon.Run (foreground server; storage.Open inside Run bootstraps the
//     §5.13 schema before the listener binds).
//   - stop: daemon.Stop — read the state file, SIGTERM the PID.
//   - status: daemon.Status — read the state file, check PID liveness,
//     print the State JSON to stdout (one line, machine-parseable).
//
// The persistent --data-dir applies to every subcommand; status also has a
// --json flag (no-op today since output is already JSON-only, but reserved
// for the §5.12 "human format by default, --json for machine" rule).
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/config"
	"github.com/madeinoz67/go-parts/internal/daemon"
	"github.com/madeinoz67/go-parts/internal/parts"
)

var version = "dev"

func main() {
	root, _ := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newRootCmd builds the cobra root (version flag, the persistent --data-dir,
// and every subcommand), returning it plus the dataDir binding the
// subcommands share. Factored out of main so docs_cli_test.go can walk the
// REAL command tree and diff it against docs/guide/cli.md — construction is
// side-effect-free (stores open at RunE time, never at wiring time).
func newRootCmd() (*cobra.Command, *string) {
	var dataDir string

	root := &cobra.Command{
		Use:     "go-parts",
		Short:   "Electronics parts database",
		Version: version,
	}
	root.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data directory (default ~/.go-parts)")

	root.AddCommand(newStartCmd(&dataDir))
	root.AddCommand(newLocationsCmd(&dataDir))
	root.AddCommand(newViaCmd(&dataDir))          // Slice 4: generic Via resolver (§5.17)
	root.AddCommand(newReindexCmd(&dataDir))      // RedTeam: FTS reindex recovery
	root.AddCommand(newDedupeReportCmd(&dataDir)) // identity uniqueness: collision report (read-only)
	root.AddCommand(newFixQtyCmd(&dataDir))       // Flat-locations: QtyOnHand re-derive
	root.AddCommand(newStopCmd(&dataDir))
	root.AddCommand(newStatusCmd(&dataDir))

	return root, &dataDir
}

// newStartCmd builds the `start` subcommand. start runs the foreground server
// over the loaded config; --bind overrides the config's Bind. The server
// does not return until SIGINT/SIGTERM arrives.
func newStartCmd(dataDir *string) *cobra.Command {
	var bind string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the go-parts foreground server (Ctrl-C to stop)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*dataDir)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if bind != "" {
				cfg.Bind = bind
			}
			return daemon.Run(cfg)
		},
	}
	cmd.Flags().StringVar(&bind, "bind", "", "bind address (default 127.0.0.1:7890)")
	return cmd
}

// newStopCmd builds the `stop` subcommand. stop signals the running daemon
// (SIGTERM) via the PID recorded in the state file. A not-running data dir
// surfaces the daemon package's error verbatim.
func newStopCmd(dataDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running go-parts daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Stop(*dataDir)
		},
	}
}

// newStatusCmd builds the `status` subcommand. status prints the State JSON
// to stdout (one line). The --json flag is reserved for a future human-format
// default + --json-for-machine split (§5.12); today both modes emit JSON.
func newStatusCmd(dataDir *string) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report the daemon's running state, bind address, and PID",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := daemon.Status(*dataDir)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(st)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", true, "emit JSON (reserved: future human format default)")
	return cmd
}

// newReindexCmd builds `go-parts reindex` — rebuilds the BM25 search index from
// the parts store. Fixes parts persisted but unsearchable (a SIGKILL/OOM between
// writePartsKey + fts.Index — the two-write partial-failure gap the RedTeam
// flagged). Parts already correctly indexed are no-ops (the FTS idempotency
// guard). Holds the Pebble flock — stop the daemon first.
func newReindexCmd(dataDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild the BM25 search index from the parts store",
		RunE: func(cmd *cobra.Command, args []string) error {
			ps, _, _, _, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			n := ps.ReindexAll()
			fmt.Printf("reindexed %d parts\n", n)
			return nil
		},
	}
}

// newFixQtyCmd builds `go-parts fix-qty` — re-derives every part's QtyOnHand
// from the component store (sum of Component.Quantity across all locations).
// A part with stock but ZERO Component records is pre-redesign legacy stock:
// it is skipped and reported (never silently zeroed) unless --force is passed.
// Holds the Pebble flock — stop the daemon first.
func newFixQtyCmd(dataDir *string) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "fix-qty",
		Short: "Re-derive all parts' QtyOnHand from component quantities",
		RunE: func(cmd *cobra.Command, args []string) error {
			ps, _, cs, _, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			fixed, skipped, err := runFixQty(ps, cs, force)
			if err != nil {
				return err
			}
			fmt.Printf("fixed %d parts (QtyOnHand re-derived from components)\n", fixed)
			if skipped > 0 {
				fmt.Printf("skipped %d parts with stock but no component records (pre-redesign legacy stock; re-stock into a location, or pass --force to zero)\n", skipped)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "zero legacy stock (parts with QtyOnHand but no component records)")
	return cmd
}

// runFixQty re-derives every part's QtyOnHand from its Component set. A part
// with stock but zero Component records is pre-redesign legacy stock — the
// derivation has nothing to sum, so unless force is set such parts are skipped
// and counted, never silently zeroed.
func runFixQty(ps *parts.Store, cs *components.Store, force bool) (fixed, skipped int, err error) {
	for _, p := range ps.List() {
		total := 0
		for _, c := range cs.FindByPart(p.ID) {
			total += c.Quantity
		}
		// Legacy stock: quantity recorded pre-redesign with no Component
		// rows to derive from. Zeroing it would destroy the only record of
		// that stock — skip and report unless the operator forced it.
		if p.QtyOnHand > 0 && total == 0 && !force {
			skipped++
			continue
		}
		if p.QtyOnHand != total {
			if err := ps.SetQty(p.ID, total); err != nil {
				return fixed, skipped, fmt.Errorf("fix %s: %w", p.ID, err)
			}
			fixed++
		}
	}
	return fixed, skipped, nil
}
