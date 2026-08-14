package locations

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
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

// ErrVersionConflict is returned by Update when the stored Version no longer
// matches expectedVersion (§5.14 — reject, never silently overwrite). A
// sentinel (not a plain fmt.Errorf) so the UI handler can distinguish it from
// a missing-parent path and surface a reload prompt (the operator's
// stale-field edit must not be applied onto a record state they never saw).
var ErrVersionConflict = errors.New("locations: version conflict")

// ErrHasParts is returned by the delete-path composition (the CLI's `locations
// remove`; the daemon delete handler when Slice 5/6 wires the locations
// transport) when a location still has parts assigned to it. locations.Store
// cannot see the parts keyspace (§5.1 — the two stores are decoupled), so the
// has-parts check is composed at the caller (via components.Store.List or the
// parts keyspace) and this sentinel is the cross-package signal. Never cascade.
var ErrHasParts = errors.New("locations: has parts")

// Store is the Location entity's CRUD authority (PRD §6.1, §5.14). No FTS —
// locations are navigated/scanned, not full-text-searched. Optimistic
// concurrency on Version backs Update; treeMu serializes each RMW so two
// concurrent writers cannot both pass the version check.
//
// Flat-locations model (2026-08-11 principal-directed redesign): no ParentID,
// no tree, no cycle-guard. Every location is a flat bin tagged with its
// physical context ("garage", "workbench"). Components carry the per-location
// stock; a location's "contents" are queried via components.Store.List(id),
// not via a parts scan.
type Store struct {
	db     *pebble.DB
	via    *via.Store
	treeMu sync.Mutex // serializes each Update's RMW so optimistic Version is race-clean
}

// NewStore returns a Location store over db whose via codes are reserved in the
// shared via index. The viaStore MUST be the single per-process *via.Store for
// db (Slice 0 via-singleton trap) — the CLI constructs its own for a one-shot;
// the daemon (Slice 3/5) threads the one it already built.
func NewStore(db *pebble.DB, viaStore *via.Store) *Store {
	return &Store{db: db, via: viaStore}
}

