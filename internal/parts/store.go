package parts

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
	"github.com/madeinoz67/go-parts/internal/via"
)

// ErrNotFound is the parts package's not-found sentinel. Store.Get wraps this
// (with %w) when the underlying Pebble lookup misses, so callers can
// `errors.Is(err, parts.ErrNotFound)` WITHOUT importing the storage engine —
// the §5.1 storage-encapsulation boundary (a Pebble swap must not leak through
// REST/RPC/MCP/Web). Delete/AdjustStock/Update call Get, so they propagate
// parts.ErrNotFound automatically — no per-method translation needed.
var ErrNotFound = errors.New("parts: not found")

// ErrLocationNotFound is returned via the injected LocationPolicy (Locations
// Slice 3a) when a part is assigned to a DefaultLocationID that does not
// reference a real Location. Prevents silent dangling references — a typo'd id
// reaching the store via raw REST JSON. The UI picker (a <select> of real
// locations) cannot produce a bad id, but the store guards the boundary anyway.
var ErrLocationNotFound = errors.New("parts: location not found")

// ErrLocationSinglePartConflict is returned via the injected LocationPolicy
// when a part is assigned to a SinglePartOnly location that already holds a
// different part (§6.1 single_part_only guard). The guard lives on the
// Part-write path (Create/Update) so every surface — CLI/REST/UI — inherits it
// without duplicating the cross-store check.
var ErrLocationSinglePartConflict = errors.New("parts: location is single-part-only and holds another part")

// stripeShards is the size of the per-id striped-lock pool. 64 is coarse enough
// to spread contention across a typical single-vault parts corpus and fine
// enough that distinct ids only rarely collide on the same shard. A collision
// over-serializes (two distinct ids hashing to the same shard serialize against
// each other for no semantic reason) but NEVER under-serializes — that's the
// only invariant the lock has to uphold (all ops on the SAME id take the same
// lock).
const stripeShards = 64

// Store is the Part entity's CRUD authority (PRD §6.1, §5.14). The parts
// keyspace is written serially per-call through Pebble; optimistic concurrency
// on Version backs Update (§5.14 — reject stale expectedVersion, never silently
// overwrite), and a per-id striped-lock pool serializes the read-check-write
// RMW of Update AND the commutative stock delta of AdjustStock so two
// concurrent writers on one part cannot both pass the version check (silent
// overwrite) and two concurrent AdjustStock calls always net their sum.
//
// Lock ordering: lockFor(id) is the OUTERMOST lock for any part operation
// that takes it (Update, AdjustStock, Delete). Under it the code calls
// Get/write/writePartsKey, and write calls fts.Index/fts.Delete which take
// the FTS's internal mu. Delete also touches the via index (via.Release →
// via.mu) within the lockFor critical section, after fts.Delete. So the
// order under lockFor(id) is: fts.mu then via.mu, taken sequentially (never
// simultaneously).
//
// Create does NOT take lockFor (new ULID — no contender can exist) and never
// holds via.mu + fts.mu simultaneously: via.Reserve takes and releases via.mu
// before Create's tail calls write, which then takes fts.mu. Neither the FTS
// nor the via index ever calls back into parts, so there is no cycle.
//
// Slice 3a adds locLocks (a second striped pool, keyed by location id) and an
// optional injected LocationPolicy for the single_part_only guard. The verified
// lock topology (adversary-confirmed acyclic across all Create/Update/Delete
// and locations.Update/Delete/Create paths): partLock and locLock are the only
// structural locks — partLock is outermost when taken (Update, AdjustStock,
// Delete); locLock nests under it on Update's assignment path and is outermost
// on Create's. Under those, the policy calls locations.Get (LOCK-FREE — a bare
// Pebble read, NOT locations.lockFor) and Store.ListByLocation (a lock-free
// keyspace scan). fts.mu and via.mu are LEAF locks, taken one at a time and
// never held simultaneously (Create: via.mu during Reserve, released, then
// fts.mu during write; Delete: fts.mu then via.mu). locations never calls into
// parts, so the one cross-package edge (parts.locLock → the policy's locations
// Get) cannot reverse. No cycle, no self-deadlock (the scan takes no parts lock).
type Store struct {
	db       *pebble.DB
	fts      *index.FTS
	via      *via.Store
	locks    [stripeShards]sync.Mutex
	locLocks [stripeShards]sync.Mutex     // serializes single_part_only assignment to the same location (Slice 3a)
	policy   atomic.Pointer[policyHolder] // the single_part_only guard; zero value (never Store'd) = no enforcement. atomic.Pointer so a concurrent SetLocationPolicy can't race Create/Update reads (adversary A4) — unconditionally -race-clean, not bootstrap-discipline-clean.
}

