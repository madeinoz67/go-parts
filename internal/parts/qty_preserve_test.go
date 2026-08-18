package parts

import "testing"

// TestUpdatePreservesQtyOnHandFromStockWriter restores the F3 invariant test
// deleted with AdjustStock: stock moves are the stock path's exclusive domain
// (SetQty — no version bump), so a full-record Update whose caller carries a
// stale QtyOnHand from an outer Get must NOT overwrite the newer stock value.
// Deterministic sequencing of the race (Update's lock makes interleaving
// irrelevant): without the in-lock `p.QtyOnHand = cur.QtyOnHand` preserve,
// outer's stale 100 wins and this test fails.
func TestUpdatePreservesQtyOnHandFromStockWriter(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "F3-KEEP", PartType: "local", QtyOnHand: 100}
	if err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	outer, err := s.Get(p.ID) // the editing caller's snapshot: v1, qty 100
	if err != nil {
		t.Fatal(err)
	}
	// Concurrent stock writer lands between the snapshot and the Update.
	if err := s.SetQty(p.ID, 90); err != nil {
		t.Fatal(err)
	}
	outer.Description = "edited"
	if err := s.Update(outer, outer.Version); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.QtyOnHand != 90 {
		t.Fatalf("QtyOnHand = %d, want 90 (stock write preserved across Update)", got.QtyOnHand)
	}
	if got.Version != 2 {
		t.Fatalf("Version = %d, want 2", got.Version)
	}
	if got.Description != "edited" {
		t.Fatalf("Description = %q, want the edit applied", got.Description)
	}
}
