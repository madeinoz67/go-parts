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

	"github.com/madeinoz67/go-parts/internal/config"
	"github.com/madeinoz67/go-parts/internal/daemon"
)

var version = "dev"

func main() {
	var dataDir string

	root := &cobra.Command{
		Use:     "go-parts",
		Short:   "Electronics parts database",
		Version: version,
	}
	root.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data directory (default ~/.go-parts)")

	root.AddCommand(newStartCmd(&dataDir))
	root.AddCommand(newStopCmd(&dataDir))
	root.AddCommand(newStatusCmd(&dataDir))
	root.AddCommand(newLocationsCmd(&dataDir))
	root.AddCommand(newViaCmd(&dataDir)) // Slice 4: generic Via resolver (§5.17)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
