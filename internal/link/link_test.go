package link

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

// newPair builds a parts.Store + locations.Store over one temp Pebble DB,
// sharing the single via.Store instance (the via-singleton invariant), and
// injects NewPolicy so the single_part_only guard is live on parts writes —
// the same wiring daemon.Run and the CLI's openStores do.
func newPair(t *testing.T) (*parts.Store, *locations.Store) {
	ps, ls, _ := newPairWithVia(t)
	return ps, ls
}

// newPairWithVia is newPair but also returns the shared via.Store (Slice 4 —
// link.Resolve needs it to drive the resolver).
func newPairWithVia(t *testing.T) (*parts.Store, *locations.Store, *via.Store) {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	vs := via.NewStore(db)
	ps := parts.NewStore(db, index.NewFTS(db), vs)
	ls := locations.NewStore(db, vs)
	ps.SetLocationPolicy(NewPolicy(ps, ls))
	return ps, ls, vs
}

// TestNewPolicy_MissingLocation pins referential integrity: assigning a part to
// a DefaultLocationID that does not reference a real Location returns
// parts.ErrLocationNotFound (no silent dangling ref).
func TestNewPolicy_MissingLocation(t *testing.T) {
	ps, _ := newPair(t)
	err := ps.Create(&parts.Part{MPN: "x", PartType: "local", DefaultLocationID: "no-such-location"})
	if !errors.Is(err, parts.ErrLocationNotFound) {
		t.Fatalf("assign to missing location: err = %v, want parts.ErrLocationNotFound", err)
	}
}

// TestNewPolicy_SharedLocationAllowsMany pins that a non-SinglePartOnly
// location imposes no exclusivity: many parts may share it.
func TestNewPolicy_SharedLocationAllowsMany(t *testing.T) {
	ps, ls := newPair(t)
	shared := &locations.Location{Label: "shared-bin"}
	if err := ls.Create(shared); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := ps.Create(&parts.Part{MPN: "R", PartType: "local", DefaultLocationID: shared.ID}); err != nil {
			t.Fatalf("create part %d in shared bin: %v", i, err)
		}
	}
	if got := ps.CountByLocation(shared.ID); got != 5 {
		t.Errorf("CountByLocation(shared) = %d, want 5", got)
	}
}

// TestNewPolicy_SinglePartOnlyRejectsSecond is the end-to-end guard test
// through the real composition layer (both stores + the injected policy): a
// SinglePartOnly location accepts the first part and rejects a second, distinct
// part with parts.ErrLocationSinglePartConflict.
func TestNewPolicy_SinglePartOnlyRejectsSecond(t *testing.T) {
	ps, ls := newPair(t)
	solo := &locations.Location{Label: "solo-bin", SinglePartOnly: true}
	if err := ls.Create(solo); err != nil {
		t.Fatal(err)
	}
	first := &parts.Part{MPN: "first", PartType: "local", DefaultLocationID: solo.ID}
	if err := ps.Create(first); err != nil {
		t.Fatalf("create first in solo: %v", err)
	}
	// A second, distinct part → conflict.
	err := ps.Create(&parts.Part{MPN: "second", PartType: "local", DefaultLocationID: solo.ID})
	if !errors.Is(err, parts.ErrLocationSinglePartConflict) {
		t.Fatalf("create second in solo: err = %v, want parts.ErrLocationSinglePartConflict", err)
	}
	// Re-saving the SAME first part (e.g. an edit that keeps the location) is
	// allowed — it is the sole occupant.
	first.Description = "edited"
	if err := ps.Update(first, first.Version); err != nil {
		t.Fatalf("re-save first in solo: %v", err)
	}
}

// TestNewPolicy_MoveBetweenSingleLocations pins the move path: a part may leave
// one SinglePartOnly location for another empty one (removal never violates
// exclusivity), and the now-empty first location accepts a different part.
func TestNewPolicy_MoveBetweenSingleLocations(t *testing.T) {
	ps, ls := newPair(t)
	soloA := &locations.Location{Label: "A", SinglePartOnly: true}
	soloB := &locations.Location{Label: "B", SinglePartOnly: true}
	if err := ls.Create(soloA); err != nil {
		t.Fatal(err)
	}
	if err := ls.Create(soloB); err != nil {
		t.Fatal(err)
	}
	p1 := &parts.Part{MPN: "p1", PartType: "local", DefaultLocationID: soloA.ID}
	ps.Create(p1)
	// Move p1 A→B (B is empty → allowed).
	p1.DefaultLocationID = soloB.ID
	if err := ps.Update(p1, p1.Version); err != nil {
		t.Fatalf("move p1 A→B: %v", err)
	}
	// soloA is now empty; a different part may take it.
	p2 := &parts.Part{MPN: "p2", PartType: "local", DefaultLocationID: soloA.ID}
	if err := ps.Create(p2); err != nil {
		t.Fatalf("create p2 in soloA after p1 left: %v", err)
	}
}

