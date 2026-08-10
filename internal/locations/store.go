package locations

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
	"github.com/madeinoz67/go-parts/internal/via"
)

// ErrNotFound is the locations package's not-found sentinel. Store.Get wraps
// pebble.ErrNotFound with %w so callers test errors.Is(err, locations.ErrNotFound)
// WITHOUT importing Pebble — the §5.1 boundary.
var ErrNotFound = errors.New("locations: not found")

// ErrHasChildren is returned by Delete when the location has child locations
// (ParentID == id). The caller must reparent the children first. Never cascade.
var ErrHasChildren = errors.New("locations: has children")

// ErrCycle is returned by Update when a ParentID change would form a cycle
// (the new parent is the location itself or one of its descendants).
var ErrCycle = errors.New("locations: parent change would form a cycle")

// stripeShards mirrors parts.Store: 64 is coarse enough to spread contention
// and fine enough that distinct ids rarely collide (collisions over-serialize,
// never under-serialize).
const stripeShards = 64

// Store is the Location entity's CRUD authority (PRD §6.1, §5.14). No FTS —
// locations are navigated/scanned, not full-text-searched. Optimistic
// concurrency on Version backs Update; a per-id striped-lock pool serializes
// Update's read-check-write RMW (including the parent-chain cycle walk) and
// Delete's has-children-check-then-delete.
//
// Lock ordering: lockFor(id) is the OUTERMOST lock. Under it the code calls
// Get/write, and Create/Delete touch the via index (via.Reserve/Release →
// via.mu). Neither via nor (trivially) anything else calls back into locations,
// so there is no cycle. locations MUST NOT import parts.
type Store struct {
	db    *pebble.DB
	via   *via.Store
	locks [stripeShards]sync.Mutex
}

// NewStore returns a Location store over db whose via codes are reserved in the
// shared via index. The viaStore MUST be the single per-process *via.Store for
// db (Slice 0 via-singleton trap) — the CLI constructs its own for a one-shot;
// the daemon (Slice 3/5) threads the one it already built.
func NewStore(db *pebble.DB, viaStore *via.Store) *Store {
	return &Store{db: db, via: viaStore}
}

func (s *Store) lockFor(id string) *sync.Mutex {
	var sum int
	for i := 0; i < len(id); i++ {
		sum += int(id[i])
	}
	return &s.locks[sum%stripeShards]
}

// Create writes a new location record, assigns id/via_code if absent, sets the
// audit timestamps + Version=1. Via-code uses the L- prefix (§5.17) with the
// same collision-retry + write-failure compensation as parts.Store.Create
// (reserve-then-write; Release on write failure so a caller-supplied code is
// never permanently burned). If ParentID is set, the parent must exist.
func (s *Store) Create(l *Location) error {
	now := time.Now().UTC()
	if l.ID == "" {
		l.ID = newID()
	}
	if l.ParentID != "" {
		if _, err := s.Get(l.ParentID); err != nil {
			return fmt.Errorf("locations: parent %s: %w", l.ParentID, err)
		}
	}
	if l.ViaCode == "" {
		for i := 0; i < 8; i++ {
			code := via.NewCode("L-")
			if err := s.via.Reserve(code, via.TypeLocation, l.ID); err == nil {
				l.ViaCode = code
				break
			} else if !errors.Is(err, via.ErrCollision) {
				return fmt.Errorf("locations: via reserve: %w", err)
			} else if i == 7 {
				return fmt.Errorf("locations: via reserve: %w (8 collisions)", via.ErrCollision)
			}
		}
	} else {
		if err := s.via.Reserve(l.ViaCode, via.TypeLocation, l.ID); err != nil {
			return fmt.Errorf("locations: via reserve %s: %w", l.ViaCode, err)
		}
	}
	if l.CreatedBy == "" {
		l.CreatedBy = "local"
	}
	l.CreatedAt, l.UpdatedAt = now, now
	l.Version = 1
	if err := s.write(l); err != nil {
		_ = s.via.Release(l.ViaCode) // compensate the reservation; part not stored
		return err
	}
	return nil
}

// Get reads a single location by id. Returns a wrapped ErrNotFound on miss.
func (s *Store) Get(id string) (*Location, error) {
	var ws [8]byte
	val, closer, err := s.db.Get(keys.LocationKey(ws, id))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, fmt.Errorf("locations: get %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("locations: get %s: %w", id, err)
	}
	defer closer.Close()
	var l Location
	if err := json.Unmarshal(val, &l); err != nil {
		return nil, fmt.Errorf("locations: decode %s: %w", id, err)
	}
	return &l, nil
}

// Update writes l under l.ID after verifying the stored Version equals
// expectedVersion (§5.14). Via-code is immutable post-Create (preserved from
// cur). ParentID changes run the cycle guard (Task 3 adds that; here Update
// preserves via-code + bumps version only — cycle guard lands with Children).
func (s *Store) Update(l *Location, expectedVersion int) error {
	mu := s.lockFor(l.ID)
	mu.Lock()
	defer mu.Unlock()
	cur, err := s.Get(l.ID)
	if err != nil {
		return err
	}
	if cur.Version != expectedVersion {
		return fmt.Errorf("locations: version conflict for %s: stored %d != expected %d", l.ID, cur.Version, expectedVersion)
	}
	// Via-code is immutable (§5.17) — preserve the stored value.
	l.ViaCode = cur.ViaCode
	l.Version = cur.Version + 1
	l.UpdatedAt = time.Now().UTC()
	return s.write(l)
}

// Delete removes a location. Task 2: plain delete + via release. Task 3 adds
// the has-children refusal before the record delete.
func (s *Store) Delete(id string) error {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	cur, err := s.Get(id)
	if err != nil {
		return err
	}
	var ws [8]byte
	if err := s.db.Delete(keys.LocationKey(ws, id), pebble.Sync); err != nil {
		return fmt.Errorf("locations: delete %s: %w", id, err)
	}
	_ = s.via.Release(cur.ViaCode) // best-effort; record already gone
	return nil
}

// List returns every location in Pebble key order (ULID-ordered). Best-effort:
// skips undecodable records (same posture as parts.Store.List).
func (s *Store) List() []*Location {
	var ws [8]byte
	lower, upper := keys.LocationPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil
	}
	defer it.Close()
	var out []*Location
	for it.First(); it.Valid(); it.Next() {
		var l Location
		if err := json.Unmarshal(it.Value(), &l); err != nil {
			continue
		}
		out = append(out, &l)
	}
	return out
}

// Count returns the number of location records via a prefix scan.
func (s *Store) Count() int {
	var ws [8]byte
	lower, upper := keys.LocationPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return 0
	}
	defer it.Close()
	n := 0
	for it.First(); it.Valid(); it.Next() {
		n++
	}
	if err := it.Error(); err != nil {
		return 0
	}
	return n
}

// write serializes l and writes it under the location key. No FTS.
func (s *Store) write(l *Location) error {
	var ws [8]byte
	val, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("locations: encode %s: %w", l.ID, err)
	}
	if err := s.db.Set(keys.LocationKey(ws, l.ID), val, pebble.Sync); err != nil {
		return fmt.Errorf("locations: write %s: %w", l.ID, err)
	}
	return nil
}
