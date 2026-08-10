package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/madeinoz67/go-parts/internal/config"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/label"
	"github.com/madeinoz67/go-parts/internal/link"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
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

// openStores opens BOTH stores + the shared via index for a cross-entity CLI
// op (delete-refuse-has-parts, the via resolver, labels). Same one-shot +
// via-singleton posture as openLocations, plus it injects link.NewPolicy so
// the single_part_only guard is live on part writes from the CLI too. Returns
// the via store too (Slice 4 — the resolver + the <id|viacode> label path need
// it); callers that don't need it discard it with _.
func openStores(dataDir string) (*parts.Store, *locations.Store, *via.Store, func(), error) {
	dataDir = config.Default(dataDir).DataDir
	storeDB, err := storage.Open(dataDir)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open store: %w", err)
	}
	cleanup := func() { storeDB.Close() }
	viaStore := via.NewStore(storeDB.DB)
	ps := parts.NewStore(storeDB.DB, index.NewFTS(storeDB.DB), viaStore)
	ls := locations.NewStore(storeDB.DB, viaStore)
	ps.SetLocationPolicy(link.NewPolicy(ps, ls))
	return ps, ls, viaStore, cleanup, nil
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
	cmd.AddCommand(newLocationsLabelCmd(dataDir))
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
		maxLabels          int
	)
	cmd := &cobra.Command{
		Use:   "bulk --method row|grid|3d --prefix box [--from/--to|--row-from/--row-to/--col-from/--col-to|--level-from/--level-to] [--parent ID]",
		Short: "Create many locations at once (row/grid/3d-grid, §7.1)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Case-fold before the 3d→3d_grid alias so --method ROW / 3D / Row
			// all work (Fix C / O4).
			canonical := strings.ToLower(method)
			// The CLI surface is `--method 3d`; the canonical value in
			// internal/locations (GenerateLabels + BulkOpts.CreationMethod)
			// is "3d_grid". Normalize here so the adapter accepts the short
			// alias and the stored CreationMethod matches the canonical set
			// documented on BulkOpts.
			if canonical == "3d" {
				canonical = "3d_grid"
			}
			// Single locations use `add`, not bulk — surface that clearly at
			// the CLI layer rather than letting it fall through to a confusing
			// GenerateLabels error (Fix G).
			if canonical == "single" {
				return fmt.Errorf("single locations use 'go-parts locations add', not bulk")
			}
			// Fat-finger guard: the --from/--to default of 0,0 silently
			// produces one "box0" for the row method. Require an explicit
			// range (Fix B / RedTeam #3).
			if canonical == "row" && from == 0 && to == 0 {
				return fmt.Errorf("--from/--to required for row bulk (default 0,0 would produce a single %s0)", prefix)
			}
			p := locations.LabelParams{
				Prefix: prefix,
				From:   from, To: to,
				RowFrom: rowFrom, RowTo: rowTo, ColFrom: colFrom, ColTo: colTo,
				LevelFrom: levelFrom, LevelTo: levelTo,
			}
			labels, err := locations.GenerateLabels(canonical, p, maxLabels)
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
	cmd.Flags().StringVar(&method, "method", "", "creation method: row, grid, or 3d (single uses 'go-parts locations add')")
	cmd.MarkFlagRequired("method")
	cmd.Flags().StringVar(&prefix, "prefix", "", "label prefix (e.g. box, shelf, rack)")
	cmd.MarkFlagRequired("prefix")
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
	cmd.Flags().IntVar(&maxLabels, "max-labels", 100, "maximum labels a single bulk may generate (sanity cap)")
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
			// remove composes both stores: the has-parts refusal needs
			// parts.CountByLocation, so open both (locations.Store can't see the
			// parts keyspace, §5.1).
			ps, s, _, cleanup, err := openStores(*dataDir)
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
				switch {
				case len(s.Children(id)) > 0:
					fmt.Printf("would remove id=%s — REFUSE (%d children; reparent first)\n", id, len(s.Children(id)))
				case ps.CountByLocation(id) > 0:
					fmt.Printf("would remove id=%s — REFUSE (%d parts assigned; reassign first)\n", id, ps.CountByLocation(id))
				default:
					fmt.Printf("would remove id=%s\n", id)
				}
				return nil
			}
			// delete-refuse-has-parts (Slice 3a): refuse before Delete if parts
			// are still assigned here. Same accepted-TOCTOU posture as the spec's
			// delete-refuse-has-parts note (homelab scale; a part can be assigned
			// between the count and the delete).
			if n := ps.CountByLocation(id); n > 0 {
				return fmt.Errorf("remove %s: %w (%d parts assigned; reassign first)", id, locations.ErrHasParts, n)
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

// newLocationsLabelCmd builds `go-parts locations label <id|viacode>` — renders
// the location's scannable SVG label (§5.17) to stdout (pipe to a file or a
// browser/print dialog). The QR encodes the configured public_base_url
// (config.json) + /via/{code}; with no base configured it encodes the relative
// /via/{code} (a scanner gets a path, less useful but not broken). Accepts an
// id or an L- via-code.
func newLocationsLabelCmd(dataDir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label <id|viacode>",
		Short: "Render the location's scannable label SVG to stdout (§5.17)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, ls, vs, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			id := args[0]
			if strings.HasPrefix(id, "L-") {
				_, vid, err := vs.Lookup(id)
				if err != nil {
					return err
				}
				id = vid
			}
			loc, err := ls.Get(id)
			if err != nil {
				return err
			}
			cfg, _ := config.Load(*dataDir) // baseURL is non-secret advice; defaults stand on miss
			svg, err := label.SVG(loc.ViaCode, loc.Label, cfg.PublicBaseURL)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(svg)
			return err
		},
	}
	return cmd
}
