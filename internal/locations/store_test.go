package locations

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/via"
)

// newStore mirrors parts/store_test.go's helper: real Pebble + a shared via.Store.
// locations.Store has no FTS, so unlike parts' helper it does not take an
// index.NewFTS arg — the shape is uniform with parts' newStoreWithVia minus FTS.
// TestTagCounts pins the Storage sidebar facet: TagCounts aggregates every
// location's Tags and returns them sorted by count desc then tag asc — the
// order the Storage sidebar renders. Mirrors parts.TestTagCounts (same
// ordering contract, one prefix scan, best-effort skip-undecodable posture).
func TestTagCounts(t *testing.T) {
	store, _ := newStore(t)
	store.Create(&Location{Label: "a", Tags: []string{"garage", "shelf"}})
	store.Create(&Location{Label: "b", Tags: []string{"garage"}})
	store.Create(&Location{Label: "c", Tags: []string{"shed"}})

	got := store.TagCounts()
	if len(got) != 3 {
		t.Fatalf("TagCounts = %d entries, want 3: %+v", len(got), got)
	}
	// garage(2) first by count; then shed(1) and shelf(1) alpha asc.
	if got[0].Tag != "garage" || got[0].Count != 2 {
		t.Errorf("got[0] = %+v, want {garage 2}", got[0])
	}
	if got[1].Tag != "shed" || got[2].Tag != "shelf" {
		t.Errorf("count-tie order should be alpha asc (shed before shelf): %+v", got)
	}
}

func newStore(t *testing.T) (*Store, *via.Store) {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	vs := via.NewStore(db)
	return NewStore(db, vs), vs
}

func TestCreateGetRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	l := &Location{Label: "Bin A3"}
	if err := s.Create(l); err != nil {
		t.Fatal(err)
	}
	if l.ID == "" || l.ViaCode == "" || l.Version != 1 {
		t.Fatalf("after Create: id=%q via=%q version=%d", l.ID, l.ViaCode, l.Version)
	}
	got, err := s.Get(l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "Bin A3" {
		t.Fatalf("label = %q", got.Label)
	}
}

func TestCreate_AssignsLViaCode(t *testing.T) {
	s, vs := newStore(t)
	l := &Location{Label: "Drawer 12"}
	if err := s.Create(l); err != nil {
		t.Fatal(err)
	}
	// L- prefix (§5.17) — not P-.
	if len(l.ViaCode) != 8 || l.ViaCode[:2] != "L-" {
		t.Fatalf("ViaCode = %q, want L-XXXXXX", l.ViaCode)
	}
	tt, id, err := vs.Lookup(l.ViaCode)
	if err != nil {
		t.Fatalf("via lookup: %v", err)
	}
	if tt != via.TypeLocation || id != l.ID {
		t.Fatalf("via = (%q,%q), want (location,%s)", tt, id, l.ID)
	}
}

func TestCreate_CallerSuppliedViaCodeReserved(t *testing.T) {
	s, _ := newStore(t)
	a := &Location{Label: "A", ViaCode: "L-DUP0001"}
	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	b := &Location{Label: "B", ViaCode: "L-DUP0001"}
	err := s.Create(b)
	if !errors.Is(err, via.ErrCollision) {
		t.Fatalf("duplicate via-code err = %v, want via.ErrCollision", err)
	}
}

func TestDelete_ReleasesViaCode(t *testing.T) {
	s, vs := newStore(t)
	l := &Location{Label: "x"}
	if err := s.Create(l); err != nil {
		t.Fatal(err)
	}
	code := l.ViaCode
	if err := s.Delete(l.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := vs.Lookup(code); !errors.Is(err, via.ErrNotFound) {
		t.Fatalf("post-delete via lookup err = %v, want via.ErrNotFound", err)
	}
}

func TestUpdateIncrementsVersion(t *testing.T) {
	s, _ := newStore(t)
	l := &Location{Label: "x"}
	s.Create(l)
	l.Label = "y"
	if err := s.Update(l, l.Version); err != nil {
		t.Fatal(err)
	}
	if l.Version != 2 {
		t.Fatalf("version = %d, want 2", l.Version)
	}
}

func TestUpdatePreservesViaCode(t *testing.T) {
	s, _ := newStore(t)
	l := &Location{Label: "x"}
	s.Create(l)
	orig := l.ViaCode
	l.ViaCode = "L-HACKED0" // caller tries to mutate — must be ignored
	if err := s.Update(l, l.Version); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(l.ID)
	if got.ViaCode != orig {
		t.Fatalf("via-code mutated to %q, want immutable %q", got.ViaCode, orig)
	}
}

func TestGetUnknownIsErrNotFound(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
