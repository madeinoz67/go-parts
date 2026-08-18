package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/config"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/label"
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

// openStores opens parts + locations + components + the shared via index for a
// cross-entity CLI op (delete-refuse-has-components, the via resolver, labels).
// Same one-shot + via-singleton posture as openLocations. Returns the component
// store too — the via resolver and the has-components delete-refuse guard both
// need it. Flat-locations model: no injected policy (the single_part_only guard
// is gone); a location's contents are its Components.
func openStores(dataDir string) (*parts.Store, *locations.Store, *components.Store, *via.Store, func(), error) {
	dataDir = config.Default(dataDir).DataDir
	storeDB, err := storage.Open(dataDir)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("open store: %w", err)
	}
	cleanup := func() { storeDB.Close() }
	viaStore := via.NewStore(storeDB.DB)
	ps := parts.NewStore(storeDB.DB, index.NewFTS(storeDB.DB), viaStore)
	ls := locations.NewStore(storeDB.DB, viaStore)
	cs := components.NewStore(storeDB.DB, ps)
	return ps, ls, cs, viaStore, cleanup, nil
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
	cmd.AddCommand(newLocationsLabelCmd(dataDir))
	cmd.AddCommand(newLocationsAddComponentCmd(dataDir))
	cmd.AddCommand(newLocationsListComponentsCmd(dataDir))
	cmd.AddCommand(newLocationsAdjustCmd(dataDir))
	cmd.AddCommand(newLocationsRemoveComponentCmd(dataDir))
	return cmd
}

