// Package components is go-parts' Component entity layer (PRD §6.1): the
// Part-at-Location junction. A Component records that a given Part is stocked
// at a given Location, with a quantity, free-form tags, and a movement history
// (commutative stock deltas, §5.14).
//
// A Component is addressed by the composite key (LocationID, PartID) under the
// components keyspace (0x13) — see internal/storage/keys. The (locID, partID)
// pair is the uniqueness boundary: one Component per part per location.
package components

import "time"

// Component is a single Part-at-Location junction (PRD §6.1). Field set
// matches the v1 storage contract; the store (a later task) serializes the
// whole struct as JSON under the components keyspace (0x13). No json tags —
// PascalCase field names, same convention as parts.Part / locations.Location.
//
// Quantity is the commutative sum of every Movement.Delta in History. The
// store maintains that invariant on every stock change (append a Movement,
// recompute Quantity); callers MUST NOT set Quantity independently of History
// except on first Create (Quantity=initial, History=[{Delta=initial}]).
type Component struct {
	LocationID string
	PartID     string
	Quantity   int
	Tags       []string
	History    []Movement
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Version    int // optimistic concurrency, same discipline as Part/Location (§5.14)
}

// Movement is a single stock-adjustment entry in a Component's History
// (§5.14 — commutative deltas). A positive Delta is stock in (order, found,
// return); a negative Delta is stock out (used, lost, transferred). Reason is
// a free-form operator note. The commutative sum of every Movement.Delta in a
// Component's History equals Component.Quantity.
type Movement struct {
	Timestamp time.Time
	Delta     int
	Reason    string
}
