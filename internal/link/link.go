// Package link is the parts↔locations composition layer (Locations Slice 3a,
// PRD §5). The two stores are deliberately decoupled — parts does not import
// locations and vice versa (§5.1 encapsulation: a Pebble swap on either
// keyspace must not leak through the other). The one cross-entity rule that
// must run on EVERY part write — the single_part_only guard (§6.1) — is
// injected into parts.Store as a parts.LocationPolicy; this package supplies
// that injection by holding both stores and reading each one's keyspace.
//
// NewPolicy returns a parts.LocationPolicy closure that:
//  1. resolves the location (→ parts.ErrLocationNotFound if absent — referential
//     integrity, so a typo'd DefaultLocationID via raw REST JSON can't create a
//     silent dangling reference);
//  2. if the location is not SinglePartOnly, allows the assignment (shared bin);
//  3. otherwise scans the parts keyspace for existing occupants and rejects
//     (parts.ErrLocationSinglePartConflict) if any occupant is a different part.
//
// Runtime lock order: parts.Store.Create/Update invoke this closure UNDER
// parts' locLock, so the order is partLock → locLock → locations.Get (which is
// LOCK-FREE — a bare Pebble read, not locations.lockFor) plus a lock-free parts
// scan (ListByLocation). locations never calls into parts, so there is no
// reverse path and no deadlock — see the parts.Store doc comment for the full
// topology.
//
// Accepted TOCTOU (adversary A5): the policy's locations.Get and the part's
// write are not atomic w.r.t. a concurrent locations.Delete of the target — the
// location can vanish between the check and the write, leaving a dangling
// DefaultLocationID from the moment of creation. This is the symmetric
// direction of the spec's accepted delete-path TOCTOU (a part can be assigned
// between the delete's has-parts count and the delete); both are
// single-operator homelab-scale windows that produce the same recoverable
// end-state (a part whose DefaultLocationID no longer resolves, fixable on the
// next Update). Closing either fully needs a cross-store transaction Pebble
// does not provide here.
package link

import (
	"errors"
	"fmt"

	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

// NewPolicy returns the single_part_only guard the daemon and CLI inject into
// parts.Store via SetLocationPolicy. One composition point — both process
// entry points (daemon.Run, the CLI's openStores) call this — so the guard is
// identical everywhere it is enforced. The returned closure captures both
// stores; it must be built AFTER both stores exist (hence the setter on
// parts.Store rather than a constructor arg).
func NewPolicy(p *parts.Store, l *locations.Store) parts.LocationPolicy {
	return func(locationID, assigningPartID string) error {
		loc, err := l.Get(locationID)
		if err != nil {
			if errors.Is(err, locations.ErrNotFound) {
				return fmt.Errorf("link: location %s: %w", locationID, parts.ErrLocationNotFound)
			}
			return fmt.Errorf("link: location %s: %w", locationID, err)
		}
		if !loc.SinglePartOnly {
			return nil // a shared location — any number of parts may live here
		}
		for _, occ := range p.ListByLocation(locationID) {
			if occ.ID != assigningPartID {
				return parts.ErrLocationSinglePartConflict
			}
		}
		return nil
	}
}

// Resolved is the Via-resolver output (§5.17 Scan-to-Find): the entity a Via
// code points at, with a location's contents embedded. Type tags the payload so
// a REST/JSON client dispatches; for a location, Contents IS the scan-to-find
// result (the parts whose DefaultLocationID == this location). No json tags —
// PascalCase field names, same wire convention as parts.Part/locations.Location
// (api.md §"JSON field names").
type Resolved struct {
	Type     string
	Location *locations.Location
	Contents []*parts.Part
	Part     *parts.Part
}

// Resolve resolves a Via code to its entity (the generic resolver, §5.17 — one
// endpoint, not one per entity). For a location the result embeds the parts
// currently assigned to it (parts.ListByLocation); for a part, just the part.
// A via-miss returns via.ErrNotFound so callers (REST → 404, CLI → error) test
// it uniformly. Read-only: locations.Get is a bare Pebble read,
// parts.ListByLocation a lock-free scan, via.Lookup a bare read — no locks
// taken, safe to call concurrently with any writer.
func Resolve(vs *via.Store, ps *parts.Store, ls *locations.Store, code string) (*Resolved, error) {
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
		return &Resolved{Type: string(t), Location: loc, Contents: ps.ListByLocation(id)}, nil
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
