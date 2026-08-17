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
	"time"

	"github.com/madeinoz67/go-parts/internal/components"
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
	TDWrap    bool // unit 2: wrap in <td colspan=7> when landing in the tr-context #exp-open
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
func (s *Server) renderConfirmFootprint(w http.ResponseWriter, action, target, swap, retarget, cancelURL, fp string, r *http.Request) {
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
	w.Header().Set("Hx-Retarget", retarget)
	w.Header().Set("Hx-Reswap", "innerHTML")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "confirm-footprint.html", confirmFootprintData{
		Footprint: fp,
		Action:    action,
		Target:    target,
		Swap:      swap,
		CancelURL: cancelURL,
		Fields:    fields,
		TDWrap:    retarget == "#exp-open",
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

// handlePartExpansion renders the part-expansion.html wrapper — the inline
// detail row inserted after the clicked row (2026-08-17 redesign, unit 1).
// Same data as handleDetail; only the wrapper differs.
func (s *Server) handlePartExpansion(w http.ResponseWriter, r *http.Request) {
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
	if err := s.tmpl.ExecuteTemplate(w, "part-expansion.html", map[string]any{
		"P":          p,
		"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
		"Locations":  s.locationOptions(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
		s.renderConfirmFootprint(w, "/ui/parts", "#parts-tbody", "afterbegin", "#form-row", "/ui/parts/new", r.PostFormValue("footprint"), r)
		return
	}
	p := &parts.Part{
		MPN:           r.PostFormValue("mpn"),
		LocalNumber:   r.PostFormValue("local_number"),
		Description:   r.PostFormValue("description"),
		PartType:      r.PostFormValue("part_type"),
		Manufacturer:  r.PostFormValue("manufacturer"),
		Footprint:     r.PostFormValue("footprint"),
		UnitOfMeasure: r.PostFormValue("unit_of_measure"),
		Tags:          parseTags(r.PostFormValue("tags")),
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
		// Identity uniqueness (schema v5): a taken MPN/local number re-renders
		// the create form in the panel with a banner (the form's own target is
		// #parts-tbody — the white-out class the archive adversary flagged;
		// retarget error fragments into the panel, always).
		if errors.Is(err, parts.ErrDuplicateMPN) || errors.Is(err, parts.ErrDuplicateLocalNumber) {
			w.Header().Set("Hx-Retarget", "#form-row")
			w.Header().Set("Hx-Reswap", "innerHTML")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = s.tmpl.ExecuteTemplate(w, "create.html", map[string]any{
				"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
				"Locations":  s.locationOptions(),
				"Error":      err.Error(),
			})
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
	if err := s.tmpl.ExecuteTemplate(w, "row-created.html", map[string]any{
		"P":          p,
		"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
		"Tags":       s.store.TagCounts(), // drives row-created's OOB tag-nav refresh
		"Active":     "",
	}); err != nil {
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
		// Edit form posts into the OPEN EXPANSION ROW (#exp-open outerHTML,
		// tr target) — the confirm must arrive table-context-safe.
		s.renderConfirmFootprint(w, "/ui/parts/"+id, "#exp-open", "outerHTML", "#exp-open", "/ui/parts/"+id, r.PostFormValue("footprint"), r)
		return
	}
	expected, _ := strconv.Atoi(r.PostFormValue("version"))
	if v := r.PostFormValue("description"); v != "" {
		cur.Description = v
	}
	if v := r.PostFormValue("footprint"); v != "" {
		cur.Footprint = v
	}
	cur.LocalNumber = strings.TrimSpace(r.PostFormValue("local_number")) // schema v5: operator's stock number, unique when non-empty
	cur.MPN = strings.TrimSpace(r.PostFormValue("mpn"))                  // editable since v5 (the edit form carries it; re-reserves in Update)
	cur.Manufacturer = r.PostFormValue("manufacturer")
	cur.UnitOfMeasure = r.PostFormValue("unit_of_measure")
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
		// Identity uniqueness (schema v5): a taken MPN/local number re-renders
		// the detail with the operator's SUBMITTED values + a banner (adversary
		// finding 4: the generic 409 "edited elsewhere — reload" fragment
		// misdiagnosed a duplicate as a concurrent edit and discarded the
		// edit on reload). 200 — htmx does not swap error-status responses.
		// Both error paths land in the OPEN EXPANSION ROW (the form's target,
		// #exp-open outerHTML — tr context): responses are the part-expansion
		// wrapper carrying the banner. 200 throughout — htmx does not swap
		// error-status responses (the adversary finding-4 lesson).
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// parts.Update has no conflict sentinel (locations does) — the message
		// is the discriminator; friendly text for the §5.14 rejection.
		msg := err.Error()
		if strings.Contains(msg, "version conflict") {
			msg = "edited elsewhere — close and reopen this row to reload the current values"
		}
		_ = s.tmpl.ExecuteTemplate(w, "part-expansion.html", map[string]any{
			"P":          cur, // the operator's submitted values — the form re-fills
			"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
			"Locations":  s.locationOptions(),
			"Error":      msg,
		})
		return
	}
	// Success → the updated detail (primary swap into #detail-panel) wrapped
	// with an OOB #tag-nav swap so a tag change refreshes the sidebar live —
	// no manual reload (same class as the Storage edit fix).
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "detail-swap.html", map[string]any{
		"P":          cur,
		"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
		"Locations":  s.locationOptions(),
		"Tags":       s.store.TagCounts(),
		"Active":     "",
	}); err != nil {
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

// --- Slice 5b: the Storage tab (locations management UI) ------------------

// locationStats tallies per-location activity in ONE components pass: counts
// (map[locID]component count — the contents column) and lastUsed
// (map[locID]most-recent Movement.Timestamp — the "last used" column; zero
// time = no movements yet). DERIVED, schema-free: no Location field, no
// cross-store write on stock moves (§5.1) — the timestamp comes from the
// components' own History, so every stock in/out updates it for free. Takes
// the caller's locations slice (their single List() pass).
func (s *Server) locationStats(locs []*locations.Location) (counts map[string]int, lastUsed map[string]time.Time) {
	counts = make(map[string]int)
	lastUsed = make(map[string]time.Time)
	if s.components == nil {
		return counts, lastUsed
	}
	for _, loc := range locs {
		comps := s.components.List(loc.ID)
		counts[loc.ID] = len(comps)
		for _, c := range comps {
			for _, m := range c.History {
				if m.Timestamp.After(lastUsed[loc.ID]) {
					lastUsed[loc.ID] = m.Timestamp
				}
			}
		}
	}
	return counts, lastUsed
}

// applyLocationSort re-orders locs in place by the requested key/direction.
// "used" sorts by last stock-movement time (zero times — never used — sort
// oldest under asc).
func applyLocationSort(locs []*locations.Location, counts map[string]int, lastUsed map[string]time.Time, key, dir string) {
	switch key {
	case "label":
		sort.Slice(locs, func(i, j int) bool { return less(locs[i].Label, locs[j].Label, dir) })
	case "via":
		sort.Slice(locs, func(i, j int) bool { return less(locs[i].ViaCode, locs[j].ViaCode, dir) })
	case "contents":
		sort.Slice(locs, func(i, j int) bool { return lessInt(counts[locs[i].ID], counts[locs[j].ID], dir) })
	case "used":
		sort.Slice(locs, func(i, j int) bool {
			a, b := lastUsed[locs[i].ID], lastUsed[locs[j].ID]
			if dir == "desc" {
				return a.After(b)
			}
			return a.Before(b)
		})
	}
}

// handleLocationsSearch renders the locations-rows.html fragment (a sortable
// <tbody> for the Storage table), mirroring the Parts shell's handleSearch +
// rows.html pattern. Reads sort/dir query params.
// handleLocationsSearch renders the locations-rows.html fragment (a sortable
// <tbody> for the Storage table), mirroring the Parts shell's handleSearch +
// rows.html pattern. Reads q/tag/sort/dir query params.
func (s *Server) handleLocationsSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "locations-rows.html", s.locationsView(
		strings.ToLower(r.URL.Query().Get("q")),
		r.URL.Query().Get("tag"),
		r.URL.Query().Get("sort"),
		r.URL.Query().Get("dir"),
		r.URL.Query().Get("archived") == "1",
	)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// locationsView resolves the Storage table view — filters (q, tag, the
// archive facet), one stats pass, sort — as the locations-rows.html data
// map. The shared re-render path for the search endpoint AND the bulk
// action handlers (a bulk action must re-render the view the operator is
// looking at, mirroring filteredParts on the Parts side). archived=true is
// the "view retired bins" facet: the DEFAULT view hides Archived locations,
// the facet view shows ONLY them (restorable via the detail toggle).
// ArchivedCount is corpus-wide (pre-filter) — the sidebar facet's badge.
func (s *Server) locationsView(q, tag, sortKey, sortDir string, archived bool) map[string]any {
	locs := s.locationOptions()
	archivedCount := 0
	for _, l := range locs {
		if l.Archived {
			archivedCount++
		}
	}
	// Filter by query: substring match on Label, ViaCode, or any Tag.
	if q != "" {
		filtered := locs[:0]
		for _, l := range locs {
			if strings.Contains(strings.ToLower(l.Label), q) ||
				strings.Contains(strings.ToLower(l.ViaCode), q) ||
				slices.Contains(l.Tags, q) {
				filtered = append(filtered, l)
			}
		}
		locs = filtered
	}
	// Filter by sidebar tag facet (exact). q and tag compose — a search-box
	// keystroke omits tag, clearing the facet (mirrors the "all" sidebar entry).
	if tag != "" {
		filtered := locs[:0]
		for _, l := range locs {
			if slices.Contains(l.Tags, tag) {
				filtered = append(filtered, l)
			}
		}
		locs = filtered
	}
	// Archive facet: default view hides retired bins; archived=true shows
	// only them. Applied last so q/tag compose with either mode.
	filtered := locs[:0]
	for _, l := range locs {
		if l.Archived == archived {
			filtered = append(filtered, l)
		}
	}
	locs = filtered
	counts, lastUsed := s.locationStats(locs)
	applyLocationSort(locs, counts, lastUsed, sortKey, sortDir)
	return map[string]any{
		"Locations":     locs,
		"Counts":        counts,
		"LastUsed":      lastUsed,
		"Tags":          s.locations.TagCounts(),
		"Active":        tag,
		"Archived":      archived,
		"ArchivedCount": archivedCount,
	}
}

// handleLocationBulkTag: POST /ui/locations/bulk-tag — adds or removes one tag
// (mode=add|remove, tag in loc_tag) on every selected location. Per-location
// Get/mutate-tags/Update under the location's own lock; idempotent both ways
// (re-adding an existing tag or removing an absent one is a no-op that still
// counts ok, no Version bump). Re-renders the operator's current view
// (q/tag/sort/dir from the POST body, injected client-side by
// locations.html's htmx:configRequest) so the table + tag sidebar refresh
// live. Mirrors the Parts handleBulkTag UX incl. the skip toast.
func (s *Server) handleLocationBulkTag(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tagFilter := r.PostFormValue("tag")                                      // hidden filter context
	newTag := strings.ToLower(strings.TrimSpace(r.PostFormValue("loc_tag"))) // the select+input
	remove := r.PostFormValue("mode") == "remove"
	var ok, skip int
	if newTag != "" {
		for _, id := range r.PostForm["id"] {
			l, err := s.locations.Get(id)
			if err != nil {
				skip++
				continue
			}
			if remove {
				tags := l.Tags[:0]
				for _, t := range l.Tags {
					if t != newTag {
						tags = append(tags, t)
					}
				}
				l.Tags = tags
			} else if !slices.Contains(l.Tags, newTag) {
				l.Tags = append(l.Tags, newTag)
			}
			if err := s.locations.Update(l, l.Version); err != nil {
				slog.Warn("loc bulk-tag: skipped (concurrent edit)", "id", id, "error", err)
				skip++
			} else {
				ok++
			}
		}
	}
	if skip > 0 {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":"Tagged %d, skipped %d (not found or concurrent edit)"}`, ok, skip))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := s.locationsView(strings.ToLower(r.PostFormValue("q")), tagFilter, r.PostFormValue("sort"), r.PostFormValue("dir"), r.PostFormValue("archived") == "1")
	_ = s.tmpl.ExecuteTemplate(w, "locations-rows.html", data)
}

// handleLocationBulkDelete: POST /ui/locations/bulk-delete — deletes every
// selected location, REFUSING any bin that still holds components (the §5.1
// composed has-stock guard: locations.Store cannot see the components
// keyspace, so the check lives here — the same composition the REST delete
// and CLI remove perform; a cross-surface link.DeleteLocation is the
// architecture-backlog's dedup answer). Skips surface in the toast, not a
// silent partial. Check-then-delete TOCTOU is v1-accepted single-operator,
// same posture as the REST/CLI paths.
func (s *Server) handleLocationBulkDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var ok, skippedStocked, skip int
	for _, id := range r.PostForm["id"] {
		if s.componentList(id) != nil && len(s.componentList(id)) > 0 {
			skippedStocked++
			continue
		}
		if err := s.locations.Delete(id); err != nil {
			skip++
		} else {
			ok++
		}
	}
	if skippedStocked > 0 || skip > 0 {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":"Deleted %d, skipped %d stocked + %d not found"}`, ok, skippedStocked, skip))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := s.locationsView(strings.ToLower(r.PostFormValue("q")), r.PostFormValue("tag"), r.PostFormValue("sort"), r.PostFormValue("dir"), r.PostFormValue("archived") == "1")
	_ = s.tmpl.ExecuteTemplate(w, "locations-rows.html", data)
}

// handleLocationArchiveToggle: POST /ui/locations/{id}/archive — flips
// Archived (soft-retire, schema v4; reversible, components untouched — never
// a delete). Responds with loc-archive-swap: the refreshed tbody (the bin
// moves between the default and archived views) + the re-rendered detail
// panel (the toggle button relabels). Archiving a stocked bin is allowed —
// reversible by design, unlike delete.
func (s *Server) handleLocationArchiveToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	l, err := s.locations.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l.Archived = !l.Archived
	if err := s.locations.Update(l, l.Version); err != nil {
		// §5.14 rejection: retarget the error into #loc-detail — the toggle
		// form's own target is #loc-tbody with hx-select, so a bare detail
		// fragment there white-outs the whole table and hides the rejection
		// (adversary finding 1). Render the CANONICAL record, never the
		// mutated-but-unpersisted l (it would mislabel the toggle button).
		cur, gErr := s.locations.Get(id)
		if gErr != nil {
			cur = l // store unreadable; best effort with what we hold
		}
		w.Header().Set("Hx-Retarget", "#loc-open")
		w.Header().Set("Hx-Reswap", "outerHTML")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.tmpl.ExecuteTemplate(w, "loc-expansion.html", s.locationDetailData(id, cur, err.Error()))
		return
	}
	// Rebuild the view the operator is actually in (adversary finding 3): the
	// archived/q/tag/sort/dir params ride the same client-side configRequest
	// injection as the bulk forms (the toggle form is covered by the
	// [hx-post*="/archive"] selector arm). The tbody refresh drops the open
	// expansion row with the old rows — correct: the bin moved views.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := s.locationsView(strings.ToLower(r.PostFormValue("q")), r.PostFormValue("tag"), r.PostFormValue("sort"), r.PostFormValue("dir"), r.PostFormValue("archived") == "1")
	_ = s.tmpl.ExecuteTemplate(w, "locations-rows.html", data)
}

// handleLocationBulkArchive: POST /ui/locations/bulk-archive — sets Archived
// on every selected location (idempotent; unarchive is the detail toggle in
// the archived view). Re-renders the operator's current view.
func (s *Server) handleLocationBulkArchive(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var ok, skip int
	for _, id := range r.PostForm["id"] {
		l, err := s.locations.Get(id)
		if err != nil {
			skip++
			continue
		}
		if l.Archived {
			ok++ // idempotent no-op
			continue
		}
		l.Archived = true
		if err := s.locations.Update(l, l.Version); err != nil {
			slog.Warn("loc bulk-archive: skipped (concurrent edit)", "id", id, "error", err)
			skip++
		} else {
			ok++
		}
	}
	if skip > 0 {
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":"Archived %d, skipped %d (not found or concurrent edit)"}`, ok, skip))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := s.locationsView(strings.ToLower(r.PostFormValue("q")), r.PostFormValue("tag"), r.PostFormValue("sort"), r.PostFormValue("dir"), r.PostFormValue("archived") == "1")
	_ = s.tmpl.ExecuteTemplate(w, "locations-rows.html", data)
}

// handleLocationsPage renders the Storage tab: the location list (label · via ·
// contents-count), an empty detail panel, and the tag sidebar facet. The
// create-single form is no longer inline above the table — it opens in the
// detail panel via the header "+ new" button (handleLocationCreateForm). List
// rows hx-get the detail fragment into #loc-detail on click (htmx).
func (s *Server) handleLocationsPage(w http.ResponseWriter, r *http.Request) {
	// One List() pass feeds the list, the contents counts, and the footer —
	// the tag facet is a Store method (its own single scan, mirroring the
	// Parts shell's handleSearch + store.TagCounts pairing).
	locs := s.locationOptions()
	archivedCount := 0
	for _, l := range locs {
		if l.Archived {
			archivedCount++
		}
	}
	counts, lastUsed := s.locationStats(locs)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "locations.html", map[string]any{
		"Locations":     locs,
		"Counts":        counts,
		"LastUsed":      lastUsed,
		"LocTags":       s.locations.TagCounts(),
		"ArchivedCount": archivedCount,
		"ActiveCount":   len(locs) - archivedCount, // footer matches the default view (adversary note 5)
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleLocationExpansion renders the loc-expansion.html wrapper — the
// inline detail row inserted after the clicked row (2026-08-17 redesign).
func (s *Server) handleLocationExpansion(w http.ResponseWriter, r *http.Request) {
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
	if err := s.tmpl.ExecuteTemplate(w, "loc-expansion.html", s.locationDetailData(id, l, "")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleLocationDetail renders the location-detail.html fragment (htmx into
// #loc-detail on row click): the fields, CONTENTS (components.List —
// scan-to-find, the point of opening a bin), an edit form, and a print-label
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
	if err := s.tmpl.ExecuteTemplate(w, "location-detail.html", s.locationDetailData(id, l, "")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// locationDetailData builds the template data for the location detail fragment
// (shared by the detail handler + the component management handlers). Includes
// a PartLookup map (component PartID → *Part for MPN display) + a PartsList
// (for the add-component picker).
func (s *Server) locationDetailData(id string, l *locations.Location, errMsg string) map[string]any {
	contents := s.componentList(id)
	partLookup := make(map[string]*parts.Part, len(contents))
	for _, c := range contents {
		if p, err := s.store.Get(c.PartID); err == nil {
			partLookup[c.PartID] = p
		}
	}
	data := map[string]any{
		"L":          l,
		"Contents":   contents,
		"PartLookup": partLookup,
		"PartsList":  s.store.List(),
		"Locations":  s.locationOptions(),
	}
	if errMsg != "" {
		data["Error"] = errMsg
	}
	return data
}

// componentList is a nil-safe helper that returns the components held at id.
// Flat-locations model: a location's contents are its Components.
func (s *Server) componentList(id string) []*components.Component {
	if s.components == nil {
		return nil
	}
	return s.components.List(id)
}

// handleLocationCreateForm renders the location-create.html fragment (the
// Storage header "+ new" button's target — mirrors Parts' handleCreateForm at
// /ui/parts/new). The form POSTs to /ui/locations (handleLocationCreate) and
// renders into the detail panel, the same surface as row-select.
func (s *Server) handleLocationCreateForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "location-create.html", map[string]any{}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// formInt parses a form value as an int (empty/invalid → 0) — the bulk form's
// numeric range fields.
func formInt(v string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(v))
	return n
}

// handleLocationBulkForm renders the bulk-create form (the "+ bulk" header
// button's target): method + prefix + per-method ranges + notes + the sanity
// cap, over the SAME GenerateLabels/CreateBulk engine the CLI drives.
func (s *Server) handleLocationBulkForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "location-bulk.html", map[string]any{}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleLocationBulkCreate: POST /ui/locations/bulk — the UI adapter over
// locations.GenerateLabels + Store.CreateBulk (the CLI is the sibling adapter;
// zero new label logic here). The UI method value "3d" adapts to the engine's
// "3d_grid", mirroring the CLI. GenerateLabels is pure and errors BEFORE any
// write, so a bad range re-renders the form with the error and nothing is
// created. A mid-bulk CreateBulk failure is partial-not-rolled-back (the
// documented recovery contract): the response refreshes the whole tbody (+
// OOB tag sidebar) with whatever now exists and the panel notice reports
// created-so-far + the error.
func (s *Server) handleLocationBulkCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	engineMethod := strings.ToLower(r.PostFormValue("method"))
	if engineMethod == "3d" {
		engineMethod = "3d_grid" // UI/CLI spelling → engine spelling
	}
	maxLabels := 100
	if v := r.PostFormValue("max_labels"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			maxLabels = n
		}
	}
	p := locations.LabelParams{
		Prefix:    r.PostFormValue("prefix"),
		From:      formInt(r.PostFormValue("from")),
		To:        formInt(r.PostFormValue("to")),
		RowFrom:   strings.ToUpper(r.PostFormValue("row_from")),
		RowTo:     strings.ToUpper(r.PostFormValue("row_to")),
		ColFrom:   formInt(r.PostFormValue("col_from")),
		ColTo:     formInt(r.PostFormValue("col_to")),
		LevelFrom: formInt(r.PostFormValue("level_from")),
		LevelTo:   formInt(r.PostFormValue("level_to")),
	}
	labels, err := locations.GenerateLabels(engineMethod, p, maxLabels)
	if err != nil {
		w.Header().Set("Hx-Retarget", "#form-row")
		w.Header().Set("Hx-Reswap", "innerHTML")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.tmpl.ExecuteTemplate(w, "location-bulk.html", map[string]any{"Error": err.Error()})
		return
	}
	created, cErr := s.locations.CreateBulk(labels, locations.BulkOpts{
		Notes:          r.PostFormValue("notes"),
		CreationMethod: engineMethod,
	})
	// Refreshed view (tbody + OOB sidebar) + the panel reset to a count notice.
	locs := s.locationOptions()
	archivedCount := 0
	for _, l := range locs {
		if l.Archived {
			archivedCount++
		}
	}
	counts, lastUsed := s.locationStats(locs)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := map[string]any{
		"Locations":     locs,
		"Counts":        counts,
		"LastUsed":      lastUsed,
		"Tags":          s.locations.TagCounts(),
		"Active":        "",
		"Archived":      false,
		"ArchivedCount": archivedCount,
		"Created":       len(created),
	}
	if cErr != nil {
		data["Partial"] = true
		data["PartialErr"] = cErr.Error()
	}
	_ = s.tmpl.ExecuteTemplate(w, "loc-bulk-created.html", data)
}

// handleLocationCreate handles the create-single form (POST /ui/locations). The
// form lives in the detail panel (loaded by the "+ new" header button); on
// submit it renders the loc-row-created fragment — the new row prepended into
// #loc-tbody (the form's hx-swap="afterbegin") + an OOB swap that opens the
// fresh bin's detail (ready to stock) + an OOB tag-sidebar refresh. Mirrors the
// Parts create UX (handleCreate → row-created.html); the former full-page
// redirect is gone (Task 43 alignment). On error the create form is re-rendered
// into #loc-detail with a banner via Hx-Retarget (the form's own target is
// #loc-tbody).
func (s *Server) handleLocationCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	l := &locations.Location{
		Label: r.PostFormValue("label"),
		Tags:  parseTags(r.PostFormValue("tags")),
		Notes: r.PostFormValue("notes"),
	}
	if err := s.locations.Create(l); err != nil {
		w.Header().Set("Hx-Retarget", "#form-row")
		w.Header().Set("Hx-Reswap", "innerHTML")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.tmpl.ExecuteTemplate(w, "location-create.html", map[string]any{
			"Error": err.Error(),
		})
		return
	}
	// Create populates ID/ViaCode/timestamps on l. Prepend the row; refresh
	// the tag sidebar. (unit 2: no panel — the operator clicks the row to
	// expand.)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "loc-row-created.html", map[string]any{
		"L":             l,
		"ZeroTime":      time.Time{},
		"Tags":          s.locations.TagCounts(),
		"Active":        "",
		"ArchivedCount": s.archivedLocationCount(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleLocationEdit handles the edit form (POST /ui/locations/{id}): Get-then-
// edit + Update with the version. On success redirect to the page; on a version
// conflict re-render the detail with a banner (mirrors the parts edit UX).
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
	cur.Tags = parseTags(r.PostFormValue("tags"))
	cur.Notes = r.PostFormValue("notes")
	if err := s.locations.Update(cur, expected); err != nil {
		// §5.14 cross-surface: a version conflict → 409 + reload prompt (NOT a
		// banner). The banner path would re-render with the canonical record's
		// Version but the user's stale fields, enabling a blind-overwrite on
		// retry; the reload forces a re-fetch. Mirrors the parts handleEdit UX;
		// conflict.html is parametrized by Reload. (Header not sent yet.)
		// Both error paths land in the OPEN EXPANSION ROW (#loc-open, the
		// form's tr target): the loc-expansion wrapper carries the banner.
		// locationDetailData is mandatory (location-detail.html indexes
		// $.PartLookup — the 2026-08-14 nil bug). 200 — htmx does not swap
		// error statuses.
		msg := err.Error()
		if errors.Is(err, locations.ErrVersionConflict) {
			msg = "edited elsewhere — close and reopen this row to reload"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.tmpl.ExecuteTemplate(w, "loc-expansion.html", s.locationDetailData(id, cur, msg))
		return
	}
	// Success → replace the open expansion row (fresh detail) + OOB refresh
	// the location's table row (instant) + the tag sidebar.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	counts, lastUsed := s.locationStats([]*locations.Location{cur})
	data := s.locationDetailData(id, cur, "")
	data["Tags"] = s.locations.TagCounts()
	data["Active"] = ""
	data["RowOOB"] = true
	data["Counts"] = counts
	data["LastUsed"] = lastUsed
	data["ArchivedCount"] = s.archivedLocationCount()
	if tErr := s.tmpl.ExecuteTemplate(w, "loc-detail-swap.html", data); tErr != nil {
		http.Error(w, tErr.Error(), http.StatusInternalServerError)
	}
}

// --- Flat-locations: component management UI handlers ----------------------

// handleComponentAddUI: POST /ui/locations/{id}/components/add — adds a part
// to this bin with an initial quantity. Re-renders the detail fragment.
func (s *Server) handleComponentAddUI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	partID := r.PostFormValue("part_id")
	qty := 0
	fmt.Sscanf(r.PostFormValue("qty"), "%d", &qty)
	if partID == "" {
		s.renderLocationDetailError(w, r, id, "select a part to add")
		return
	}
	if err := s.components.Add(id, partID, qty, parseTags(r.PostFormValue("tags"))); err != nil {
		s.renderLocationDetailError(w, r, id, err.Error())
		return
	}
	s.renderLocationDetail(w, r, id)
}

// handleComponentAdjustUI: POST /ui/locations/{id}/components/{partId}/adjust
// — the stock in/out form (Task 45): qty + dir ("in"/"out", carried by the
// submit button's name/value) + a REQUIRED reason, computed into the signed
// delta for AdjustQty (which appends the Movement{Timestamp, Delta, Reason}
// history entry and re-derives Part.QtyOnHand). The blind +/− steppers are
// gone — every movement carries an operator reason, enforced server-side (the
// client `required` attribute is convenience, not the contract). Re-renders
// the detail (history included) on success; the error banner records nothing.
func (s *Server) handleComponentAdjustUI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	partID := r.PathValue("partId")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	if reason == "" {
		s.renderLocationDetailError(w, r, id, "a reason is required for stock movements")
		return
	}
	qty := formInt(r.PostFormValue("qty"))
	if qty <= 0 {
		s.renderLocationDetailError(w, r, id, "qty must be a positive number")
		return
	}
	delta := qty
	if r.PostFormValue("dir") == "out" {
		delta = -qty
	}
	if err := s.components.AdjustQty(id, partID, delta, reason); err != nil {
		s.renderLocationDetailError(w, r, id, err.Error())
		return
	}
	s.renderLocationDetail(w, r, id)
}

// handleComponentRemoveUI: POST /ui/locations/{id}/components/{partId}/remove
// — removes a component from this bin. Re-renders the detail.
func (s *Server) handleComponentRemoveUI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	partID := r.PathValue("partId")
	if err := s.components.Remove(id, partID); err != nil {
		s.renderLocationDetailError(w, r, id, err.Error())
		return
	}
	s.renderLocationDetail(w, r, id)
}

// handlePartLocations: GET /ui/parts/{id}/locations — renders a read-only
// "stock at locations" fragment (which bins hold this part + quantities).
func (s *Server) handlePartLocations(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	comps := s.components.FindByPart(id)
	locLookup := make(map[string]*locations.Location)
	for _, c := range comps {
		if l, err := s.locations.Get(c.LocationID); err == nil {
			locLookup[c.LocationID] = l
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.ExecuteTemplate(w, "part-locations.html", map[string]any{
		"Components": comps,
		"LocLookup":  locLookup,
	})
}

// archivedLocationCount is the corpus-wide archived-bin count (the sidebar
// facet badge) for single-location response paths that don't run
// locationsView.
func (s *Server) archivedLocationCount() int {
	n := 0
	for _, l := range s.locationOptions() {
		if l.Archived {
			n++
		}
	}
	return n
}

// renderLocationDetail re-renders the OPEN EXPANSION ROW (unit 2: the
// component-management forms post into #loc-open, a tr target — so the
// response is the loc-expansion wrapper, not bare detail content). The
// common success path for the component handlers.
func (s *Server) renderLocationDetail(w http.ResponseWriter, r *http.Request, id string) {
	l, err := s.locations.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Loud, not swallowed: a template failure here silently blanks the row
	// (the 2026-08-14 nil-PartLookup bug class) — surface it instead.
	if tErr := s.tmpl.ExecuteTemplate(w, "loc-expansion.html", s.locationDetailData(id, l, "")); tErr != nil {
		http.Error(w, tErr.Error(), http.StatusInternalServerError)
	}
}

// renderLocationDetailError re-renders the expansion row with an error banner.
func (s *Server) renderLocationDetailError(w http.ResponseWriter, r *http.Request, id, errMsg string) {
	l, err := s.locations.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.ExecuteTemplate(w, "loc-expansion.html", s.locationDetailData(id, l, errMsg))
}

// applySort re-orders pts in place by the requested key/direction. No-op when
// key is unrecognized (the default BM25 relevance order from FTS.Search is
// preserved).
func applySort(pts []*parts.Part, key, dir string) {
	switch key {
	case "local":
		sort.Slice(pts, func(i, j int) bool { return less(pts[i].LocalNumber, pts[j].LocalNumber, dir) })
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
