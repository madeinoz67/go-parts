package main

// tui.go — the terminal surface (PRD §5.11): a Bubble Tea REST client of the
// running daemon, never a second Pebble opener. The §5.12 shape: one top-level
// verb like start/stop/status; --host overrides the configured bind the same
// way start's --bind does (same config.Load resolution, http:// prepended).

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/madeinoz67/go-parts/internal/config"
	"github.com/madeinoz67/go-parts/internal/tui"
)

// newTUICmd builds the `tui` subcommand — the alt-screen split view (filter +
// table over detail pane) over the running daemon. Startup reachability fails
// FAST with the fix, not a blank TUI: spec §6. Stats is the cheapest GET the
// daemon serves, so it doubles as the liveness probe.
func newTUICmd(dataDir *string) *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Terminal UI — search, browse, adjust stock (needs a running server)",
		RunE: func(_ *cobra.Command, _ []string) error {
			addr := host
			if addr == "" {
				cfg, err := config.Load(*dataDir)
				if err != nil {
					return fmt.Errorf("load config: %w", err)
				}
				addr = cfg.Bind
			}
			base := "http://" + addr
			c := tui.NewClient(base)
			if _, err := c.Stats(); err != nil {
				fmt.Fprintf(os.Stderr, "go-parts: cannot reach %s — is `go-parts start` running? (%v)\n", base, err)
				os.Exit(1)
			}
			p := tea.NewProgram(tui.NewProgramModel(c), tea.WithAltScreen())
			_, err := p.Run()
			return err
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "go-parts server address host:port (default: the configured bind)")
	return cmd
}
