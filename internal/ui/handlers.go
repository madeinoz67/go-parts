package ui

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
)

// commonFootprints is the baseline set shown in the footprint datalist even
// when the DB is empty. Merged with store.DistinctFootprints() so the dropdown
// always shows common packages + any custom ones the operator has used.
var commonFootprints = []string{
	"0402", "0603", "0805", "1206", "1210",
	"SOT-23", "SOT-223", "SOD-123", "SOD-323",
	"SOIC-8", "SOIC-14", "SOIC-16", "TSSOP-8", "TSSOP-14", "TSSOP-20",
	"MSOP-8", "MSOP-10",
	"QFN-24", "QFN-32", "QFN-48",
	"TQFP-44", "TQFP-64", "TQFP-100",
	"DPAK", "D2PAK", "TO-220", "TO-252",
	"DIP-8", "DIP-14", "DIP-16", "DIP-28",
	"TO-92", "THT",
}

// mergeFootprints returns the sorted union of common + db-sourced footprints
// (deduped), so the datalist always shows the baseline + any custom values.
func mergeFootprints(common, db []string) []string {
	seen := make(map[string]bool, len(common)+len(db))
	out := make([]string, 0, len(common)+len(db))
	for _, f := range common {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, f := range db {
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// parseTags splits a comma-separated string into a trimmed []string (empty
// entries dropped). normalizeTags (store.Create/Update) lowercases on save.
func parseTags(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// formatTags joins a []string into a comma-separated display string (for
// pre-filling an edit form's tags input).
func formatTags(tags []string) string {
	return strings.Join(tags, ", ")
}

// parseKV parses a "key=value\nkey=value" textarea into map[string]string.
// Blank lines and lines without '=' are skipped. Used for Specs + CustomFields.
func parseKV(s string) map[string]string {
	if s == "" {
		return nil
	}
	m := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k != "" {
			m[k] = strings.TrimSpace(v)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// formatKV renders a map as sorted "key=value\n" text (for pre-filling an
// edit form's textarea). Keys sorted for deterministic display.
func formatKV(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+m[k])
	}
	return strings.Join(lines, "\n")
}

// formField is a single name/value pair rendered as a hidden input by
// confirm-footprint.html, so the confirm re-submit preserves the original
// form values the user entered (not just the footprint).
type formField struct {
	Name  string
	Value string
}

// confirmFootprintData is the template data for confirm-footprint.html. The
// confirm form re-posts to Action with Target/Swap matching the original
// form's, so the success fragment (row for create, detail for edit) lands in
// the right place. CancelURL re-fetches the original form via #detail-panel.
type confirmFootprintData struct {
	Footprint string
	Action    string
	Target    string
	Swap      string
	CancelURL string
	Fields    []formField
}

// renderConfirmFootprint emits the "unknown footprint — confirm?" prompt and
// stops the save. It is the shared guard path for handleCreate/handleEdit:
// neither handler proceeds to store.Create/Update while the prompt is pending.
//
// The prompt must render where the form lives (#detail-panel), but the create
// form's own hx-target is #parts-tbody — so the first response sets
// Hx-Retarget/Hx-Reswap to redirect the swap into #detail-panel (the edit
// form already targets #detail-panel, so the retarget is a harmless no-op
// there). The confirm re-submit's Target/Swap match the original form's so
// the success fragment lands correctly on the second POST.
func (s *Server) renderConfirmFootprint(w http.ResponseWriter, action, target, swap, cancelURL, fp string, r *http.Request) {
	// Carry every submitted field forward as a hidden input, except the
	// confirm flag (the template adds that fresh) so a stale value can't
	// bypass a fresh prompt.
	keys := make([]string, 0, len(r.PostForm))
	for k := range r.PostForm {
		if k == "footprint_confirmed" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := make([]formField, 0, len(keys))
	for _, k := range keys {
		for _, v := range r.PostForm[k] {
			fields = append(fields, formField{Name: k, Value: v})
		}
	}
	w.Header().Set("Hx-Retarget", "#detail-panel")
	w.Header().Set("Hx-Reswap", "innerHTML")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "confirm-footprint.html", confirmFootprintData{
		Footprint: fp,
		Action:    action,
		Target:    target,
		Swap:      swap,
		CancelURL: cancelURL,
		Fields:    fields,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// footprintNeedsConfirm reports whether fp is outside the known set (common
// baseline + DB-sourced). Used by the create/edit typo guard.
func (s *Server) footprintNeedsConfirm(fp string) bool {
	if fp == "" {
		return false
	}
	known := mergeFootprints(commonFootprints, s.store.DistinctFootprints())
	return !slices.Contains(known, fp)
}

// handleSearch renders the rows.html fragment (a <tbody id="parts-tbody">) for a
// search query. It is the live-filter + sort + initial-load endpoint wired into
// the shell (layout.html): the search input's hx-trigger="keyup", the column
// headers' sort links, the tag sidebar's ?tag= links, and the table body's
// hx-trigger="load" all hit this route.
//
// The handler is a thin read-and-render wrapper around filteredParts, which
// holds the search (FTS) + tag + low filters + sort pipeline. q is the live
// text-query (empty → list-the-corpus path, since FTS.Search returns nil when
// tokenize yields no terms), tag is the sidebar facet value, and low gates the
// low-stock chip (Task 7's toolbar surface — wired into the helper now so the
// bulk handlers in Tasks 6/8 inherit the same view-resolution path for free).
// The handler calls fts.Search + store.Get IN-PROCESS (PRD §5.2 — the web UI
// is a 5th surface over the core, never over REST).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	tag := r.URL.Query().Get("tag")
	lowParam := r.URL.Query().Get("low")
	low := lowParam == "1"
	sortKey := r.URL.Query().Get("sort") // "mpn" | "qty" | ""
	sortDir := r.URL.Query().Get("dir")  // "asc" | "desc"

	pts := s.filteredParts(q, tag, low, sortKey, sortDir)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The data map carries Tags + Active so rows.html can emit an OOB swap of
	// #tag-nav alongside #parts-tbody. htmx applies hx-swap-oob elements after
	// the primary swap, so every search/keystroke refreshes the sidebar with
	// the active tag highlighted (cat-item active) — without it, layout.html's
	// inline render hard-codes Active="" and the copper highlight never lands.
	//
	// View-state for the bulk-bar's hidden fields is NOT fed from here. The OOB
	// #toolbar swap was removed in 720daba (it mangled <form>s in table context),
	// so the hidden q/tag/low/sort/dir fields render EMPTY in the shell and stay
	// empty. Bulk-action filter-preservation is now client-side: layout.html's
	// htmx:configRequest handler injects the live view state into the bulk POST
	// body (§7.2), and the bulk handlers below read it back out of r.PostForm.
	// The data map here therefore passes only what rows.html itself consumes
	// (Parts/Q/Tags/Active); Low/Sort/Dir travel only as URL query params on the
	// GET that rendered the current view, tracked in the browser.
	if err := s.tmpl.ExecuteTemplate(w, "rows.html", map[string]any{
		"Parts":  pts,
		"Q":      q,
		"Tags":   s.store.TagCounts(),
		"Active": tag,
		"Tag":    tag,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// filteredParts resolves the current view's parts list — search + tag + low
// filters then sort — and is the single shared view-resolution path for
// handleSearch and the bulk action handlers (Tasks 6/8). A bulk action must
// re-render the same view the user is looking at (same tag/low/sort context),
// so the helpers route through here instead of re-implementing the pipeline.
//
// Pipeline (preserves handleSearch's pre-refactor behavior exactly):
//  1. Source — empty q → store.List() (the table's initial-load + cleared-search
//     cases); else fts.Search + store.Get on each hit (FTS is the corpus-shaped
//     result, Get materializes each *Part).
//  2. Tag filter — in-place drop of parts whose Tags slice does not contain the
//     requested tag. Tags persist lowercased (parts.normalizeTags on Create/
//     Update), and the sidebar's ?tag= values come from TagCounts which reads
//     the same lowercased store, so the comparison is case-consistent without a
//     ToLower here. (A split would be a normalizeTags bug, not this filter's.)
//  3. Low-stock filter — in-place drop of parts with QtyOnHand > ReorderPoint.
//     No UI wire yet (Task 7 owns the chip); the branch is here so the helper
//     is complete for the bulk handlers.
//  4. Sort — applySort reorders in place by the requested key/direction.
//
// The two in-place filters use the standard `out := pts[:0]` aliasing pattern;
// it is safe because range captures the slice header once and the append-writes
// never get ahead of the iteration reads (out's length ≤ current index).
func (s *Server) filteredParts(q, tag string, low bool, sortKey, sortDir string) []*parts.Part {
	var pts []*parts.Part
	if q == "" {
		// Empty query: FTS.Search returns nil (no tokens), so list the corpus.
		pts = s.store.List()
	} else {
		var ws [8]byte
		hits := s.fts.Search(ws, q, 500)
		pts = make([]*parts.Part, 0, len(hits))
		for _, h := range hits {
			if p, err := s.store.Get(h.ID); err == nil {
				pts = append(pts, p)
			}
		}
	}
	if tag != "" {
		out := pts[:0]
		for _, p := range pts {
			if slices.Contains(p.Tags, tag) {
				out = append(out, p)
			}
		}
		pts = out
	}
	if low {
		out := pts[:0]
		for _, p := range pts {
			if p.QtyOnHand <= p.ReorderPoint {
				out = append(out, p)
			}
		}
		pts = out
	}
	applySort(pts, sortKey, sortDir)
	return pts
}

// handleDetail renders the detail.html fragment for a single part (the row-
// select → detail-panel flow wired into rows.html: each <tr> carries
// hx-get="/ui/parts/{id}" hx-target="#detail-panel"). Calls store.Get
// IN-PROCESS (PRD §5.2 — the web UI is a 5th surface over the core, never over
// REST). parts.ErrNotFound maps to HTTP 404; any other storage error degrades
// loudly to 500 rather than rendering a partial record.
func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.Get(id)
	if err != nil {
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "detail.html", map[string]any{
		"P":          p,
		"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
		"Locations":  s.locationOptions(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleCreateForm renders the create.html form fragment (the "+ new" nav
// button's target). The form POSTs to /ui/parts (handleCreate). Renders into
// the detail panel — same target as row-select, so the create form and the
// detail view share one swap surface by design.
func (s *Server) handleCreateForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "create.html", map[string]any{
		"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
		"Locations":  s.locationOptions(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleCreate parses the create form, calls store.Create IN-PROCESS (PRD §5.2
// — the web UI is a 5th surface over the core, never over REST), and renders
// the new row plus an OOB swap that clears #detail-panel (row-created.html).
// The form's hx-swap="afterbegin" prepends the row to #parts-tbody; the OOB
// <section id="detail-panel" hx-swap-oob> resets the panel to the "select a
// part" hint, firing on BOTH create paths — the normal form (which lives in
// #detail-panel) and the confirm-footprint prompt (also in #detail-panel).
// store.Create assigns ID/ViaCode/timestamps and indexes the FTS — the row
// template reads them straight off the populated *Part, so the response carries
// the canonical ULID for subsequent row-select.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Footprint typo guard: if the submitted footprint is not in the known set
	// (common baseline + DB-sourced) and the user has not explicitly confirmed
	// via the hidden footprint_confirmed field, render the confirm prompt
	// instead of saving. The confirm re-submit re-posts the original fields
	// (carried as hidden inputs by confirm-footprint.html) + the flag, which
	// bypasses this guard on the second pass. Target/Swap match the create
	// form's (#parts-tbody / afterbegin) so a confirmed save still prepends
	// the new row to the table.
	if r.PostFormValue("footprint_confirmed") != "true" && s.footprintNeedsConfirm(r.PostFormValue("footprint")) {
		s.renderConfirmFootprint(w, "/ui/parts", "#parts-tbody", "afterbegin", "/ui/parts/new", r.PostFormValue("footprint"), r)
		return
	}
	p := &parts.Part{
		MPN:               r.PostFormValue("mpn"),
		Description:       r.PostFormValue("description"),
		PartType:          r.PostFormValue("part_type"),
		Manufacturer:      r.PostFormValue("manufacturer"),
		Footprint:         r.PostFormValue("footprint"),
		UnitOfMeasure:     r.PostFormValue("unit_of_measure"),
		Tags:              parseTags(r.PostFormValue("tags")),
		DefaultLocationID: r.PostFormValue("default_location_id"), // Slice 3b; guard fires in store.Create
	}
	if v := r.PostFormValue("qty"); v != "" {
		fmt.Sscanf(v, "%d", &p.QtyOnHand)
	}
	if v := r.PostFormValue("reorder_threshold"); v != "" {
		fmt.Sscanf(v, "%d", &p.ReorderPoint)
	}
	if v := r.PostFormValue("package_qty"); v != "" {
		fmt.Sscanf(v, "%d", &p.PackageQty)
	}
	if err := s.store.Create(p); err != nil {
		// Slice 3b: a single_part_only / missing-location guard failure on
		// create is a user error, not a 500. Re-render the create form into
		// #detail-panel with a banner (the form's own hx-target is #parts-tbody,
		// so retarget — same mechanism as renderConfirmFootprint). A fresh form
		// is rendered (the rare create-path guard failure doesn't preserve the
		// typed entries, unlike handleEdit which preserves via the Get-then-edit
		// cur); the banner carries the reason.
		if errors.Is(err, parts.ErrLocationSinglePartConflict) || errors.Is(err, parts.ErrLocationNotFound) {
			msg := "that bin is single-part-only and already holds a different part"
			if errors.Is(err, parts.ErrLocationNotFound) {
				msg = "that location does not exist"
			}
			w.Header().Set("Hx-Retarget", "#detail-panel")
			w.Header().Set("Hx-Reswap", "innerHTML")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if tErr := s.tmpl.ExecuteTemplate(w, "create.html", map[string]any{
				"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
				"Locations":  s.locationOptions(),
				"Error":      msg,
			}); tErr != nil {
				http.Error(w, tErr.Error(), http.StatusInternalServerError)
			}
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// row-created.html renders the new row (prepended into #parts-tbody by the
	// form's hx-swap="afterbegin") PLUS an OOB swap that resets #detail-panel
	// to the "select a part" hint. The OOB element mirrors the panel's original
	// <section class="detail" id="detail-panel"> tag/class so the swap replaces
	// like-for-like and CSS (.detail, .detail.open) keeps applying. This clears
	// the panel on BOTH create paths: the normal create form (which lives in
	// #detail-panel) and the confirm-footprint prompt (also in #detail-panel).
	if err := s.tmpl.ExecuteTemplate(w, "row-created.html", map[string]any{"P": p, "Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints())}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleEdit applies an inline edit to a single part (the detail-panel form
// wired into detail.html: hx-post="/ui/parts/{id}" with a hidden version field
// for optimistic concurrency). It follows the Get-then-edit contract required
// by store.Update (PRD §5.14): the current record is loaded, the patched
// fields are applied onto it, then store.Update(cur, expectedVersion) is
// called. Constructing a fresh Part from the form would zero CreatedAt/
// CreatedBy and — per store.Update's stock contract — would still see
// QtyOnHand reset to the in-lock current value, but the create-time audit
// fields cannot be reconstructed from a form post. Get-then-edit round-trips
// them. Calls store IN-PROCESS (PRD §5.2 — the web UI is a 5th surface over
// the core, never over REST).
//
// On version conflict the response is 409 + the conflict.html fragment ("edited
// elsewhere — reload") so the client can re-fetch the canonical record. Not-
// found maps to 404 (parts.ErrNotFound), matching handleDetail.
func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Get-then-edit (T8 contract): load current, apply patched fields, Update.
	cur, err := s.store.Get(id)
	if err != nil {
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Footprint typo guard (same as handleCreate). CancelURL re-fetches the
	// canonical record so the form reverts to the stored footprint value —
	// the typo the user wants to fix is exactly what cancel discards.
	if r.PostFormValue("footprint_confirmed") != "true" && s.footprintNeedsConfirm(r.PostFormValue("footprint")) {
		s.renderConfirmFootprint(w, "/ui/parts/"+id, "#detail-panel", "innerHTML", "/ui/parts/"+id, r.PostFormValue("footprint"), r)
		return
	}
	expected, _ := strconv.Atoi(r.PostFormValue("version"))
	if v := r.PostFormValue("description"); v != "" {
		cur.Description = v
	}
	if v := r.PostFormValue("footprint"); v != "" {
		cur.Footprint = v
	}
	cur.Manufacturer = r.PostFormValue("manufacturer")
	cur.UnitOfMeasure = r.PostFormValue("unit_of_measure")
	// Slice 3b: location assignment. Read explicitly (not zero-skip) so the
	// <select>'s empty option clears DefaultLocationID (the guard allows "").
	// The checkbox submits only when checked; unchecked → "" → false.
	cur.DefaultLocationID = r.PostFormValue("default_location_id")
	cur.DefaultLocationMandatory = r.PostFormValue("default_location_mandatory") == "true"
	cur.Tags = parseTags(r.PostFormValue("tags"))
	cur.Specs = parseKV(r.PostFormValue("specs"))
	cur.CustomFields = parseKV(r.PostFormValue("custom_fields"))
	if v := r.PostFormValue("reorder_threshold"); v != "" {
		fmt.Sscanf(v, "%d", &cur.ReorderPoint)
	}
	if v := r.PostFormValue("package_qty"); v != "" {
		fmt.Sscanf(v, "%d", &cur.PackageQty)
	}
	if err := s.store.Update(cur, expected); err != nil {
		// Slice 3b: location-assignment guard errors (3a's injected policy) →
		// re-render the detail form with the user's edits preserved + a banner
		// so they pick a different bin. Distinct from a version conflict (the
		// record changed under you → conflict.html reload). The banner reuses
		// the .conflict styling.
		if errors.Is(err, parts.ErrLocationSinglePartConflict) || errors.Is(err, parts.ErrLocationNotFound) {
			msg := "that bin is single-part-only and already holds a different part"
			if errors.Is(err, parts.ErrLocationNotFound) {
				msg = "that location no longer exists"
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if tErr := s.tmpl.ExecuteTemplate(w, "detail.html", map[string]any{
				"P":          cur,
				"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
				"Locations":  s.locationOptions(),
				"Error":      msg,
			}); tErr != nil {
				http.Error(w, tErr.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		if tErr := s.tmpl.ExecuteTemplate(w, "conflict.html", map[string]any{"Reload": "/ui/parts/" + id}); tErr != nil {
			// Header already sent (409); the best we can do is nothing — the
			// fragment is short and the template engine doesn't error mid-write.
			_ = tErr
		}
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "detail.html", map[string]any{
		"P":          cur,
		"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
		"Locations":  s.locationOptions(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleStock applies a commutative stock delta inline from the detail panel
// (the inline stock form wired into detail.html: hx-post="/ui/parts/{id}/stock"
// with a delta field). It calls store.AdjustStock IN-PROCESS (PRD §5.2 — the
// web UI is a 5th surface over the core, never over REST), then re-renders the
// detail.html fragment with the updated QtyOnHand. AdjustStock does not bump
// Version (§5.14 — stock is authoritative), so the round-tripped *Part from
// store.Get carries the post-delta QtyOnHand and the unchanged Version, which
// keeps the inline edit form's hidden version field consistent on the next
// submit. parts.ErrNotFound maps to HTTP 404, matching handleDetail/handleEdit.
func (s *Server) handleStock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var delta int
	fmt.Sscanf(r.PostFormValue("delta"), "%d", &delta)
	if err := s.store.AdjustStock(id, delta, "ui"); err != nil {
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p, err := s.store.Get(id)
	if err != nil {
		// AdjustStock succeeded but the record is now unreadable — treat as
		// not-found (the canonical not-found recovery for a single-part read).
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "detail.html", map[string]any{"P": p, "Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints())}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleBulkDelete deletes every selected part (the bulk-action bar's delete
// form, §7.2 — checked boxes share name="id" and submit as native form data via
// the form's hx-include="[name=id]"). Each id is deleted in-process via
// store.Delete; an unknown id is skipped (idempotent per-row — ErrNotFound is
// not a failure here). After deleting, the current view (q/tag/low/sort/dir,
// read from the POST body — injected client-side by layout.html's
// htmx:configRequest handler, since the bulk-bar's hidden fields render empty
// in the shell) is re-rendered via filteredParts so the table reflects the
// post-delete state and the selection clears (the deleted rows' checkboxes are
// gone, so the client's updateBulkBar reverts to the chip row).
func (s *Server) handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var ok, skip int
	for _, id := range r.PostForm["id"] {
		if err := s.store.Delete(id); err != nil {
			skip++ // unknown id / concurrent edit
		} else {
			ok++
		}
	}
	if skip > 0 {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":"Deleted %d, skipped %d (not found or concurrent edit)"}`, ok, skip))
	}
	q := r.PostFormValue("q")
	tag := r.PostFormValue("tag")
	lowParam := r.PostFormValue("low")
	low := lowParam == "1"
	sortKey := r.PostFormValue("sort")
	sortDir := r.PostFormValue("dir")
	pts := s.filteredParts(q, tag, low, sortKey, sortDir)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Tags + Active drive the rows.html OOB #tag-nav swap (Task 4) — without
	// them, deleting the last part carrying a tag would leave the sidebar
	// showing a stale count. Passing them here means a bulk-delete refreshes
	// the sidebar live (htmx applies the hx-swap-oob element after the primary
	// tbody swap), matching handleSearch/handleBulkTag.
	//
	// The view state itself (q/tag/low/sort/dir) arrives via the POST body, not
	// a server-fed hidden field: layout.html's htmx:configRequest handler
	// injects it client-side (§7.2 filter-preservation). The OOB #toolbar swap
	// was removed in 720daba, so the bulk-bar's hidden fields render empty in
	// the shell and are never re-rendered server-side — the browser owns them.
	if err := s.tmpl.ExecuteTemplate(w, "rows.html", map[string]any{
		"Parts":  pts,
		"Q":      q,
		"Tags":   s.store.TagCounts(),
		"Active": tag,
		"Tag":    tag,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleBulkTag adds a tag to every selected part (the bulk-action bar's tag
// form, §7.2). Per-part Get/append-tag/Update under each part's own striped
// lock (§5.14) — N independent updates, no cross-part atomicity needed. The
// applied tag is lowercased + trimmed (defense-in-depth matching normalizeTags,
// §5.7, which lowercases again on save). Re-renders the current view (q/low/
// sort/dir plus the current tag filter) plus an OOB swap of the tag sidebar so
// counts update live — passing Tags + Active into rows.html drives the same
// Task-4 OOB #tag-nav mechanism that handleSearch uses for the active highlight.
//
// Form field disambiguation: the bulk-tag form carries TWO fields — the text
// input for the new tag is name="new_tag"; the hidden field name="tag" carries
// the current filter context (mirroring the delete form). The new tag is read
// unambiguously from r.PostFormValue("new_tag"); tagFilter (the hidden filter)
// drives filteredParts + the sidebar Active highlight. That hidden field
// renders empty in the shell — layout.html's htmx:configRequest handler
// populates it client-side with the active tag filter (§7.2 filter-preservation).
//
// Idempotency: the slices.Contains guard skips the append when the tag is
// already on the part, so re-tagging p1 with a tag it already has neither
// duplicates the entry nor bumps Version. An empty/whitespace new-tag
// short-circuits the loop (nothing to add); an unknown id is skipped via
// store.Get's ErrNotFound.
func (s *Server) handleBulkTag(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tagFilter := r.PostFormValue("tag")                                      // hidden filter context
	newTag := strings.ToLower(strings.TrimSpace(r.PostFormValue("new_tag"))) // text input
	var ok, skip int
	for _, id := range r.PostForm["id"] {
		if newTag == "" {
			break
		}
		p, err := s.store.Get(id)
		if err != nil {
			skip++
			continue
		}
		if !slices.Contains(p.Tags, newTag) {
			p.Tags = append(p.Tags, newTag)
		}
		if err := s.store.Update(p, p.Version); err != nil {
			slog.Warn("bulk-tag: part skipped (concurrent edit)", "id", id, "tag", newTag, "error", err)
			skip++
		} else {
			ok++
		}
	}
	if skip > 0 {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":"Tagged %d, skipped %d (not found or concurrent edit)"}`, ok, skip))
	}
	q := r.PostFormValue("q")
	lowParam := r.PostFormValue("low")
	low := lowParam == "1"
	sortKey := r.PostFormValue("sort")
	sortDir := r.PostFormValue("dir")
	pts := s.filteredParts(q, tagFilter, low, sortKey, sortDir)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Same OOB #tag-nav swap as handleBulkDelete (Tags + Active refresh the
	// sidebar live). Filter context for the re-render arrives via the POST
	// body, injected client-side by layout.html's htmx:configRequest handler —
	// the OOB #toolbar swap was removed in 720daba, so bulk filter-preservation
	// is now a client-side concern, not a server-fed one (§7.2).
	if err := s.tmpl.ExecuteTemplate(w, "rows.html", map[string]any{
		"Parts":  pts,
		"Q":      q,
		"Tags":   s.store.TagCounts(),
		"Active": tagFilter,
		"Tag":    tagFilter,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleBulkMove sets DefaultLocationID on every selected part to the chosen
// location (the bulk-action bar's move form, §7.2, Slice 3b). Per-part
// Get/set-location/Update — the single_part_only guard fires per part under
// each part's own lock + the target location's locLock. Moving N parts onto a
// shared location succeeds for all N; onto a SinglePartOnly location exactly
// one succeeds and the rest conflict → slog.Warn per skip (the same
// degrade-loudly posture as handleBulkTag's concurrent-edit warn). An empty
// target short-circuits (the "move to…" placeholder option).
//
// Re-renders the current view (q/tag/low/sort/dir read from the POST body —
// injected client-side by layout.html's htmx:configRequest handler for
// #bulkMoveForm, same mechanism as delete/tag).
func (s *Server) handleBulkMove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tagFilter := r.PostFormValue("tag")
	target := r.PostFormValue("move_location")
	var ok, skip int
	for _, id := range r.PostForm["id"] {
		if target == "" {
			break
		}
		p, err := s.store.Get(id)
		if err != nil {
			skip++
			continue
		}
		p.DefaultLocationID = target
		if err := s.store.Update(p, p.Version); err != nil {
			slog.Warn("bulk-move: part skipped", "id", id, "location", target, "error", err)
			skip++
		} else {
			ok++
		}
	}
	if skip > 0 {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":"Moved %d, skipped %d (guard conflict or concurrent edit)"}`, ok, skip))
	}
	q := r.PostFormValue("q")
	lowParam := r.PostFormValue("low")
	low := lowParam == "1"
	sortKey := r.PostFormValue("sort")
	sortDir := r.PostFormValue("dir")
	pts := s.filteredParts(q, tagFilter, low, sortKey, sortDir)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "rows.html", map[string]any{
		"Parts":  pts,
		"Q":      q,
		"Tags":   s.store.TagCounts(),
		"Active": tagFilter,
		"Tag":    tagFilter,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// --- Slice 5b: the Storage tab (locations management UI) ------------------

// locationCounts tallies parts per location in ONE parts scan (map[locID]count)
// — the contents-count column in the locations list. O(parts), the cheaper
// direction at homelab scale (not locations×parts scans).
func (s *Server) locationCounts() map[string]int {
	counts := make(map[string]int)
	for _, p := range s.store.List() {
		if p.DefaultLocationID != "" {
			counts[p.DefaultLocationID]++
		}
	}
	return counts
}

// handleLocationsPage renders the Storage tab: the location list (label · via ·
// contents-count) + an empty detail panel + the create-single form. List rows
// hx-get the detail fragment into #loc-detail on click (htmx).
func (s *Server) handleLocationsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "locations.html", map[string]any{
		"Locations": s.locationOptions(),
		"Counts":    s.locationCounts(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleLocationDetail renders the location-detail.html fragment (htmx into
// #loc-detail on row click): the fields, CONTENTS (parts.ListByLocation —
// scan-to-find, the point of opening a bin), an edit form (parent as a select
// of other locations — the cycle guard is the authority), and a print-label
// button. locations.ErrNotFound → 404.
func (s *Server) handleLocationDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	l, err := s.locations.Get(id)
	if err != nil {
		if errors.Is(err, locations.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "location-detail.html", map[string]any{
		"L":         l,
		"Contents":  s.store.ListByLocation(id),
		"Locations": s.locationOptions(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleLocationCreate handles the create-single form (POST /ui/locations): a
// plain form that POSTs then redirects to the page (full reload — robust, no
// htmx partial). On a guard error (e.g. missing parent) re-renders the page
// with a banner.
func (s *Server) handleLocationCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	l := &locations.Location{
		Label:          r.PostFormValue("label"),
		ParentID:       r.PostFormValue("parent_id"),
		SinglePartOnly: r.PostFormValue("single_part_only") == "true",
		Notes:          r.PostFormValue("notes"),
	}
	if err := s.locations.Create(l); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.tmpl.ExecuteTemplate(w, "locations.html", map[string]any{
			"Locations": s.locationOptions(),
			"Counts":    s.locationCounts(),
			"Error":     err.Error(),
		})
		return
	}
	http.Redirect(w, r, "/ui/locations", http.StatusSeeOther)
}

// handleLocationEdit handles the edit form (POST /ui/locations/{id}): Get-then-
// edit + Update with the version. On success redirect to the page; on a cycle,
// parent-miss, or version conflict re-render the detail with a banner (mirrors
// the parts edit UX).
func (s *Server) handleLocationEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cur, err := s.locations.Get(id)
	if err != nil {
		if errors.Is(err, locations.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	expected, _ := strconv.Atoi(r.PostFormValue("version"))
	cur.Label = r.PostFormValue("label")
	cur.ParentID = r.PostFormValue("parent_id")
	cur.Notes = r.PostFormValue("notes")
	cur.SinglePartOnly = r.PostFormValue("single_part_only") == "true"
	if err := s.locations.Update(cur, expected); err != nil {
		// §5.14 cross-surface: a version conflict → 409 + reload prompt (NOT a
		// banner). The banner path would re-render with the canonical record's
		// Version but the user's stale fields, enabling a blind-overwrite on
		// retry; the reload forces a re-fetch. Mirrors the parts handleEdit UX;
		// conflict.html is parametrized by Reload. (Header not sent yet.)
		if errors.Is(err, locations.ErrVersionConflict) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			_ = s.tmpl.ExecuteTemplate(w, "conflict.html", map[string]any{"Reload": "/ui/locations/" + id, "Target": "#loc-detail"})
			return
		}
		// cycle / parent-miss → banner (the operator fixes the input, not a
		// concurrent edit). Header not sent yet (200 implied).
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.tmpl.ExecuteTemplate(w, "location-detail.html", map[string]any{
			"L":         cur,
			"Contents":  s.store.ListByLocation(id),
			"Locations": s.locationOptions(),
			"Error":     err.Error(),
		})
		return
	}
	// Success → re-render the updated detail (htmx swaps it into #loc-detail,
	// matching the parts edit UX; the edit form is hx-post, not a plain POST,
	// so a version-conflict's conflict.html fragment renders correctly with the
	// reload link live — not as a bare dead-end page).
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if tErr := s.tmpl.ExecuteTemplate(w, "location-detail.html", map[string]any{
		"L":         cur,
		"Contents":  s.store.ListByLocation(id),
		"Locations": s.locationOptions(),
	}); tErr != nil {
		http.Error(w, tErr.Error(), http.StatusInternalServerError)
	}
}

// applySort re-orders pts in place by the requested key/direction. No-op when
// key is unrecognized (the default BM25 relevance order from FTS.Search is
// preserved).
func applySort(pts []*parts.Part, key, dir string) {
	switch key {
	case "mpn":
		sort.Slice(pts, func(i, j int) bool { return less(pts[i].MPN, pts[j].MPN, dir) })
	case "footprint":
		sort.Slice(pts, func(i, j int) bool { return less(pts[i].Footprint, pts[j].Footprint, dir) })
	case "qty":
		sort.Slice(pts, func(i, j int) bool { return lessInt(pts[i].QtyOnHand, pts[j].QtyOnHand, dir) })
	}
}

func less(a, b, dir string) bool {
	if dir == "desc" {
		return a > b
	}
	return a < b
}

func lessInt(a, b int, dir string) bool {
	if dir == "desc" {
		return a > b
	}
	return a < b
}
