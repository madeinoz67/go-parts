package mcp

// tools.go — the read tools (spec Unit 1). Each is a thin rendering of the
// same store calls the REST/UI surfaces make; filters mirror ui.filteredParts
// exactly so results are identical across surfaces (PRD §5.2).

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/parts"
)

// --- argument helpers (JSON numbers arrive as float64) ---

func argStr(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func argInt(args map[string]any, key string) (int, bool) {
	f, ok := args[key].(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func argBool(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

// --- shared filters (ui.filteredParts parity; see Global Constraints) ---

func filterTag(pts []*parts.Part, tag string) []*parts.Part {
	out := pts[:0]
	for _, p := range pts {
		if slices.Contains(p.Tags, tag) {
			out = append(out, p)
		}
	}
	return out
}

// filterLow KEEPS QtyOnHand <= ReorderPoint — the same comparison as the UI's
// low filter (which drops QtyOnHand > ReorderPoint), including the 0/0 edge.
func filterLow(pts []*parts.Part) []*parts.Part {
	out := pts[:0]
	for _, p := range pts {
		if p.QtyOnHand <= p.ReorderPoint {
			out = append(out, p)
		}
	}
	return out
}

// stockedLabels renders where a part is stocked: one entry per component.
// Used by search_parts (summary) and get_part (full stock list).
func (s *Server) stockedLabels(partID string) []map[string]any {
	out := []map[string]any{}
	for _, c := range s.components.FindByPart(partID) {
		label, code := c.LocationID, ""
		if loc, err := s.locations.Get(c.LocationID); err == nil {
			label, code = loc.Label, loc.ViaCode
		}
		out = append(out, map[string]any{"label": label, "via_code": code, "qty": c.Quantity})
	}
	return out
}

// resolvePart resolves a part selector: exact ID, "P-" via-code, or a trimmed
// MPN/LocalNumber scan over List() — the same List() the UI table reads
// (there is no public by-identity lookup; the 0x14 index is store-private).
// Multiple matches (legacy pre-v5 duplicate MPNs, or a cross-field collision)
// return every match so the caller errors with the list — never a silent pick.
func (s *Server) resolvePart(sel string) ([]*parts.Part, error) {
	if sel == "" {
		return nil, errors.New("part selector is empty — pass id, mpn, or local_number")
	}
	if strings.HasPrefix(sel, "P-") {
		_, id, err := s.via.Lookup(sel)
		if err != nil {
			return nil, err
		}
		p, err := s.store.Get(id)
		if err != nil {
			return nil, err
		}
		return []*parts.Part{p}, nil
	}
	if p, err := s.store.Get(sel); err == nil {
		return []*parts.Part{p}, nil
	}
	trimmed := strings.TrimSpace(sel)
	var matches []*parts.Part
	for _, p := range s.store.List() {
		if strings.TrimSpace(p.MPN) == trimmed || strings.TrimSpace(p.LocalNumber) == trimmed {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no part matches %q: %w", sel, parts.ErrNotFound)
	}
	return matches, nil
}

// --- the read tools ---

func (s *Server) toolSearchParts(args map[string]any) (string, error) {
	q := argStr(args, "query")
	limit := 20
	if v, ok := argInt(args, "limit"); ok && v > 0 && v <= 100 {
		limit = v
	}
	var pts []*parts.Part
	if q == "" {
		pts = s.store.List() // ui.filteredParts' empty-q path: list the corpus
	} else {
		var ws [8]byte
		for _, h := range s.fts.Search(ws, q, 20) {
			if p, err := s.store.Get(h.ID); err == nil {
				pts = append(pts, p) // skip vanished-between-hit-and-hydrate (rest.handleSearch)
			}
		}
	}
	if tag := argStr(args, "tag"); tag != "" {
		pts = filterTag(pts, tag)
	}
	if argBool(args, "low") {
		pts = filterLow(pts)
	}
	if len(pts) > limit {
		pts = pts[:limit]
	}
	out := make([]map[string]any, 0, len(pts))
	for _, p := range pts {
		out = append(out, map[string]any{
			"id": p.ID, "local_number": p.LocalNumber, "mpn": p.MPN,
			"description": p.Description, "qty_on_hand": p.QtyOnHand,
			"locations": s.stockedLabels(p.ID),
		})
	}
	return render(out)
}

func (s *Server) toolGetPart(args map[string]any) (string, error) {
	sel := argStr(args, "id")
	if sel == "" {
		sel = argStr(args, "mpn")
	}
	if sel == "" {
		sel = argStr(args, "local_number")
	}
	matches, err := s.resolvePart(sel)
	if err != nil {
		return "", err
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, p := range matches {
			names[i] = p.ID + " (" + p.MPN + ")"
		}
		return "", fmt.Errorf("selector %q matches %d parts — pass the exact id: %s", sel, len(matches), strings.Join(names, ", "))
	}
	p := matches[0]
	var movs []components.Movement
	for _, c := range s.components.FindByPart(p.ID) {
		movs = append(movs, c.History...)
	}
	sort.Slice(movs, func(i, j int) bool { return movs[i].Timestamp.After(movs[j].Timestamp) })
	if len(movs) > 10 {
		movs = movs[:10]
	}
	return render(map[string]any{"part": p, "stock": s.stockedLabels(p.ID), "recent_movements": movs})
}

func (s *Server) toolListLowStock(args map[string]any) (string, error) {
	_ = args
	low := filterLow(s.store.List())
	out := make([]map[string]any, 0, len(low))
	for _, p := range low {
		out = append(out, map[string]any{
			"id": p.ID, "mpn": p.MPN, "local_number": p.LocalNumber,
			"qty_on_hand": p.QtyOnHand, "reorder_point": p.ReorderPoint,
		})
	}
	return render(out)
}

func (s *Server) toolGetInventoryStats(args map[string]any) (string, error) {
	_ = args
	low, out := 0, 0
	for _, p := range s.store.List() {
		if p.QtyOnHand <= p.ReorderPoint {
			low++
		}
		if p.QtyOnHand <= 0 {
			out++
		}
	}
	locs := 0
	if s.locations != nil {
		locs = s.locations.Count()
	}
	return render(map[string]int{
		"parts_total": s.store.Count(), "locations_total": locs,
		"low_stock": low, "out_of_stock": out,
	})
}

// render marshals a tool payload to the JSON text MCP content carries.
func render(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
