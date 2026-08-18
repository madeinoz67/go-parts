package main

import (
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

// newFixQtyStores builds a parts+components pair over a fresh Pebble DB —
// runFixQty's whole world.
func newFixQtyStores(t *testing.T) (*parts.Store, *components.Store) {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vs := via.NewStore(db)
	ps := parts.NewStore(db, index.NewFTS(db), vs)
	return ps, components.NewStore(db, ps)
}

// TestRunFixQty_LegacyStockNotZeroed pins code-review finding 1: a pre-redesign
// part carries QtyOnHand directly with NO Component records. Re-deriving its
// quantity from an empty component set used to SetQty(id, 0) — silently
// destroying the only record of that stock. The default run must SKIP such
// parts and report them.
func TestRunFixQty_LegacyStockNotZeroed(t *testing.T) {
	ps, cs := newFixQtyStores(t)
	p := &parts.Part{MPN: "LEGACY-100", PartType: "local", QtyOnHand: 100}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	fixed, skipped, err := runFixQty(ps, cs, false)
	if err != nil {
		t.Fatal(err)
	}
	if fixed != 0 || skipped != 1 {
		t.Fatalf("fixed=%d skipped=%d, want fixed=0 skipped=1 (legacy stock untouched)", fixed, skipped)
	}
	got, err := ps.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.QtyOnHand != 100 {
		t.Fatalf("legacy part QtyOnHand = %d, want 100 (stock preserved)", got.QtyOnHand)
	}
}

// TestRunFixQty_ForceZeroesLegacyStock: --force makes the legacy skip an
// explicit, counted zero instead of a silent one.
func TestRunFixQty_ForceZeroesLegacyStock(t *testing.T) {
	ps, cs := newFixQtyStores(t)
	p := &parts.Part{MPN: "LEGACY-F", PartType: "local", QtyOnHand: 7}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	fixed, skipped, err := runFixQty(ps, cs, true)
	if err != nil {
		t.Fatal(err)
	}
	if fixed != 1 || skipped != 0 {
		t.Fatalf("fixed=%d skipped=%d, want fixed=1 skipped=0", fixed, skipped)
	}
	got, _ := ps.Get(p.ID)
	if got.QtyOnHand != 0 {
		t.Fatalf("forced legacy QtyOnHand = %d, want 0", got.QtyOnHand)
	}
}

// TestRunFixQty_DerivesStockedParts: a part WITH components still re-derives
// normally (the command's original purpose), and an already-consistent part is
// left alone.
func TestRunFixQty_DerivesStockedParts(t *testing.T) {
	ps, cs := newFixQtyStores(t)
	p := &parts.Part{MPN: "STOCKED", PartType: "local", QtyOnHand: 999}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := cs.Add("loc1", p.ID, 10, nil); err != nil {
		t.Fatal(err)
	}
	// Add already re-derived the cache to 10; introduce drift the way it
	// arises in practice (a legacy write or a pre-guard binary) so the
	// command's re-derivation has real work to do.
	if err := ps.SetQty(p.ID, 999); err != nil {
		t.Fatal(err)
	}
	fixed, skipped, err := runFixQty(ps, cs, false)
	if err != nil {
		t.Fatal(err)
	}
	if fixed != 1 || skipped != 0 {
		t.Fatalf("fixed=%d skipped=%d, want fixed=1 skipped=0", fixed, skipped)
	}
	got, _ := ps.Get(p.ID)
	if got.QtyOnHand != 10 {
		t.Fatalf("QtyOnHand = %d, want 10 (sum of components)", got.QtyOnHand)
	}
}
