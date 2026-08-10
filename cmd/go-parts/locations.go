package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/storage"
	"github.com/madeinoz67/go-parts/internal/via"
	"github.com/spf13/cobra"
)

// openLocations opens the store directly for a one-shot CLI op. It holds the
// Pebble flock, so it FAILS if the daemon is running (v1 single-operator
// posture: stop the daemon for bulk CLI ops, or use the future web UI). The
// via.Store is the only one in this process — the via-singleton invariant
// (one *via.Store per DB per process) holds.
func openLocations(dataDir string) (*locations.Store, func(), error) {
	storeDB, err := storage.Open(dataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("open store: %w", err)
	}
	cleanup := func() { storeDB.Close() }
	viaStore := via.NewStore(storeDB.DB)
	return locations.NewStore(storeDB.DB, viaStore), cleanup, nil
}

func newLocationsCmd(dataDir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "locations",
		Short: "Manage physical storage locations (§7.1)",
	}
	cmd.AddCommand(newLocationsAddCmd(dataDir))
	cmd.AddCommand(newLocationsListCmd(dataDir))
	cmd.AddCommand(newLocationsRemoveCmd(dataDir))
	cmd.AddCommand(newLocationsTreeCmd(dataDir))
	return cmd
}

func newLocationsAddCmd(dataDir *string) *cobra.Command {
	var (
		parent         string
		singlePartOnly bool
		notes          string
	)
	cmd := &cobra.Command{
		Use:   "add --label \"Bin A3\" [--parent ID] [--single-part-only] [--notes ...]",
		Short: "Create a single location",
		RunE: func(cmd *cobra.Command, args []string) error {
			label, _ := cmd.Flags().GetString("label")
			if label == "" {
				return fmt.Errorf("--label is required")
			}
			s, cleanup, err := openLocations(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			l := &locations.Location{
				Label:          label,
				ParentID:       parent,
				SinglePartOnly: singlePartOnly,
				Notes:          notes,
			}
			if err := s.Create(l); err != nil {
				return err
			}
			fmt.Printf("created %s  via=%s  id=%s\n", l.Label, l.ViaCode, l.ID)
			return nil
		},
	}
	cmd.Flags().String("label", "", "location label (e.g. \"Bin A3\")")
	cmd.Flags().StringVar(&parent, "parent", "", "parent location id (nested storage)")
	cmd.Flags().BoolVar(&singlePartOnly, "single-part-only", false, "bin holds only one part type")
	cmd.Flags().StringVar(&notes, "notes", "", "free-text notes")
	return cmd
}

func newLocationsListCmd(dataDir *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list [--json]",
		Short: "List all locations",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cleanup, err := openLocations(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			all := s.List()
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(all)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "LABEL\tVIA\tID\tPARENT")
			for _, l := range all {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", l.Label, l.ViaCode, l.ID, l.ParentID)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func newLocationsRemoveCmd(dataDir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <id|viacode>",
		Short: "Remove a location (refuses if it has children)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cleanup, err := openLocations(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			id := args[0]
			// Accept a Via code too (L-...): resolve it.
			if len(id) > 2 && id[:2] == "L-" {
				l, err := s.ByVia(id)
				if err != nil {
					return err
				}
				id = l.ID
			}
			if err := s.Delete(id); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", id)
			return nil
		},
	}
	return cmd
}

func newLocationsTreeCmd(dataDir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Print the location hierarchy",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cleanup, err := openLocations(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			all := s.List()
			byParent := map[string][]*locations.Location{}
			for _, l := range all {
				byParent[l.ParentID] = append(byParent[l.ParentID], l)
			}
			var walk func(parentID string, depth int)
			walk = func(parentID string, depth int) {
				for _, l := range byParent[parentID] {
					for i := 0; i < depth; i++ {
						fmt.Print("  ")
					}
					fmt.Printf("- %s (%s)\n", l.Label, l.ViaCode)
					walk(l.ID, depth+1)
				}
			}
			walk("", 0)
			return nil
		},
	}
	return cmd
}
