// Package link is the parts↔locations↔components composition layer (PRD §5).
// The three stores are deliberately decoupled — parts, locations, and components
// do not import each other (§5.1 encapsulation: a Pebble swap on any keyspace
// must not leak through the others). The Via resolver is the one composition
// point that holds all three: a via code resolves to a Part, or to a Location
// WITH its embedded Component set (the scan-to-find result — "what's in this
// bin?").
//
// Flat-locations model (2026-08-11 redesign): there is no single_part_only
// guard (the policy seam is gone) and no DefaultLocationID on Part. A
// location's "contents" are its Components (one Component per part per
// location, with a quantity + history), NOT parts scanned by a default pointer.
package link

import (
	"fmt"

	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

// Resolved is the Via-resolver output (§5.17 Scan-to-Find): the entity a Via
// code points at, with a location's Components embedded. Type tags the payload
// so a REST/JSON client dispatches; for a location, Components IS the
// scan-to-find result (the bin's contents). No json tags — PascalCase field
// names, same wire convention as parts.Part/locations.Location (api.md
// §"JSON field names").
type Resolved struct {
	Type       string
	Location   *locations.Location
	Components []*components.Component
	Part       *parts.Part
}

// Resolve resolves a Via code to its entity (the generic resolver, §5.17 — one
// endpoint, not one per entity). For a location the result embeds the
// components currently stored there (components.Store.List → the scan-to-find
// result); for a part, just the part. A via-miss returns via.ErrNotFound so
// callers (REST → 404, CLI → error) test it uniformly. Read-only: every read
// is a bare Pebble op or a lock-free scan — no locks taken, safe to call
// concurrently with any writer.
func Resolve(vs *via.Store, ps *parts.Store, cs *components.Store, ls *locations.Store, code string) (*Resolved, error) {
	t, id, err := vs.Lookup(code)
	if err != nil {
		return nil, err
	}
	switch t {
	case via.TypeLocation:
		loc, err := ls.Get(id)
		if err != nil {
			return nil, err
		}
		return &Resolved{Type: string(t), Location: loc, Components: cs.List(id)}, nil
	case via.TypePart:
		p, err := ps.Get(id)
		if err != nil {
			return nil, err
		}
		return &Resolved{Type: string(t), Part: p}, nil
	default:
		return nil, fmt.Errorf("link: via %s: unknown entity type %q", code, t)
	}
}
