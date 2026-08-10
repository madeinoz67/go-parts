package via

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble"
)

func newTestDB(t *testing.T) *pebble.DB {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNewCode_Format(t *testing.T) {
	for _, tc := range []struct{ prefix, want string }{
		{"P-", "P-"},
		{"L-", "L-"},
	} {
		got := NewCode(tc.prefix)
		if !strings.HasPrefix(got, tc.want) {
			t.Fatalf("NewCode(%q) = %q, want prefix %q", tc.prefix, got, tc.want)
		}
		// prefix + exactly 6 base32 chars
		if len(got) != len(tc.want)+6 {
			t.Fatalf("NewCode(%q) = %q, want %d chars total", tc.prefix, got, len(tc.want)+6)
		}
	}
}

func TestReserveAndLookup_RoundTrip(t *testing.T) {
	s := NewStore(newTestDB(t))
	if err := s.Reserve("P-AAAAAA", TypePart, "part-1"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	tt, id, err := s.Lookup("P-AAAAAA")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if tt != TypePart || id != "part-1" {
		t.Fatalf("lookup = (%q,%q), want (part,part-1)", tt, id)
	}
}

func TestReserve_Collision(t *testing.T) {
	s := NewStore(newTestDB(t))
	if err := s.Reserve("L-BBBBBB", TypeLocation, "loc-1"); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	err := s.Reserve("L-BBBBBB", TypeLocation, "loc-2")
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("second reserve err = %v, want ErrCollision", err)
	}
	// original entry untouched
	_, id, err := s.Lookup("L-BBBBBB")
	if err != nil || id != "loc-1" {
		t.Fatalf("after collision, lookup id = %q err %v, want loc-1/nil", id, err)
	}
}

func TestLookup_NotFound(t *testing.T) {
	s := NewStore(newTestDB(t))
	_, _, err := s.Lookup("P-ZZZZZZ")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup miss err = %v, want ErrNotFound", err)
	}
}

func TestRelease_RemovesEntry(t *testing.T) {
	s := NewStore(newTestDB(t))
	if err := s.Reserve("P-CCCCC1", TypePart, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Release("P-CCCCC1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	_, _, err := s.Lookup("P-CCCCC1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-release lookup err = %v, want ErrNotFound", err)
	}
}

// TestReserve_ConcurrentSameCode proves Reserve's check-then-write is
// serialized: two goroutines racing the SAME code → exactly one wins, the
// other gets ErrCollision. Without mu, both could observe not-present and both
// write (no ErrCollision to either) — that is the bug this test pins.
func TestReserve_ConcurrentSameCode(t *testing.T) {
	s := NewStore(newTestDB(t))
	const code = "P-RACE01"
	var wg sync.WaitGroup
	var errs [2]error
	start := make(chan struct{})
	for i := range 2 {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = s.Reserve(code, TypePart, "part-race")
		}()
	}
	close(start)
	wg.Wait()
	// Exactly one nil (winner), one ErrCollision (loser).
	nils, collisions := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			nils++
		case errors.Is(e, ErrCollision):
			collisions++
		default:
			t.Fatalf("unexpected err: %v", e)
		}
	}
	if nils != 1 || collisions != 1 {
		t.Fatalf("concurrent reserve: nils=%d collisions=%d, want 1/1 (mu must serialize the RMW)", nils, collisions)
	}
}