func newLocationsAddCmd(dataDir *string) *cobra.Command {
	var (
		tags   []string
		notes  string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "add --label \"Bin A3\" [--tag garage --tag workbench] [--notes ...]",
		Short: "Create a single location",
		RunE: func(cmd *cobra.Command, args []string) error {
			label, _ := cmd.Flags().GetString("label")
			if label == "" {
				return fmt.Errorf("--label is required")
			}
			if dryRun {
				fmt.Printf("would create location label=%q tags=%v notes=%q\n", label, tags, notes)
				return nil
			}
			s, cleanup, err := openLocations(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			l := &locations.Location{
				Label: label,
				Tags:  tags,
				Notes: notes,
			}
			if err := s.Create(l); err != nil {
				return err
			}
			fmt.Printf("created %s  via=%s  id=%s\n", l.Label, l.ViaCode, l.ID)
			return nil
		},
	}
	cmd.Flags().String("label", "", "location label (e.g. \"Bin A3\")")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "physical-context tag (repeatable: --tag garage --tag workbench)")
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
		notes              string
		dryRun             bool
		maxLabels          int
		separator          string // Task 44a: label separator, default "-" ("" glues)
	)
	cmd := &cobra.Command{
		Use:   "bulk --method row|grid|3d --prefix box [--from/--to|--row-from/--row-to/--col-from/--col-to|--level-from/--level-to]",
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
				Separator: &separator,
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
	cmd.Flags().StringVar(&notes, "notes", "", "free-text notes applied to every row")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the labels that would be created without writing")
	cmd.Flags().IntVar(&maxLabels, "max-labels", 100, "maximum labels a single bulk may generate (sanity cap)")
	cmd.Flags().StringVar(&separator, "separator", "-", "label separator joining prefix and coordinates (default -; pass an empty string to glue: box1)")
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
			fmt.Fprintln(tw, "LABEL\tVIA\tID\tTAGS")
			for _, l := range all {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", l.Label, l.ViaCode, l.ID, strings.Join(l.Tags, ","))
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
		Short: "Remove a location (refuses if it still holds stock)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// remove composes stores: the has-components refusal needs
			// components.List, so open both (locations.Store can't see the
			// components keyspace, §5.1).
			_, s, cs, _, cleanup, err := openStores(*dataDir)
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
			comps := cs.List(id)
			if dryRun {
				if n := len(comps); n > 0 {
					fmt.Printf("would remove id=%s — REFUSE (%d components assigned; reassign first)\n", id, n)
				} else {
					fmt.Printf("would remove id=%s\n", id)
				}
				return nil
			}
			// delete-refuse-has-components: refuse before Delete if components
			// are still held here. Same accepted-TOCTOU posture as the spec's
			// delete-refuse note (homelab scale; a component can be added
			// between the count and the delete).
			if n := len(comps); n > 0 {
				return fmt.Errorf("remove %s: %w (%d components assigned; reassign first)", id, locations.ErrHasParts, n)
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
			_, ls, _, vs, cleanup, err := openStores(*dataDir)
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

// resolveID resolves a via code (L-.../P-...) to its entity id, or returns the
// bare id as-is. Used by the component CLI commands that accept <id|via>.
func resolveID(vs *via.Store, id string) (string, error) {
	if len(id) > 2 && (id[:2] == "L-" || id[:2] == "P-") {
		_, resolved, err := vs.Lookup(id)
		if err != nil {
			return "", err
		}
		return resolved, nil
	}
	return id, nil
}

// newLocationsAddComponentCmd: go-parts locations add-component <loc> <part> <qty> [--tag ...]
func newLocationsAddComponentCmd(dataDir *string) *cobra.Command {
	var tags []string
	cmd := &cobra.Command{
		Use:   "add-component <locID|via> <partID|via> <qty>",
		Short: "Add a part to a location with an initial quantity",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, cs, vs, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			locID, err := resolveID(vs, args[0])
			if err != nil {
				return err
			}
			partID, err := resolveID(vs, args[1])
			if err != nil {
				return err
			}
			qty, err := strconv.Atoi(args[2])
			if err != nil {
				return fmt.Errorf("qty must be an integer: %w", err)
			}
			if err := cs.Add(locID, partID, qty, tags); err != nil {
				return err
			}
			fmt.Printf("added %d × %s → %s\n", qty, args[1], args[0])
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "component tags (repeatable)")
	return cmd
}

// newLocationsListComponentsCmd: go-parts locations list-components <loc>
func newLocationsListComponentsCmd(dataDir *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list-components <locID|via>",
		Short: "List components at a location",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, cs, vs, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			locID, err := resolveID(vs, args[0])
			if err != nil {
				return err
			}
			comps := cs.List(locID)
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(comps)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PARTID\tQTY\tTAGS")
			for _, c := range comps {
				fmt.Fprintf(tw, "%s\t%d\t%s\n", c.PartID, c.Quantity, strings.Join(c.Tags, ","))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

// newLocationsAdjustCmd: go-parts locations adjust <loc> <part> <delta> [--reason ...]
func newLocationsAdjustCmd(dataDir *string) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "adjust <locID|via> <partID|via> <delta>",
		Short: "Adjust a component's quantity (stock in/out with history)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, cs, vs, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			locID, err := resolveID(vs, args[0])
			if err != nil {
				return err
			}
			partID, err := resolveID(vs, args[1])
			if err != nil {
				return err
			}
			delta, err := strconv.Atoi(args[2])
			if err != nil {
				return fmt.Errorf("delta must be an integer: %w", err)
			}
			if err := cs.AdjustQty(locID, partID, delta, reason); err != nil {
				return err
			}
			c, _ := cs.Get(locID, partID)
			fmt.Printf("adjusted by %d → qty=%d\n", delta, c.Quantity)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for the adjustment (recorded in history)")
	return cmd
}

// newLocationsRemoveComponentCmd: go-parts locations remove-component <loc> <part>
func newLocationsRemoveComponentCmd(dataDir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove-component <locID|via> <partID|via>",
		Short: "Remove a component from a location",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, cs, vs, cleanup, err := openStores(*dataDir)
			if err != nil {
				return err
			}
			defer cleanup()
			locID, err := resolveID(vs, args[0])
			if err != nil {
				return err
			}
			partID, err := resolveID(vs, args[1])
			if err != nil {
				return err
			}
			if err := cs.Remove(locID, partID); err != nil {
				return err
			}
			fmt.Printf("removed %s from %s\n", args[1], args[0])
			return nil
		},
	}
	return cmd
}