// NewStore returns a Part store over db whose writes keep fts in sync and
// whose via codes are reserved in the shared via index. The striped-lock pool
// is zero-initialized (unlocked) — NewStore does not need to prime it.
func NewStore(db *pebble.DB, fts *index.FTS, viaStore *via.Store) *Store {
	return &Store{db: db, fts: fts, via: viaStore}
}

// lockFor returns the mutex governing operations on id. All Store ops that
// touch a single part's record (Update, AdjustStock) MUST take this lock so
// their read-modify-write critical sections serialize per-id. Byte-sum hashing
// is allocation-free and collision-tolerant (collisions over-serialize, never
// under-serialize — see stripeShards).
func (s *Store) lockFor(id string) *sync.Mutex {
	var sum int
	for i := 0; i < len(id); i++ {
		sum += int(id[i])
	}
	return &s.locks[sum%stripeShards]
}

// LocationPolicy is the cross-store hook that lets Store enforce the
// single_part_only guard (§6.1) WITHOUT importing the locations package (§5.1
// encapsulation: the two stores stay decoupled; the composition is injected).
// The daemon and CLI inject the real implementation (internal/link.NewPolicy);
// a nil policy (the zero value) means no enforcement — standalone parts.Store
// use and tests that predate locations skip the check.
//
// The hook receives the location being assigned and the id of the part being
// assigned to it. It returns nil if the assignment is allowed, or a wrapped
// error otherwise: ErrLocationNotFound (no such location) or
// ErrLocationSinglePartConflict (SinglePartOnly location already holds a
// different part). The impl reads the Location's SinglePartOnly flag via
// locations.Store and the current occupants via Store.ListByLocation.
type LocationPolicy func(locationID, assigningPartID string) error

// policyHolder wraps LocationPolicy so it can live in an atomic.Pointer
// (atomic.Pointer[T] stores *T). The zero-value Pointer (never Store'd) loads
// as nil → no enforcement, the standalone/test posture.
type policyHolder struct{ fn LocationPolicy }

// SetLocationPolicy installs the single_part_only guard. The store holds the
// policy in an atomic.Pointer, so this is safe to call any time — including
// concurrently with Create/Update (adversary A4): atomic.Pointer.Store/Load
// establishes happens-before without a bootstrap-ordering requirement. The
// daemon and CLI both call it once at bootstrap after locations.Store exists;
// tests call it to exercise the guard. Pass the SAME *Store you are wiring
// (link.NewPolicy captures a store and the guard should scan the one it protects).
func (s *Store) SetLocationPolicy(fn LocationPolicy) {
	s.policy.Store(&policyHolder{fn: fn})
}

// lockForLoc returns the mutex governing single_part_only assignment to locID
// (Slice 3a). Distinct locations spread across the pool; the same location
// always maps to the same lock — the invariant that serializes two concurrent
// assigns of different parts into one SinglePartOnly location (closing the
// per-location TOCTOU that the per-part `locks` pool cannot, since two
// different parts hash to different part-shards). Same byte-sum scheme as
// lockFor.
func (s *Store) lockForLoc(id string) *sync.Mutex {
	var sum int
	for i := 0; i < len(id); i++ {
		sum += int(id[i])
	}
	return &s.locLocks[sum%stripeShards]
}

