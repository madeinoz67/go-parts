package components

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
)

// ErrNotFound is the components package's not-found sentinel. Store.Get wraps
// pebble.ErrNotFound with %w so callers test errors.Is(err, components.ErrNotFound)
// WITHOUT importing Pebble — the §5.1 boundary, same pattern as parts/locations.
var ErrNotFound = errors.New("components: not found")

// ErrDuplicate is returned by Add when a Component already exists at the given
// (locID, partID) pair — the uniqueness boundary is one Component per part per
// location (§6.1). A sentinel (not a plain fmt.Errorf) so the REST/UI handler
// can distinguish "create collision" from a generic store error and surface a
// meaningful message rather than a 500.
var ErrDuplicate = errors.New("components: already exists")

// stripeShards is the size of the per-partID striped-lock pool. Same rationale
// as parts.Store.stripeShards: coarse enough to spread contention across a
// homelab-scale corpus, fine enough that distinct partIDs rarely collide. A
// collision over-serializes (two distinct partIDs hashing to the same shard
// serialize for no semantic reason) but NEVER under-serializes — the only
// invariant the lock upholds is that all ops on the SAME partID take the same
// lock, and that's what makes the QtyOnHand re-derivation sound (the
// FindByPart scan sees a consistent, committed view of every Component for
// that partID).
const stripeShards = 64

// Store is the Component entity's CRUD authority (PRD §6.1, §5.14). The
// components keyspace (0x13) is written serially per-call through Pebble. A
// per-partID striped-lock pool serializes Add/AdjustQty/Remove so:
//
//  1. The (locID, partID) duplicate check on Add is TOCTOU-safe (two concurrent
//     Adds at the same pair serialize — the second sees the first's record).
//  2. The read-modify-write on a Component's Quantity + History is atomic.
//  3. The QtyOnHand cache re-derivation (FindByPart scan → sum → parts.SetQty)
//     sees a consistent view: no concurrent writer can add or remove a
//     Component for this partID mid-scan.
//
// Lock ordering: components.lockFor(partID) is OUTERMOST. Under it the code
// calls FindByPart (a lock-free Pebble scan) and then parts.SetQty, which takes
// parts.lockFor(partID) internally. The lock order is always
// components-lock → parts-lock (one direction); parts.Store never calls back
// into components (§5.1 — one-way dependency), so there is no cycle.
//
// The per-partID granularity (rather than per-(locID,partID)) means two
// Components of the same partID at DIFFERENT locations serialize against each
// other. That is correct, not over-conservative: they share the QtyOnHand
// cache, and the recompute must see the sum of ALL locations' quantities.
// Two components of DIFFERENT partIDs never conflict (different scan filters,
// different cache keys) and only collide if they hash to the same shard
// (over-serialization, never under-serialization).
type Store struct {
	db    *pebble.DB
	parts *parts.Store
	locks [stripeShards]sync.Mutex
}

// NewStore returns a Component store over db. ps is the parts.Store used to
// re-derive a Part's QtyOnHand cache after every stock-affecting operation
// (Add/AdjustQty/Remove). The striped-lock pool is zero-initialized (unlocked).
func NewStore(db *pebble.DB, ps *parts.Store) *Store {
	return &Store{db: db, parts: ps}
}

// lockFor returns the mutex governing operations on partID. All Store ops
// that touch a Component's quantity or existence (Add, AdjustQty, Remove) MUST
// take this lock so their read-modify-write critical sections and the QtyOnHand
// cache re-derivation serialize per-partID. Byte-sum hashing, same scheme as
// parts.Store.lockFor (allocation-free, collision-tolerant).
func (s *Store) lockFor(partID string) *sync.Mutex {
	var sum int
	for i := 0; i < len(partID); i++ {
		sum += int(partID[i])
	}
	return &s.locks[sum%stripeShards]
}

// Add creates a Component at (locID, partID) with the given initial quantity
// and tags. If a Component already exists at that pair, returns ErrDuplicate
// (§6.1 — one Component per part per location). The initial Movement
// {Delta: qty, Reason: "initial"} is appended to History so Quantity and
// History[sum(Delta)] agree from the first write. After creating the Component,
// the Part's QtyOnHand cache is re-derived (sum of all Component.Quantity for
// this partID across every location) and written via parts.SetQty.
//
// The entire check-then-create-then-recompute is serialized under
// lockFor(partID) so two concurrent Adds at the same (locID, partID) cannot
// both pass the duplicate check (TOCTOU), and the recompute sees a consistent
// Component set.
func (s *Store) Add(locID, partID string, qty int, tags []string) error {
	mu := s.lockFor(partID)
	mu.Lock()
	defer mu.Unlock()

	var ws [8]byte
	key := keys.ComponentKey(ws, locID, partID)
	_, closer, err := s.db.Get(key)
	if err == nil {
		closer.Close()
		return fmt.Errorf("components: add %s/%s: %w", locID, partID, ErrDuplicate)
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		return fmt.Errorf("components: add %s/%s: %w", locID, partID, err)
	}

	now := time.Now().UTC()
	c := &Component{
		LocationID: locID,
		PartID:     partID,
		Quantity:   qty,
		Tags:       tags,
		History:    []Movement{{Timestamp: now, Delta: qty, Reason: "initial"}},
		CreatedAt:  now,
		UpdatedAt:  now,
		Version:    1,
	}
	if err := s.write(c); err != nil {
		return err
	}
	return s.recomputePartQty(partID)
}

