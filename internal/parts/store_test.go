package parts

import (
	"fmt"

	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/via"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db, index.NewFTS(db), via.NewStore(db))
}

// newStoreWithVia is the variant used by the via-spine tests, which need the
// SAME *via.Store the Store uses so they can call Lookup/Reserve directly.
func newStoreWithVia(t *testing.T) (*Store, *via.Store) {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	vs := via.NewStore(db)
	return NewStore(db, index.NewFTS(db), vs), vs
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
		// Distinct MPNs — uniqueness (schema v5) rejects a repeated MPN.
		if err := s.Create(&Part{MPN: fmt.Sprintf("C%d", i), PartType: "local"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.Count(); n != 3 {
		t.Fatalf("after 3 Creates Count = %d, want 3", n)
	}
}

// --- Part identity uniqueness (schema v5, 0x14 index) -----------------------

// TestCreateDuplicateMPNRejected pins the principal-directed contract: a
// second part with an already-pinned MPN is rejected with ErrDuplicateMPN
// and NOTHING is written (no orphaned index entry, Count unchanged).
func TestCreateDuplicateMPNRejected(t *testing.T) {
	s := newStore(t)
	if err := s.Create(&Part{MPN: "RC0805FR-0710KL", PartType: "local"}); err != nil {
		t.Fatal(err)
	}
	err := s.Create(&Part{MPN: "RC0805FR-0710KL", PartType: "local"})
	if !errors.Is(err, ErrDuplicateMPN) {
		t.Fatalf("dup MPN = %v, want ErrDuplicateMPN", err)
	}
	if n := s.Count(); n != 1 {
		t.Errorf("rejected create must write nothing; Count=%d want 1", n)
	}
	// The failed create must not burn the operator's NEXT choice either.
	if err := s.Create(&Part{MPN: "RC0805FR-0710KL2", PartType: "local"}); err != nil {
		t.Errorf("create after rejection failed: %v", err)
	}
}

// TestCreateDuplicateLocalNumberRejected: LocalNumber uniqueness applies to
// non-empty values; EMPTY is exempt (pre-backfill parts carry none).
func TestCreateDuplicateLocalNumberRejected(t *testing.T) {
	s := newStore(t)
	if err := s.Create(&Part{MPN: "A1", LocalNumber: "R001", PartType: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(&Part{MPN: "A2", LocalNumber: "R001", PartType: "local"}); !errors.Is(err, ErrDuplicateLocalNumber) {
		t.Fatalf("dup LocalNumber = %v, want ErrDuplicateLocalNumber", err)
	}
	// Empty is exempt — two parts with no local number coexist.
	if err := s.Create(&Part{MPN: "A3", PartType: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(&Part{MPN: "A4", PartType: "local"}); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateMPNRenameReReserves pins the edit path: renaming MPN re-reserves
// the new value, releases the old (reusable by a later create), and a rename
// onto a pinned value rejects with no partial state.
func TestUpdateMPNRenameReReserves(t *testing.T) {
	s := newStore(t)
	a := &Part{MPN: "OLD", PartType: "local"}
	s.Create(a)
	b := &Part{MPN: "TAKEN", PartType: "local"}
	s.Create(b)

	// Rename a → NEW frees OLD.
	a.MPN = "NEW"
	if err := s.Update(a, a.Version); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(&Part{MPN: "OLD", PartType: "local"}); err != nil {
		t.Errorf("released OLD should be reusable: %v", err)
	}
	// Renaming onto a pinned value rejects; the record is unchanged.
	a.MPN = "TAKEN"
	if err := s.Update(a, a.Version); !errors.Is(err, ErrDuplicateMPN) {
		t.Fatalf("rename onto taken MPN = %v, want ErrDuplicateMPN", err)
	}
	got, _ := s.Get(a.ID)
	if got.MPN != "NEW" {
		t.Errorf("rejected rename must not write; MPN=%q want NEW", got.MPN)
	}
}

// TestDeleteReleasesIdent: deleting a part releases its MPN + LocalNumber for
// reuse.
func TestDeleteReleasesIdent(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "GONE", LocalNumber: "L009", PartType: "local"}
	s.Create(p)
	if err := s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(&Part{MPN: "GONE", LocalNumber: "L009", PartType: "local"}); err != nil {
		t.Errorf("released identities should be reusable: %v", err)
	}
}

// TestConcurrentSameMPNOneWinner: N concurrent creates of one MPN — exactly
// one wins (the identMu check-then-write guard), the rest reject cleanly.
func TestConcurrentSameMPNOneWinner(t *testing.T) {
	s := newStore(t)
	const n = 16
	errs := make(chan error, n)
	for range n {
		go func() { errs <- s.Create(&Part{MPN: "RACE", PartType: "local"}) }()
	}
	ok := 0
	for range n {
		if err := <-errs; err == nil {
			ok++
		} else if !errors.Is(err, ErrDuplicateMPN) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("exactly one winner expected, got %d", ok)
	}
	if c := s.Count(); c != 1 {
		t.Errorf("Count=%d, want 1", c)
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

// TestCreate_ReservesViaCode proves Create writes a via-index entry the
// resolver can look up. Closes the open uniqueness gap (part.go's newViaCode
// TODO: "uniqueness enforced by an indexed write in a later task").
func TestCreate_ReservesViaCode(t *testing.T) {
	s, vs := newStoreWithVia(t)
	p := &Part{MPN: "VIA-1", PartType: "local", Footprint: "0805"}
	if err := s.Create(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ViaCode == "" {
		t.Fatal("Create did not assign a ViaCode")
	}
	tt, id, err := vs.Lookup(p.ViaCode)
	if err != nil {
		t.Fatalf("via lookup: %v", err)
	}
	if tt != via.TypePart || id != p.ID {
		t.Fatalf("via lookup = (%q,%q), want (part,%s)", tt, id, p.ID)
	}
}

// TestDelete_ReleasesViaCode proves Delete removes the via entry (no dangling
// index → a future Create can reuse the resolver cleanly).
func TestDelete_ReleasesViaCode(t *testing.T) {
	s, vs := newStoreWithVia(t)
	p := &Part{MPN: "VIA-2", PartType: "local", Footprint: "0805"}
	if err := s.Create(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	code := p.ViaCode
	if err := s.Delete(p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := vs.Lookup(code); !errors.Is(err, via.ErrNotFound) {
		t.Fatalf("post-delete lookup err = %v, want via.ErrNotFound", err)
	}
}

// TestCreate_CallerSuppliedViaCodeReserved proves a caller-supplied code is
// reserved (not silently accepted duplicate) — the gap-closure.
func TestCreate_CallerSuppliedViaCodeReserved(t *testing.T) {
	s, _ := newStoreWithVia(t)
	a := &Part{ViaCode: "P-DUP001", MPN: "A", PartType: "local", Footprint: "0805"}
	if err := s.Create(a); err != nil {
		t.Fatalf("first create: %v", err)
	}
	b := &Part{ViaCode: "P-DUP001", MPN: "B", PartType: "local", Footprint: "0805"}
	err := s.Create(b)
	if !errors.Is(err, via.ErrCollision) {
		t.Fatalf("duplicate via-code create err = %v, want via.ErrCollision", err)
	}
}

// TestUpdatePreservesViaCode pins §5.17's via-code immutability: a full-record
// Update MUST silently drop any caller-supplied ViaCode change and preserve
// the stored value, so the via index never needs re-pointing on edit. Without
// this guard, slice 3's part-location edits could accidentally regress the
// immutability invariant. (Update already preserves QtyOnHand for the F3
// invariant — this is its via-code analogue.)
func TestUpdatePreservesViaCode(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "IMMUT-1", PartType: "local", Footprint: "0805"}
	if err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	original := p.ViaCode
	if original == "" {
		t.Fatal("Create did not assign a ViaCode")
	}
	// Caller attempts to change the ViaCode on edit.
	p.MPN = "IMMUT-1-edited"
	p.ViaCode = "P-HACKED"
	if err := s.Update(p, p.Version); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ViaCode != original {
		t.Fatalf("stored ViaCode = %q, want %q (via-code MUST be immutable post-Create, §5.17)", got.ViaCode, original)
	}
	if got.MPN != "IMMUT-1-edited" {
		t.Errorf("stored MPN = %q, want IMMUT-1-edited (the legitimate edit must land)", got.MPN)
	}
}

// TestRelease_MakesCodeReservableAgain proves the via.Release compensation
// mechanism works: after Release, a previously-reserved code can be reserved
// again. This is the recovery the Create tail's `s.via.Release(p.ViaCode)`
// gives a caller when write(p) fails — without that compensation, a
// caller-supplied code that failed write would be permanently burned
// (via.ErrCollision forever; Delete can't release it because the part was
// never stored).
//
// Why this tests the mechanism rather than triggering Create's write-failure
// directly: both via.Reserve and Store.write use the SAME *pebble.DB, so any
// fault that breaks write also breaks the earlier Reserve — there is no
// narrow window between them reachable without refactoring parts.Store to
// take a write interface. (The Tier-3 adversary reproduced this reachability
// gap.) So we prove the mechanism — Release flips a reserved code back to
// reservable — which is exactly the property the compensation relies on.
func TestRelease_MakesCodeReservableAgain(t *testing.T) {
	_, vs := newStoreWithVia(t)
	const code = "P-COMP01"
	if err := vs.Reserve(code, via.TypePart, "part-1"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// Simulate the compensation path: write failed → Release the reserved code.
	if err := vs.Release(code); err != nil {
		t.Fatalf("release (compensation): %v", err)
	}
	// The same code MUST be re-reservable now — this is the recovery the
	// compensation gives a caller after a Create write-failure.
	if err := vs.Reserve(code, via.TypePart, "part-2"); err != nil {
		t.Fatalf("re-reserve after Release: %v (compensation mechanism broken — caller code stays burned)", err)
	}
	// And the re-reserve points at the new id (the retry's fresh part).
	tt, id, err := vs.Lookup(code)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if tt != via.TypePart || id != "part-2" {
		t.Fatalf("lookup = (%q,%q), want (part,part-2)", tt, id)
	}
}

// TestReindexAll pins the reindex recovery path (RedTeam: no reindex existed).
// Simulates the two-write partial-failure gap (writePartsKey succeeded, fts.Index
// didn't): drop the FTS entry, verify the part is unsearchable, ReindexAll,
// verify it's searchable again.
func TestReindexAll(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "REIDX", Description: "reindex-test", PartType: "local"}
	if err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	var ws [8]byte
	// Precondition: searchable.
	if hits := s.fts.Search(ws, "REIDX", 10); len(hits) != 1 {
		t.Fatalf("pre-drop: %d hits, want 1", len(hits))
	}
	// Simulate the partial failure: drop the FTS entry (the part is still
	// persisted in the parts keyspace, just not indexed).
	s.fts.Delete(ws, p.ID, p.indexContent())
	if hits := s.fts.Search(ws, "REIDX", 10); len(hits) != 0 {
		t.Fatalf("post-drop: %d hits, want 0 (FTS entry removed)", len(hits))
	}
	// Reindex fixes the missing entry.
	n := s.ReindexAll()
	if n != 1 {
		t.Errorf("ReindexAll = %d parts, want 1", n)
	}
	if hits := s.fts.Search(ws, "REIDX", 10); len(hits) != 1 {
		t.Errorf("post-reindex: %d hits, want 1 (re-indexed)", len(hits))
	}
}
