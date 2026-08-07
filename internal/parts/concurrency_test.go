package parts

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrentAdjustStock pins §5.14's commutative-delta invariant: N
// goroutines each applying M decrements of `delta` must net exactly N*M*delta,
// regardless of interleaving. Two concurrent -10 must net -20. The per-id
// striped lock serializes same-id AdjustStock calls so the read-modify-write
// on QtyOnHand is atomic; without it, lost updates make the final count
// non-deterministic (and typically much higher than the expected value).
//
// AdjustStock must NOT bump Version — stock is authoritative (§5.14).
func TestConcurrentAdjustStock(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "C1", PartType: "linked", QtyOnHand: 100}
	if err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	const (
		goroutines = 20
		perG       = 10
		delta      = -10
	)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				if err := s.AdjustStock(p.ID, delta, "test"); err != nil {
					t.Errorf("AdjustStock: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	want := 100 + goroutines*perG*delta // 20 * 10 * -10 = -2000 → 100 - 2000 = -1900
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.QtyOnHand != want {
		t.Fatalf("QtyOnHand = %d, want %d (lost update — commutative delta was not atomic under contention)", got.QtyOnHand, want)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1 (AdjustStock must not bump version per §5.14)", got.Version)
	}
}

// TestConcurrentUpdateNoLostUpdate pins §5.14's "reject, never silently
// overwrite" invariant under contention. N goroutines each attempt to append a
// unique tag to the same part, retrying on stale-version rejection. Without the
// per-id striped lock around Update's read-check-write, two concurrent writers
// can both read v1, both pass the check, both write v2 → the first writer's
// tag is silently lost (a silent overwrite, the §5.14 prohibition). With the
// lock, losers get a stale-version error and retry; no Update is ever silently
// overwritten, and Version increments exactly once per successful Update.
//
// Run with -race -count to stress.
func TestConcurrentUpdateNoLostUpdate(t *testing.T) {
	s := newStore(t)
	p := &Part{MPN: "RACE", PartType: "local", Tags: []string{}}
	if err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	const goroutines = 20
	var (
		successCount  int64
		conflictCount int64
	)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tag := fmt.Sprintf("g%d", i)
			// Retry loop: an optimistic-concurrency conflict returns an error
			// and the goroutine re-Gets and tries again. The point of the
			// test is that a SUCCESSFUL Update is never a silent overwrite.
			for attempt := 0; attempt < 200; attempt++ {
				cur, err := s.Get(p.ID)
				if err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				modified := *cur // shallow copy — Tags is the only field we mutate, and we rebuild it below
				modified.Tags = append(append([]string{}, cur.Tags...), tag)
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
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Silent-overwrite signal #1: every successful Update's tag must be present.
	// If two writers silently overwrote each other, one tag would be missing.
	if len(got.Tags) != goroutines {
		t.Fatalf("Tags len = %d, want %d (silent overwrite — at least one goroutine's tag was lost)", len(got.Tags), goroutines)
	}
	// Silent-overwrite signal #2: Version must equal 1 (initial) + the number
	// of successful Updates. Two concurrent writers that both passed the check
	// on v1 and both wrote v2 would leave Version at 2 while successCount is 2
	// (expected 3) — the math breaks under lost update.
	if got.Version != 1+int(successCount) {
		t.Fatalf("Version = %d, want %d (silent overwrite — version did not increment once per successful Update)", got.Version, 1+int(successCount))
	}
	// Contention signal: under N concurrent updaters on the same id, at least
	// some Updates must have been rejected for a stale version (otherwise the
	// per-id lock isn't doing anything, or the test is too small to exercise
	// contention). A zero conflictCount with 20 goroutines indicates the test
	// did not actually stress the locking path.
	if conflictCount == 0 {
		t.Fatalf("conflictCount = 0 — expected at least some stale-version rejections under %d concurrent updaters", goroutines)
	}
	// Uniqueness: no duplicate tags (a duplicate would mean one goroutine's
	// tag silently overwrote another's — the §5.14 prohibition).
	seen := make(map[string]bool, goroutines)
	for _, tag := range got.Tags {
		if seen[tag] {
			t.Fatalf("duplicate tag %q in final state — a silent overwrite happened", tag)
		}
		seen[tag] = true
	}
}