// List returns every Component whose LocationID == locID, via a per-location
// prefix scan over the components keyspace (the "what's in this bin?" query,
// §6.1). Best-effort read-only: skips undecodable records (same posture as
// parts.Store.List). No lock — a Pebble scan is a consistent snapshot.
func (s *Store) List(locID string) []*Component {
	if locID == "" {
		return nil
	}
	var ws [8]byte
	lower, upper := keys.ComponentLocationPrefixBound(ws, locID)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil
	}
	defer it.Close()
	var out []*Component
	for it.First(); it.Valid(); it.Next() {
		var c Component
		if err := json.Unmarshal(it.Value(), &c); err != nil {
			continue
		}
		out = append(out, &c)
	}
	return out
}

// Get reads a single Component by its (locID, partID) composite key. Returns a
// wrapped ErrNotFound on miss, so callers test errors.Is without importing
// Pebble — same boundary as parts/locations.
func (s *Store) Get(locID, partID string) (*Component, error) {
	var ws [8]byte
	val, closer, err := s.db.Get(keys.ComponentKey(ws, locID, partID))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, fmt.Errorf("components: get %s/%s: %w", locID, partID, ErrNotFound)
		}
		return nil, fmt.Errorf("components: get %s/%s: %w", locID, partID, err)
	}
	defer closer.Close()
	var c Component
	if err := json.Unmarshal(val, &c); err != nil {
		return nil, fmt.Errorf("components: decode %s/%s: %w", locID, partID, err)
	}
	return &c, nil
}

// AdjustQty applies a commutative stock delta (§5.14) to the Component's
// Quantity: Quantity += delta, a Movement{Delta: delta, Reason: reason} is
// appended to History, and the Part's QtyOnHand cache is re-derived from the
// full Component set for this partID. A negative delta is stock out; positive
// is stock in.
//
// The entire read-modify-write + cache recompute is serialized under
// lockFor(partID) so two concurrent AdjustQty calls on the same part (whether
// at the same location or different ones) always net their sum in both the
// Component.Quantity and the Part.QtyOnHand cache. Does NOT bump the
// Component's Version (§5.14 — commutative stock delta, same discipline as
// parts.AdjustStock).
func (s *Store) AdjustQty(locID, partID string, delta int, reason string) error {
	mu := s.lockFor(partID)
	mu.Lock()
	defer mu.Unlock()

	c, err := s.Get(locID, partID)
	if err != nil {
		return err
	}
	c.Quantity += delta
	c.History = append(c.History, Movement{
		Timestamp: time.Now().UTC(),
		Delta:     delta,
		Reason:    reason,
	})
	c.UpdatedAt = time.Now().UTC()
	if err := s.write(c); err != nil {
		return err
	}
	return s.recomputePartQty(partID)
}

// Remove deletes the Component at (locID, partID) and re-derives the Part's
// QtyOnHand cache from the remaining Component set (the part's stock across
// every other location). Returns ErrNotFound if no such Component exists.
// Serialized under lockFor(partID) so the recompute sees a consistent view.
func (s *Store) Remove(locID, partID string) error {
	mu := s.lockFor(partID)
	mu.Lock()
	defer mu.Unlock()

	var ws [8]byte
	if _, err := s.Get(locID, partID); err != nil {
		return err
	}
	if err := s.db.Delete(keys.ComponentKey(ws, locID, partID), pebble.Sync); err != nil {
		return fmt.Errorf("components: delete %s/%s: %w", locID, partID, err)
	}
	return s.recomputePartQty(partID)
}

// FindByPart returns every Component whose PartID == partID, via a full
// component-prefix scan + decode filter (§6.1 reverse query: "where is this
// part stocked?"). Best-effort read-only: skips undecodable records. No reverse
// index — v1 scale doesn't justify one (YAGNI; the parts keyspace's
// ListByLocation is the same scan-and-filter shape). No lock — a Pebble scan is
// a consistent snapshot; Add/AdjustQty/Remove callers that need a consistent
// in-lock view call this under lockFor(partID) (via recomputePartQty).
func (s *Store) FindByPart(partID string) []*Component {
	if partID == "" {
		return nil
	}
	var ws [8]byte
	lower, upper := keys.ComponentPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil
	}
	defer it.Close()
	var out []*Component
	for it.First(); it.Valid(); it.Next() {
		var c Component
		if err := json.Unmarshal(it.Value(), &c); err != nil {
			continue
		}
		if c.PartID == partID {
			out = append(out, &c)
		}
	}
	return out
}

// recomputePartQty re-derives the Part's QtyOnHand = sum of every
// Component.Quantity for partID, and writes it via parts.SetQty (no Version
// bump, no FTS). Called under lockFor(partID) by Add/AdjustQty/Remove, so the
// FindByPart scan sees a consistent, committed view of the Component set — no
// concurrent writer can add/remove/adjust a Component for this partID
// mid-scan.
func (s *Store) recomputePartQty(partID string) error {
	total := 0
	for _, c := range s.FindByPart(partID) {
		total += c.Quantity
	}
	return s.parts.SetQty(partID, total)
}

// write serializes c and writes it under the composite components key.
func (s *Store) write(c *Component) error {
	var ws [8]byte
	val, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("components: encode %s/%s: %w", c.LocationID, c.PartID, err)
	}
	if err := s.db.Set(keys.ComponentKey(ws, c.LocationID, c.PartID), val, pebble.Sync); err != nil {
		return fmt.Errorf("components: write %s/%s: %w", c.LocationID, c.PartID, err)
	}
	return nil
}
