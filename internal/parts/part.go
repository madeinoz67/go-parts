// Package parts is go-parts' Part entity layer (PRD §6.1): the Part model and a
// CRUD store over Pebble that keeps the field-weighted BM25 FTS in sync.
//
// The store is the single authority for part-record writes: every Create/Update/
// Delete mutates the parts keyspace and the FTS postings in one logical step.
// Optimistic concurrency (§5.14) lives on the Version field — Update rejects a
// stale expectedVersion, never silently overwrites. Stock adjustment (Task 9)
// does NOT bump version; it is a commutative delta on a striped-lock pool and
// will extend the Store struct with that pool.
package parts

import (
	"time"

	"github.com/oklog/ulid/v2"
)

// Part is a single electronics part record (PRD §6.1 subset). Field set matches
// the v1 retrieval/storage contract: the FTS consumes a derived field map (see
// indexText), and the store serializes the whole struct as JSON under the parts
// keyspace. No json tags — REST (Task 10) and the e2e test (Task 12) use Go
// field names; adding tags later is additive and breaks nothing.
type Part struct {
	ID             string
	MPN            string
	Manufacturer   string
	Category       string
	Subcategory    string
	PartType       string // linked | local
	ViaCode        string
	Description    string
	Specs          map[string]string
	Footprint      string
	UnitOfMeasure  string
	PackageQty     int
	QtyOnHand      int
	ReorderPoint   int // PRD's single threshold — badge flips to warn/LOW at/below this
	Tags           []string
	CustomFields   map[string]string
	DatasheetStore string // empty until §5.6 slice
	DatasheetRef   string
	// DefaultLocationID is the part's home location (§6.1 default_location_id).
	// "" = unassigned. When non-empty it MUST reference a real Location; the
	// single_part_only guard (Store.SetLocationPolicy) rejects assignment to a
	// SinglePartOnly location that already holds a different part. Additive
	// (Locations Slice 3a) — old JSON decodes to "" with no schema bump (§5.13).
	DefaultLocationID string
	// DefaultLocationMandatory marks stock for this part as addable only at its
	// default location (§6.1 default_location_mandatory). Enforced on the stock
	// path, not here; the field is carried + round-tripped by the store.
	DefaultLocationMandatory bool
	CreatedBy                string
	UpdatedBy                string
	CreatedAt                time.Time
	UpdatedAt                time.Time
	Version                  int
}

// newID returns a ULID — the canonical part identifier used as the Pebble key
// payload under the parts prefix and as the FTS document id.
func newID() string { return ulid.Make().String() }
