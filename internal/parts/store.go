package parts

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
)

// Store is the Part entity's CRUD authority. It owns no locks of its own in
// Task 8 — the parts keyspace is written serially per-call. Task 9 EXTENDS this
// struct to add a striped-lock pool ([64]sync.Mutex) for AdjustStock's
// commutative stock deltas; do not pre-add it here.
type Store struct {
	db  *pebble.DB
	fts *index.FTS
}

// NewStore returns a Part store over db whose writes keep fts in sync.
func NewStore(db *pebble.DB, fts *index.FTS) *Store {
	return &Store{db: db, fts: fts}
}

// Create writes a new part record, assigns id/via_code if absent, sets the
// audit timestamps, and Version=1. The FTS is indexed from the part's field
// map. Idempotent at the FTS layer (re-indexing an existing id is a no-op there
// but Create itself is not idempotent at the parts keyspace — same id always
// overwrites the prior record).
func (s *Store) Create(p *Part) error {
	now := time.Now().UTC()
	if p.ID == "" {
		p.ID = newID()
	}
	if p.ViaCode == "" {
		p.ViaCode = newViaCode()
	}
	if p.CreatedBy == "" {
		p.CreatedBy = "local"
	}
	p.UpdatedBy = p.CreatedBy
	p.CreatedAt, p.UpdatedAt = now, now
	p.Version = 1
	return s.write(p)
}

// Get reads a single part record by id. Returns a wrapped pebble.ErrNotFound
// when the record is absent.
func (s *Store) Get(id string) (*Part, error) {
	var ws [8]byte
	val, closer, err := s.db.Get(keys.PartsKey(ws, id))
	if err != nil {
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
// Update DOES NOT preserve CreatedAt/CreatedBy if the caller passes a Part
// missing them — the caller (typically REST T10) is expected to Get-then-edit
// so the create-time audit fields round-trip. Documented as a T10 concern.
func (s *Store) Update(p *Part, expectedVersion int) error {
	cur, err := s.Get(p.ID)
	if err != nil {
		return err
	}
	if cur.Version != expectedVersion {
		return fmt.Errorf("parts: version conflict for %s: stored %d != expected %d", p.ID, cur.Version, expectedVersion)
	}
	// Delete the prior FTS entry before writing the new record so the Index
	// call's idempotency guard doesn't no-op on the still-indexed id.
	var ws [8]byte
	s.fts.Delete(ws, p.ID, cur.indexContent())
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
func (s *Store) Delete(id string) error {
	cur, err := s.Get(id)
	if err != nil {
		return err
	}
	var ws [8]byte
	s.fts.Delete(ws, id, cur.indexContent())
	if err := s.db.Delete(keys.PartsKey(ws, id), pebble.Sync); err != nil {
		return fmt.Errorf("parts: delete %s: %w", id, err)
	}
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

// write serializes p and writes it under the parts key, then indexes its field
// map into the FTS. Called by Create and Update.
func (s *Store) write(p *Part) error {
	var ws [8]byte
	val, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("parts: encode %s: %w", p.ID, err)
	}
	if err := s.db.Set(keys.PartsKey(ws, p.ID), val, pebble.Sync); err != nil {
		return fmt.Errorf("parts: write %s: %w", p.ID, err)
	}
	s.fts.Index(ws, p.ID, p.indexText())
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
