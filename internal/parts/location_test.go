package parts

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// TestPartBackwardCompat_OldJSONNoLocationFields pins §5.13's no-bump contract:
// a JSON blob written before DefaultLocationID/DefaultLocationMandatory existed
// decodes cleanly to ""/false. Additive fields must not force a schema migration.
func TestPartBackwardCompat_OldJSONNoLocationFields(t *testing.T) {
	// Minimal pre-Slice-3a part JSON (no location fields).
	const old = `{"ID":"x","MPN":"old","PartType":"local","Version":1}`
	var p Part
	if err := json.Unmarshal([]byte(old), &p); err != nil {
		t.Fatalf("decode old JSON: %v", err)
	}
	if p.DefaultLocationID != "" {
		t.Errorf("DefaultLocationID = %q, want \"\" (additive field, old JSON)", p.DefaultLocationID)
	}
	if p.DefaultLocationMandatory {
		t.Errorf("DefaultLocationMandatory = true, want false (additive field, old JSON)")
	}
}

// TestListByLocation_CountByLocation_Parity pins the two cross-entity query
// methods: assign N parts to L (and some to other locations / unassigned),
// then ListByLocation(L) returns exactly those N and CountByLocation(L) == N.
// Empty locID is not queryable (returns nil/0).
func TestListByLocation_CountByLocation_Parity(t *testing.T) {
	s := newStore(t)
	const L1, L2 = "loc-1", "loc-2"
	for _, p := range []struct {
		mpn, loc string
	}{
		{"a", L1}, {"b", L1}, {"c", L1}, // 3 in L1
		{"d", L2}, {"e", L2}, // 2 in L2
		{"f", ""}, {"g", ""}, // 2 unassigned
	} {
		if err := s.Create(&Part{MPN: p.mpn, PartType: "local", DefaultLocationID: p.loc}); err != nil {
			t.Fatalf("create %s: %v", p.mpn, err)
		}
	}

	if got, want := s.CountByLocation(L1), 3; got != want {
		t.Errorf("CountByLocation(L1) = %d, want %d", got, want)
	}
	listed := s.ListByLocation(L1)
	if len(listed) != 3 {
		t.Fatalf("ListByLocation(L1) = %d parts, want 3: %+v", len(listed), listed)
	}
	// Every listed part is actually in L1 (no leakage from L2/unassigned).
	for _, p := range listed {
		if p.DefaultLocationID != L1 {
			t.Errorf("ListByLocation(L1) returned part %q with DefaultLocationID=%q", p.MPN, p.DefaultLocationID)
		}
	}
	if got, want := s.CountByLocation(L2), 2; got != want {
		t.Errorf("CountByLocation(L2) = %d, want %d", got, want)
	}
	// Empty locID is not a queryable location.
	if got := s.CountByLocation(""); got != 0 {
		t.Errorf("CountByLocation(\"\") = %d, want 0", got)
	}
	if got := s.ListByLocation(""); got != nil {
		t.Errorf("ListByLocation(\"\") = %v, want nil", got)
	}
	// Unknown location → empty, not an error.
	if got := s.CountByLocation("no-such-loc"); got != 0 {
		t.Errorf("CountByLocation(unknown) = %d, want 0", got)
	}
}

// --- single_part_only guard tests (Slice 3a) -------------------------------

// soloPolicy mimics internal/link.NewPolicy for the part-package-internal
// guard tests: "solo" is a SinglePartOnly location holding the parts already
// assigned to it (via ListByLocation); any other location is unconstrained.
// "missing" models a non-existent location → ErrLocationNotFound.
func soloPolicy(s *Store) LocationPolicy {
	return func(locID, assigningID string) error {
		if locID == "missing" {
			return ErrLocationNotFound
		}
		if locID != "solo" {
			return nil // not SinglePartOnly
		}
		for _, p := range s.ListByLocation(locID) {
			if p.ID != assigningID {
				return ErrLocationSinglePartConflict
			}
		}
		return nil
	}
}

// TestCreate_SinglePartOnlyGuard pins the guard on the Create path: assigning a
// second distinct part to a SinglePartOnly location is rejected.
func TestCreate_SinglePartOnlyGuard(t *testing.T) {
	s := newStore(t)
	s.SetLocationPolicy(soloPolicy(s))

	first := &Part{MPN: "first", PartType: "local", DefaultLocationID: "solo"}
	if err := s.Create(first); err != nil {
		t.Fatalf("create first in solo: %v", err)
	}
	// A second, distinct part in the same SinglePartOnly location → conflict.
	err := s.Create(&Part{MPN: "second", PartType: "local", DefaultLocationID: "solo"})
	if !errors.Is(err, ErrLocationSinglePartConflict) {
		t.Fatalf("create second in solo: err = %v, want ErrLocationSinglePartConflict", err)
	}
	// The rejected second part is never stored: the guard runs BEFORE via-reserve
	// (Create assigns the ULID, checks the policy, only then reserves a code), so
	// there is no via reservation to compensate and no orphan index entry.
	// A part in a non-single location is fine.
	if err := s.Create(&Part{MPN: "third", PartType: "local", DefaultLocationID: "shared"}); err != nil {
		t.Fatalf("create third in shared (non-single): %v", err)
	}
}

// TestCreate_GuardRejectsMissingLocation pins the referential-integrity check:
// assigning to a non-existent location returns ErrLocationNotFound (no silent
// dangling DefaultLocationID).
func TestCreate_GuardRejectsMissingLocation(t *testing.T) {
	s := newStore(t)
	s.SetLocationPolicy(soloPolicy(s))
	err := s.Create(&Part{MPN: "x", PartType: "local", DefaultLocationID: "missing"})
	if !errors.Is(err, ErrLocationNotFound) {
		t.Fatalf("create in missing location: err = %v, want ErrLocationNotFound", err)
	}
}

