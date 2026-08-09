package parts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/index"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db, index.NewFTS(db))
}

func TestCreateGetRoundTrip(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "RC0805FR-0710KL", PartType: "linked", Description: "10k 0805 resistor", Tags: []string{"resistor"}}
	if err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Version != 1 {
		t.Fatalf("after Create: id=%q version=%d", p.ID, p.Version)
	}
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MPN != p.MPN {
		t.Fatalf("got MPN %q want %q", got.MPN, p.MPN)
	}
}

func TestUpdateIncrementsVersion(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "X1", PartType: "local"}
	s.Create(p)
	p.Description = "edited"
	if err := s.Update(p, p.Version); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if p.Version != 2 {
		t.Fatalf("version = %d, want 2", p.Version)
	}
}

// TestUpdateRejectsStaleVersion pins §5.14's "reject, never silently overwrite"
// invariant: an Update whose expectedVersion does not match the stored version
// must error, and the stored record must be unchanged.
func TestUpdateRejectsStaleVersion(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "STALE", PartType: "local"}
	if err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	stale := p.Version // 1
	// Simulate a concurrent write bumping the version to 2.
	p.Description = "first-edit"
	if err := s.Update(p, p.Version); err != nil {
		t.Fatalf("setup Update: %v", err)
	}
	if p.Version != 2 {
		t.Fatalf("setup: version = %d, want 2", p.Version)
	}
	// Now attempt an update with the stale expectedVersion=1.
	staleUpdate := &Part{ID: p.ID, MPN: "STALE", Description: "racy-write", Version: 1}
	err := s.Update(staleUpdate, stale)
	if err == nil {
		t.Fatal("Update with stale expectedVersion unexpectedly succeeded (§5.14 violation)")
	}
	// Verify the stored record is unchanged from the most-recent write.
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 {
		t.Errorf("after rejected Update: stored version = %d, want 2", got.Version)
	}
	if got.Description != "first-edit" {
		t.Errorf("after rejected Update: stored description = %q, want %q", got.Description, "first-edit")
	}
}

// TestDeleteRemovesRecordAndIndex verifies the primary record is gone and the
// FTS no longer surfaces it (Delete reached the index too).
func TestDeleteRemovesRecordAndIndex(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "DEL-MPN", Description: "delete-me unique-token"}
	if err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	var ws [8]byte
	if len(s.fts.Search(ws, "DEL-MPN", 10)) == 0 {
		t.Fatal("precondition: FTS did not index DEL-MPN")
	}
	if err := s.Delete(p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(p.ID); err == nil {
		t.Fatal("Get after Delete: expected error, got nil")
	}
	if hits := s.fts.Search(ws, "DEL-MPN", 10); len(hits) != 0 {
		t.Errorf("FTS after Delete returned %d hits, want 0", len(hits))
	}
}

// TestCountScansKeyspace verifies Count returns the number of part records.
func TestCountScansKeyspace(t *testing.T) {
	s := newStore(t)
	if n := s.Count(); n != 0 {
		t.Fatalf("empty store Count = %d, want 0", n)
	}
	for i := 0; i < 3; i++ {
		if err := s.Create(&Part{MPN: "C", PartType: "local"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.Count(); n != 3 {
		t.Fatalf("after 3 Creates Count = %d, want 3", n)
	}
}

// TestIndexTextEmitsWeightedFields guards the contract between Part.indexText
// and internal/index/fts.go's fieldWeight switch: the field keys the FTS
// weights HIGH/MID must be present verbatim, and spec:/custom: prefixes land
// at body weight by falling through to the default branch.
func TestIndexTextEmitsWeightedFields(t *testing.T) {
	p := &Part{
		MPN:          "HIGH-MPN",
		Manufacturer: "HIGH-MFR",
		ViaCode:      "P-VIA",
		Description:  "MID-DESC",
		Category:     "MID-CAT",
		Subcategory:  "MID-SUB",
		Footprint:    "MID-FOOT",
		Tags:         []string{"body-tag"},
		Specs:        map[string]string{"voltage": "5V"},
		CustomFields: map[string]string{"project": "go-parts"},
	}
	f := p.indexText()
	for _, k := range []string{"mpn", "manufacturer", "via_code", "description", "category", "subcategory", "footprint"} {
		if _, ok := f[k]; !ok {
			t.Errorf("indexText missing required field key %q (FTS weighting depends on it)", k)
		}
	}
	if f["mpn"] != "HIGH-MPN" {
		t.Errorf("indexText[mpn] = %q, want HIGH-MPN", f["mpn"])
	}
	if f["spec:voltage"] != "5V" {
		t.Errorf("indexText[spec:voltage] = %q, want 5V", f["spec:voltage"])
	}
	if f["custom:project"] != "go-parts" {
		t.Errorf("indexText[custom:project] = %q, want go-parts", f["custom:project"])
	}
}

// TestTagCounts pins §5.7's dynamic tag facet: Store.TagCounts tallies each
// part's Tags and returns them sorted by count desc then tag asc — the order
// slice 3b's sidebar renders. Mirrors DistinctFootprints' PartsPrefixBound scan
// + skip-undecodable posture (read-only, best-effort).
func TestTagCounts(t *testing.T) {
	store := newStore(t)
	store.Create(&Part{MPN: "a", PartType: "local", Tags: []string{"resistor", "smd"}})
	store.Create(&Part{MPN: "b", PartType: "local", Tags: []string{"resistor"}})
	store.Create(&Part{MPN: "c", PartType: "local", Tags: []string{"capacitor"}})

	got := store.TagCounts()
	if len(got) != 3 {
		t.Fatalf("TagCounts = %d entries, want 3: %+v", len(got), got)
	}
	// resistor(2) first by count; then capacitor(1) and smd(1) alpha asc.
	if got[0].Tag != "resistor" || got[0].Count != 2 {
		t.Errorf("got[0] = %+v, want {resistor 2}", got[0])
	}
	if got[1].Tag != "capacitor" || got[2].Tag != "smd" {
		t.Errorf("count-tie order should be alpha asc (capacitor before smd): %+v", got)
	}
}

// TestTagsLowercasedOnSave pins §5.7's "Resistor and resistor are the same tag"
// contract: Create (and by symmetry Update) MUST lowercase tags before they
// hit the keyspace, so the TagCounts facet never splits on case.
func TestTagsLowercasedOnSave(t *testing.T) {
	store := newStore(t)
	p := &Part{MPN: "x", PartType: "local", Tags: []string{"Resistor", "SMD"}}
	store.Create(p)
	got, _ := store.Get(p.ID)
	for _, tg := range got.Tags {
		if tg != strings.ToLower(tg) {
			t.Errorf("tag %q was not lowercased on save", tg)
		}
	}
}
