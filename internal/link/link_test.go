package link

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

// newPairWithVia builds the full composition (parts + locations + components
// + via) over one temp Pebble DB, sharing the single via.Store instance. The
// flat-locations model: link.Resolve threads the components store so a
// location's "contents" are its Components.
func newPairWithVia(t *testing.T) (*parts.Store, *locations.Store, *components.Store, *via.Store) {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	vs := via.NewStore(db)
	ps := parts.NewStore(db, index.NewFTS(db), vs)
	ls := locations.NewStore(db, vs)
	cs := components.NewStore(db, ps)
	return ps, ls, cs, vs
}

// --- Slice 4: via resolver (link.Resolve) ----------------------------------

// TestResolve_LocationEmbedsComponents is scan-to-find (§5.17): resolving a
// location's via-code returns the location WITH its embedded Components.
// Flat-locations model: a location's contents are its Components (one per
// part per location, with quantity + history), NOT parts scanned by a default
// pointer.
func TestResolve_LocationEmbedsComponents(t *testing.T) {
	ps, ls, cs, vs := newPairWithVia(t)
	bin := &locations.Location{Label: "Bin"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	// Two parts, each with a Component at bin.
	for _, mpn := range []string{"a", "b"} {
		p := &parts.Part{MPN: mpn, PartType: "local"}
		if err := ps.Create(p); err != nil {
			t.Fatal(err)
		}
		if err := cs.Add(bin.ID, p.ID, 1, nil); err != nil {
			t.Fatalf("Add component for %s: %v", mpn, err)
		}
	}
	r, err := Resolve(vs, ps, cs, ls, bin.ViaCode)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Type != "location" || r.Location == nil || r.Location.ID != bin.ID {
		t.Fatalf("Resolve = %+v, want type=location with the bin", r)
	}
	if len(r.Components) != 2 {
		t.Errorf("Resolve location components = %d, want 2 (scan-to-find)", len(r.Components))
	}
	if r.Part != nil {
		t.Errorf("Resolve location should not carry a Part")
	}
}

// TestResolve_Part pins the part branch: a P- code resolves to just the part.
func TestResolve_Part(t *testing.T) {
	ps, _, cs, vs := newPairWithVia(t)
	p := &parts.Part{MPN: "RP", PartType: "local"}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve(vs, ps, cs, nil, p.ViaCode)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Type != "part" || r.Part == nil || r.Part.ID != p.ID {
		t.Fatalf("Resolve = %+v, want type=part with the part", r)
	}
	if r.Location != nil || r.Components != nil {
		t.Errorf("Resolve part should not carry a location/components")
	}
}

// TestResolve_UnknownCode pins the via-miss → via.ErrNotFound (REST → 404).
func TestResolve_UnknownCode(t *testing.T) {
	_, _, cs, vs := newPairWithVia(t)
	_, err := Resolve(vs, nil, cs, nil, "L-NOPE")
	if !errors.Is(err, via.ErrNotFound) {
		t.Fatalf("Resolve unknown code err = %v, want via.ErrNotFound", err)
	}
}