// TestDeleteRefuseHasParts_Composition pins the delete-path composition the CLI
// `locations remove` (and the daemon delete handler, once Slice 5/6 wires the
// transport) relies on: a location with assigned parts is detected by
// CountByLocation (the refuse condition → locations.ErrHasParts), and once the
// parts are reassigned the location is deletable. The check composes at the
// caller because locations.Store cannot see the parts keyspace (§5.1).
func TestDeleteRefuseHasParts_Composition(t *testing.T) {
	ps, ls := newPair(t)
	bin := &locations.Location{Label: "bin"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	// Assign a part to the bin → the refuse condition holds.
	p := &parts.Part{MPN: "p", PartType: "local", DefaultLocationID: bin.ID}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	if n := ps.CountByLocation(bin.ID); n != 1 {
		t.Fatalf("CountByLocation(bin) = %d, want 1 (refuse condition)", n)
	}
	// Delete MUST be refused while parts are assigned — simulate the caller's
	// guard (the exact check `locations remove` runs before locations.Delete).
	if ps.CountByLocation(bin.ID) > 0 {
		// Caller would return locations.ErrHasParts here; confirm Delete has NOT
		// run by verifying the location is still present.
		if _, err := ls.Get(bin.ID); err != nil {
			t.Fatalf("location vanished before delete: %v", err)
		}
	} else {
		t.Fatal("refuse condition not detected — guard would let a has-parts delete through")
	}
	// Reassign the part away (clear its location) → the refuse condition clears.
	p.DefaultLocationID = ""
	if err := ps.Update(p, p.Version); err != nil {
		t.Fatalf("clear part location: %v", err)
	}
	if n := ps.CountByLocation(bin.ID); n != 0 {
		t.Fatalf("CountByLocation(bin) after reassign = %d, want 0 (allow condition)", n)
	}
	// Now the location is deletable.
	if err := ls.Delete(bin.ID); err != nil {
		t.Fatalf("delete empty bin: %v", err)
	}
}

// --- Slice 4: via resolver (link.Resolve) ----------------------------------

// TestResolve_LocationEmbedsContents is scan-to-find (§5.17): resolving a
// location's via-code returns the location WITH its assigned parts embedded.
func TestResolve_LocationEmbedsContents(t *testing.T) {
	ps, ls, vs := newPairWithVia(t)
	bin := &locations.Location{Label: "Bin"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	for _, mpn := range []string{"a", "b"} {
		if err := ps.Create(&parts.Part{MPN: mpn, PartType: "local", DefaultLocationID: bin.ID}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Resolve(vs, ps, ls, bin.ViaCode)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Type != "location" || r.Location == nil || r.Location.ID != bin.ID {
		t.Fatalf("Resolve = %+v, want type=location with the bin", r)
	}
	if len(r.Contents) != 2 {
		t.Errorf("Resolve location contents = %d parts, want 2 (scan-to-find)", len(r.Contents))
	}
	if r.Part != nil {
		t.Errorf("Resolve location should not carry a Part")
	}
}

// TestResolve_Part pins the part branch: a P- code resolves to just the part.
func TestResolve_Part(t *testing.T) {
	ps, ls, vs := newPairWithVia(t)
	p := &parts.Part{MPN: "RP", PartType: "local"}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve(vs, ps, ls, p.ViaCode)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Type != "part" || r.Part == nil || r.Part.ID != p.ID {
		t.Fatalf("Resolve = %+v, want type=part with the part", r)
	}
	if r.Location != nil || r.Contents != nil {
		t.Errorf("Resolve part should not carry a location/contents")
	}
}

// TestResolve_UnknownCode pins the via-miss → via.ErrNotFound (REST → 404).
func TestResolve_UnknownCode(t *testing.T) {
	ps, ls, vs := newPairWithVia(t)
	_, err := Resolve(vs, ps, ls, "L-NOPE")
	if !errors.Is(err, via.ErrNotFound) {
		t.Fatalf("Resolve unknown code err = %v, want via.ErrNotFound", err)
	}
}
