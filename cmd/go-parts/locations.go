package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/madeinoz67/go-parts/internal/config"
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
//
// The empty --data-dir default is resolved via config.Default BEFORE
// storage.Open, mirroring how `start` resolves it via config.Load — without
// this, `go-parts locations add` with no --data-dir fails (mkdir ""). See
// Slice 1 Gate fix-wave I1.
func openLocations(dataDir string) (*locations.Store, func(), error) {
	dataDir = config.Default(dataDir).DataDir
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
	cmd.AddCommand(newLocationsBulkCmd(dataDir))
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
		dryRun         bool
	)
	cmd := &cobra.Command{
		Use:   "add --label \"Bin A3\" [--parent ID] [--single-part-only] [--notes ...]",
		Short: "Create a single location",
		RunE: func(cmd *cobra.Command, args []string) error {
			label, _ := cmd.Flags().GetString("label")
			if label == "" {
				return fmt.Errorf("--label is required")
			}
			if dryRun {
				fmt.Printf("would create location label=%q parent=%q single-part-only=%v notes=%q\n", label, parent, singlePartOnly, notes)
				return nil
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
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without executing")
	return cmd
}

func newLocationsBulkCmd(dataDir *string) *cobra.Command {
	var (
		method             string
		prefix             string
		from, to           int    // row numeric range
		rowFrom, rowTo     string // grid/3d alpha rows
		colFrom, colTo     int    // grid/3d numeric cols
		levelFrom, levelTo int    // 3d numeric levels
		parent             string
		singlePartOnly     bool
		notes              string
		dryRun             bool
	)
	cmd := &cobra.Command{
		Use:   "bulk --method row|grid|3d --prefix box [--from/--to|--row-from/--row-to/--col-from/--col-to|--level-from/--level-to] [--parent ID]",
		Short: "Create many locations at once (row/grid/3d-grid, §7.1)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// The CLI surface is `--method 3d`; the canonical value in
			// internal/locations (GenerateLabels + BulkOpts.CreationMethod)
			// is "3d_grid". Normalize here so the adapter accepts the short
			// alias and the stored CreationMethod matches the canonical set
			// documented on BulkOpts.
			canonical := method
			if canonical == "3d" {
				canonical = "3d_grid"
			}
			p := locations.LabelParams{
				Prefix: prefix,
				From:   from, To: to,
				RowFrom: rowFrom, RowTo: rowTo, ColFrom: colFrom, ColTo: colTo,
				LevelFrom: levelFrom, LevelTo: levelTo,
			}
			labels, err := locations.GenerateLabels(canonical, p)
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Printf("--dry-run: would create %d location(s):\n", len(labels))
				for _, l := range labels {
					fmt.Println("  " + l)
				}
				return nil
			}
			s, cleanup, err := openLocations(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			created, err := s.CreateBulk(labels, locations.BulkOpts{
				ParentID:       parent,
				SinglePartOnly: singlePartOnly,
				Notes:          notes,
				CreationMethod: canonical,
			})
			for _, l := range created {
				fmt.Printf("created %s  via=%s  id=%s\n", l.Label, l.ViaCode, l.ID)
			}
			if err != nil {
				return fmt.Errorf("bulk create failed after %d ok: %w", len(created), err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&method, "method", "", "creation method: row|grid|3d (single uses `add`)")
	cmd.MarkFlagRequired("method")
	cmd.Flags().StringVar(&prefix, "prefix", "", "label prefix (e.g. box, shelf, rack)")
	cmd.Flags().IntVar(&from, "from", 0, "row: numeric range start (inclusive)")
	cmd.Flags().IntVar(&to, "to", 0, "row: numeric range end (inclusive)")
	cmd.Flags().StringVar(&rowFrom, "row-from", "", "grid/3d: first row letter (A-Z)")
	cmd.Flags().StringVar(&rowTo, "row-to", "", "grid/3d: last row letter (A-Z)")
	cmd.Flags().IntVar(&colFrom, "col-from", 0, "grid/3d: first column (inclusive)")
	cmd.Flags().IntVar(&colTo, "col-to", 0, "grid/3d: last column (inclusive)")
	cmd.Flags().IntVar(&levelFrom, "level-from", 0, "3d: first level (inclusive)")
	cmd.Flags().IntVar(&levelTo, "level-to", 0, "3d: last level (inclusive)")
	cmd.Flags().StringVar(&parent, "parent", "", "parent location id (nested storage)")
	cmd.Flags().BoolVar(&singlePartOnly, "single-part-only", false, "each bin holds only one part type")
	cmd.Flags().StringVar(&notes, "notes", "", "free-text notes applied to every row")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the labels that would be created without writing")
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
			if all == nil {
				all = []*locations.Location{} // emit [] not null on empty (jq-friendly)
			}
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
	var dryRun bool
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
			if dryRun {
				if n := len(s.Children(id)); n > 0 {
					fmt.Printf("would remove id=%s — REFUSE (%d children; reparent first)\n", id, n)
				} else {
					fmt.Printf("would remove id=%s\n", id)
				}
				return nil
			}
			if err := s.Delete(id); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without executing")
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
					for range depth {
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
