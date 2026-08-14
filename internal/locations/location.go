// Package locations is go-parts' Location entity layer (PRD §6.1): the
// physical-storage Location model + a CRUD store over Pebble. Locations model
// bins/drawers/shelves/boxes (nested), are addressed by a Via code (§5.17), and
// are navigated/scanned — they are NOT full-text-indexed (no FTS, unlike parts).
package locations

import (
	"time"

	"github.com/oklog/ulid/v2"
)

// Location is a single physical storage location (PRD §6.1). Field set matches
// the v1 storage contract; the store serializes the whole struct as JSON under
// the locations keyspace (0x11). No json tags — PascalCase field names, same
// convention as parts.Part.
type Location struct {
	ID             string
	Label          string   // "Bin A3", "Drawer 12"
	ViaCode        string   // "L-7B3D1E" — unique, indexed (§5.17)
	Tags           []string // physical context: "garage", "workbench" — replaces ParentID (flat model)
	CreationMethod string   // "single" | "row" | "grid" | "3d_grid" — reference metadata (creation-only)
	Notes          string
	Archived       bool   // soft-retire (schema v4): hidden from the default Storage view, restorable — never deletes components
	CreatedBy      string // nullable, "local" in v1 (§5.8)
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Version        int // optimistic concurrency, same discipline as Part (§5.14)
}

// newID returns a ULID — the canonical location identifier used as the Pebble
// key payload under the locations prefix.
func newID() string { return ulid.Make().String() }