// Create writes a new part record, assigns id/via_code if absent, sets the
// audit timestamps, and Version=1. The FTS is indexed from the part's field
// map. Idempotent at the FTS layer (re-indexing an existing id is a no-op there
// but Create itself is not idempotent at the parts keyspace — same id always
// overwrites the prior record).
func (s *Store) Create(p *Part) error {
	normalizeTags(p)
	now := time.Now().UTC()
	if p.ID == "" {
		p.ID = newID()
	}
	// single_part_only guard (Slice 3a, §6.1). When assigning to a non-empty
	// default location under an injected policy, take the per-location lock and
	// HOLD it across the via-reserve + write so a concurrent Create/Update
	// assigning a different part to the same SinglePartOnly location cannot
	// interleave: the loser re-checks under locLock and conflicts. Lock order
	// for Create is locLock → via.mu → fts.mu (Create never takes the per-part
	// lock — new ULID, no contender); no path takes these in reverse, so there
	// is no cycle (see Store doc for the full order).
	if h := s.policy.Load(); h != nil && p.DefaultLocationID != "" {
		locMu := s.lockForLoc(p.DefaultLocationID)
		locMu.Lock()
		defer locMu.Unlock()
		if err := h.fn(p.DefaultLocationID, p.ID); err != nil {
			return err
		}
	}
	if p.ViaCode == "" {
		// Generate + reserve with collision-retry. Astronomically rare, but the
		// index is the uniqueness authority, so we loop until Reserve succeeds.
		for i := range 8 {
			code := via.NewCode("P-")
			if err := s.via.Reserve(code, via.TypePart, p.ID); err == nil {
				p.ViaCode = code
				break
			} else if !errors.Is(err, via.ErrCollision) {
				return fmt.Errorf("parts: via reserve: %w", err)
			} else if i == 7 {
				return fmt.Errorf("parts: via reserve: %w (8 collisions)", via.ErrCollision)
			}
		}
	} else {
		// Caller-supplied code: reserve it (closes the unenforced-uniqueness gap).
		if err := s.via.Reserve(p.ViaCode, via.TypePart, p.ID); err != nil {
			return fmt.Errorf("parts: via reserve %s: %w", p.ViaCode, err)
		}
	}
	if p.CreatedBy == "" {
		p.CreatedBy = "local"
	}
	p.UpdatedBy = p.CreatedBy
	p.CreatedAt, p.UpdatedAt = now, now
	p.Version = 1
	// Compensate the via reservation if the write fails. By this point
	// via.Reserve has already recorded code→{type,id}; a write failure means
	// the part is NOT stored, so without a Release the reserved code would be
	// orphaned. For auto-generated codes a retry makes a fresh code (harmless);
	// for caller-supplied codes the operator's chosen code would be permanently
	// burned (via.ErrCollision forever — Delete requires the part to exist, so
	// nothing else would ever Release it). Release is best-effort: a Release
	// failure leaves a dangling index entry (random codes block nothing in
	// practice), which is strictly better than the burned-code outcome.
	if err := s.write(p); err != nil {
		_ = s.via.Release(p.ViaCode)
		return err
	}
	return nil
}

// Get reads a single part record by id. Returns a wrapped parts.ErrNotFound
// (which itself wraps pebble.ErrNotFound) when the record is absent — callers
// outside the package test with errors.Is(err, parts.ErrNotFound) so the
// storage engine does not leak past the §5.1 boundary.
func (s *Store) Get(id string) (*Part, error) {
	var ws [8]byte
	val, closer, err := s.db.Get(keys.PartsKey(ws, id))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, fmt.Errorf("parts: get %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("parts: get %s: %w", id, err)
	}
	defer closer.Close()
	var p Part
	if err := json.Unmarshal(val, &p); err != nil {
		return nil, fmt.Errorf("parts: decode %s: %w", id, err)
	}
	return &p, nil
}

