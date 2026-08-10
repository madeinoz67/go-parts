package locations

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrentUpdateNoLostUpdate pins §5.14's "reject, never silently
// overwrite" invariant under contention, mirroring the parts package's test of
// the same name. N goroutines each attempt to append a unique marker to the
// same location's Notes, retrying on stale-version rejection. Without the
// per-id striped lock around Update's read-check-write, two concurrent writers
// can both read v1, both pass the check, both write v2 → the first writer's
// marker is silently lost (the §5.14 prohibition). With the lock, losers get a
// stale-version error and retry; no Update is ever silently overwritten, and
// Version increments exactly once per successful Update.
//
// Run with -race -count to stress.
func TestConcurrentUpdateNoLostUpdate(t *testing.T) {
	s, _ := newStore(t)
	l := &Location{Label: "RACE", Notes: ""}
	if err := s.Create(l); err != nil {
		t.Fatal(err)
	}
	const goroutines = 20
	var (
		successCount  int64
		conflictCount int64
	)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := fmt.Sprintf(" g%d", i)
			// Retry loop: an optimistic-concurrency conflict returns an error
			// and the goroutine re-Gets and tries again. The point of the
			// test is that a SUCCESSFUL Update is never a silent overwrite.
			for attempt := 0; attempt < 200; attempt++ {
				cur, err := s.Get(l.ID)
				if err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				modified := *cur // shallow copy — Notes is the only field we mutate
				modified.Notes = cur.Notes + marker
				if err := s.Update(&modified, cur.Version); err == nil {
					atomic.AddInt64(&successCount, 1)
					return
				}
				atomic.AddInt64(&conflictCount, 1)
			}
			t.Errorf("goroutine %d: exhausted retries", i)
		}(i)
	}
	wg.Wait()

	if successCount != goroutines {
		t.Fatalf("successCount = %d, want %d (not all goroutines converged within retry budget)", successCount, goroutines)
	}
	got, err := s.Get(l.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Silent-overwrite signal: every successful Update's marker must be present.
	// If two writers silently overwrote each other, one marker would be missing.
	for i := range goroutines {
		marker := fmt.Sprintf(" g%d", i)
		if !strings.Contains(got.Notes, marker) {
			t.Fatalf("Notes %q missing marker %q (silent overwrite — at least one goroutine's marker was lost)", got.Notes, marker)
		}
	}
	// Silent-overwrite signal #2: Version must equal 1 (initial) + the number
	// of successful Updates.
	if got.Version != 1+int(successCount) {
		t.Fatalf("Version = %d, want %d (silent overwrite — version did not increment once per successful Update)", got.Version, 1+int(successCount))
	}
	// Contention signal: under N concurrent updaters on the same id, at least
	// some Updates must have been rejected for a stale version.
	if conflictCount == 0 {
		t.Fatalf("conflictCount = 0 — expected at least some stale-version rejections under %d concurrent updaters", goroutines)
	}
}

// TestConcurrentUpdateVsDeleteNoResurrection pins §5.14's prohibition against
// silent loss under a Delete-vs-Update race on the same id. Without Delete
// holding lockFor(id), the interleaving
//
//	Update reads location (v) ──┐
//	                          ├── Delete erases record ── Update's version check passes → writes (v+1)
//
// leads to the deleted location being RESURRECTED (the §5.14 prohibition:
// never silently overwrite/lose). Under lockFor, either Delete runs last
// (location gone, stays gone) or Update runs last (its Get sees not-found →
// Update errors, no resurrection).
//
// The invariant asserted: if Delete succeeded, the location MUST be absent —
// no exception. Update may legitimately error under contention (stale version
// or not-found), which is the correct loud-failure behavior.
//
// Probabilistic — run with -race -count to stress.
func TestConcurrentUpdateVsDeleteNoResurrection(t *testing.T) {
	const iterations = 200
	for i := range iterations {
		s, _ := newStore(t)
		l := &Location{Label: "RACE", Notes: "orig"}
		if err := s.Create(l); err != nil {
			t.Fatal(err)
		}
		id, ver := l.ID, l.Version

		var wg sync.WaitGroup
		var delErr, updErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			ll := &Location{ID: id, Label: "RACE", Notes: "edited", Version: ver}
			updErr = s.Update(ll, ver)
		}()
		go func() {
			defer wg.Done()
			delErr = s.Delete(id)
		}()
		wg.Wait()

		_, getErr := s.Get(id)
		// Invariant: if Delete succeeded, the location MUST be gone (no resurrection).
		if delErr == nil && getErr == nil {
			t.Fatalf("iteration %d: resurrection — Delete succeeded but location %q is still present", i, id)
		}
		_ = updErr // Update may legitimately error (stale version / not-found) under contention
	}
}