// TestUpdate_SinglePartOnlyGuard pins the guard on the Update path: re-assigning
// a part already in a SinglePartOnly location (to the same location) is allowed;
// moving a SECOND part in is rejected.
func TestUpdate_SinglePartOnlyGuard(t *testing.T) {
	s := newStore(t)
	s.SetLocationPolicy(soloPolicy(s))

	p1 := &Part{MPN: "p1", PartType: "local", DefaultLocationID: "solo"}
	s.Create(p1)
	// Re-save p1 in solo (no change of location) → allowed.
	p1.Description = "edit"
	if err := s.Update(p1, p1.Version); err != nil {
		t.Fatalf("update p1 in solo (same loc): %v", err)
	}
	// Assign p2 (currently unassigned) into solo → conflict.
	p2 := &Part{MPN: "p2", PartType: "local"}
	s.Create(p2)
	p2.DefaultLocationID = "solo"
	err := s.Update(p2, p2.Version)
	if !errors.Is(err, ErrLocationSinglePartConflict) {
		t.Fatalf("update p2 into solo: err = %v, want ErrLocationSinglePartConflict", err)
	}
	// Moving p1 OUT of solo (clearing) is always allowed.
	p1.DefaultLocationID = ""
	if err := s.Update(p1, p1.Version); err != nil {
		t.Fatalf("update p1 clearing location: %v", err)
	}
	// Now p2 can move into solo (p1 left).
	p2.DefaultLocationID = "solo"
	if err := s.Update(p2, p2.Version); err != nil {
		t.Fatalf("update p2 into solo after p1 left: %v", err)
	}
}

// TestNilPolicy_NoGuard pins the backward-compat posture: a Store with no
// injected policy (the zero value) performs no single_part_only enforcement,
// so standalone/test use that predates locations is unaffected.
func TestNilPolicy_NoGuard(t *testing.T) {
	s := newStore(t) // no SetLocationPolicy
	// Two parts in the same "solo" location — no policy means no enforcement.
	if err := s.Create(&Part{MPN: "a", PartType: "local", DefaultLocationID: "solo"}); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := s.Create(&Part{MPN: "b", PartType: "local", DefaultLocationID: "solo"}); err != nil {
		t.Fatalf("create b (nil policy → no guard): %v", err)
	}
}

// TestSetLocationPolicy_ConcurrentNoRace is the adversary-A4 regression. Before
// atomic.Pointer the policy field was a plain func read by Create/Update and
// written by SetLocationPolicy with no synchronization — a concurrent
// SetLocationPolicy + Create (with a NON-EMPTY DefaultLocationID, which is
// load-bearing: without it the guard short-circuits at p.DefaultLocationID !=
// "" and never reads s.policy, hiding the race) fired -race reliably. The
// atomic.Pointer makes the field unconditionally -race-clean. Run with -race.
func TestSetLocationPolicy_ConcurrentNoRace(t *testing.T) {
	s := newStore(t)
	noop := LocationPolicy(func(string, string) error { return nil })
	s.SetLocationPolicy(noop)

	var wg sync.WaitGroup
	wg.Add(2)
	// Writer: re-set the policy repeatedly.
	go func() {
		defer wg.Done()
		for range 200 {
			s.SetLocationPolicy(noop)
		}
	}()
	// Reader: Create reads s.policy on every call (DefaultLocationID non-empty).
	go func() {
		defer wg.Done()
		for range 200 {
			_ = s.Create(&Part{MPN: "x", PartType: "local", DefaultLocationID: "solo"})
		}
	}()
	wg.Wait()
}

// TestConcurrentAssign_SinglePartOnly_TOCTOU is the §5.14 TOCTOU closure test.
// N goroutines each create a distinct part, then each re-assigns its part into
// the SAME SinglePartOnly location. Without locLocks, multiple goroutines pass
// the empty-location check concurrently and the location ends with >1 part (a
// single_part_only violation). With locLocks, exactly ONE wins and the rest get
// ErrLocationSinglePartConflict. Run with -race.
func TestConcurrentAssign_SinglePartOnly_TOCTOU(t *testing.T) {
	s := newStore(t)
	s.SetLocationPolicy(soloPolicy(s))

	const N = 50
	parts := make([]*Part, N)
	for i := range N {
		p := &Part{MPN: "c", PartType: "local"}
		if err := s.Create(p); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		parts[i] = p
	}

	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		ok, conflict int
		conflictErrs []error
	)
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Reload fresh copy + version (each goroutine races from the same base).
			p, err := s.Get(parts[i].ID)
			if err != nil {
				t.Errorf("get %d: %v", i, err)
				return
			}
			p.DefaultLocationID = "solo"
			err = s.Update(p, p.Version)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrLocationSinglePartConflict):
				conflict++
				conflictErrs = append(conflictErrs, err)
			default:
				t.Errorf("goroutine %d unexpected err: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if ok != 1 {
		t.Errorf("concurrent assign to single-part-only: %d succeeded, want exactly 1 (locLocks TOCTOU closure failed)", ok)
	}
	if conflict != N-1 {
		t.Errorf("conflict count = %d, want %d", conflict, N-1)
	}
	_ = conflictErrs
	// Invariant: the SinglePartOnly location holds exactly one part now.
	if got := s.CountByLocation("solo"); got != 1 {
		t.Errorf("post-race CountByLocation(solo) = %d, want 1 (single_part_only violated)", got)
	}
}