// Update writes p under p.ID after verifying the stored Version equals
// expectedVersion (§5.14 — reject, never silently overwrite). On success the
// stored Version is bumped by 1 and the same bump is reflected on p. The FTS
// entry for the prior content is removed before the new content is indexed
// (Index's idempotency guard would otherwise treat a still-indexed id as a
// no-op and leave stale postings in place).
//
// Stock contract: QtyOnHand is preserved from the in-lock stored record on
// every Update — the caller's p.QtyOnHand is IGNORED. Stock is AdjustStock's
// exclusive domain (§5.14: commutative delta, no version bump); a full-record
// edit must not touch it. Without this, a Get→modify→Update caller carrying a
// stale QtyOnHand from their outer Get would silently overwrite a concurrent
// AdjustStock delta (the F3 race: AdjustStock doesn't bump Version, so the
// caller's version check still passes). Stock changes go through AdjustStock.
//
// The entire read-check-write (Get → version-check → Delete FTS → write) is
// serialized under lockFor(p.ID) so two concurrent Updates on the same part
// cannot both pass the version check and silently overwrite each other (the
// §5.14 prohibition). The lock is the OUTERMOST lock held; under it the code
// takes fts.mu (one direction, no cycle — see Store doc).
//
// Update DOES NOT preserve CreatedAt/CreatedBy if the caller passes a Part
// missing them — the caller (typically REST T10) is expected to Get-then-edit
// so the create-time audit fields round-trip. Documented as a T10 concern.
func (s *Store) Update(p *Part, expectedVersion int) error {
	normalizeTags(p)
	mu := s.lockFor(p.ID)
	mu.Lock()
	defer mu.Unlock()
	cur, err := s.Get(p.ID)
	if err != nil {
		return err
	}
	if cur.Version != expectedVersion {
		return fmt.Errorf("parts: version conflict for %s: stored %d != expected %d", p.ID, cur.Version, expectedVersion)
	}
	// single_part_only guard (Slice 3a, §6.1). Fires only on a real assignment:
	// new DefaultLocationID is non-empty AND differs from the stored value.
	// partLock is already held; locLock(new location) is taken UNDER it (fixed
	// order partLock → locLock) and held across fts.Delete + write so two
	// concurrent assigns of distinct parts to the same SinglePartOnly location
	// serialize — the loser re-checks under locLock and conflicts. The OLD
	// location is intentionally not locked: removing a part can never violate
	// single_part_only (a location with ≤1 occupant after removal is fine). defer
	// is LIFO, so locLock releases before partLock — symmetric acquire/release.
	reassign := p.DefaultLocationID != "" && p.DefaultLocationID != cur.DefaultLocationID
	if h := s.policy.Load(); reassign && h != nil {
		locMu := s.lockForLoc(p.DefaultLocationID)
		locMu.Lock()
		defer locMu.Unlock()
		if err := h.fn(p.DefaultLocationID, p.ID); err != nil {
			return err
		}
	}
	// Delete the prior FTS entry before writing the new record so the Index
	// call's idempotency guard doesn't no-op on the still-indexed id.
	var ws [8]byte
	s.fts.Delete(ws, p.ID, cur.indexContent())
	// Preserve authoritative stock: ignore the caller's p.QtyOnHand (which may
	// be stale from an outer Get) and write the in-lock current value. Stock is
	// AdjustStock's exclusive domain (§5.14); Update must not touch it.
	p.QtyOnHand = cur.QtyOnHand
	// Via-code is immutable post-Create (a scannable identity, §5.17). Preserve
	// the stored value and ignore the caller's, so the via index never needs
	// re-pointing on a full-record edit.
	p.ViaCode = cur.ViaCode
	p.Version = cur.Version + 1
	p.UpdatedAt = time.Now().UTC()
	if p.UpdatedBy == "" {
		p.UpdatedBy = "local"
	}
	return s.write(p)
}