// Create writes a new location record, assigns id/via_code if absent, sets the
// audit timestamps + Version=1. Via-code uses the L- prefix (§5.17) with the
// same collision-retry + write-failure compensation as parts.Store.Create
// (reserve-then-write; Release on write failure so a caller-supplied code is
// never permanently burned). Tags are lowercased via normalizeTags before save.
func (s *Store) Create(l *Location) error {
	normalizeTags(l)
	now := time.Now().UTC()
	if l.ID == "" {
		l.ID = newID()
	}
	if l.ViaCode == "" {
		for i := 0; i < 8; i++ { // index used for last-iteration detection (i == 7)
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
		_ = s.via.Release(l.ViaCode) // compensate the reservation; location not stored
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
// cur). Flat-locations model: no ParentID, no cycle guard, no parent-exists
// check. Tags are normalized via normalizeTags before write.
func (s *Store) Update(l *Location, expectedVersion int) error {
	normalizeTags(l)
	s.treeMu.Lock()
	defer s.treeMu.Unlock()
	cur, err := s.Get(l.ID)
	if err != nil {
		return err
	}
	if cur.Version != expectedVersion {
		return fmt.Errorf("locations: version conflict for %s: stored %d != expected %d: %w", l.ID, cur.Version, expectedVersion, ErrVersionConflict)
	}
	// Via-code is immutable (§5.17) — preserve the stored value.
	l.ViaCode = cur.ViaCode
	l.Version = cur.Version + 1
	l.UpdatedAt = time.Now().UTC()
	return s.write(l)
}

// Delete removes a location. Flat-locations model: plain delete + via release —
// no children (no tree), so no has-children check. The has-parts composition
// (does this bin still hold stock via Components?) is the caller's
// responsibility: locations.Store cannot see the components keyspace (§5.1).
func (s *Store) Delete(id string) error {
	s.treeMu.Lock()
	defer s.treeMu.Unlock()
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

// TagCount is one tag → location-count row for the Storage sidebar facet — the
// locations mirror of parts.TagCount (same field names, so the tag-nav partial
// renders either shape).
type TagCount struct {
	Tag   string
	Count int
}

// TagCounts aggregates locations per tag via one prefix scan (skips
// undecodable records, best-effort — same posture as parts.Store.TagCounts).
// Tags are lowercased on save (normalizeTags), so no normalization happens
// here — a Garage/garage split would be a bug in normalizeTags, not something
// TagCounts defends against. Flat-locations: a location's tags are its
// organizational layer (nesting was removed in the redesign), so the facet
// counts locations, not components. Sorted count desc then tag asc — the
// order the Storage sidebar renders. Nil-safe: a nil *Store (a test Server
// wired without locations) returns nil, mirroring ui.locationOptions' guard.
func (s *Store) TagCounts() []TagCount {
	if s == nil {
		return nil
	}
	var ws [8]byte
	lower, upper := keys.LocationPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil
	}
	defer it.Close()
	counts := make(map[string]int)
	for it.First(); it.Valid(); it.Next() {
		var l Location
		if err := json.Unmarshal(it.Value(), &l); err != nil {
			continue
		}
		for _, tg := range l.Tags {
			counts[tg]++
		}
	}
	out := make([]TagCount, 0, len(counts))
	for tg, c := range counts {
		out = append(out, TagCount{Tag: tg, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

// ByVia resolves a Via code to its Location (via.Lookup → Get). A via-miss is
// wrapped as ErrNotFound so callers test errors.Is(err, ErrNotFound) for both
// "no such via code" and "via resolved but record gone".
func (s *Store) ByVia(code string) (*Location, error) {
	tt, id, err := s.via.Lookup(code)
	if err != nil {
		if errors.Is(err, via.ErrNotFound) {
			return nil, fmt.Errorf("locations: via %s: %w", code, ErrNotFound)
		}
		return nil, fmt.Errorf("locations: via %s: %w", code, err)
	}
	_ = tt // TypeLocation by construction; a mismatch is a corrupt index, not actionable here
	return s.Get(id)
}

// normalizeTags lowercases all tags in place (mirrors parts.Store.normalizeTags
// — tags are the organizational layer for flat bins, replacing ParentID).
// Called by Create and Update so tag-based queries never split on case.
func normalizeTags(l *Location) {
	for i, tg := range l.Tags {
		l.Tags[i] = strings.ToLower(tg)
	}
}

// BulkOpts carries the shared fields applied to every location created by a
// bulk run. Label is NOT here — each row gets its own label from GenerateLabels.
type BulkOpts struct {
	Notes          string
	CreationMethod string // "single" | "row" | "grid" | "3d_grid" — reference metadata
}

// CreateBulk mints one Location per label (shared opts), looping Create. It is
// NOT atomic: a failure mid-bulk returns the successfully-created rows so far
// plus the error (Pebble has no cross-key transactions in v1; a partial bulk is
// reported, not rolled back). Each row is an ordinary Location — creation_method
// is reference metadata, so anything created in bulk can be renamed/reparented/
// removed individually afterward (§7.1).
//
// Recovery: a partial bulk is NOT rolled back; re-running collides on
// already-reserved caller-supplied via-codes and creates duplicate locations on
// random codes (same labels, different codes). To recover from a partial
// failure, list the created locations, diff against intent, and delete the
// unwanted rows. A true idempotent-retry design (batch-id, deterministic codes)
// is deferred to Slice 5 when REST lands; v1 is CLI-only with no daemon
// transport for locations.
func (s *Store) CreateBulk(labels []string, opts BulkOpts) ([]*Location, error) {
	out := make([]*Location, 0, len(labels))
	for _, label := range labels {
		l := &Location{
			Label:          label,
			Notes:          opts.Notes,
			CreationMethod: opts.CreationMethod,
		}
		if err := s.Create(l); err != nil {
			return out, err // partial: created-so-far + the error
		}
		out = append(out, l)
	}
	return out, nil
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
