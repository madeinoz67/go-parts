// Package via is go-parts' Via-code index (PRD §5.17): the shared spine that
// makes a Via code unique across entity types and resolvable in one indexed
// lookup. Locations (Slice 1) and Parts both write here; GET /via/{code}
// reads here. Depends only on Pebble + internal/storage/keys — it never
// imports an entity package, so there is no upward dependency or cycle.
//
// The keyspace is 0x12 (kind | ws(8) | code-string); ws is zero in v1.
package via

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
)

// EntityType tags which entity a Via code points at. The generic resolver
// (GET /via/{code}) reads this to dispatch to the right store.
type EntityType string

const (
	TypePart     EntityType = "part"
	TypeLocation EntityType = "location"
)

// ErrNotFound is the not-found sentinel for Lookup. Wraps pebble.ErrNotFound
// so callers test with errors.Is without importing Pebble (§5.1 boundary).
var ErrNotFound = errors.New("via: not found")

// ErrCollision is returned by Reserve when the code is already taken. The
// caller's collision-retry loop regenerates via NewCode until Reserve succeeds.
var ErrCollision = errors.New("via: code collision")

// entry is the JSON value stored under each via key: {type,id}.
type entry struct {
	Type EntityType `json:"type"`
	ID   string     `json:"id"`
}

// NewCode returns a type-prefixed 6-char code: "P-XXXXXX" or "L-XXXXXX".
// 5 random bytes → base32 (10 chars) → first 6. Unique-enough for single-vault
// v1; the index + collision-retry make actual uniqueness hold even on the
// astronomically rare dup. Generalized from parts.newViaCode.
//
// prefix is the "<letter>-" entity convention ("P-" for parts, "L-" for
// locations). Callers MUST NOT pass empty or garbage — the result would be a
// 6-char code with no entity discriminant, breaking the resolver's
// human-readable shape and colliding visually across entity types.
func NewCode(prefix string) string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return prefix + base32.StdEncoding.EncodeToString(b[:])[:6]
}

// Store is the Via-code index authority. One instance is shared across entity
// stores (constructed once at daemon bootstrap, passed to parts + locations).
type Store struct {
	db *pebble.DB
	mu sync.Mutex // serializes Reserve's check-then-write (see TestReserve_ConcurrentSameCode)
}

// NewStore returns a via index over db. The mutex is per-instance, so there
// must be exactly ONE *Store per *pebble.DB — constructing two over the same
// DB re-opens the same-code collision hole (each instance's Get-then-Set RMW
// is invisible to the other; with two instances, two concurrent Reserves of
// the same code both observe not-present and both write). The daemon
// constructs one *Store at bootstrap and threads it to every entity store
// (parts, later locations); any new caller (including test helpers) MUST
// reuse that instance, not call NewStore again for the same DB.
func NewStore(db *pebble.DB) *Store {
	return &Store{db: db}
}

// Reserve writes the index entry for code→{type,id}. It is the uniqueness
// authority: a code already present returns ErrCollision (the caller
// regenerates). The check-then-write is serialized under mu so two concurrent
// Reserves of the same code cannot both observe not-present and both write —
// exactly one wins, the other gets ErrCollision.
func (s *Store) Reserve(code string, t EntityType, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ws [8]byte
	key := keys.ViaKey(ws, code)
	_, closer, err := s.db.Get(key)
	if err == nil {
		_ = closer.Close()
		return ErrCollision
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		return fmt.Errorf("via: reserve %s: %w", code, err)
	}
	val, mErr := json.Marshal(entry{Type: t, ID: id})
	if mErr != nil {
		return fmt.Errorf("via: encode %s: %w", code, mErr)
	}
	if err := s.db.Set(key, val, pebble.Sync); err != nil {
		return fmt.Errorf("via: reserve %s: %w", code, err)
	}
	return nil
}

// Lookup resolves a code to {type, id}. Powers GET /via/{code}.
func (s *Store) Lookup(code string) (EntityType, string, error) {
	var ws [8]byte
	val, closer, err := s.db.Get(keys.ViaKey(ws, code))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return "", "", ErrNotFound
		}
		return "", "", fmt.Errorf("via: lookup %s: %w", code, err)
	}
	defer closer.Close()
	var e entry
	if err := json.Unmarshal(val, &e); err != nil {
		return "", "", fmt.Errorf("via: decode %s: %w", code, err)
	}
	return e.Type, e.ID, nil
}

// Release removes the index entry. Called on entity delete. Pebble Delete is
// idempotent (a missing key is a no-op), so releasing a twice-deleted code is
// safe. Takes mu for defensive parity with Reserve so the Release path shares
// Reserve's serialization discipline (pure Delete interleavings on disjoint
// keys cannot corrupt the index; mu keeps the Release API posture uniform
// with Reserve's RMW).
func (s *Store) Release(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ws [8]byte
	if err := s.db.Delete(keys.ViaKey(ws, code), pebble.Sync); err != nil {
		return fmt.Errorf("via: release %s: %w", code, err)
	}
	return nil
}