// Delete removes a part record and its FTS entry. Idempotent at the FTS layer;
// the parts-keyspace delete is a Pebble no-op if the record is already gone
// (but a prior Get means we error on missing records before reaching the delete).
//
// The entire read-modify-write (Get → fts.Delete → db.Delete) is serialized
// under lockFor(id) so Delete cannot interleave with Update/AdjustStock on the
// same id (§5.14). Without this lock, Update could read the part (v), Delete
// could remove record + FTS, then Update's version check still passes and it
// writes the record back (v+1) → the deleted part is resurrected. Under the
// lock, either Delete runs last (part gone, stays gone) or Update runs last
// (Update's Get sees not-found → Update errors, no resurrection). The lock is
// the OUTERMOST lock held; under it the code takes fts.mu (one direction, no
// cycle — see Store doc).
func (s *Store) Delete(id string) error {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	cur, err := s.Get(id)
	if err != nil {
		return err
	}
	var ws [8]byte
	s.fts.Delete(ws, id, cur.indexContent())
	if err := s.db.Delete(keys.PartsKey(ws, id), pebble.Sync); err != nil {
		return fmt.Errorf("parts: delete %s: %w", id, err)
	}
	// Release the via index entry. The record is already gone; a Release failure
	// leaves a dangling index entry → Lookup returns this id → Get misses → 404.
	// Codes are random, so a dangling entry blocks nothing in practice.
	_ = s.via.Release(cur.ViaCode)
	return nil
}

// Count returns the number of part records via a prefix scan over the parts
// keyspace. Used by GET /stats. Encapsulates the scan so rest does not import
// pebble or reach into unexported state.
func (s *Store) Count() int {
	var ws [8]byte
	lower, upper := keys.PartsPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return 0 // best-effort: a failed scan reports zero rather than a misleading count
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

// List returns every part record via a prefix scan over the parts keyspace, in
// Pebble key order (ULID-ordered by part ID). Used by the web UI's empty-query
// search (the table's initial-load + cleared-search cases). Encapsulates the
// scan + decode so the UI does not import pebble or reach into unexported state.
// A failed scan or decode reports a partial result (the records decoded so far)
// rather than failing the whole call — the UI is best-effort read-only.
func (s *Store) List() []*Part {
	var ws [8]byte
	lower, upper := keys.PartsPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil
	}
	defer it.Close()
	var out []*Part
	for it.First(); it.Valid(); it.Next() {
		var p Part
		if err := json.Unmarshal(it.Value(), &p); err != nil {
			continue // skip undecodable record rather than failing the whole list
		}
		out = append(out, &p)
	}
	return out
}

// ListByLocation returns every part whose DefaultLocationID == locID, via a
// prefix scan over the parts keyspace + a decode filter (Locations Slice 3a,
// §6.1). Powers scan-to-find ("what's in this bin?") and the single_part_only
// guard's "does this location already hold a different part" check. Best-effort
// read-only: skips undecodable records (same posture as List). No reverse index
// — v1 scale doesn't justify one (YAGNI; the spec §13 defers location→[part ids]).
func (s *Store) ListByLocation(locID string) []*Part {
	if locID == "" {
		return nil // unassigned is not a queryable location
	}
	var ws [8]byte
	lower, upper := keys.PartsPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil
	}
	defer it.Close()
	var out []*Part
	for it.First(); it.Valid(); it.Next() {
		var p Part
		if err := json.Unmarshal(it.Value(), &p); err != nil {
			continue
		}
		if p.DefaultLocationID == locID {
			out = append(out, &p)
		}
	}
	return out
}

// CountByLocation returns the number of parts whose DefaultLocationID == locID.
// Powers the delete-refuse-has-parts guard composed at the caller layer (the
// CLI's `locations remove`; the daemon delete handler when Slice 5/6 wires the
// locations transport). locID == "" returns 0 (unassigned is not queryable).
func (s *Store) CountByLocation(locID string) int {
	if locID == "" {
		return 0
	}
	var ws [8]byte
	lower, upper := keys.PartsPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return 0
	}
	defer it.Close()
	n := 0
	for it.First(); it.Valid(); it.Next() {
		var p Part
		if err := json.Unmarshal(it.Value(), &p); err != nil {
			continue
		}
		if p.DefaultLocationID == locID {
			n++
		}
	}
	if err := it.Error(); err != nil {
		return 0
	}
	return n
}

