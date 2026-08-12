package components

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

// newStore builds a Component store backed by a fresh Pebble DB + a real
// parts.Store (the cache-writer target). Tests create real Parts first because
// recomputePartQty → parts.SetQty → parts.Get(partID), so the part must exist.
func newStore(t *testing.T) (*Store, *parts.Store) {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "c"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ps := parts.NewStore(db, index.NewFTS(db), via.NewStore(db))
	return NewStore(db, ps), ps
}

// makePart creates a minimal Part and returns its ID. The Part must exist
// because Add/AdjustQty/Remove call recomputePartQty → parts.SetQty → parts.Get.
func makePart(t *testing.T, ps *parts.Store, mpn string) string {
	t.Helper()
	p := &parts.Part{MPN: mpn, PartType: "local"}
	if err := ps.Create(p); err != nil {
		t.Fatalf("create part: %v", err)
	}
	return p.ID
}

// TestAddListGetRoundTrip pins the basic CRUD shape: Add creates a Component
// at (locID, partID), List returns it by locID, Get reads it by the composite
// key, and every field round-trips.
func TestAddListGetRoundTrip(t *testing.T) {
	s, ps := newStore(t)
	locID := "01J000000000000000000L0C1"
	partID := makePart(t, ps, "RC0805FR-0710KL")

	if err := s.Add(locID, partID, 42, []string{"reel", "preferred"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := s.Get(locID, partID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Quantity != 42 {
		t.Errorf("Quantity: got %d, want 42", got.Quantity)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "reel" || got.Tags[1] != "preferred" {
		t.Errorf("Tags: got %v, want [reel preferred]", got.Tags)
	}
	if len(got.History) != 1 {
		t.Fatalf("History len: got %d, want 1", len(got.History))
	}
	if got.History[0].Delta != 42 {
		t.Errorf("History[0].Delta: got %d, want 42", got.History[0].Delta)
	}
	if got.History[0].Reason != "initial" {
		t.Errorf("History[0].Reason: got %q, want \"initial\"", got.History[0].Reason)
	}
	if got.Version != 1 {
		t.Errorf("Version: got %d, want 1", got.Version)
	}
	if got.LocationID != locID || got.PartID != partID {
		t.Errorf("IDs: got loc=%q part=%q", got.LocationID, got.PartID)
	}

	listed := s.List(locID)
	if len(listed) != 1 {
		t.Fatalf("List len: got %d, want 1", len(listed))
	}
	if listed[0].PartID != partID {
		t.Errorf("List[0].PartID: got %q, want %q", listed[0].PartID, partID)
	}
}

// TestAddDuplicate verifies the (locID, partID) uniqueness boundary: a second
// Add at the same pair returns ErrDuplicate.
func TestAddDuplicate(t *testing.T) {
	s, ps := newStore(t)
	locID := "01J000000000000000000L0C2"
	partID := makePart(t, ps, "DUP-TEST")

	if err := s.Add(locID, partID, 10, nil); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	err := s.Add(locID, partID, 5, nil)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Add: got %v, want ErrDuplicate", err)
	}
}

// TestAddSamePartDifferentLocation verifies the pair boundary from the other
// direction: the SAME partID at a DIFFERENT location is allowed (that's the
// whole point of the junction — one part can be stocked at multiple bins).
func TestAddSamePartDifferentLocation(t *testing.T) {
	s, ps := newStore(t)
	loc1 := "01J000000000000000000L0A1"
	loc2 := "01J000000000000000000L0A2"
	partID := makePart(t, ps, "MULTI-LOC")

	if err := s.Add(loc1, partID, 10, nil); err != nil {
		t.Fatalf("Add loc1: %v", err)
	}
	if err := s.Add(loc2, partID, 20, nil); err != nil {
		t.Fatalf("Add loc2: %v", err)
	}
	if len(s.List(loc1)) != 1 {
		t.Errorf("List(loc1): got %d, want 1", len(s.List(loc1)))
	}
	if len(s.List(loc2)) != 1 {
		t.Errorf("List(loc2): got %d, want 1", len(s.List(loc2)))
	}
}

// TestGetNotFound verifies the not-found path wraps ErrNotFound.
func TestGetNotFound(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Get("nope-loc", "nope-part")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// TestAdjustQty verifies the core stock-delta path: AdjustQty updates
// Quantity, appends a Movement to History, and re-derives the Part's QtyOnHand
// cache to the sum of all Component.Quantity for that partID.
func TestAdjustQty(t *testing.T) {
	s, ps := newStore(t)
	locID := "01J000000000000000000L0B1"
	partID := makePart(t, ps, "ADJ-QTY")

	if err := s.Add(locID, partID, 100, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.AdjustQty(locID, partID, -30, "used in build"); err != nil {
		t.Fatalf("AdjustQty -30: %v", err)
	}

	got, err := s.Get(locID, partID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Quantity != 70 {
		t.Errorf("Quantity: got %d, want 70", got.Quantity)
	}
	if len(got.History) != 2 {
		t.Fatalf("History len: got %d, want 2 (initial + adjustment)", len(got.History))
	}
	if got.History[1].Delta != -30 {
		t.Errorf("History[1].Delta: got %d, want -30", got.History[1].Delta)
	}
	if got.History[1].Reason != "used in build" {
		t.Errorf("History[1].Reason: got %q, want \"used in build\"", got.History[1].Reason)
	}

	// Part.QtyOnHand cache must equal the sum: only one component, 70.
	p, err := ps.Get(partID)
	if err != nil {
		t.Fatalf("parts.Get: %v", err)
	}
	if p.QtyOnHand != 70 {
		t.Errorf("Part.QtyOnHand: got %d, want 70", p.QtyOnHand)
	}
}

// TestAdjustQtyMultiLocation verifies the cache re-derivation across multiple
// locations: the Part.QtyOnHand cache is the sum of ALL Component.Quantity
// across every location, and adjusting one location's quantity re-derives the
// full sum.
func TestAdjustQtyMultiLocation(t *testing.T) {
	s, ps := newStore(t)
	loc1 := "01J000000000000000000L0M1"
	loc2 := "01J000000000000000000L0M2"
	loc3 := "01J000000000000000000L0M3"
	partID := makePart(t, ps, "MULTI-ADJ")

	// Stock 10 + 20 + 30 = 60 across three bins.
	s.Add(loc1, partID, 10, nil)
	s.Add(loc2, partID, 20, nil)
	s.Add(loc3, partID, 30, nil)

	p, _ := ps.Get(partID)
	if p.QtyOnHand != 60 {
		t.Fatalf("after Add: QtyOnHand = %d, want 60", p.QtyOnHand)
	}

	// Use 15 from loc2: 10 + 5 + 30 = 45.
	s.AdjustQty(loc2, partID, -15, "build")

	p, _ = ps.Get(partID)
	if p.QtyOnHand != 45 {
		t.Errorf("after AdjustQty: QtyOnHand = %d, want 45", p.QtyOnHand)
	}
}

// TestAdjustQtyNotFound verifies AdjustQty on a non-existent component wraps
// ErrNotFound.
func TestAdjustQtyNotFound(t *testing.T) {
	s, ps := newStore(t)
	partID := makePart(t, ps, "ADJ-NF")
	err := s.AdjustQty("nope-loc", partID, 5, "x")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// TestRemove verifies Remove deletes the Component and re-derives the cache:
// after removing one of two locations' components, the Part.QtyOnHand reflects
// only the remaining location's quantity.
func TestRemove(t *testing.T) {
	s, ps := newStore(t)
	loc1 := "01J000000000000000000L0R1"
	loc2 := "01J000000000000000000L0R2"
	partID := makePart(t, ps, "RM-TEST")

	s.Add(loc1, partID, 30, nil)
	s.Add(loc2, partID, 50, nil)

	p, _ := ps.Get(partID)
	if p.QtyOnHand != 80 {
		t.Fatalf("setup: QtyOnHand = %d, want 80", p.QtyOnHand)
	}

	if err := s.Remove(loc1, partID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Component is gone.
	if _, err := s.Get(loc1, partID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Remove: got %v, want ErrNotFound", err)
	}
	// Cache re-derived: only loc2's 50 remains.
	p, _ = ps.Get(partID)
	if p.QtyOnHand != 50 {
		t.Errorf("after Remove: QtyOnHand = %d, want 50", p.QtyOnHand)
	}
}

// TestRemoveLastComponent verifies that removing the LAST component for a part
// zeros the Part.QtyOnHand cache (sum of empty set = 0).
func TestRemoveLastComponent(t *testing.T) {
	s, ps := newStore(t)
	locID := "01J000000000000000000L0RL"
	partID := makePart(t, ps, "RM-LAST")

	s.Add(locID, partID, 25, nil)
	s.Remove(locID, partID)

	p, _ := ps.Get(partID)
	if p.QtyOnHand != 0 {
		t.Errorf("after removing last component: QtyOnHand = %d, want 0", p.QtyOnHand)
	}
}

// TestRemoveNotFound verifies Remove on a non-existent component wraps
// ErrNotFound.
func TestRemoveNotFound(t *testing.T) {
	s, ps := newStore(t)
	partID := makePart(t, ps, "RM-NF")
	err := s.Remove("nope-loc", partID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// TestFindByPart verifies the reverse query: FindByPart returns every Component
// stocking the given partID, across all locations.
func TestFindByPart(t *testing.T) {
	s, ps := newStore(t)
	loc1 := "01J000000000000000000L0F1"
	loc2 := "01J000000000000000000L0F2"
	loc3 := "01J000000000000000000L0F3"
	partA := makePart(t, ps, "FIND-A")
	partB := makePart(t, ps, "FIND-B")

	s.Add(loc1, partA, 10, nil)
	s.Add(loc2, partA, 20, nil)
	s.Add(loc3, partB, 5, nil) // different part, must not appear

	got := s.FindByPart(partA)
	if len(got) != 2 {
		t.Fatalf("FindByPart(partA): got %d, want 2", len(got))
	}
	qty := 0
	for _, c := range got {
		if c.PartID != partA {
			t.Errorf("PartID: got %q, want %q", c.PartID, partA)
		}
		qty += c.Quantity
	}
	if qty != 30 {
		t.Errorf("sum of quantities: got %d, want 30", qty)
	}

	gotB := s.FindByPart(partB)
	if len(gotB) != 1 {
		t.Fatalf("FindByPart(partB): got %d, want 1", len(gotB))
	}
}

// TestFindByPartEmpty verifies the reverse query on a part with no components
// returns nil (not an error — best-effort read).
func TestFindByPartEmpty(t *testing.T) {
	s, ps := newStore(t)
	partID := makePart(t, ps, "FIND-EMPTY")
	if got := s.FindByPart(partID); len(got) != 0 {
		t.Errorf("FindByPart on no-components: got %d, want 0", len(got))
	}
}

// TestListEmpty verifies List on a location with no components returns nil.
func TestListEmpty(t *testing.T) {
	s, _ := newStore(t)
	if got := s.List("01J000000000000000000EMPTY"); len(got) != 0 {
		t.Errorf("List on empty location: got %d, want 0", len(got))
	}
}

// TestConcurrentAdjustQty hammers AdjustQty on the same partID from multiple
// goroutines. The per-partID striped lock must make the final Quantity equal
// the exact sum of every delta (no lost updates) and the Part.QtyOnHand cache
// must agree. -race is the real gate here — a lock-ordering or RMW bug surfaces
// as either a data race or a wrong total.
func TestConcurrentAdjustQty(t *testing.T) {
	s, ps := newStore(t)
	locID := "01J000000000000000000L0C0"
	partID := makePart(t, ps, "CONCURRENT")

	if err := s.Add(locID, partID, 0, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	const goroutines = 20
	const perGoroutine = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if err := s.AdjustQty(locID, partID, 1, "concurrent +1"); err != nil {
					t.Errorf("AdjustQty: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	want := goroutines * perGoroutine
	got, _ := s.Get(locID, partID)
	if got.Quantity != want {
		t.Errorf("Component.Quantity after %d concurrent +1: got %d, want %d", want, got.Quantity, want)
	}
	p, _ := ps.Get(partID)
	if p.QtyOnHand != want {
		t.Errorf("Part.QtyOnHand cache: got %d, want %d", p.QtyOnHand, want)
	}
	if len(got.History) != 1+want {
		t.Errorf("History len: got %d, want %d (1 initial + %d adjustments)", len(got.History), 1+want, want)
	}
}
