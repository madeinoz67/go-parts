package mcp

// tools.go — the read tools (spec Unit 1). Each is a thin rendering of the
// same store calls the REST/UI surfaces make; filters mirror ui.filteredParts
// exactly so results are identical across surfaces (PRD §5.2).

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
)

// --- argument helpers (JSON numbers arrive as float64) ---

func argStr(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// argInt reads an integer argument. A PRESENT but non-integral value (a
// fractional float, or a non-number) is a usage error naming the argument —
// never a silent truncation ({"delta":2.5} recording 2 is silently-wrong) and
// never a silent fall-back to "absent". ok=false means genuinely absent.
func argInt(args map[string]any, key string) (int, bool, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false, nil
	}
	f, isNum := v.(float64)
	if !isNum {
		return 0, true, fmt.Errorf("%s must be an integer (got %T)", key, v)
	}
	if f != math.Trunc(f) {
		return 0, true, fmt.Errorf("%s must be an integer (got %v)", key, f)
	}
	return int(f), true, nil
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
	if v, ok, err := argInt(args, "limit"); err != nil {
		return "", err
	} else if ok && v > 0 && v <= 100 {
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

// --- write tools (spec Unit 2) ---

// resolveLocation resolves a location selector: "L-" via-code (ByVia) or an
// exact Label match over List(). Labels are NOT unique (only ViaCode is), so
// an ambiguous label errors listing every candidate's via-code — never a
// silent pick (spec: disambiguation is errors, not defaults).
func (s *Server) resolveLocation(sel string) (*locations.Location, error) {
	if sel == "" {
		return nil, errors.New("location selector is empty")
	}
	if strings.HasPrefix(sel, "L-") {
		return s.locations.ByVia(sel)
	}
	var matches []*locations.Location
	for _, l := range s.locations.List() {
		if l.Label == sel {
			matches = append(matches, l)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no location matches %q: %w", sel, locations.ErrNotFound)
	}
	names := make([]string, len(matches))
	for i, l := range matches {
		names[i] = l.Label + " (" + l.ViaCode + ")"
	}
	return nil, fmt.Errorf("label %q matches %d locations — pass the via-code: %s", sel, len(matches), strings.Join(names, ", "))
}

// stockedLabelList renders "Label (L-code)" strings for error messages.
func (s *Server) stockedLabelList(partID string) []string {
	entries := s.stockedLabels(partID)
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = fmt.Sprintf("%v (%v)", e["label"], e["via_code"])
	}
	return out
}

// toolUpsertPart is the full-record upsert: an empty ID creates; otherwise the
// record's Version field IS the optimistic-concurrency token (the agent's
// get_part → edit → upsert loop; REST expresses the same §5.14 discipline via
// If-Match). Stale versions get the store's conflict sentinel verbatim.
// QtyOnHand in the body is authoritative ONLY on create — Store.Update
// preserves the in-lock server-side value (F3: stock is adjust_stock's domain).
func (s *Server) toolUpsertPart(args map[string]any) (string, error) {
	raw, ok := args["part"].(map[string]any)
	if !ok {
		return "", errors.New(`"part" object is required (PascalCase fields, same shape get_part returns)`)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	var p parts.Part
	if err := json.Unmarshal(b, &p); err != nil {
		return "", fmt.Errorf("part fields: %v (PascalCase keys, same as REST)", err)
	}
	if p.ID == "" {
		// Create-branch audit zeroing (rest.handleCreate parity, F2): a caller
		// MUST NOT set ID/Version/audit fields — they are server-assigned.
		// Zeroing them defensively keeps a caller-supplied CreatedBy from
		// spoofing the audit trail (Store.Create's "local" default is
		// authoritative) and guards the timestamps/Version against any future
		// drift in the store's create contract.
		p.ID = ""
		p.Version = 0
		p.CreatedBy = ""
		p.UpdatedBy = ""
		p.CreatedAt = time.Time{}
		p.UpdatedAt = time.Time{}
		if err := s.store.Create(&p); err != nil {
			return "", err
		}
	} else {
		if p.Version < 1 {
			return "", errors.New("update requires the part's current Version — fetch via get_part; stale versions are rejected")
		}
		if err := s.store.Update(&p, p.Version); err != nil {
			return "", err
		}
	}
	fresh, err := s.store.Get(p.ID)
	if err != nil {
		return "", err
	}
	return render(fresh)
}

// toolAdjustStock moves stock at ONE location: it requires an explicit
// location (label or L- via-code) and a reason, appends a Movement via
// components.AdjustQty (which re-derives Part.QtyOnHand — the engine's own
// recomputePartQty path), and errors listing the part's actual stocked
// locations when the part isn't stocked at the requested one.
func (s *Server) toolAdjustStock(args map[string]any) (string, error) {
	partSel := argStr(args, "part")
	locSel := argStr(args, "location")
	reason := argStr(args, "reason")
	delta, haveDelta, err := argInt(args, "delta")
	if err != nil {
		return "", err
	}
	if partSel == "" || locSel == "" || reason == "" || !haveDelta {
		return "", errors.New("part, location, delta, and reason are all required")
	}
	matches, err := s.resolvePart(partSel)
	if err != nil {
		return "", err
	}
	if len(matches) > 1 {
		// Same candidate formatting as get_part — an agent disambiguates in
		// one round-trip instead of re-issuing get_part.
		names := make([]string, len(matches))
		for i, p := range matches {
			names[i] = p.ID + " (" + p.MPN + ")"
		}
		return "", fmt.Errorf("part selector %q matches %d parts — pass the exact id: %s", partSel, len(matches), strings.Join(names, ", "))
	}
	p := matches[0]
	loc, err := s.resolveLocation(locSel)
	if err != nil {
		return "", err
	}
	if _, err := s.components.Get(loc.ID, p.ID); err != nil {
		if at := s.stockedLabelList(p.ID); len(at) == 0 {
			return "", fmt.Errorf("part %s (%s) is not stocked anywhere yet — stock it at a location first", p.MPN, p.ID)
		} else {
			return "", fmt.Errorf("part %s (%s) is not stocked at %s — stocked at: %s",
				p.MPN, p.ID, loc.Label, strings.Join(at, ", "))
		}
	}
	if err := s.components.AdjustQty(loc.ID, p.ID, delta, reason); err != nil {
		return "", err
	}
	comp, err := s.components.Get(loc.ID, p.ID)
	if err != nil {
		return "", err
	}
	fresh, err := s.store.Get(p.ID)
	if err != nil {
		return "", err
	}
	mov := components.Movement{}
	if len(comp.History) > 0 {
		mov = comp.History[len(comp.History)-1]
	}
	return render(map[string]any{
		"movement": mov, "component": comp, "part_qty_on_hand": fresh.QtyOnHand,
	})
}