// DistinctFootprints returns the sorted set of distinct non-empty Footprint
// values across all part records, via a prefix scan over the parts keyspace
// (same PartsPrefixBound pattern as Count/List). Used by the web UI's create +
// edit forms to populate a <datalist> of existing footprints — selectable from
// the corpus, typeable for a new value. Encapsulates the scan so the UI does
// not import pebble or reach into unexported state. A failed scan or decode
// reports a partial result (the values decoded so far) rather than failing the
// whole call — the UI is best-effort read-only, matching List's posture.
func (s *Store) DistinctFootprints() []string {
	var ws [8]byte
	lower, upper := keys.PartsPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil
	}
	defer it.Close()
	seen := make(map[string]struct{})
	for it.First(); it.Valid(); it.Next() {
		var p Part
		if err := json.Unmarshal(it.Value(), &p); err != nil {
			continue // skip undecodable record rather than failing the whole scan
		}
		if p.Footprint == "" {
			continue
		}
		seen[p.Footprint] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for fp := range seen {
		out = append(out, fp)
	}
	sort.Strings(out)
	return out
}

// TagCount is a single tag-facet entry: the tag and how many parts carry it.
type TagCount struct {
	Tag   string
	Count int
}

// TagCounts returns every tag in use across the catalog with its part count,
// sorted by count desc then tag asc — the §5.7/§6.2 dynamic sidebar facet.
// Same PartsPrefixBound scan as List/DistinctFootprints; best-effort read-only
// (skips undecodable records). Tags are lowercased on save (normalizeTags), so
// no normalization happens here — a Resistor/resistor split would be a bug in
// normalizeTags, not something TagCounts defends against.
func (s *Store) TagCounts() []TagCount {
	var ws [8]byte
	lower, upper := keys.PartsPrefixBound(ws)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil
	}
	defer it.Close()
	counts := make(map[string]int)
	for it.First(); it.Valid(); it.Next() {
		var p Part
		if err := json.Unmarshal(it.Value(), &p); err != nil {
			continue
		}
		for _, tg := range p.Tags {
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

// normalizeTags lowercases all tags in place (§5.7 — Resistor and resistor are
// the same tag). Called by Create and Update so the sidebar facet (TagCounts)
// never splits on case. Tags persist lowercased; queries/filters that compare
// against tags must lowercase their comparison side (slice 3b's responsibility).
func normalizeTags(p *Part) {
	for i, tg := range p.Tags {
		p.Tags[i] = strings.ToLower(tg)
	}
}

// AdjustStock applies a commutative stock delta (§5.14) to QtyOnHand under a
// per-id striped lock. Two concurrent -10 calls always net -20 regardless of
// interleaving; the read-modify-write on QtyOnHand is atomic per-id.
//
// Stock is authoritative: AdjustStock does NOT bump Version (§5.14) and does
// NOT touch the FTS — QtyOnHand is not an indexed field, so the FTS content is
// unchanged. (Stock changes write the parts keyspace only via writePartsKey.)
//
// The `reason` parameter is accepted for a future stock-movement audit log
// (PRD §5.14 envisions a movement history); it is not persisted in v1.
//
// The lock is the OUTERMOST lock held; under it the code calls writePartsKey
// (no further locks) — there is no fts.mu interaction here because the FTS is
// untouched. AdjustStock-vs-Update on the same id serialize via lockFor; an
// AdjustStock running concurrently on the same id as an Update cannot see a
// half-written FTS or a stale Version.
func (s *Store) AdjustStock(id string, delta int, reason string) error {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	p, err := s.Get(id)
	if err != nil {
		return err
	}
	p.QtyOnHand += delta
	return s.writePartsKey(p)
}

// SetQty writes JUST the QtyOnHand field (no Version bump, no FTS) — the cache
// writer for the Component store's stock-adjustment path (§6.1). Reads the part
// under lockFor(id), sets QtyOnHand=qty, writes the parts key only via
// writePartsKey. Mirrors the AdjustStock no-FTS discipline: QtyOnHand is not an
// indexed field, so the FTS is untouched. Under lockFor(id) so it serializes
// with Update/AdjustStock on the same id (§5.14). Called by components.Store
// after Add/AdjustQty/Remove re-derives the Part's total stock from its
// Component set.
func (s *Store) SetQty(id string, qty int) error {
	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	p, err := s.Get(id)
	if err != nil {
		return err
	}
	p.QtyOnHand = qty
	return s.writePartsKey(p)
}

// ReindexAll re-indexes every part into the FTS, fixing entries missed by a
// partial failure (writePartsKey succeeded but fts.Index didn't — a SIGKILL/OOM
// between the two Pebble writes). Parts already correctly indexed are no-ops
// (the FTS idempotency guard: Index skips ids in the indexed-set). Returns the
// count of parts scanned. Does NOT fix stale entries (a part whose content
// changed but whose old FTS entry wasn't deleted) — that needs a full FTS
// keyspace reset, a future enhancement. (RedTeam: no reindex path existed.)
func (s *Store) ReindexAll() int {
	var ws [8]byte
	n := 0
	for _, p := range s.List() {
		s.fts.Index(ws, p.ID, p.indexText())
		n++
	}
	return n
}

// write serializes p and writes it under the parts key, then indexes its field
// map into the FTS. Called by Create and Update.
func (s *Store) write(p *Part) error {
	if err := s.writePartsKey(p); err != nil {
		return err
	}
	var ws [8]byte
	s.fts.Index(ws, p.ID, p.indexText())
	return nil
}

// writePartsKey serializes p and writes it under the parts key (no FTS, no
// version bump). Used by write (Create/Update — which then indexes) and by
// AdjustStock (where the FTS content is unchanged because QtyOnHand is not an
// indexed field). Extracted so AdjustStock does not rely on the FTS
// idempotency guard to no-op an unchanged index.
func (s *Store) writePartsKey(p *Part) error {
	var ws [8]byte
	val, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("parts: encode %s: %w", p.ID, err)
	}
	if err := s.db.Set(keys.PartsKey(ws, p.ID), val, pebble.Sync); err != nil {
		return fmt.Errorf("parts: write %s: %w", p.ID, err)
	}
	return nil
}

// indexText returns the field map the FTS consumes for this part. The keys
// MUST match the fieldWeight switch in internal/index/fts.go:
//   - HIGH (3.0): mpn, manufacturer, via_code
//   - MID  (2.0): description, category, subcategory, footprint
//   - BODY (1.0): everything else (tags, spec:<k>, custom:<k>)
//
// Specs and CustomFields are emitted with `spec:`/`custom:` prefixes so they
// fall through to the body-weight default branch — their keys never collide
// with the HIGH/MID names.
func (p *Part) indexText() map[string]string {
	f := map[string]string{
		"mpn":          p.MPN,
		"manufacturer": p.Manufacturer,
		"via_code":     p.ViaCode,
		"description":  p.Description,
		"category":     p.Category,
		"subcategory":  p.Subcategory,
		"footprint":    p.Footprint,
		"tags":         strings.Join(p.Tags, " "),
	}
	for k, v := range p.Specs {
		f["spec:"+k] = v
	}
	for k, v := range p.CustomFields {
		f["custom:"+k] = v
	}
	return f
}

// indexContent is the flat content string fed to FTS.Delete. The FTS posting
// key shape puts id at the END (after a 0x00 sep), so delete-by-id is not
// possible — the caller must supply the document's text so the FTS can recover
// the term set. Tokenize splits on non-alphanumerics, so joining values with
// spaces yields the same token multiset Index produced (the field-weighting
// only affects tf magnitude, not which terms are present). Keys are emitted in
// a deterministic sorted order to keep the content string reproducible.
func (p *Part) indexContent() string {
	m := p.indexText()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(m[k])
	}
	return b.String()
}
